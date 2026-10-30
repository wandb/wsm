package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	testregistry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/wandb/wsm/pkg/operator"
	helmregistry "helm.sh/helm/v4/pkg/registry"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
)

func transportTestRegistry(t *testing.T, useTLS bool) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(testregistry.New(testregistry.Logger(log.New(io.Discard, "", 0))))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	if useTLS {
		server.StartTLS()
	} else {
		server.Start()
	}
	t.Cleanup(server.Close)
	return server
}

// Trust the source's certificate without skipping verification. Tests are not
// parallel because ORAS's default retry client uses http.DefaultTransport.
func trustRegistryCertificate(t *testing.T, server *httptest.Server) {
	t.Helper()
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport.TLSClientConfig = &tls.Config{RootCAs: roots}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous; transport.CloseIdleConnections() })
}

func testRegistryRepo(t *testing.T, ref string, transport operator.RegistryTransport) *remote.Repository {
	t.Helper()
	repo, err := registryRepository(ref, transport)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func pushTestBlob(t *testing.T, repo *remote.Repository, mediaType string, body []byte) ocispec.Descriptor {
	t.Helper()
	desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(body), Size: int64(len(body))}
	if err := repo.Push(t.Context(), desc, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	return desc
}

func pushTestManifest(t *testing.T, repo *remote.Repository, tag, configType, layerType string, config, layer []byte) ocispec.Descriptor {
	t.Helper()
	configDesc := pushTestBlob(t, repo, configType, config)
	layerDesc := pushTestBlob(t, repo, layerType, layer)
	body, err := json.Marshal(ocispec.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageManifest, Config: configDesc, Layers: []ocispec.Descriptor{layerDesc}})
	if err != nil {
		t.Fatal(err)
	}
	desc := pushTestBlob(t, repo, ocispec.MediaTypeImageManifest, body)
	if err := repo.Tag(t.Context(), desc, tag); err != nil {
		t.Fatal(err)
	}
	return desc
}

func seedTestImage(t *testing.T, ref string, transport operator.RegistryTransport) {
	t.Helper()
	repo := testRegistryRepo(t, ref, transport)
	pushTestManifest(t, repo, repo.Reference.Reference, ocispec.MediaTypeImageConfig, ocispec.MediaTypeImageLayer, []byte(`{"architecture":"amd64","os":"linux"}`), []byte("test-layer"))
}

func seedTestChart(t *testing.T, target, repository, chartName, version string) {
	t.Helper()
	chartVersion := strings.TrimPrefix(version, "v")
	files := map[string][]byte{
		chartName + "/Chart.yaml":         []byte(fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", chartName, chartVersion)),
		chartName + "/templates/pod.yaml": []byte("apiVersion: v1\nkind: Pod\nmetadata:\n  name: fixture\nspec:\n  containers:\n    - name: fixture\n      image: example.invalid/test/image:v1\n"),
	}
	layer, err := buildLayerTarGz(files)
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]string{"name": chartName, "version": chartVersion})
	if err != nil {
		t.Fatal(err)
	}
	repo := testRegistryRepo(t, target+"/"+repository, operator.RegistryTransport{PlainHTTP: true})
	pushTestManifest(t, repo, version, helmregistry.ConfigMediaType, helmregistry.ChartLayerMediaType, config, layer)
}

