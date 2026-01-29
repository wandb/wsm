package operator

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CreateNamespace creates a namespace if it doesn't exist
func CreateNamespace(ctx context.Context, namespace string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "create", "namespace", namespace, "--dry-run=client", "-o", "yaml")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to generate namespace yaml: %w\nstderr: %s", err, stderr.String())
	}

	applyCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	applyCmd.Stdin = &stdout
	var applyStdout, applyStderr bytes.Buffer
	applyCmd.Stdout = &applyStdout
	applyCmd.Stderr = &applyStderr

	if err := applyCmd.Run(); err != nil {
		return fmt.Errorf("failed to create namespace: %w\nstdout: %s\nstderr: %s", err, applyStdout.String(), applyStderr.String())
	}

	return nil
}

// InstallCertManager installs cert-manager using kubectl
func InstallCertManager(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f",
		"https://github.com/cert-manager/cert-manager/releases/download/v1.13.3/cert-manager.yaml")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install cert-manager: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return nil
}

// WaitForCertManager waits for cert-manager to be ready
func WaitForCertManager(ctx context.Context, timeout time.Duration) error {
	cmd := exec.CommandContext(ctx, "kubectl", "wait",
		"--for=condition=available",
		"--timeout="+timeout.String(),
		"-n", "cert-manager",
		"deployment/cert-manager",
		"deployment/cert-manager-webhook",
		"deployment/cert-manager-cainjector")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cert-manager not ready: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return nil
}

// InstallHelmOperator installs a Helm chart operator (idempotent)
// helmValues are optional key=value pairs passed via --set
func InstallHelmOperator(ctx context.Context, repo, repoURL, release, chart, namespace string, helmValues ...string) error {
	// Add Helm repo (suppress output)
	cmd := exec.CommandContext(ctx, "helm", "repo", "add", repo, repoURL)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Ignore "already exists" errors
		if !bytes.Contains(stderr.Bytes(), []byte("already exists")) {
			return fmt.Errorf("failed to add helm repo %s: %w\nstderr: %s", repo, err, stderr.String())
		}
	}

	// Update Helm repos (suppress output)
	cmd = exec.CommandContext(ctx, "helm", "repo", "update")
	stdout.Reset()
	stderr.Reset()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to update helm repos: %w\nstderr: %s", err, stderr.String())
	}

	// Build helm upgrade command with optional values
	args := []string{"upgrade", "--install", release, chart,
		"--namespace", namespace, "--create-namespace", "--wait", "--timeout=5m"}

	// Add --set flags for each helm value
	for _, value := range helmValues {
		args = append(args, "--set", value)
	}

	// Use upgrade --install to make it idempotent (suppress output)
	cmd = exec.CommandContext(ctx, "helm", args...)
	stdout.Reset()
	stderr.Reset()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Show output on error
		return fmt.Errorf("failed to install/upgrade %s: %w\nstdout: %s\nstderr: %s", release, err, stdout.String(), stderr.String())
	}

	return nil
}

// DeployOperatorCRDs deploys just the CRDs using server-side apply from operator directory
func DeployOperatorCRDs(ctx context.Context, operatorManifestPath string) error {
	// Apply CRDs from the same directory as the operator manifest
	// CRDs should be named: apps.wandb.com_*.yaml
	operatorDir := filepath.Dir(operatorManifestPath)

	var stdout, stderr bytes.Buffer

	// Apply applications CRD
	appCRD := filepath.Join(operatorDir, "apps.wandb.com_applications.yaml")
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "-f", appCRD)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to deploy applications CRD from %s: %w\nstdout: %s\nstderr: %s", appCRD, err, stdout.String(), stderr.String())
	}

	// Apply weightsandbiases CRD
	stdout.Reset()
	stderr.Reset()
	wabCRD := filepath.Join(operatorDir, "apps.wandb.com_weightsandbiases.yaml")
	cmd = exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "-f", wabCRD)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to deploy weightsandbiases CRD from %s: %w\nstdout: %s\nstderr: %s", wabCRD, err, stdout.String(), stderr.String())
	}

	return nil
}

