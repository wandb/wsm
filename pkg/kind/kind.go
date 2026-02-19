package kind

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CreateCluster creates a Kind cluster with specified name and number of worker nodes
func CreateCluster(ctx context.Context, name string, workers int) error {
	// Check if cluster already exists
	exists, err := ClusterExists(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to check if cluster exists: %w", err)
	}
	if exists {
		return fmt.Errorf("cluster '%s' already exists", name)
	}

	// Generate Kind cluster config
	config := generateClusterConfig(workers)

	// Create cluster using kind CLI
	cmd := exec.CommandContext(ctx, "kind", "create", "cluster", "--name", name, "--config=-")
	cmd.Stdin = strings.NewReader(config)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create kind cluster: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return nil
}

// DeleteCluster deletes a Kind cluster by name
func DeleteCluster(ctx context.Context, name string) error {
	// Check if cluster exists
	exists, err := ClusterExists(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to check if cluster exists: %w", err)
	}
	if !exists {
		return fmt.Errorf("cluster '%s' does not exist", name)
	}

	// Delete cluster using kind CLI
	cmd := exec.CommandContext(ctx, "kind", "delete", "cluster", "--name", name)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete kind cluster: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return nil
}

// ClusterExists checks if a Kind cluster with the given name exists
func ClusterExists(ctx context.Context, name string) (bool, error) {
	cmd := exec.CommandContext(ctx, "kind", "get", "clusters")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to get kind clusters: %w", err)
	}

	clusters := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, cluster := range clusters {
		if strings.TrimSpace(cluster) == name {
			return true, nil
		}
	}

	return false, nil
}

// InstallMetricsServer installs and patches the Kubernetes metrics-server for Kind
func InstallMetricsServer(ctx context.Context) error {
	// Install metrics-server
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f",
		"https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install metrics-server: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	// Patch metrics-server for Kind (insecure TLS)
	patchJSON := `[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]`
	cmd = exec.CommandContext(ctx, "kubectl", "patch", "-n", "kube-system",
		"deployment", "metrics-server", "--type=json", "-p", patchJSON)
	stdout.Reset()
	stderr.Reset()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to patch metrics-server: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return nil
}

// SetKubectlContext sets the kubectl context to the specified Kind cluster
func SetKubectlContext(ctx context.Context, name string) error {
	contextName := fmt.Sprintf("kind-%s", name)
	cmd := exec.CommandContext(ctx, "kubectl", "config", "use-context", contextName)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set kubectl context: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return nil
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

// CheckDependencies verifies that required CLI tools are installed
func CheckDependencies() error {
	requiredTools := []string{"kind", "kubectl", "jq"}

	for _, tool := range requiredTools {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s is required but not installed. Please install %s", tool, tool)
		}
	}

	return nil
}

// CreateDeploymentMarker creates a ConfigMap marker to track wsm-managed deployments
// Note: Assumes the namespace already exists (created by operator manifest)
func CreateDeploymentMarker(ctx context.Context, clusterName, namespace string) error {
	markerYAML := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: wsm-deployment-marker
  namespace: %s
  labels:
    app.kubernetes.io/managed-by: wsm
data:
  cluster-name: "%s"
  created-by: "wsm"
  components: "kind-cluster,cert-manager,operator,third-party-operators,wandb-cr"
`, namespace, clusterName)

	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(markerYAML)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create deployment marker: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return nil
}

// HasDeploymentMarker checks if a deployment marker exists
func HasDeploymentMarker(ctx context.Context, namespace string) (bool, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "configmap", "wsm-deployment-marker", "-n", namespace)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// If not found, return false without error
		if strings.Contains(stderr.String(), "not found") {
			return false, nil
		}
		return false, fmt.Errorf("failed to check for deployment marker: %w\nstderr: %s", err, stderr.String())
	}

	return true, nil
}

// DeleteDeploymentMarker removes the deployment marker ConfigMap
func DeleteDeploymentMarker(ctx context.Context, namespace string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "delete", "configmap", "wsm-deployment-marker", "-n", namespace, "--ignore-not-found=true")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete deployment marker: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return nil
}

// LoadImageToCluster loads a Docker image into a Kind cluster
func LoadImageToCluster(ctx context.Context, imageName, clusterName string) error {
	fmt.Printf("  → Loading image '%s' into kind cluster '%s'...\n", imageName, clusterName)

	cmd := exec.CommandContext(ctx, "kind", "load", "docker-image", imageName, "--name", clusterName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to load image into kind cluster: %w", err)
	}

	return nil
}

// generateClusterConfig creates a Kind cluster configuration with specified worker nodes
func generateClusterConfig(workers int) string {
	config := `kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: kindest/node:v1.35.1@sha256:05d7bcdefbda08b4e038f644c4df690cdac3fba8b06f8289f30e10026720a1ab
`
	for i := 0; i < workers; i++ {
		config += "- role: worker\n  image: kindest/node:v1.35.1@sha256:05d7bcdefbda08b4e038f644c4df690cdac3fba8b06f8289f30e10026720a1ab\n"
	}
	return config
}