func TestMirrorServerManifestHTTPDestination(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("HELM_CACHE_HOME", t.TempDir())
	t.Setenv("HELM_REGISTRY_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "kubeconfig"))
	source := transportTestRegistry(t, true)
	trustRegistryCertificate(t, source)
	target := transportTestRegistry(t, false)
	sourceHost, targetHost := source.Listener.Addr().String(), target.Listener.Addr().String()
	version := "0.84.0"
	seedTestImage(t, sourceHost+"/wandb/api:"+version, operator.RegistryTransport{})
	files := map[string][]byte{"manifest.yaml": []byte(fmt.Sprintf("applications:\n  api:\n    image:\n      repository: %s/wandb/api\n      tag: %s\n", sourceHost, version))}
	if err := pushManifestArtifact(t.Context(), sourceHost, version, files, operator.RegistryTransport{}); err != nil {
		t.Fatal(err)
	}
	plainHTTP := operator.RegistryTransport{PlainHTTP: true}
	if err := mirrorServerManifest(t.Context(), targetHost, version, sourceHost+"/wandb/server-manifest", mustExclude(t, nil, nil, true), operator.RegistryTransport{}, plainHTTP, false); err != nil {
		t.Fatal(err)
	}
	mirrored, err := pullManifestYAMLFrom(t.Context(), targetHost+"/wandb/server-manifest", version, plainHTTP)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := collectManifestImages(mirrored, mustExclude(t, nil, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]struct{}{targetHost + "/wandb/api:" + version: {}}; !reflect.DeepEqual(imageSet(refs), want) {
		t.Fatalf("rewritten images = %v, want %v", imageSet(refs), want)
	}
	if status, detail := checkOne(t.Context(), targetHost+"/wandb/api:"+version, plainHTTP); status != "present" {
		t.Fatalf("mirrored application image = %s: %s", status, detail)
	}

	// Exercise the complete check command, including chart rendering, manifest
	// readback and every application image, with --plain-http --wandb-version.
	seedTestChart(t, targetHost, "wandb/charts/operator", "operator", defaultOperatorChartVersion)
	seedTestChart(t, targetHost, "jetstack/charts/cert-manager", "cert-manager", operator.CertManagerVersion)
	seedTestChart(t, targetHost, "nginx/charts/nginx-gateway-fabric", "nginx-gateway-fabric", operator.NginxGatewayVersion)
	seedTestChart(t, targetHost, "prometheus-community/charts/kube-state-metrics", "kube-state-metrics", operator.KubeStateMetricsVersion)
	plan, err := buildMirrorPlan(t.Context(), targetHost, defaultOperatorChartVersion, true, plainHTTP)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range plan {
		if !strings.Contains(item.dst, "/charts/") {
			seedTestImage(t, item.dst, plainHTTP)
		}
	}
	cmd := registryCheckCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--registry", targetHost, "--plain-http", "--wandb-version", version, "--skip-managed-images", "--fail-on-missing"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCopyImageTargetTransport(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	source := transportTestRegistry(t, true)
	trustRegistryCertificate(t, source)
	src := source.Listener.Addr().String() + "/test/image:v1"
	seedTestImage(t, src, operator.RegistryTransport{})
	for _, tc := range []struct {
		name      string
		tls       bool
		transport operator.RegistryTransport
		wantError string
	}{
		{name: "verified HTTPS", tls: true},
		{name: "explicit HTTP", transport: operator.RegistryTransport{PlainHTTP: true}},
		{name: "HTTP and TLS skip", transport: operator.RegistryTransport{PlainHTTP: true, InsecureSkipTLSVerify: true}},
		{name: "HTTPS with TLS skip", tls: true, transport: operator.RegistryTransport{InsecureSkipTLSVerify: true}},
		{name: "TLS skip does not enable HTTP", transport: operator.RegistryTransport{InsecureSkipTLSVerify: true}, wantError: "HTTP response to HTTPS client"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := transportTestRegistry(t, tc.tls)
			dst := target.Listener.Addr().String() + "/test/image:v1"
			skipped, err := copyImage(t.Context(), src, dst, tc.transport)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected %q, got %v", tc.wantError, err)
				}
				return
			}
			if err != nil || skipped {
				t.Fatalf("first copy: skipped=%v err=%v", skipped, err)
			}
			skipped, err = copyImage(t.Context(), src, dst, tc.transport)
			if err != nil || !skipped {
				t.Fatalf("repeat copy: skipped=%v err=%v", skipped, err)
			}
		})
	}
}

func TestCopyImageTargetOptionsDoNotRelaxSource(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	for _, tc := range []struct {
		name string
		tls  bool
		want string
	}{
		{name: "HTTP source stays HTTPS", want: "HTTP response to HTTPS client"},
		{name: "source certificate stays verified", tls: true, want: "x509:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := transportTestRegistry(t, tc.tls)
			target := transportTestRegistry(t, false)
			_, err := copyImage(t.Context(), source.Listener.Addr().String()+"/test/image:v1", target.Listener.Addr().String()+"/test/image:v1", operator.RegistryTransport{PlainHTTP: true, InsecureSkipTLSVerify: true})
			if err == nil || !strings.Contains(err.Error(), "resolve source") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected verified HTTPS source error %q, got %v", tc.want, err)
			}
		})
	}
}

