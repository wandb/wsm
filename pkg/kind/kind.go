package kind

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"

	"github.com/wandb/wsm/pkg/kubectl"
	"k8s.io/apimachinery/pkg/types"
	config "sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
	"sigs.k8s.io/kind/pkg/cluster"
)

// CreateCluster creates a Kind cluster with specified name and number of worker nodes
func CreateCluster(ctx context.Context, name string, workers int) error {
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
	kindConfig := generateClusterConfig(workers)
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
	defer resp.Body.Close()

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

// generateClusterConfig creates a Kind cluster configuration with specified worker nodes
func generateClusterConfig(workers int) config.Cluster {
	kindConfig := config.Cluster{}
	if workers > 0 {
		kindConfig.Nodes = make([]config.Node, workers+1)
		kindConfig.Nodes[0] = config.Node{Role: config.ControlPlaneRole, Image: "kindest/node:v1.35.1@sha256:05d7bcdefbda08b4e038f644c4df690cdac3fba8b06f8289f30e10026720a1ab"}
		for i := 0; i < workers; i++ {
			kindConfig.Nodes[i+1] = config.Node{Role: config.WorkerRole, Image: "kindest/node:v1.35.1@sha256:05d7bcdefbda08b4e038f644c4df690cdac3fba8b06f8289f30e10026720a1ab"}
		}
	}

	return kindConfig
}