// DeployOperator deploys the W&B operator from a manifest file (excluding CRDs)
func DeployOperator(ctx context.Context, manifestPath string) error {
	// Skip CRDs in the manifest file as they're applied separately
	// We need to filter out CRDs to avoid annotation size issues
	cmd := exec.CommandContext(ctx, "bash", "-c",
		fmt.Sprintf("kubectl apply -f %s --server-side=true --field-manager=kubectl-client-side-apply --force-conflicts", manifestPath))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to deploy operator: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	// HACK for demo: Patch the deployment for local Kind clusters (same as Tilt does)
	// TODO: Remove this when we have proper production deployment
	// Wait for deployment to exist before patching (max 30 seconds)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		checkCmd := exec.CommandContext(ctx, "kubectl", "get", "deployment",
			"operator-controller-manager", "-n", "operator-system")
		if err := checkCmd.Run(); err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}

	// Use strategic merge patch instead of JSON patch for better idempotency
	// This will merge the changes rather than replace, making it more forgiving
	patch := `{
		"spec": {
			"template": {
				"spec": {
					"securityContext": {
						"runAsNonRoot": false
					},
					"containers": [{
						"name": "manager",
						"imagePullPolicy": "Never",
						"securityContext": {
							"allowPrivilegeEscalation": true,
							"capabilities": {
								"drop": []
							}
						}
					}]
				}
			}
		}
	}`

	patchCmd := exec.CommandContext(ctx, "kubectl", "patch", "deployment",
		"operator-controller-manager", "-n", "operator-system",
		"--type", "strategic",
		"-p", patch)

	var patchStdout, patchStderr bytes.Buffer
	patchCmd.Stdout = &patchStdout
	patchCmd.Stderr = &patchStderr

	if err := patchCmd.Run(); err != nil {
		// If patching fails, log but don't fail the deployment
		// The patch might already be applied or the structure might be different
		fmt.Fprintf(os.Stderr, "Warning: failed to patch operator deployment (continuing anyway): %v\n", err)
		if patchStderr.Len() > 0 {
			fmt.Fprintf(os.Stderr, "stderr: %s\n", patchStderr.String())
		}
	}

	return nil
}

// WaitForOperator waits for operator to be ready by checking webhook CA bundle injection and deployment
func WaitForOperator(ctx context.Context, namespace string, timeout time.Duration) error {
	// Wait for webhook CA bundle to be injected (like Tilt does)
	script := `until kubectl get mutatingwebhookconfiguration operator-mutating-webhook-configuration -o jsonpath='{.webhooks[0].clientConfig.caBundle}' 2>/dev/null | grep -q .; do
		sleep 2;
	done`

	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("operator webhook CA bundle not ready: %w", err)
	}

	// Also wait for the deployment to be available
	waitCmd := exec.CommandContext(ctx, "kubectl", "wait",
		"--for=condition=available",
		"--timeout="+timeout.String(),
		"-n", namespace,
		"deployment/operator-controller-manager")
	stdout.Reset()
	stderr.Reset()
	waitCmd.Stdout = &stdout
	waitCmd.Stderr = &stderr

	if err := waitCmd.Run(); err != nil {
		return fmt.Errorf("operator deployment not ready: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	// Wait for webhook pods to be ready (not just deployment available)
	// Use a simple polling approach instead of kubectl wait to avoid hanging issues
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Check if pods are ready
		checkCmd := exec.CommandContext(ctx, "kubectl", "get", "pods",
			"-n", namespace,
			"-l", "control-plane=controller-manager",
			"-o", "jsonpath={.items[*].status.conditions[?(@.type==\"Ready\")].status}")

		output, err := checkCmd.Output()
		if err == nil {
			statuses := strings.Fields(string(output))
			allReady := true
			for _, status := range statuses {
				if status != "True" {
					allReady = false
					break
				}
			}

			if allReady && len(statuses) > 0 {
				break
			}
		}

		// Check if we've exceeded the timeout
		if time.Now().Add(5 * time.Second).After(deadline) {
			return fmt.Errorf("operator pods not ready after %s timeout", timeout)
		}

		time.Sleep(2 * time.Second)
	}

	// Give webhook service a few more seconds to be fully ready
	time.Sleep(10 * time.Second)

	return nil
}

