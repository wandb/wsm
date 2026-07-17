package kind

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/wandb/wsm/pkg/kubectl"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	config "sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
	"sigs.k8s.io/kind/pkg/cluster"
)

// DefaultNodeImage is the kind/node image used when none is supplied.
// Pinned by digest so a mirrored copy must match this exact manifest.
const DefaultNodeImage = "kindest/node:v1.35.1@sha256:05d7bcdefbda08b4e038f644c4df690cdac3fba8b06f8289f30e10026720a1ab"

// CreateCluster creates a Kind cluster with specified name and number of worker nodes.
// If nodeImage is empty, DefaultNodeImage is used.
func CreateCluster(ctx context.Context, name string, workers int, httpPort int32, httpsPort int32, nodeImage string, insecureRegistryHost string) error {
	provider := cluster.NewProvider()

	// Check if cluster already exists
	exists, err := ClusterExists(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to check if cluster exists: %w", err)
	}
	if exists {
		return fmt.Errorf("cluster '%s' already exists", name)
	}

	// Generate Kind cluster config
	kindConfig := generateClusterConfig(workers, httpPort, httpsPort, nodeImage, insecureRegistryHost)
	// Create cluster using kind library
	if err := provider.Create(
		name,
		cluster.CreateWithV1Alpha4Config(&kindConfig),
		cluster.CreateWithDisplayUsage(true),
		cluster.CreateWithDisplaySalutation(true),
	); err != nil {
		return fmt.Errorf("failed to create kind cluster: %w", err)
	}

	if insecureRegistryHost != "" {
		if err := writeInsecureRegistryHostsConfig(ctx, name, insecureRegistryHost); err != nil {
			return err
		}
	}

	return nil
}

// DeleteCluster deletes a Kind cluster by name
func DeleteCluster(ctx context.Context, name string) error {
	provider := cluster.NewProvider()

	// Check if cluster exists
	exists, err := ClusterExists(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to check if cluster exists: %w", err)
	}
	if !exists {
		return fmt.Errorf("cluster '%s' does not exist", name)
	}

	// Delete cluster using kind library
	if err := provider.Delete(name, ""); err != nil {
		return fmt.Errorf("failed to delete kind cluster: %w", err)
	}

	return nil
}

// ListClusters returns the names of all local Kind clusters.
func ListClusters() ([]string, error) {
	provider := cluster.NewProvider()
	names, err := provider.List()
	if err != nil {
		return nil, fmt.Errorf("failed to list kind clusters: %w", err)
	}
	return names, nil
}

// ClusterExists checks if a Kind cluster with the given name exists
func ClusterExists(ctx context.Context, name string) (bool, error) {
	provider := cluster.NewProvider()
	clusters, err := provider.List()
	if err != nil {
		return false, fmt.Errorf("failed to list kind clusters: %w", err)
	}

	return slices.Contains(clusters, name), nil
}

// InstallMetricsServer installs and patches the Kubernetes metrics-server for Kind
func InstallMetricsServer(ctx context.Context) error {
	// Install metrics-server
	const metricsServerURL = "https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"
	resp, err := http.Get(metricsServerURL)
	if err != nil {
		return fmt.Errorf("failed to download metrics-server manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download metrics-server manifest: status code %d", resp.StatusCode)
	}

	manifest, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read metrics-server manifest: %w", err)
	}

	if err := kubectl.ApplyYAML(ctx, manifest); err != nil {
		return fmt.Errorf("failed to install metrics-server: %w", err)
	}

	// Patch metrics-server for Kind (insecure TLS)
	patchJSON := `[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]`
	if err := kubectl.PatchDeployment(ctx, "metrics-server", "kube-system", types.JSONPatchType, []byte(patchJSON)); err != nil {
		return fmt.Errorf("failed to patch metrics-server: %w", err)
	}

	// Wait for metrics-server to be ready
	if err := WaitForMetricsServer(ctx, 2*time.Minute); err != nil {
		return fmt.Errorf("metrics-server did not become ready: %w", err)
	}

	return nil
}

// SetKubectlContext sets the kubectl context to the specified Kind cluster
func SetKubectlContext(ctx context.Context, name string) {
	contextName := fmt.Sprintf("kind-%s", name)
	kubectl.SetContext(contextName)
}

