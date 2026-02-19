package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/kind"
	"github.com/wandb/wsm/pkg/operator"
	"gopkg.in/yaml.v3"
)

func init() {
	rootCmd.AddCommand(DeployV2Cmd())
	rootCmd.AddCommand(ClusterCmd())
}

var (
	defaultCR = `apiVersion: apps.wandb.com/v2
kind: WeightsAndBiases
metadata:
  name: wandb-dev-v2
spec:
  wandb:
    hostname: http://localhost:8080
    version: 0.78.0-pre-manifest-testing.11
    internalServiceAuth: 
      enabled: true
    features:
      proxy: true
  size: dev
  retentionPolicy:
    onDelete: purge
  mysql:
    enabled: true
    telemetry:
      enabled: true
  redis:
    enabled: true
    telemetry:
      enabled: true
  kafka:
    enabled: true
    telemetry:
      enabled: true
  minio:
    enabled: true
    telemetry:
      enabled: true
  clickhouse:
    enabled: true
    telemetry:
      enabled: true
`
)

func performDeploy(setupCluster bool, wait bool, clusterName string, workers int, operatorChartVersion string, wandbVersion string, crFile string, namespace string, operatorNamespace string) error {
	ctx := context.Background()

	// Calculate total steps based on flags
	totalSteps := 4 // Always: cert-manager, deploy operator, create CR
	if setupCluster {
		totalSteps++
	}
	if wait {
		totalSteps++
	}
	currentStep := 1

	// TODO: Persist manifest file to Kubernetes (e.g., as ConfigMap) for operator to read
	// Currently the operator uses a baked-in manifest from the image (/0.76.1.yaml).
	// The operator should evolve to read the manifest from a ConfigMap instead of embedding it.
	// This will allow users to specify different manifest versions via --manifest-path.

	fmt.Printf("\nDeploying W&B (v%s) to cluster '%s'\n\n", wandbVersion, clusterName)

	// Step 1: Setup K8s cluster if requested
	if setupCluster {
		fmt.Printf("[%d/%d] Setting up cluster (%d workers)...", currentStep, totalSteps, workers)
		start := time.Now()

		if err := kind.CheckDependencies(); err != nil {
			fmt.Println(" ✗")
			return err
		}

		exists, err := kind.ClusterExists(ctx, clusterName)
		if err != nil {
			fmt.Println(" ✗")
			return fmt.Errorf("failed to check if cluster exists: %w", err)
		}

		if !exists {
			if err := kind.CreateCluster(ctx, clusterName, workers); err != nil {
				fmt.Println(" ✗")
				return err
			}

			if err := kind.SetKubectlContext(ctx, clusterName); err != nil {
				fmt.Println(" ✗")
				return err
			}

			if err := kind.InstallMetricsServer(ctx); err != nil {
				fmt.Println(" ✗")
				return err
			}
		}

		fmt.Printf(" ✓ (%s)\n", time.Since(start).Round(time.Second))
		currentStep++
	}

	// Step 2: Install cert-manager
	fmt.Printf("[%d/%d] Installing cert-manager...", currentStep, totalSteps)
	start := time.Now()

	if err := operator.InstallCertManager(ctx); err != nil {
		fmt.Println(" ✗")
		return err
	}

	if err := operator.WaitForCertManager(ctx, 5*time.Minute); err != nil {
		fmt.Println(" ✗")
		return err
	}

	fmt.Printf(" ✓ (%s)\n", time.Since(start).Round(time.Second))
	currentStep++

	// Step 3: Create infra-operators namespace
	if err := operator.CreateNamespace(ctx, operatorNamespace); err != nil {
		return err
	}

	// Step 4: Deploy W&B operator
	fmt.Printf("[%d/%d] Deploying Required operators...", currentStep, totalSteps)
	start = time.Now()

	if err := operator.DeployOperator(ctx, operatorNamespace, operatorChartVersion); err != nil {
		fmt.Println(" ✗")
		return err
	}

	if err := operator.WaitForOperator(ctx, operatorNamespace, 5*time.Minute); err != nil {
		fmt.Println(" ✗")
		return err
	}

	if setupCluster {
		if err := kind.CreateDeploymentMarker(ctx, clusterName, operatorNamespace); err != nil {
			fmt.Println(" ✗")
			return err
		}
	}

	fmt.Printf(" ✓ (%s)\n", time.Since(start).Round(time.Second))
	currentStep++

	// Step 5: Create W&B instance
	fmt.Printf("[%d/%d] Creating W&B instance...", currentStep, totalSteps)
	start = time.Now()

	// Step 3: Create infra-operators namespace
	if err := operator.CreateNamespace(ctx, namespace); err != nil {
		return err
	}

	if err := operator.ApplyCR(ctx, crFile, namespace); err != nil {
		fmt.Println(" ✗")
		return err
	}

	fmt.Printf(" ✓ (%s)\n", time.Since(start).Round(time.Second))
	currentStep++

	// Step 6: Wait for CR to be ready (if requested)
	if wait {
		fmt.Printf("[%d/%d] Waiting for W&B instance to be ready...", currentStep, totalSteps)
		start = time.Now()

		// Extract CR name from the CR file
		crName, err := extractCRName(crFile)
		if err != nil {
			fmt.Println(" ✗")
			return fmt.Errorf("failed to extract CR name: %w", err)
		}

		if err := operator.WaitForCRReady(ctx, namespace, crName, 30*time.Minute); err != nil {
			fmt.Println(" ✗")
			return err
		}

		fmt.Printf(" ✓ (%s)\n", time.Since(start).Round(time.Second))
	}

	return nil
}

