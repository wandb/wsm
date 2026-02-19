package operator

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/go-connections/tlsconfig"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/cli"
	"helm.sh/helm/v4/pkg/registry"
	v1 "helm.sh/helm/v4/pkg/release/v1"
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

// DeployOperator deploys the W&B operator chart version specified.  The chart is called operator and is available in oci://us-docker.pkg.dev/wandb-production/public/wandb/charts
func DeployOperator(ctx context.Context, namespace string, version string) error {
	const repositoryURL = "oci://us-docker.pkg.dev/wandb-production/public/wandb/charts"
	const chartName = "operator"
	const chartRef = repositoryURL + "/" + chartName
	const releaseName = "wandb-operator"

	// Initialize Helm settings
	settings := cli.New()
	settings.SetNamespace(namespace)

	// Initialize action configuration
	actionConfig, err := initActionConfig(settings)
	if err != nil {
		return fmt.Errorf("failed to initialize action config: %w", err)
	}

	// Create registry client
	registryClient, err := newRegistryClient(settings, "", "", "", false, false)
	if err != nil {
		return fmt.Errorf("failed to create registry client: %w", err)
	}
	actionConfig.RegistryClient = registryClient

	// Check if release already exists
	releaseExists, err := checkReleaseExists(actionConfig, releaseName)
	if err != nil {
		return fmt.Errorf("failed to check if release exists: %w", err)
	}

	releaseValues := map[string]interface{}{
		"wandb": map[string]interface{}{
			"install": false,
		},
	}

	if releaseExists {
		// Create upgrade action
		upgradeClient := action.NewUpgrade(actionConfig)
		upgradeClient.Namespace = namespace
		upgradeClient.Version = version
		upgradeClient.WaitStrategy = "hookOnly"

		// Get the chart
		cp, err := upgradeClient.ChartPathOptions.LocateChart(chartRef, settings)
		if err != nil {
			return fmt.Errorf("failed to locate chart: %w", err)
		}

		// Load the chart
		chartRequested, err := loader.Load(cp)
		if err != nil {
			return fmt.Errorf("failed to load chart: %w", err)
		}

		// Run the upgrade
		_, err = upgradeClient.RunWithContext(ctx, releaseName, chartRequested, releaseValues)
		if err != nil {
			return fmt.Errorf("failed to upgrade operator chart: %w", err)
		}
	} else {
		// Create install action
		installClient := action.NewInstall(actionConfig)
		installClient.Namespace = namespace
		installClient.ReleaseName = releaseName
		installClient.Version = version
		installClient.WaitStrategy = "hookOnly"

		// Get the chart
		cp, err := installClient.ChartPathOptions.LocateChart(chartRef, settings)
		if err != nil {
			return fmt.Errorf("failed to locate chart: %w", err)
		}

		// Load the chart
		chartRequested, err := loader.Load(cp)
		if err != nil {
			return fmt.Errorf("failed to load chart: %w", err)
		}

		// Run the install
		_, err = installClient.RunWithContext(ctx, chartRequested, releaseValues)
		if err != nil {
			return fmt.Errorf("failed to install operator chart: %w", err)
		}
	}

	return nil
}

// checkReleaseExists checks if a Helm release exists
func checkReleaseExists(actionConfig *action.Configuration, releaseName string) (bool, error) {
	listClient := action.NewList(actionConfig)
	listClient.SetStateMask()

	releases, err := listClient.Run()
	if err != nil {
		return false, err
	}
	for _, r := range releases {
		release := r.(*v1.Release)
		if release.Name == releaseName {
			return true, nil
		}
	}
	return false, nil
}

// TODO refactor these into the helm pkg

var helmDriver string = os.Getenv("HELM_DRIVER")

func initActionConfig(settings *cli.EnvSettings) (*action.Configuration, error) {
	return initActionConfigList(settings, false)
}

func initActionConfigList(settings *cli.EnvSettings, allNamespaces bool) (*action.Configuration, error) {

	actionConfig := new(action.Configuration)

	namespace := func() string {
		// For list action, you can pass an empty string instead of settings.Namespace() to list
		// all namespaces
		if allNamespaces {
			return ""
		}
		return settings.Namespace()
	}()

	if err := actionConfig.Init(
		settings.RESTClientGetter(),
		namespace,
		helmDriver); err != nil {
		return nil, err
	}

	return actionConfig, nil
}

func newRegistryClient(settings *cli.EnvSettings, certFile, keyFile, caFile string, insecureSkipTLSVerify, plainHTTP bool) (*registry.Client, error) {

	opts := []registry.ClientOption{
		registry.ClientOptDebug(settings.Debug),
		registry.ClientOptEnableCache(true),
		registry.ClientOptWriter(os.Stderr),
		registry.ClientOptCredentialsFile(settings.RegistryConfig),
	}

	if plainHTTP {
		opts = append(opts, registry.ClientOptPlainHTTP())
	}

	if certFile != "" && keyFile != "" || caFile != "" || insecureSkipTLSVerify {
		tlsConf, err := tlsconfig.Client(tlsconfig.Options{
			InsecureSkipVerify: insecureSkipTLSVerify,
			CAFile:             caFile,
			KeyFile:            keyFile,
			CertFile:           certFile,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to load client TLS certs: %w", err)
		}

		opts = append(opts, registry.ClientOptHTTPClient(&http.Client{
			Transport: &http.Transport{
				TLSClientConfig: tlsConf,
				Proxy:           http.ProxyFromEnvironment,
			},
		}))
	}

	// Create a new registry client
	registryClient, err := registry.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize registry client: %w", err)
	}

	return registryClient, nil
}

// WaitForOperator waits for operator to be ready by checking webhook CA bundle injection and deployment
func WaitForOperator(ctx context.Context, namespace string, timeout time.Duration) error {
	// Wait for webhook CA bundle to be injected (like Tilt does)
	script := `until kubectl get mutatingwebhookconfiguration wandb-operator-mutating-webhook-configuration -o jsonpath='{.webhooks[0].clientConfig.caBundle}' 2>/dev/null | grep -q .; do
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
		"deployment/wandb-operator")
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
			"-l", "app.kubernetes.io/name=wandb-operator",
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