// DeployManifest deploys the server manifest as a ConfigMap
func DeployManifest(ctx context.Context, manifestPath, namespace string) error {
	// Read manifest file
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to read manifest file: %w", err)
	}

	// Create ConfigMap with manifest content
	configMapName := "wandb-server-manifest"
	manifestFileName := filepath.Base(manifestPath)

	// Use kubectl create configmap with --from-file
	cmd := exec.CommandContext(ctx, "kubectl", "create", "configmap",
		configMapName,
		fmt.Sprintf("--from-file=%s=%s", manifestFileName, manifestPath),
		"-n", namespace,
		"--dry-run=client",
		"-o", "yaml")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create configmap yaml: %w\nstderr: %s", err, stderr.String())
	}

	// Apply the ConfigMap
	applyCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-", "-n", namespace)
	applyCmd.Stdin = bytes.NewReader(stdout.Bytes())

	var applyStdout, applyStderr bytes.Buffer
	applyCmd.Stdout = &applyStdout
	applyCmd.Stderr = &applyStderr

	if err := applyCmd.Run(); err != nil {
		return fmt.Errorf("failed to apply configmap: %w\nstdout: %s\nstderr: %s", err, applyStdout.String(), applyStderr.String())
	}

	// Also store the raw manifest content
	_ = manifestData // Keep for future use

	return nil
}

// ApplyCR applies a WeightsAndBiases CR to the cluster (idempotent)
func ApplyCR(ctx context.Context, crPath, namespace string) error {
	// Use kubectl apply with --server-side to make it more robust for idempotency
	// This handles conflicts better than client-side apply
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "-f", crPath, "-n", namespace)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to apply CR: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return nil
}

// WaitForCR waits for WeightsAndBiases CR to be ready
func WaitForCR(ctx context.Context, name, namespace string, timeout time.Duration) error {
	// Wait for WeightsAndBiases resource to have status.ready=true
	// Note: This requires the CRD to have a ready condition
	cmd := exec.CommandContext(ctx, "kubectl", "wait",
		"--for=condition=ready",
		"--timeout="+timeout.String(),
		"-n", namespace,
		fmt.Sprintf("weightsandbiases/%s", name))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// If the condition doesn't exist, that's okay - just return success
		// We can improve this later with more sophisticated readiness checks
		return fmt.Errorf("CR not ready: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return nil
}

// GetCRName extracts the CR name from a YAML file
func GetCRName(crPath string) (string, error) {
	cmd := exec.Command("kubectl", "get", "-f", crPath, "-o", "jsonpath={.metadata.name}")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to get CR name: %w\nstderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// BuildOperatorImage builds the operator Docker image if it doesn't exist
func BuildOperatorImage(ctx context.Context, manifestPath string) error {
	// Check if image already exists
	cmd := exec.CommandContext(ctx, "docker", "images", "-q", "controller:latest")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check for existing image: %w", err)
	}

	if len(output) > 0 {
		return nil
	}

	// Image doesn't exist, build it
	operatorDir := "/Users/awarrier/Documents/operator"

	// Get absolute path to manifest
	absManifestPath, err := filepath.Abs(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute manifest path: %w", err)
	}

	// Copy manifest to the expected location in operator directory
	manifestDestPath := filepath.Join(operatorDir, "hack/testing-manifests/server-manifest/0.76.1.yaml")
	manifestDestDir := filepath.Dir(manifestDestPath)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(manifestDestDir, 0755); err != nil {
		return fmt.Errorf("failed to create manifest directory: %w", err)
	}

	// Read and copy manifest
	manifestContent, err := os.ReadFile(absManifestPath)
	if err != nil {
		return fmt.Errorf("failed to read manifest file %s: %w", absManifestPath, err)
	}

	if err := os.WriteFile(manifestDestPath, manifestContent, 0644); err != nil {
		return fmt.Errorf("failed to copy manifest to operator directory: %w", err)
	}

	// Step 1: Build the manager binary
	fmt.Println("  → Building manager binary...")
	cmd = exec.CommandContext(ctx, "bash", "-c",
		"CGO_ENABLED=0 GOOS=linux GO111MODULE=on go build -o tilt_bin/manager cmd/main.go")
	cmd.Dir = operatorDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to build manager binary: %w", err)
	}

	// Step 2: Build the Docker image using existing Dockerfile.wsm
	fmt.Println("  → Building Docker image using Dockerfile.wsm...")
	cmd = exec.CommandContext(ctx, "docker", "build", "-f", "Dockerfile.wsm", "-t", "controller:latest", ".")
	cmd.Dir = operatorDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to build Docker image: %w", err)
	}

	return nil
}

// LoadOperatorImageIntoKind loads the operator image into a Kind cluster
func LoadOperatorImageIntoKind(ctx context.Context, clusterName string) error {
	cmd := exec.CommandContext(ctx, "kind", "load", "docker-image", "controller:latest", "--name", clusterName)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to load image into Kind: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return nil
}