// DeployV2Cmd returns the deploy-v2 command with subcommands
func DeployV2Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy-v2",
		Short: "Deploy v2 operator and resources",
		Long:  `Deploy the v2 operator, server manifest, and custom resources`,
	}

	cmd.AddCommand(v2OperatorCmd())

	return cmd
}

func v2OperatorCmd() *cobra.Command {
	var setupCluster bool
	var wait bool
	var clusterName string
	var workers int
	var operatorVersion string
	var operatorChartVersion string
	var wandbVersion string
	var crFile string
	var licenseFile string
	var wandbNamespace string
	var operatorNamespace string

	cmd := &cobra.Command{
		Use:   "operator",
		Short: "Deploy the v2 operator",
		Long:  `Deploy the v2 operator with specified versions and configuration`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check required dependencies
			if _, err := exec.LookPath("kubectl"); err != nil {
				return fmt.Errorf("kubectl is required but not found in PATH. Please install kubectl: https://kubernetes.io/docs/tasks/tools/")
			}
			if _, err := exec.LookPath("helm"); err != nil {
				return fmt.Errorf("helm is required but not found in PATH. Please install helm: https://helm.sh/docs/intro/install/")
			}

			// Use built-in default CR if the file doesn't exist
			if _, err := os.Stat(crFile); os.IsNotExist(err) {
				// Write default CR to a temp file
				tempDir := os.TempDir()
				tempCRFile := filepath.Join(tempDir, "wsm-default-cr.yaml")
				if err := os.WriteFile(tempCRFile, []byte(defaultCR), 0644); err != nil {
					return fmt.Errorf("failed to create default CR: %w", err)
				}
				crFile = tempCRFile
			}

			// Inject license into CR if provided
			if licenseFile != "" {
				modifiedCRFile, err := injectLicenseIntoCR(crFile, licenseFile)
				if err != nil {
					return fmt.Errorf("failed to inject license: %w", err)
				}
				crFile = modifiedCRFile
			}

			// Perform the deployment
			deployStart := time.Now()
			if err := performDeploy(setupCluster, wait, clusterName, workers, operatorChartVersion, wandbVersion, crFile, wandbNamespace, operatorNamespace); err != nil {
				fmt.Printf("\n✗ Deployment failed: %v\n", err)
				return err
			}

			// Success summary
			totalTime := time.Since(deployStart).Round(time.Second)
			fmt.Printf("\n✓ Deployment complete! (%s total)\n\n", totalTime)
			fmt.Println("Access your W&B instance:")
			if setupCluster {
				fmt.Printf("  • Kubectl context: kind-%s\n", clusterName)
			}
			fmt.Printf("  • Namespace: %s\n", wandbNamespace)
			fmt.Printf("  • Status: kubectl get wandb -n %s\n", wandbNamespace)
			fmt.Println()
			return nil
		},
	}

	cmd.Flags().BoolVar(&setupCluster, "setup-k8s-cluster", false, "Setup a Kind cluster before deploying")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for the W&B instance to be ready (status.ready == true)")
	cmd.Flags().StringVar(&clusterName, "cluster-name", "kind", "Name of the Kind cluster (only used with --setup-k8s-cluster)")
	cmd.Flags().IntVar(&workers, "workers", 3, "Number of worker nodes (only used with --setup-k8s-cluster)")

	cmd.Flags().StringVar(&operatorVersion, "operator-version", "", "Operator image version (e.g., v2.0.0)")
	cmd.Flags().StringVar(&operatorChartVersion, "operator-chart-version", "1.5.2", "Operator Chart version (e.g., v2.0.0)")

	cmd.Flags().StringVar(&wandbVersion, "wandb-version", "0.78.0-pre-operator-v2-no-app.0", "Server manifest version (e.g., 0.76.1)")

	// CR deployment
	cmd.Flags().StringVar(&crFile, "cr-file", "", "Path to WeightsAndBiases CR YAML (uses built-in default if not provided)")
	cmd.Flags().StringVar(&licenseFile, "license-file", "", "Path to W&B license file (optional, injected into spec.wandb.license)")

	// Namespaces
	cmd.Flags().StringVar(&wandbNamespace, "wandbNamespace", "wandb", "Namespace for CR")
	cmd.Flags().StringVar(&operatorNamespace, "operator-namespace", "wandb-operators", "Namespace for operator")

	return cmd
}

