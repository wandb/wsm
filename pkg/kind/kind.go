package kind

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"time"

	"github.com/wandb/wsm/pkg/kubectl"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	config "sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
	"sigs.k8s.io/kind/pkg/cluster"
)

// CreateCluster creates a Kind cluster with specified name and number of worker nodes
func CreateCluster(ctx context.Context, name string, workers int, httpPort int32, httpsPort int32) error {
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
	kindConfig := generateClusterConfig(workers, httpPort, httpsPort)
	// Create cluster using kind library
	if err := provider.Create(
		name,
		cluster.CreateWithV1Alpha4Config(&kindConfig),
		cluster.CreateWithDisplayUsage(true),
		cluster.CreateWithDisplaySalutation(true),
	); err != nil {
		return fmt.Errorf("failed to create kind cluster: %w", err)
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
func generateClusterConfig(workers int, httpPort int32, httpsPort int32) config.Cluster {
	const nodeImage = "kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1592d7752b164530a36f855cb0f2bdcbadd48"

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
	return kindConfig
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