// CleanupDistDirectory removes the dist/ directory if it exists
func CleanupDistDirectory() error {
	if _, err := os.Stat("dist"); err == nil {
		if err := os.RemoveAll("dist"); err != nil {
			return fmt.Errorf("failed to remove dist directory: %w", err)
		}
	}
	return nil
}

// LoadImageToCluster loads a Docker image into a Kind cluster
func LoadImageToCluster(ctx context.Context, imageName, clusterName string) error {
	fmt.Printf("  → Loading image '%s' into kind cluster '%s'...\n", imageName, clusterName)

	// Since the kind library doesn't have a simple one-liner for "docker save | kind load",
	// and it involves complex logic to handle different container runtimes and image formats,
	// we continue to use the kind CLI for this specific operation as it is the most reliable way
	// to ensure the image is correctly exported from the local docker daemon and loaded into kind nodes.
	cmd := exec.CommandContext(ctx, "kind", "load", "docker-image", imageName, "--name", clusterName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to load image into kind cluster: %w", err)
	}

	return nil
}

// generateClusterConfig creates a Kind cluster configuration with specified worker nodes.
// The control-plane node is always configured with:
//   - extraPortMappings: host:httpPort → container:httpPort, host:httpsPort → container:httpsPort
//   - ingress-ready node label so nginx-ingress can bind to those ports
//
// When insecureRegistryHost is non-empty, containerd is patched so kubelet pulls
// from that host over plain HTTP. Pairs with `wsm registry mirror --to <host> --insecure`
// and `wsm deploy-v2 operator --mirror-registry <host> --insecure-registry`.
func generateClusterConfig(workers int, httpPort int32, httpsPort int32, nodeImage string, insecureRegistryHost string) config.Cluster {
	if nodeImage == "" {
		nodeImage = DefaultNodeImage
	}

	controlPlane := config.Node{
		Role:  config.ControlPlaneRole,
		Image: nodeImage,
		ExtraPortMappings: []config.PortMapping{
			{ContainerPort: 31437, HostPort: httpPort, Protocol: config.PortMappingProtocolTCP},
			{ContainerPort: 30478, HostPort: httpsPort, Protocol: config.PortMappingProtocolTCP},
		},
		KubeadmConfigPatches: []string{`kind: InitConfiguration
nodeRegistration:
  kubeletExtraArgs:
    node-labels: "ingress-ready=true"
`},
	}

	kindConfig := config.Cluster{}
	kindConfig.Nodes = make([]config.Node, workers+1)
	kindConfig.Nodes[0] = controlPlane
	for i := 0; i < workers; i++ {
		kindConfig.Nodes[i+1] = config.Node{Role: config.WorkerRole, Image: nodeImage}
	}

	if insecureRegistryHost != "" {
		// Tell containerd to read per-host config from /etc/containerd/certs.d.
		// We write hosts.toml there in writeInsecureRegistryHostsConfig after
		// the cluster comes up.
		//
		// We can't put the registry endpoint config directly in the patch:
		// containerd v2.2 (kindest/node v1.35) chokes on the
		// `mirrors.X.endpoint` merge — Kind's mergo rewrites the entire CRI
		// plugin block into a different TOML layout that disables the CRI
		// gRPC service and kubelet fails with "unknown service
		// runtime.v1.RuntimeService". The config_path form is well-tested.
		kindConfig.ContainerdConfigPatches = []string{
			`[plugins."io.containerd.grpc.v1.cri".registry]
  config_path = "/etc/containerd/certs.d"
`,
		}
	}

	return kindConfig
}

// upstreamRegistryMirrors maps the public registries the managed-service images
// (ClickHouse/Kafka/MySQL/Redis/object-store operators + their data-plane pods)
// come from to their canonical server URLs. The operator hardcodes these image
// refs with no override knob, so the only way to make them pull from the mirror
// is a transparent containerd registry mirror: containerd requests the
// host-stripped path (e.g. quay.io/strimzi/kafka → <mirror>/v2/strimzi/kafka),
// which is exactly where `wsm registry mirror` pushes them.
//
// us-docker.pkg.dev is included only for the Bufstream Kafka image
// (us-docker.pkg.dev/buf-images-1/...), a managed data-plane image the operator
// emits with its upstream ref. W&B's own charts/operator/app images use
// mirror-host refs (Helm values + server-manifest rewrite → <mirror>/wandb/*),
// so they never transit this mirror and its project-stripped path doesn't
// collide with them.
var upstreamRegistryMirrors = map[string]string{
	"docker.io":         "https://registry-1.docker.io",
	"quay.io":           "https://quay.io",
	"ghcr.io":           "https://ghcr.io",
	"us-docker.pkg.dev": "https://us-docker.pkg.dev",
}

