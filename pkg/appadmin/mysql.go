// Package appadmin administers the W&B application's own data on an instance:
// promoting and demoting instance admins, and migrating every account from one
// email domain to another.
//
// It lives in wsm rather than in a caller so the logic is written once and used
// by both the `wsm user` commands and Watchtower's Auth tab. Watchtower is a web
// UI over this library; duplicating the logic there meant the CLI could not do
// it at all, and the two would drift.
//
// The two halves deliberately take different routes:
//
//   - Admin status goes through the app's GraphQL updateUser mutation carrying
//     the CALLER's credentials, so the app authorizes it as that user and runs
//     its own business logic (team-role propagation, billing). Nothing here holds
//     an elevated credential.
//   - Email-domain migration has no API — the app offers no way to change another
//     user's address — so it is SQL against the instance's MySQL using the
//     operator-published connection. That is a service credential rather than the
//     caller's identity, and the rewrite is irreversible, so callers are expected
//     to gate it and to dry-run first.
package appadmin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	v2 "github.com/wandb/operator/api/v2"
	"github.com/wandb/wsm/pkg/kubectl"
	"github.com/wandb/wsm/pkg/operator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// dialTimeout bounds the connect attempt. The MySQL host is an in-cluster
// Service DNS name, so out-of-cluster callers fail here rather than hanging.
const dialTimeout = 10 * time.Second

// Connect resolves the instance's MySQL credentials and opens a connection.
// Credentials come from status.mysqlStatus[default].connection, which the
// operator populates for BOTH managed and external MySQL — so this works
// regardless of how the datastore is provisioned, and never has to guess at
// Moco's secret naming.
func Connect(ctx context.Context, namespace, crName string) (*sql.DB, error) {
	cr, err := operator.GetCR(ctx, crName, namespace)
	if err != nil {
		return nil, fmt.Errorf("read instance %s/%s: %w", namespace, crName, err)
	}

	status, ok := cr.Status.MySQLStatus[v2.DefaultInstanceName]
	if !ok {
		return nil, fmt.Errorf("instance %s/%s publishes no MySQL connection yet; it may still be reconciling", namespace, crName)
	}
	conn := status.Connection

	resolve, err := secretResolver(ctx, namespace)
	if err != nil {
		return nil, err
	}

	host, err := resolve("host", conn.Host)
	if err != nil {
		return nil, err
	}
	port, err := resolve("port", conn.Port)
	if err != nil {
		return nil, err
	}
	database, err := resolve("database", conn.Database)
	if err != nil {
		return nil, err
	}
	username, err := resolve("username", conn.Username)
	if err != nil {
		return nil, err
	}
	password, err := resolve("password", conn.Password)
	if err != nil {
		return nil, err
	}

	// The operator publishes optional TLS material alongside the credentials.
	// Ignoring it built a plaintext DSN, so an instance whose MySQL requires TLS
	// could not connect at all — dry runs and migrations both failed.
	tlsParam, err := resolveTLS(ctx, namespace, conn, resolve)
	if err != nil {
		return nil, err
	}

	// interpolateParams stays off so every statement uses real placeholders —
	// these queries take user-supplied domains and identifiers.
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true&interpolateParams=false&timeout=%s%s",
		username, password, net.JoinHostPort(host, port), database, dialTimeout, tlsParam)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(2)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to mysql at %s: %w (the host is an in-cluster Service, so this only resolves from inside the cluster)", host, err)
	}
	return db, nil
}