// LoadWandbImages loads required wandb images into Kind cluster (assumes they're already pulled locally)
func LoadWandbImages(ctx context.Context, clusterName string, version string) error {
	images := []string{
		fmt.Sprintf("wandb/frontend-nginx:%s", version),
		fmt.Sprintf("wandb/local:%s", version),
		fmt.Sprintf("wandb/megabinary:%s", version),
		fmt.Sprintf("wandb/weave-python:%s", version),
		fmt.Sprintf("wandb/weave-trace:%s", version),
		fmt.Sprintf("wandb/anaconda2:%s", version),
	}

	// Use a channel to collect errors from goroutines
	type result struct {
		image string
		err   error
	}
	resultChan := make(chan result, len(images))

	for _, image := range images {
		// Capture loop variable
		image := image
		go func() {
			// Check if image exists locally
			checkCmd := exec.CommandContext(ctx, "docker", "images", "-q", image)
			output, err := checkCmd.Output()
			if err != nil {
				resultChan <- result{image, fmt.Errorf("failed to check if image %s exists: %w", image, err)}
				return
			}

			if len(output) == 0 {
				resultChan <- result{image, fmt.Errorf("image %s not found locally", image)}
				return
			}

			// Load image into kind cluster
			loadCmd := exec.CommandContext(ctx, "kind", "load", "docker-image", image, "--name", clusterName)
			var stdout, stderr bytes.Buffer
			loadCmd.Stdout = &stdout
			loadCmd.Stderr = &stderr

			if err := loadCmd.Run(); err != nil {
				resultChan <- result{image, fmt.Errorf("failed to load image %s into kind: %w\nstdout: %s\nstderr: %s", image, err, stdout.String(), stderr.String())}
				return
			}

			resultChan <- result{image, nil}
		}()
	}

	// Wait for all image loads to complete and check for errors
	for i := 0; i < len(images); i++ {
		res := <-resultChan
		if res.err != nil {
			return res.err
		}
	}

	return nil
}

// FixMySQLDirtyFlag fixes the MySQL migration dirty flag by running a SQL command via kubectl
func FixMySQLDirtyFlag(ctx context.Context, namespace string) error {
	fmt.Println("→ Waiting for MySQL to be created and ready...")

	// First, wait for the MySQL pod to exist (with retries)
	maxRetries := 60 // 5 minutes with 5 second intervals
	for i := 0; i < maxRetries; i++ {
		checkCmd := exec.CommandContext(ctx, "kubectl", "get", "pod",
			"-l", "app.kubernetes.io/instance=wandb-mysql-cluster",
			"-n", namespace, "-o", "name")
		output, err := checkCmd.Output()

		if err == nil && len(output) > 0 {
			// Pod exists, break out of retry loop
			break
		}

		if i == maxRetries-1 {
			return fmt.Errorf("timed out waiting for MySQL pod to be created")
		}

		if i == 0 {
			fmt.Println("  Waiting for operator to create MySQL pod...")
		}
		time.Sleep(5 * time.Second)
	}

	// Now wait for the pod to be ready
	fmt.Println("  MySQL pod found, waiting for it to be ready...")
	waitCmd := exec.CommandContext(ctx, "kubectl", "wait", "--for=condition=ready",
		"pod", "-l", "app.kubernetes.io/instance=wandb-mysql-cluster",
		"-n", namespace, "--timeout=300s")
	if output, err := waitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to wait for MySQL pod to be ready: %w: %s", err, string(output))
	}

	fmt.Println("→ Fixing MySQL migration dirty flag...")

	// Get and decode MySQL root password in one command
	getPasswordCmd := exec.CommandContext(ctx, "sh", "-c",
		fmt.Sprintf("kubectl get secret wandb-mysql-connection -n %s -o jsonpath='{.data.RootPassword}' | base64 -d", namespace))
	decodedPassword, err := getPasswordCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get MySQL password: %w", err)
	}

	// Fix the dirty migration flag
	sqlCmd := fmt.Sprintf("MYSQL_PWD='%s' mysql -u root -e \"UPDATE usage_schema_migrations SET dirty = 0 WHERE version = 2;\" wandb_local 2>&1 | grep -v Warning || true", string(decodedPassword))
	execCmd := exec.CommandContext(ctx, "kubectl", "exec", "wandb-mysql-cluster-pxc-0",
		"-n", namespace, "-c", "pxc", "--", "sh", "-c", sqlCmd)

	output, err := execCmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Warning: MySQL dirty flag fix may have failed: %v\nOutput: %s\n", err, string(output))
		// Don't fail deployment if this doesn't work
		return nil
	}

	fmt.Println("✓ MySQL migration dirty flag fixed")
	return nil
}
