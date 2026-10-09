package supportbundle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	v2 "github.com/wandb/operator/api/v2"
	"github.com/wandb/wsm/pkg/kubectl"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ErrUnsupportedBackend is returned for object stores retrieve/delete cannot reach yet.
var ErrUnsupportedBackend = errors.New("only S3-compatible object stores are supported for retrieve/delete in this version")

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Size int64
	ETag string
}

// ObjectStore is the bucket surface retrieve/delete need.
type ObjectStore interface {
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	// ReadFrom streams the object from offset; etag guards against the object
	// changing between resumed reads.
	ReadFrom(ctx context.Context, key string, offset int64, etag string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// Location is a parsed artifact URI.
type Location struct {
	Scheme string
	Bucket string
	Key    string
}

func ParseLocation(raw string) (Location, error) {
	if err := validateLocation(raw); err != nil {
		return Location{}, err
	}
	u, _ := url.Parse(raw)
	return Location{Scheme: u.Scheme, Bucket: u.Host, Key: strings.TrimPrefix(u.Path, "/")}, nil
}

// Connection is the resolved object-store connection. It contains secret
// values and must never be printed or persisted.
type Connection struct {
	Provider       string
	Host           string
	Port           string
	Bucket         string
	Region         string
	AccessKey      string
	SecretKey      string
	TLS            bool
	ForcePathStyle bool
}

// ResolveConnection reads the connection fields referenced by the CR status.
func ResolveConnection(ctx context.Context, cs kubernetes.Interface, inst *Installation) (*Connection, error) {
	secrets := map[string]map[string][]byte{}
	read := func(v v2.ValueOrSecret) (string, error) {
		if v.Value != "" {
			return v.Value, nil
		}
		ref := v.SecretKeyRef()
		if ref == nil {
			return "", nil
		}
		data, ok := secrets[ref.Name]
		if !ok {
			secret, err := cs.CoreV1().Secrets(inst.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if err != nil {
				return "", apiError("get", "Secret", inst.Namespace, ref.Name, err)
			}
			data = secret.Data
			secrets[ref.Name] = data
		}
		return string(data[ref.Key]), nil
	}

	c := inst.ObjectStore
	conn := &Connection{}
	var err error
	fields := []struct {
		dst *string
		src v2.ValueOrSecret
	}{
		{&conn.Provider, c.Provider}, {&conn.Host, c.Endpoint}, {&conn.Port, c.Port},
		{&conn.Bucket, c.Bucket}, {&conn.Region, c.Region},
		{&conn.AccessKey, c.AccessKey}, {&conn.SecretKey, c.SecretKey},
	}
	for _, f := range fields {
		if *f.dst, err = read(f.src); err != nil {
			return nil, err
		}
	}
	tls, err := read(c.TlsEnabled)
	if err != nil {
		return nil, err
	}
	pathStyle, err := read(c.ForcePathStyle)
	if err != nil {
		return nil, err
	}
	conn.TLS, _ = strconv.ParseBool(tls)
	conn.ForcePathStyle, _ = strconv.ParseBool(pathStyle)
	if conn.Provider == "" {
		conn.Provider = "s3"
	}
	return conn, nil
}

var inClusterHost = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]*[a-z0-9])?)\.([a-z0-9]([-a-z0-9]*[a-z0-9])?)\.svc(\.cluster\.local)?$`)

// OpenS3 returns an S3 client for the installation's bucket. In-cluster
// endpoints (managed SeaweedFS) are reached through a port-forward while
// requests keep the in-cluster Host so signatures still verify. Without static
// keys the workstation's default AWS credential chain is used.
func OpenS3(ctx context.Context, cfg *rest.Config, cs *kubernetes.Clientset, inst *Installation) (ObjectStore, string, func(), error) {
	conn, err := ResolveConnection(ctx, cs, inst)
	if err != nil {
		return nil, "", nil, err
	}
	if conn.Provider != "s3" {
		return nil, "", nil, fmt.Errorf("object store provider %q: %w", conn.Provider, ErrUnsupportedBackend)
	}

	closeFn := func() {}
	var endpoint string
	httpClient := awshttp.NewBuildableClient()
	if conn.Host != "" {
		scheme := "http"
		if conn.TLS {
			scheme = "https"
		}
		hostPort := conn.Host
		if conn.Port != "" {
			hostPort = net.JoinHostPort(conn.Host, conn.Port)
		}
		endpoint = scheme + "://" + hostPort

		if match := inClusterHost.FindStringSubmatch(conn.Host); match != nil {
			port, err := strconv.Atoi(conn.Port)
			if err != nil {
				return nil, "", nil, fmt.Errorf("in-cluster object store has no valid port")
			}
			session, err := kubectl.PortForward(ctx, cfg, cs, match[3], match[1], port, 0)
			if err != nil {
				return nil, "", nil, fmt.Errorf("port-forward to object store: %w", err)
			}
			closeFn = func() { _ = session.Close() }
			local := net.JoinHostPort("127.0.0.1", strconv.Itoa(session.LocalPort))
			httpClient = httpClient.WithTransportOptions(func(t *http.Transport) {
				dialer := &net.Dialer{}
				t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					if addr == hostPort {
						addr = local
					}
					return dialer.DialContext(ctx, network, addr)
				}
			})
		}
	}

	region := conn.Region
	if region == "" {
		region = "us-east-1"
	}
	loadOpts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region), awsconfig.WithHTTPClient(httpClient)}
	if conn.AccessKey != "" && conn.SecretKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(conn.AccessKey, conn.SecretKey, "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		closeFn()
		return nil, "", nil, fmt.Errorf("configure S3 client: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		o.UsePathStyle = conn.ForcePathStyle
		// S3-compatible stores (SeaweedFS, MinIO) don't all support the SDK's default checksums.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	return &s3Store{client: client, bucket: conn.Bucket}, conn.Bucket, closeFn, nil
}

type s3Store struct {
	client *s3.Client
	bucket string
}

func (s *s3Store) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("stat object: %w", err)
	}
	return ObjectInfo{Size: aws.ToInt64(out.ContentLength), ETag: aws.ToString(out.ETag)}, nil
}

func (s *s3Store) ReadFrom(ctx context.Context, key string, offset int64, etag string) (io.ReadCloser, error) {
	in := &s3.GetObjectInput{Bucket: &s.bucket, Key: &key}
	if offset > 0 {
		in.Range = aws.String(fmt.Sprintf("bytes=%d-", offset))
	}
	if etag != "" {
		in.IfMatch = aws.String(etag)
	}
	out, err := s.client.GetObject(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("read object: %w", err)
	}
	return out.Body, nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key}); err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

// ObjectKey checks that an artifact lives in the installation's bucket and
// looks like a support bundle before wsm reads or deletes it.
func ObjectKey(art *Artifact, bucket string) (string, error) {
	loc, err := ParseLocation(art.Location)
	if err != nil {
		return "", err
	}
	if loc.Scheme != "s3" {
		return "", fmt.Errorf("artifact scheme %q: %w", loc.Scheme, ErrUnsupportedBackend)
	}
	if loc.Bucket != bucket {
		return "", fmt.Errorf("artifact bucket %q does not match the installation bucket", loc.Bucket)
	}
	if !strings.HasSuffix(loc.Key, ".tar.gz") {
		return "", fmt.Errorf("artifact key %q is not a .tar.gz bundle", loc.Key)
	}
	return loc.Key, nil
}