// secretResolver returns a function that reads a ValueOrSecret, caching the
// Secrets it fetches — every connection field usually points at the same one.
func secretResolver(ctx context.Context, namespace string) (func(string, v2.ValueOrSecret) (string, error), error) {
	_, clientset, err := kubectl.GetClientset()
	if err != nil {
		return nil, fmt.Errorf("not connected to cluster: %w", err)
	}
	cache := map[string]map[string][]byte{}

	return func(field string, v v2.ValueOrSecret) (string, error) {
		if v.Value != "" {
			return v.Value, nil
		}
		ref := v.SecretKeyRef()
		if ref == nil {
			return "", fmt.Errorf("MySQL connection field %q is unset on the instance status", field)
		}
		data, ok := cache[ref.Name]
		if !ok {
			secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if err != nil {
				return "", fmt.Errorf("read secret %s/%s for MySQL %s: %w", namespace, ref.Name, field, err)
			}
			data = secret.Data
			cache[ref.Name] = data
		}
		value, ok := data[ref.Key]
		if !ok {
			return "", fmt.Errorf("secret %s/%s has no key %q (needed for MySQL %s)", namespace, ref.Name, ref.Key, field)
		}
		return string(value), nil
	}, nil
}

// resolveTLS turns the connection's optional TLS fields into a DSN `tls=` value,
// registering a custom config when a CA or client keypair is published.
//
// Returns "" when TLS is not configured, leaving the DSN as-is. The `tls` field
// carries a boolean-ish string; a CA or client cert implies TLS even when it is
// unset, since publishing certificates for a plaintext connection is not a
// meaningful configuration.
func resolveTLS(
	ctx context.Context,
	namespace string,
	conn v2.MysqlConnection,
	resolve func(string, v2.ValueOrSecret) (string, error),
) (string, error) {
	read := func(field string, v v2.ValueOrSecret) (string, error) {
		if v.IsZero() {
			return "", nil
		}
		return resolve(field, v)
	}

	tlsRaw, err := read("tls", conn.Tls)
	if err != nil {
		return "", err
	}
	ca, err := read("sslCa", conn.SslCa)
	if err != nil {
		return "", err
	}
	cert, err := read("sslCert", conn.SslCert)
	if err != nil {
		return "", err
	}
	key, err := read("sslKey", conn.SslKey)
	if err != nil {
		return "", err
	}

	enabled, err := tlsRequested(tlsRaw)
	if err != nil {
		return "", err
	}
	// Certificates imply TLS even when `tls` is unset: publishing them for a
	// plaintext connection is not a meaningful configuration.
	if !enabled && ca == "" && cert == "" {
		return "", nil
	}

	// TLS wanted but no custom material: hand the driver its own mode name.
	// skip-verify and preferred are registered driver modes with meanings "true"
	// does not carry, so collapsing them to true silently upgraded a deliberately
	// relaxed setting into full verification.
	if ca == "" && cert == "" {
		switch mode := strings.ToLower(strings.TrimSpace(tlsRaw)); mode {
		case "skip-verify", "preferred":
			return "&tls=" + mode, nil
		default:
			return "&tls=true", nil
		}
	}

	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if ca != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(ca)) {
			return "", errors.New("MySQL sslCa is not valid PEM")
		}
		cfg.RootCAs = pool
	}
	if cert != "" || key != "" {
		if cert == "" || key == "" {
			return "", errors.New("MySQL TLS needs both sslCert and sslKey, or neither")
		}
		pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
		if err != nil {
			return "", fmt.Errorf("MySQL client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}

	// The driver looks configs up by name from a package-global registry, so the
	// name is scoped per namespace to keep concurrent instances from colliding.
	name := "wsm-appadmin-" + namespace
	if err := mysqldriver.RegisterTLSConfig(name, cfg); err != nil {
		return "", fmt.Errorf("register MySQL TLS config: %w", err)
	}
	return "&tls=" + name, nil
}

// tlsRequested reads the connection's `tls` field. The operator writes a
// boolean-ish string; "skip-verify" and "preferred" are passed through by the
// driver and accepted here so an operator-published value is never rejected.
func tlsRequested(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "false", "0", "off", "disabled":
		return false, nil
	case "true", "1", "on", "enabled", "required", "skip-verify", "preferred":
		return true, nil
	default:
		return false, fmt.Errorf("unrecognized MySQL tls value %q", raw)
	}
}
