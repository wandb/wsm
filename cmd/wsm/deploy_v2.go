package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/kind"
	"github.com/wandb/wsm/pkg/operator"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/ptr"

	"github.com/wandb/operator/api/v2"
)

func init() {
	rootCmd.AddCommand(DeployV2Cmd())
	rootCmd.AddCommand(ClusterCmd())
}

var (
	wandbCR = &v2.WeightsAndBiases{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps.wandb.com/v2",
			Kind:       "WeightsAndBiases",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "wandb-dev-v2",
		},
		Spec: v2.WeightsAndBiasesSpec{
			Wandb: v2.WandbAppSpec{
				Hostname: "http://localhost:8080",
				Version:  "0.78.0-pre-manifest-testing.11",
				Features: map[string]bool{"proxy": true},
				InternalServiceAuth: v2.InternalServiceAuth{
					Enabled: ptr.Bool(true),
				},
			},
			MySQL: v2.WBMySQLSpec{
				WBInfraSpec: v2.WBInfraSpec{Enabled: true},
				Telemetry:   v2.Telemetry{Enabled: true},
			},
			Redis: v2.WBRedisSpec{
				WBInfraSpec: v2.WBInfraSpec{Enabled: true},
				Telemetry:   v2.Telemetry{Enabled: true},
			},
			Kafka: v2.WBKafkaSpec{
				WBInfraSpec: v2.WBInfraSpec{Enabled: true},
				Telemetry:   v2.Telemetry{Enabled: true},
			},
			Minio: v2.WBMinioSpec{
				WBInfraSpec: v2.WBInfraSpec{Enabled: true},
				Telemetry:   v2.Telemetry{Enabled: true},
			},
			ClickHouse: v2.WBClickHouseSpec{
				WBInfraSpec: v2.WBInfraSpec{Enabled: true},
				Telemetry:   v2.Telemetry{Enabled: true},
			},
		},
	}
)

func performDeploy(setupCluster bool, wait bool, clusterName string, workers int, operatorChartVersion string, wandbVersion string, operatorNamespace string) error {
	ctx := context.Background()

	// Calculate total steps based on flags
	totalSteps := 3 // Always: cert-manager, deploy operator, create CR
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

		err := performCreateCluster(ctx, clusterName, workers)
		if err != nil {
			return err
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

	// Step 3: Create infra-operators wandbNamespace
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

	// Step 3: Create infra-operators wandbNamespace
	if err := operator.CreateNamespace(ctx, wandbCR.Namespace); err != nil {
		return err
	}

	if err := operator.ApplyCR(ctx, wandbCR); err != nil {
		fmt.Println(" ✗")
		return err
	}

	fmt.Printf(" ✓ (%s)\n", time.Since(start).Round(time.Second))
	currentStep++

	// Step 6: Wait for CR to be ready (if requested)
	if wait {
		fmt.Printf("[%d/%d] Waiting for W&B instance to be ready...", currentStep, totalSteps)
		start = time.Now()

		if err := operator.WaitForCRReady(ctx, wandbCR.Namespace, wandbCR.Name, 30*time.Minute); err != nil {
			fmt.Println(" ✗")
			return err
		}

		fmt.Printf(" ✓ (%s)\n", time.Since(start).Round(time.Second))
	}

	return nil
}

func performCreateCluster(ctx context.Context, clusterName string, workers int) error {
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

		kind.SetKubectlContext(ctx, clusterName)

		if err := kind.InstallMetricsServer(ctx); err != nil {
			fmt.Println(" ✗")
			return err
		}
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
	var license string
	var licenseFile string
	var wandbNamespace string
	var operatorNamespace string

	cmd := &cobra.Command{
		Use:   "operator",
		Short: "Deploy the v2 operator",
		Long:  `Deploy the v2 operator with specified versions and configuration`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if crFile != "" {
				var err error
				wandbCR, err = readCRFile(crFile)
				if err != nil {
					fmt.Printf("failed to read CR file: %w", err)
					return err
				}
			}

			if license != "" || licenseFile != "" {
				if license != "" {
					wandbCR.Spec.Wandb.License = license
				}
				if licenseFile != "" {
					licenseData, err := os.ReadFile(licenseFile)
					if err != nil {
						fmt.Printf("failed to read license file: %w", err)
						return err
					}
					wandbCR.Spec.Wandb.License = strings.TrimSpace(string(licenseData))
				}
			}

			wandbCR.Namespace = wandbNamespace
			wandbCR.Spec.Wandb.Version = wandbVersion

			// Perform the deployment
			deployStart := time.Now()
			if err := performDeploy(setupCluster, wait, clusterName, workers, operatorChartVersion, wandbVersion, operatorNamespace); err != nil {
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
	// TODO readd this when the CR reports ready properly
	//cmd.Flags().BoolVar(&wait, "wait", false, "Wait for the W&B instance to be ready (status.ready == true)")
	cmd.Flags().StringVar(&clusterName, "cluster-name", "kind", "Name of the Kind cluster (only used with --setup-k8s-cluster)")
	cmd.Flags().IntVar(&workers, "workers", 0, "Number of worker nodes (only used with --setup-k8s-cluster)")

	cmd.Flags().StringVar(&operatorVersion, "operator-version", "", "Operator image version (e.g., v2.0.0) - defaults to value in the chart")
	cmd.Flags().StringVar(&operatorChartVersion, "operator-chart-version", "1.5.2", "Operator Chart version (e.g., v2.0.0)")

	cmd.Flags().StringVar(&wandbVersion, "wandb-version", "0.78.0-pre-operator-v2-no-app.0", "Server manifest version (e.g., 0.76.1)")

	// CR deployment
	cmd.Flags().StringVar(&crFile, "cr-file", "", "Path to WeightsAndBiases CR YAML (uses built-in default if not provided)")
	cmd.Flags().StringVar(&license, "license", "", "W&B license string (optional, injected into spec.wandb.license)")
	cmd.Flags().StringVar(&licenseFile, "license-file", "", "Path to W&B license file (optional, injected into spec.wandb.license)")

	// Namespaces
	cmd.Flags().StringVar(&wandbNamespace, "wandbNamespace", "wandb", "Namespace for CR")
	cmd.Flags().StringVar(&operatorNamespace, "operator-namespace", "wandb-operators", "Namespace for operator")

	return cmd
}

func readCRFile(crPath string) (*v2.WeightsAndBiases, error) {
	crData, err := os.ReadFile(crPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read CR file: %w", err)
	}
	cr := &v2.WeightsAndBiases{}
	err = yaml.Unmarshal(crData, cr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse CR YAML: %w", err)
	}
	return cr, nil
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

	cmd.AddCommand(clusterCreateCmd())
	cmd.AddCommand(clusterTeardownCmd())
	cmd.AddCommand(clusterCleanupCmd())

	return cmd
}

func clusterCreateCmd() *cobra.Command {
	var clusterName string
	var workers int

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new kind cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := performCreateCluster(context.Background(), clusterName, workers); err != nil {
				fmt.Printf("✗ Cluster Create failed: %v\n", err)
				return err
			}
			fmt.Printf("✓ Kind cluster '%s' created successfully\n", clusterName)
			return nil
		},
	}

	cmd.Flags().StringVar(&clusterName, "cluster-name", "kind", "Name of the Kind cluster (only used with --setup-k8s-cluster)")
	cmd.Flags().IntVar(&workers, "workers", 0, "Number of worker nodes (only used with --setup-k8s-cluster)")

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
