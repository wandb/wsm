package main

import (
	"encoding/base64"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/wandb/wsm/pkg/operator"
)

func TestRegistryCheckChartTransportFlags(t *testing.T) {
	t.Setenv("HELM_CACHE_HOME", t.TempDir())
	t.Setenv("HELM_REGISTRY_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	cases := []struct {
		name      string
		tls       bool
		flags     []string
		wantError string
		contacted bool
	}{
		{name: "private HTTPS rejected by default", tls: true, wantError: "x509:"},
		{name: "insecure preserves HTTPS", tls: true, flags: []string{"--insecure"}, wantError: "not found", contacted: true},
		{name: "explicit HTTP", flags: []string{"--plain-http"}, wantError: "not found", contacted: true},
		{name: "HTTP and TLS skip are independent", flags: []string{"--plain-http", "--insecure"}, wantError: "not found", contacted: true},
		{name: "insecure alone does not enable HTTP", flags: []string{"--insecure"}, wantError: "HTTP response to HTTPS client"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.NotFound(w, r)
			}))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			if tc.tls {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			cmd := registryCheckCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(append([]string{"--registry", server.Listener.Addr().String()}, tc.flags...))
			err := cmd.ExecuteContext(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected %q, got %v", tc.wantError, err)
			}
			if got := requests.Load() > 0; got != tc.contacted {
				t.Fatalf("registry contacted = %v, want %v", got, tc.contacted)
			}
		})
	}
}

func TestRegistryCheckPlainHTTPRejectsServerManifest(t *testing.T) {
	cmd := registryCheckCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--registry", "unused.invalid", "--plain-http", "--wandb-version", "0.84.0"})
	err := cmd.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "OCI server manifests require HTTPS") {
		t.Fatalf("expected server-manifest HTTPS requirement, got %v", err)
	}
}

func TestCheckOneRegistryTransport(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	body := []byte(`{"schemaVersion":2}`)
	cases := []struct {
		name      string
		tls       bool
		transport operator.RegistryTransport
		status    int
		reference string
		want      string
	}{
		{name: "private HTTPS", tls: true, status: 200, reference: ":v1", want: "error"},
		{name: "HTTPS with TLS skip", tls: true, transport: operator.RegistryTransport{InsecureSkipTLSVerify: true}, status: 200, reference: ":v1", want: "present"},
		{name: "HTTP with TLS skip only", transport: operator.RegistryTransport{InsecureSkipTLSVerify: true}, status: 200, reference: ":v1", want: "error"},
		{name: "explicit HTTP", transport: operator.RegistryTransport{PlainHTTP: true}, status: 200, reference: ":v1", want: "present"},
		{name: "default tag", transport: operator.RegistryTransport{PlainHTTP: true}, status: 200, want: "present"},
		{name: "digest reference", transport: operator.RegistryTransport{PlainHTTP: true}, status: 200, reference: "@" + digest.FromBytes(body).String(), want: "present"},
		{name: "missing", transport: operator.RegistryTransport{PlainHTTP: true}, status: 404, reference: ":v1", want: "missing"},
		{name: "forbidden", transport: operator.RegistryTransport{PlainHTTP: true}, status: 403, reference: ":v1", want: "unauthorized"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status != http.StatusOK {
					http.Error(w, http.StatusText(tc.status), tc.status)
					return
				}
				ref := strings.TrimLeft(tc.reference, ":@")
				if ref == "" {
					ref = "latest"
				}
				if r.URL.Path != "/v2/test/manifests/"+ref {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				w.Header().Set("Docker-Content-Digest", digest.FromBytes(body).String())
				_, _ = w.Write(body)
			}))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			if tc.tls {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			got, detail := checkOne(t.Context(), server.Listener.Addr().String()+"/test"+tc.reference, tc.transport)
			if got != tc.want {
				t.Fatalf("status = %q, want %q: %s", got, tc.want, detail)
			}
		})
	}
}

func TestCheckOneDockerCredentials(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", configDir)
	var authenticated atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "test-user" || password != "test-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="test-registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authenticated.Store(true)
		body := []byte(`{"schemaVersion":2}`)
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("Docker-Content-Digest", digest.FromBytes(body).String())
		_, _ = w.Write(body)
	}))
	defer server.Close()
	host := server.Listener.Addr().String()
	encoded := base64.StdEncoding.EncodeToString([]byte("test-user:test-password"))
	config := `{"auths":{"` + host + `":{"auth":"` + encoded + `"}}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	status, detail := checkOne(t.Context(), host+"/test:v1", operator.RegistryTransport{InsecureSkipTLSVerify: true})
	if status != "present" || !authenticated.Load() {
		t.Fatalf("authenticated registry check = %q: %s", status, detail)
	}
}