func writeInsecureRegistryHostsConfig(ctx context.Context, clusterName, insecureRegistryHost string) error {
	nodeName := clusterName + "-control-plane"

	// 1. The mirror host itself, served over plain HTTP. Covers references that
	// already point at <mirror>/... (W&B charts/operator/app images).
	script := hostsTomlScript(insecureRegistryHost, fmt.Sprintf(`server = "http://%s"

[host."http://%s"]
  capabilities = ["pull", "resolve"]
  skip_verify = true
`, insecureRegistryHost, insecureRegistryHost))

	// 2. Transparent mirrors for the public registries the managed-service images
	// live on, so their hardcoded refs resolve to the mirror. The `server` line
	// keeps online installs working: containerd falls back to upstream for any
	// image not present in the mirror.
	for host, server := range upstreamRegistryMirrors {
		script += hostsTomlScript(host, fmt.Sprintf(`server = "%s"

[host."http://%s"]
  capabilities = ["pull", "resolve"]
  skip_verify = true
`, server, insecureRegistryHost))
	}

	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", nodeName, "sh", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("configure insecure registry %s on %s: %w (%s)", insecureRegistryHost, nodeName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// hostsTomlScript returns a shell snippet that writes hosts.toml for one
// registry host under containerd's certs.d directory.
func hostsTomlScript(registryHost, hostsToml string) string {
	return fmt.Sprintf(`set -e
mkdir -p /etc/containerd/certs.d/%s
cat > /etc/containerd/certs.d/%s/hosts.toml <<'WSM_EOF'
%sWSM_EOF
`, registryHost, registryHost, hostsToml)
}

// InstallIngressNGINX installs the nginx ingress controller for Kind clusters and waits
// for it to be ready. This enables Ingress resources to work with the host port mappings
// configured in generateClusterConfig (host 8080 → container 80).
func InstallIngressNGINX(ctx context.Context) error {
	const manifestURL = "https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml"

	resp, err := http.Get(manifestURL)
	if err != nil {
		return fmt.Errorf("failed to download nginx ingress manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download nginx ingress manifest: status %d", resp.StatusCode)
	}

	manifest, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read nginx ingress manifest: %w", err)
	}

	if err := kubectl.ApplyYAML(ctx, manifest); err != nil {
		return fmt.Errorf("failed to apply nginx ingress manifest: %w", err)
	}

	return nil
}

// WaitForIngressNGINX waits until the nginx ingress controller pod is ready.
func WaitForIngressNGINX(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ready, err := ingressControllerReady(ctx)
		if err == nil && ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("nginx ingress controller did not become ready within %s", timeout)
}

func ingressControllerReady(ctx context.Context) (bool, error) {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return false, err
	}
	pods, err := cs.CoreV1().Pods("ingress-nginx").List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/component=controller",
	})
	if err != nil || len(pods.Items) == 0 {
		return false, err
	}
	for _, pod := range pods.Items {
		for _, cond := range pod.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "True" {
				return true, nil
			}
		}
	}
	return false, nil
}

func metricsServerReady(ctx context.Context) (bool, error) {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return false, err
	}
	pods, err := cs.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{
		LabelSelector: "k8s-app=metrics-server",
	})
	if err != nil || len(pods.Items) == 0 {
		return false, err
	}
	for _, pod := range pods.Items {
		for _, cond := range pod.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "True" {
				return true, nil
			}
		}
	}
	return false, nil
}

// WaitForMetricsServer waits until the metrics-server pod is ready.
func WaitForMetricsServer(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ready, err := metricsServerReady(ctx)
		if err == nil && ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("metrics-server did not become ready within %s", timeout)
}
