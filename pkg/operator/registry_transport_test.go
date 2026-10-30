package operator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"helm.sh/helm/v4/pkg/registry"
)

func TestRenderChartImagesRegistryTransport(t *testing.T) {
	t.Setenv("HELM_CACHE_HOME", t.TempDir())
	t.Setenv("HELM_CONFIG_HOME", t.TempDir())
	t.Setenv("HELM_REGISTRY_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "config"))
	cases := []struct {
		name      string
		tls       bool
		transport RegistryTransport
		wantError string
	}{
		{name: "HTTPS verifies certificates by default", tls: true, wantError: "x509:"},
		{name: "HTTPS with certificate verification skipped", tls: true, transport: RegistryTransport{InsecureSkipTLSVerify: true}},
		{name: "HTTP cannot contact HTTPS", tls: true, transport: RegistryTransport{PlainHTTP: true}, wantError: "400"},
		{name: "TLS skip does not override HTTP", tls: true, transport: RegistryTransport{PlainHTTP: true, InsecureSkipTLSVerify: true}, wantError: "400"},
		{name: "HTTPS cannot contact HTTP", wantError: "HTTP response to HTTPS client"},
		{name: "TLS skip does not allow HTTP fallback", transport: RegistryTransport{InsecureSkipTLSVerify: true}, wantError: "HTTP response to HTTPS client"},
		{name: "explicit HTTP", transport: RegistryTransport{PlainHTTP: true}},
		{name: "explicit HTTP with TLS skip", transport: RegistryTransport{PlainHTTP: true, InsecureSkipTLSVerify: true}},
	}
	handler := registryChartFixture(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(handler)
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			if tc.tls {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			ref := "oci://" + server.Listener.Addr().String() + "/fixture"
			images, err := RenderChartImages(t.Context(), ref, "1.0.0", nil, tc.transport)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected error containing %q, got %v", tc.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{"example.com/test/image:v1"}; !reflect.DeepEqual(images, want) {
				t.Fatalf("rendered images = %v, want %v", images, want)
			}
		})
	}
}

// Serve a complete OCI Helm chart so successful cases exercise chart retrieval
// and rendering, not just reaching an HTTP endpoint.
func registryChartFixture(t *testing.T) http.Handler {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	files := map[string]string{
		"fixture/Chart.yaml":         "apiVersion: v2\nname: fixture\nversion: 1.0.0\n",
		"fixture/templates/pod.yaml": "apiVersion: v1\nkind: Pod\nmetadata:\n  name: fixture\nspec:\n  containers:\n    - name: fixture\n      image: example.com/test/image:v1\n",
	}
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	config := []byte(`{"name":"fixture","version":"1.0.0"}`)
	chart := archive.Bytes()
	descriptor := func(mediaType string, data []byte) ocispec.Descriptor {
		return ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
	}
	manifest, err := json.Marshal(ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    descriptor(registry.ConfigMediaType, config),
		Layers:    []ocispec.Descriptor{descriptor(registry.ChartLayerMediaType, chart)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		mediaType := "application/octet-stream"
		switch r.URL.Path {
		case "/v2/":
			body, mediaType = []byte("{}"), "application/json"
		case "/v2/fixture/manifests/1.0.0", "/v2/fixture/manifests/" + digest.FromBytes(manifest).String():
			body, mediaType = manifest, ocispec.MediaTypeImageManifest
		case "/v2/fixture/blobs/" + digest.FromBytes(config).String():
			body = config
		case "/v2/fixture/blobs/" + digest.FromBytes(chart).String():
			body = chart
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("Docker-Content-Digest", digest.FromBytes(body).String())
		_, _ = w.Write(body)
	})
}
