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

// DeployOperatorCRDs deploys just the CRDs using server-side apply from operator manifest directory
func DeployOperatorCRDs(ctx context.Context, operatorManifestDir string) error {
	// Apply CRDs from the operator manifest directory
	// CRDs should be named: apps.wandb.com_*.yaml
	var stdout, stderr bytes.Buffer

	// Apply applications CRD
	appCRD := filepath.Join(operatorManifestDir, "apps.wandb.com_applications.yaml")
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "-f", appCRD)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to deploy applications CRD from %s: %w\nstdout: %s\nstderr: %s", appCRD, err, stdout.String(), stderr.String())
	}

	// Apply weightsandbiases CRD
	stdout.Reset()
	stderr.Reset()
	wabCRD := filepath.Join(operatorManifestDir, "apps.wandb.com_weightsandbiases.yaml")
	cmd = exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "-f", wabCRD)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to deploy weightsandbiases CRD from %s: %w\nstdout: %s\nstderr: %s", wabCRD, err, stdout.String(), stderr.String())
	}

	return nil
}

// DeployOperator deploys the W&B operator from a manifest directory (excluding CRDs)
func DeployOperator(ctx context.Context, operatorManifestDir string) error {
	// Apply operator.yaml from the manifest directory
	// CRDs are applied separately to avoid annotation size issues
	operatorManifest := filepath.Join(operatorManifestDir, "operator.yaml")

	cmd := exec.CommandContext(ctx, "bash", "-c",
		fmt.Sprintf("kubectl apply -f %s --server-side=true --field-manager=kubectl-client-side-apply --force-conflicts", operatorManifest))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to deploy operator from %s: %w\nstdout: %s\nstderr: %s", operatorManifest, err, stdout.String(), stderr.String())
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

// WaitForCRReady waits for a WeightsAndBiases CR to reach ready state
func WaitForCRReady(ctx context.Context, namespace, crName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for CR %s/%s to be ready after %v", namespace, crName, timeout)
			}

			// Check if CR is ready
			cmd := exec.CommandContext(ctx, "kubectl", "get", "wandb", crName,
				"-n", namespace,
				"-o", "jsonpath={.status.ready}")

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			if err := cmd.Run(); err != nil {
				// CR might not exist yet or error getting status, continue waiting
				continue
			}

			ready := strings.TrimSpace(stdout.String())
			if ready == "true" {
				return nil
			}
		}
	}
}