// injectLicenseIntoCR reads a CR file, injects the license, and writes to a temp file
func injectLicenseIntoCR(crPath, licensePath string) (string, error) {
	// Read the CR YAML
	crData, err := os.ReadFile(crPath)
	if err != nil {
		return "", fmt.Errorf("failed to read CR file: %w", err)
	}

	// Read the license
	licenseData, err := os.ReadFile(licensePath)
	if err != nil {
		return "", fmt.Errorf("failed to read license file: %w", err)
	}
	license := strings.TrimSpace(string(licenseData))

	// Parse CR YAML
	var cr map[string]interface{}
	if err := yaml.Unmarshal(crData, &cr); err != nil {
		return "", fmt.Errorf("failed to parse CR YAML: %w", err)
	}

	// Inject license into spec.wandb.license
	spec, ok := cr["spec"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("CR missing 'spec' field")
	}

	wandb, ok := spec["wandb"].(map[string]interface{})
	if !ok {
		wandb = make(map[string]interface{})
		spec["wandb"] = wandb
	}

	wandb["license"] = license

	// Marshal back to YAML
	modifiedData, err := yaml.Marshal(cr)
	if err != nil {
		return "", fmt.Errorf("failed to marshal modified CR: %w", err)
	}

	// Write to temp file
	tempDir := os.TempDir()
	tempFile := filepath.Join(tempDir, "wsm-modified-cr.yaml")
	if err := os.WriteFile(tempFile, modifiedData, 0644); err != nil {
		return "", fmt.Errorf("failed to write modified CR: %w", err)
	}

	return tempFile, nil
}

// extractCRName reads a CR YAML file and extracts the metadata.name field
func extractCRName(crPath string) (string, error) {
	crData, err := os.ReadFile(crPath)
	if err != nil {
		return "", fmt.Errorf("failed to read CR file: %w", err)
	}

	var cr map[string]interface{}
	if err := yaml.Unmarshal(crData, &cr); err != nil {
		return "", fmt.Errorf("failed to parse CR YAML: %w", err)
	}

	metadata, ok := cr["metadata"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("CR missing 'metadata' field")
	}

	name, ok := metadata["name"].(string)
	if !ok {
		return "", fmt.Errorf("CR missing 'metadata.name' field")
	}

	return name, nil
}