func TestCopyImageMultiPlatformLargeConfig(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	source := transportTestRegistry(t, true)
	trustRegistryCertificate(t, source)
	target := transportTestRegistry(t, false)
	sourceRepo := testRegistryRepo(t, source.Listener.Addr().String()+"/test/multi", operator.RegistryTransport{})
	config := []byte(`{"padding":"` + strings.Repeat("x", 5*1024*1024) + `"}`)
	var manifests []ocispec.Descriptor
	for _, arch := range []string{"amd64", "arm64"} {
		desc := pushTestManifest(t, sourceRepo, arch, ocispec.MediaTypeImageConfig, ocispec.MediaTypeImageLayer, config, []byte(arch))
		desc.Platform = &ocispec.Platform{OS: "linux", Architecture: arch}
		manifests = append(manifests, desc)
	}
	index, err := json.Marshal(ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageIndex, Manifests: manifests})
	if err != nil {
		t.Fatal(err)
	}
	desc := pushTestBlob(t, sourceRepo, ocispec.MediaTypeImageIndex, index)
	if err := sourceRepo.Tag(t.Context(), desc, "v1"); err != nil {
		t.Fatal(err)
	}
	dst := target.Listener.Addr().String() + "/test/multi:v1"
	if _, err := copyImage(t.Context(), sourceRepo.Reference.String()+":v1", dst, operator.RegistryTransport{PlainHTTP: true}); err != nil {
		t.Fatal(err)
	}
	targetRepo := testRegistryRepo(t, dst, operator.RegistryTransport{PlainHTTP: true})
	got, err := targetRepo.Resolve(t.Context(), "v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != desc.Digest {
		t.Fatalf("index digest = %s, want %s", got.Digest, desc.Digest)
	}
	for _, platform := range manifests {
		if _, err := content.FetchAll(t.Context(), targetRepo, platform); err != nil {
			t.Fatal(err)
		}
	}
	configDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromBytes(config), Size: int64(len(config))}
	copied, err := content.FetchAll(t.Context(), targetRepo, configDesc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(copied, config) {
		t.Fatal("large config was not preserved")
	}
}

func TestServerManifestTargetTransport(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	files := map[string][]byte{"manifest.yaml": []byte("applications: {}\n")}
	for _, tc := range []struct {
		name      string
		tls       bool
		transport operator.RegistryTransport
		wantError string
	}{
		{name: "untrusted HTTPS rejected", tls: true, wantError: "x509:"},
		{name: "private HTTPS", tls: true, transport: operator.RegistryTransport{InsecureSkipTLSVerify: true}},
		{name: "HTTP", transport: operator.RegistryTransport{PlainHTTP: true}},
		{name: "HTTP with TLS skip", transport: operator.RegistryTransport{PlainHTTP: true, InsecureSkipTLSVerify: true}},
		{name: "TLS skip does not enable HTTP", transport: operator.RegistryTransport{InsecureSkipTLSVerify: true}, wantError: "HTTP response to HTTPS client"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := transportTestRegistry(t, tc.tls)
			host := server.Listener.Addr().String()
			err := pushManifestArtifact(t.Context(), host, "0.84.0", files, tc.transport)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected %q, got %v", tc.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := pullManifestYAMLFrom(t.Context(), host+"/wandb/server-manifest", "0.84.0", tc.transport)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, files) {
				t.Fatalf("manifest = %v, want %v", got, files)
			}
		})
	}
}

func TestUpstreamImageRepositoryNormalization(t *testing.T) {
	digestRef := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct{ input, want string }{
		{"busybox", "docker.io/library/busybox:latest"},
		{"docker.io/busybox:latest", "docker.io/library/busybox:latest"},
		{"index.docker.io/busybox:latest", "docker.io/library/busybox:latest"},
		{"altinity/clickhouse-operator:0.26.3", "docker.io/altinity/clickhouse-operator:0.26.3"},
		{"chrislusf/seaweedfs-operator:1.0.32", "docker.io/chrislusf/seaweedfs-operator:1.0.32"},
		{"quay.io/jetstack/cert-manager:v1", "quay.io/jetstack/cert-manager:v1"},
		{"busybox@" + digestRef, "docker.io/library/busybox@" + digestRef},
		{"localhost:5000/test/image:v1", "localhost:5000/test/image:v1"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			repo, err := upstreamImageRepository(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if repo.Reference.String() != tc.want || repo.PlainHTTP {
				t.Fatalf("source = %s, plainHTTP = %v; want %s over HTTPS", repo.Reference.String(), repo.PlainHTTP, tc.want)
			}
		})
	}
}
