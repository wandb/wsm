package operator

import (
	"context"
	"strings"
	"testing"
)

// The extractor is pure and network-free: it must find images from container
// image scalars, initContainers, {repository,tag} CR maps, and --…-image args in
// both "--flag=val" and "--flag", "val" forms.
func TestExtractImages(t *testing.T) {
	const manifests = `
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      initContainers:
        - name: init
          image: busybox:1.36
      containers:
        - name: controller
          image: ghcr.io/cybozu-go/moco:0.36.0
          args:
            - --agent-image
            - ghcr.io/cybozu-go/moco-agent:0.16.0
            - --mysqld-exporter-image=ghcr.io/cybozu-go/moco/mysqld_exporter:0.19.0.1
            - --zap-log-level=info
---
apiVersion: cert-manager.io/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
        - name: cm
          image: quay.io/jetstack/cert-manager-controller:v1.20.2
          args:
            - --acme-http01-solver-image=quay.io/jetstack/cert-manager-acmesolver:v1.20.2
---
apiVersion: gateway.nginx.org/v1alpha2
kind: NginxProxy
spec:
  image:
    repository: ghcr.io/nginx/nginx-gateway-fabric/nginx
    tag: "2.5.1"
`
	got := map[string]bool{}
	for _, i := range extractImages(manifests) {
		got[i] = true
	}
	want := []string{
		"busybox:1.36",
		"ghcr.io/cybozu-go/moco:0.36.0",
		"ghcr.io/cybozu-go/moco-agent:0.16.0",
		"ghcr.io/cybozu-go/moco/mysqld_exporter:0.19.0.1",
		"quay.io/jetstack/cert-manager-controller:v1.20.2",
		"quay.io/jetstack/cert-manager-acmesolver:v1.20.2",
		"ghcr.io/nginx/nginx-gateway-fabric/nginx:2.5.1",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("expected image %q not extracted; got=%v", w, keys(got))
		}
	}
	// A non-image flag value must not be mistaken for an image.
	if got["info"] {
		t.Errorf("--zap-log-level value leaked in as an image")
	}
	if len(got) != len(want) {
		t.Errorf("extracted %d images, want %d: %v", len(got), len(want), keys(got))
	}
}

// Network-dependent: renders the real operator chart. Skips if the registry is
// unreachable so it never breaks offline/CI runs.
func TestOperatorChartImages_Real(t *testing.T) {
	imgs, err := OperatorChartImages(context.Background(), OperatorChartRepo, "2.0.0-beta.3", nil, false)
	if err != nil {
		t.Skipf("cannot reach operator chart registry: %v", err)
	}
	got := map[string]bool{}
	for _, i := range imgs {
		got[i] = true
	}
	// Correct managed-operator + moco-sidecar set for 2.0.0-beta.3, including the
	// drift fix (seaweedfs-operator 1.0.32, not the old hard-coded 1.0.21).
	want := []string{
		"altinity/clickhouse-operator:0.26.3",
		"altinity/metrics-exporter:0.26.3",
		"chrislusf/seaweedfs-operator:1.0.32",
		"ghcr.io/cybozu-go/moco:0.36.0",
		"ghcr.io/cybozu-go/moco-agent:0.16.0",
		"ghcr.io/cybozu-go/moco/fluent-bit:5.0.2.1",
		"ghcr.io/cybozu-go/moco/mysqld_exporter:0.19.0.1",
		"quay.io/opstree/redis-operator:v0.22.2",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("expected rendered image %q missing; got=%v", w, keys(got))
		}
	}
	// The alpine/k8s crdHook image is a phantom (hook disabled by default) and must
	// not be derived.
	for i := range got {
		if strings.HasPrefix(i, "alpine/k8s") {
			t.Errorf("phantom image %q should not be derived", i)
		}
	}
}

// Network-dependent: renders the three tool charts. Skips if unreachable.
func TestToolChartImages_Real(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		fn   func(context.Context, string, bool) ([]string, error)
		ref  string
		want []string
		// absent must not appear (cert-manager's startupapicheck is disabled).
		absent string
	}{
		{
			name: "cert-manager",
			fn:   CertManagerImages,
			ref:  "oci://quay.io/jetstack/charts/cert-manager",
			want: []string{
				"quay.io/jetstack/cert-manager-controller:" + CertManagerVersion,
				"quay.io/jetstack/cert-manager-webhook:" + CertManagerVersion,
				"quay.io/jetstack/cert-manager-cainjector:" + CertManagerVersion,
				"quay.io/jetstack/cert-manager-acmesolver:" + CertManagerVersion,
			},
			absent: "cert-manager-startupapicheck",
		},
		{
			name: "nginx-gateway",
			fn:   NginxGatewayImages,
			ref:  "oci://ghcr.io/nginx/charts/nginx-gateway-fabric",
			want: []string{
				"ghcr.io/nginx/nginx-gateway-fabric:" + NginxGatewayVersion,
				"ghcr.io/nginx/nginx-gateway-fabric/nginx:" + NginxGatewayVersion,
			},
		},
		{
			name: "kube-state-metrics",
			fn:   KubeStateMetricsImages,
			ref:  "oci://ghcr.io/prometheus-community/charts/kube-state-metrics",
			want: []string{
				"registry.k8s.io/kube-state-metrics/kube-state-metrics:" + KubeStateMetricsImageTag,
			},
		},
	}
	for _, c := range cases {
		imgs, err := c.fn(ctx, c.ref, false)
		if err != nil {
			t.Skipf("%s: cannot reach registry: %v", c.name, err)
		}
		got := map[string]bool{}
		for _, i := range imgs {
			got[i] = true
		}
		for _, w := range c.want {
			if !got[w] {
				t.Errorf("%s: expected image %q missing; got=%v", c.name, w, keys(got))
			}
		}
		if c.absent != "" {
			for i := range got {
				if strings.Contains(i, c.absent) {
					t.Errorf("%s: image %q should not be derived", c.name, i)
				}
			}
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