func performTeardown(clusterName string) error {
	ctx := context.Background()

	fmt.Printf("→ Tearing down cluster '%s'...\n", clusterName)

	// Delete the Kind cluster
	if err := kind.DeleteCluster(ctx, clusterName); err != nil {
		return err
	}

	// Cleanup dist directory
	if err := kind.CleanupDistDirectory(); err != nil {
		// Non-fatal, just warn
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	return nil
}

// ClusterCmd returns the cluster command with subcommands
func ClusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage Kubernetes clusters",
		Long:  `Create and delete Kubernetes clusters for local development`,
	}

	cmd.AddCommand(clusterTeardownCmd())
	cmd.AddCommand(clusterCleanupCmd())

	return cmd
}

func clusterTeardownCmd() *cobra.Command {
	var clusterName string

	cmd := &cobra.Command{
		Use:   "teardown",
		Short: "Teardown Kind cluster and cleanup",
		Long:  `Delete the Kind cluster and cleanup resources`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := performTeardown(clusterName); err != nil {
				fmt.Printf("✗ Teardown failed: %v\n", err)
				return err
			}

			fmt.Printf("✓ Kind cluster '%s' deleted successfully\n", clusterName)
			return nil
		},
	}

	cmd.Flags().StringVar(&clusterName, "name", "kind", "Name of the Kind cluster to delete")

	return cmd
}

func performCleanup(clusterName string, deleteCluster bool, operatorNamespace string, namespace string) error {
	ctx := context.Background()

	// Check for deployment marker
	fmt.Println("→ Checking for wsm deployment marker...")
	hasMarker, err := kind.HasDeploymentMarker(ctx, operatorNamespace)
	if err != nil {
		return err
	}

	if !hasMarker {
		return fmt.Errorf("no wsm deployment marker found - cluster may not be managed by wsm")
	}

	// TODO: Delete W&B CR (respects retentionPolicy)
	fmt.Println("→ Deleting W&B CR...")
	// operator.DeleteCR(ctx, namespace)

	// TODO: Delete operator
	fmt.Println("→ Deleting operator...")
	// operator.DeleteOperator(ctx, operatorNamespace)

	// TODO: Delete third-party operators
	fmt.Println("→ Deleting third-party operators...")
	// operator.DeleteThirdPartyOperators(ctx)

	// TODO: Delete cert-manager
	fmt.Println("→ Deleting cert-manager...")
	// operator.DeleteCertManager(ctx)

	// Delete the deployment marker
	fmt.Println("→ Removing deployment marker...")
	if err := kind.DeleteDeploymentMarker(ctx, operatorNamespace); err != nil {
		return err
	}

	// Optionally delete the Kind cluster
	if deleteCluster {
		fmt.Printf("→ Deleting Kind cluster '%s'...\n", clusterName)
		if err := kind.DeleteCluster(ctx, clusterName); err != nil {
			return err
		}

		if err := kind.CleanupDistDirectory(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		}
	}

	return nil
}

func clusterCleanupCmd() *cobra.Command {
	var clusterName string
	var deleteCluster bool
	var operatorNamespace string
	var namespace string

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Cleanup wsm-managed resources",
		Long:  `Delete all resources deployed by wsm, including operator, CR, and optionally the cluster`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := performCleanup(clusterName, deleteCluster, operatorNamespace, namespace); err != nil {
				fmt.Printf("✗ Cleanup failed: %v\n", err)
				return err
			}

			fmt.Println("✓ Cleanup completed successfully")
			if deleteCluster {
				fmt.Printf("→ Kind cluster '%s' deleted\n", clusterName)
			} else {
				fmt.Printf("→ Kind cluster '%s' preserved\n", clusterName)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&clusterName, "name", "kind", "Name of the Kind cluster")
	cmd.Flags().BoolVar(&deleteCluster, "delete-cluster", false, "Also delete the Kind cluster")
	cmd.Flags().StringVar(&operatorNamespace, "operator-namespace", "operator-system", "Namespace where operator is deployed")
	cmd.Flags().StringVar(&namespace, "namespace", "default", "Namespace where CR is deployed")

	return cmd
}
