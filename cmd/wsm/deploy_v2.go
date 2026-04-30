package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/kind"
	"github.com/wandb/wsm/pkg/kubectl"
	"github.com/wandb/wsm/pkg/operator"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/ptr"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	certmanagermetav1 "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	"github.com/wandb/operator/api/v2"
)

func init() {
	rootCmd.AddCommand(DeployV2Cmd())
	rootCmd.AddCommand(ClusterCmd())
}

// TODO once an official release publishes a manifest, we should switch to lookup up the most recent non-dev release and not have a default.
const defaultWandbVersion = "0.80.0"

const (
	certManagerInstallModeAuto  = "auto"
	certManagerInstallModeTrue  = "true"
	certManagerInstallModeFalse = "false"

	nginxGatewayInstallModeAuto  = "auto"
	nginxGatewayInstallModeTrue  = "true"
	nginxGatewayInstallModeFalse = "false"
)

var (
	wandbCR = &v2.WeightsAndBiases{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps.wandb.com/v2",
			Kind:       "WeightsAndBiases",
		},
		Spec: v2.WeightsAndBiasesSpec{
			Wandb: v2.WandbAppSpec{
				Hostname: "http://localhost:8080",
				Features: map[string]bool{},
				InternalServiceAuth: v2.InternalServiceAuth{
					Enabled: ptr.Bool(false),
				},
			},
			MySQL: v2.MySQLSpec{
				ManagedMysql: &v2.ManagedMysqlSpec{
					Telemetry: v2.Telemetry{
						Enabled: false,
					},
				},
			},
			Redis: v2.RedisSpec{
				ManagedRedis: &v2.ManagedRedisSpec{
					Telemetry: v2.Telemetry{
						Enabled: false,
					},
				},
			},
			Kafka: v2.KafkaSpec{
				ManagedKafka: &v2.ManagedKafkaSpec{
					Telemetry: v2.Telemetry{
						Enabled: false,
					},
				},
			},
			ObjectStore: v2.ObjectStoreSpec{
				ManagedObjectStore: &v2.ManagedObjectStoreSpec{
					Telemetry: v2.Telemetry{
						Enabled: false,
					},
				},
			},
			ClickHouse: v2.ClickHouseSpec{
				ManagedClickHouse: &v2.ManagedClickHouseSpec{
					Telemetry: v2.Telemetry{
						Enabled: false,
					},
				},
			},
		},
	}
)

// DeployV2Cmd returns the deploy-v2 command with subcommands
func DeployV2Cmd() *cobra.Command {
	var kubeContext string
	cmd := &cobra.Command{
		Use:   "deploy-v2",
		Short: "Deploy v2 operator and resources",
		Long:  `Deploy the v2 operator, server manifest, and custom resources`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if kubeContext == "" {
				return errors.New("context is required")
			}

			kubectl.SetContext(kubeContext)
			return nil
		},
	}
	cmd.PersistentFlags().StringVar(&kubeContext, "context", "", "name of the kubeconfig context to use (Required)")
	// CR deployment
	cmd.PersistentFlags().String("cr-file", "", "Path to WeightsAndBiases CR YAML (uses built-in default if not provided)")
	cmd.PersistentFlags().Bool("create-ca", true, "Create a self-signed CA certificate for the W&B instance")
	cmd.PersistentFlags().Bool("create-aws-ingress-class", false, "Create an AWS Ingress Class for the W&B instance (requires --ingress-class to be set)")
	cmd.PersistentFlags().Bool("create-aws-storage-class", false, "Create a Storage class for the W&B instance")
	cmd.PersistentFlags().String("ingress-class", "", "Enable Ingress support with the specified ingress class")
	cmd.PersistentFlags().String("gateway-class", "nginx", "Enable Gateway API support with the specified gateway class")
	cmd.PersistentFlags().String("issuer-name", "", "TLS issuer name for Gateway API")
	cmd.PersistentFlags().Bool("add-ingress-annotations", false, "Add cloud provider annotations to Ingress or Gateway API")
	cmd.PersistentFlags().String("license", "", "W&B license string (optional, injected into spec.wandb.license)")
	cmd.PersistentFlags().String("license-file", "", "Path to W&B license file (optional, injected into spec.wandb.license)")
	cmd.PersistentFlags().String("observability-mode", "off", "Enable observability for applications")
	cmd.PersistentFlags().String("retention-policy", "detach", "Retention policy for W&B instance (detach, purge) - defaults to detach")
	cmd.PersistentFlags().String("size", "small", "W&B instance size (dev, micro, small, medium, large, xlarge, 2xlarge, 4xlarge)")
	cmd.PersistentFlags().String("wandb-hostname", "http://localhost:8080", "Hostname to use for the W&B instance")
	cmd.PersistentFlags().String("wandb-name", "wandb", "Name of the W&B instance")
	cmd.PersistentFlags().String("wandb-version", "", "Server manifest version (e.g., 0.76.1)")
	cmd.PersistentFlags().String("wandb-namespace", "wandb", "Namespace for CR")
	// TODO readd this when the CR reports ready properly
	//cmd.Flags().Bool("wait", false, "Wait for the W&B instance to be ready (status.ready == true)")

	cmd.AddCommand(operatorDeployCmd())
	cmd.AddCommand(wandbCmd())

	return cmd
}

func wandbCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wandb",
		Short: "Manage W&B instances",
		Long:  `Manage W&B instances`,
	}

	cmd.AddCommand(wandbCreateCmd())
	cmd.AddCommand(wandbDestroyCmd())
	cmd.AddCommand(wandbGetCACertCmd())

	return cmd
}

func wandbDestroyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "Destroy an instance of W&B",
		Long:  `Destroy an instance of W&B`,
		RunE: func(cmd *cobra.Command, args []string) error {
			wandbNamespace, _ := cmd.Flags().GetString("wandb-namespace")
			wandbName, _ := cmd.Flags().GetString("wandb-name")

			ctx := context.Background()

			err := destroyWandbCR(ctx, wandbName, wandbNamespace)
			if err != nil {
				return err
			}

			return nil
		},
	}

	return cmd
}

func wandbGetCACertCmd() *cobra.Command {
	var outputDir string

	cmd := &cobra.Command{
		Use:   "get-ca-cert",
		Short: "Write the W&B CA certificate to a local file",
		Long:  `Retrieve the W&B deployment CA certificate and write it to a local file named after the W&B CR`,
		RunE: func(cmd *cobra.Command, args []string) error {
			wandbNamespace, _ := cmd.Flags().GetString("wandb-namespace")
			wandbName, _ := cmd.Flags().GetString("wandb-name")

			outputPath, err := exportWandbCACert(kubectl.GetSecretDataMap, os.WriteFile, wandbName, wandbNamespace, outputDir)
			if err != nil {
				return err
			}

			fmt.Printf("Wrote W&B CA certificate to %s\n", outputPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&outputDir, "output-dir", ".", "Directory to write the CA certificate file")

	return cmd
}

func exportWandbCACert(
	getSecretDataMap func(name string, namespace string) (map[string][]byte, error),
	writeFile func(name string, data []byte, perm os.FileMode) error,
	wandbName string,
	namespace string,
	outputDir string,
) (string, error) {
	secretName := wandbName + "-root-cert"

	secretData, err := getSecretDataMap(secretName, namespace)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve CA secret %q in namespace %q: %w", secretName, namespace, err)
	}

	certData, err := caCertFromSecretData(secretData)
	if err != nil {
		return "", fmt.Errorf("CA certificate does not exist for W&B CR %q: %w", wandbName, err)
	}

	outputPath := filepath.Join(outputDir, wandbName+".crt")
	if err := writeFile(outputPath, certData, 0644); err != nil {
		return "", fmt.Errorf("failed to write CA certificate to %q: %w", outputPath, err)
	}

	return outputPath, nil
}

func caCertFromSecretData(secretData map[string][]byte) ([]byte, error) {
	if certData, ok := secretData["ca.crt"]; ok && len(certData) > 0 {
		return certData, nil
	}

	if certData, ok := secretData["tls.crt"]; ok && len(certData) > 0 {
		return certData, nil
	}

	return nil, errors.New("secret is missing non-empty \"ca.crt\" or \"tls.crt\" data")
}

func wandbCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy a W&B instance",
		Long:  `Deploy a W&B instance with specified versions and configuration`,
		RunE: func(cmd *cobra.Command, args []string) error {
			crFile, _ := cmd.Flags().GetString("cr-file")
			createCA, _ := cmd.Flags().GetBool("create-ca")
			createAwsIngressClass, _ := cmd.Flags().GetBool("create-aws-ingress-class")
			createAwsStorageClass, _ := cmd.Flags().GetBool("create-aws-storage-class")
			addIngressAnnotations, _ := cmd.Flags().GetBool("add-ingress-annotations")
			license, _ := cmd.Flags().GetString("license")
			licenseFile, _ := cmd.Flags().GetString("license-file")
			telemetryMode, _ := cmd.Flags().GetString("observability-mode")
			gatewayClass, _ := cmd.Flags().GetString("gateway-class")
			ingressClass, _ := cmd.Flags().GetString("ingress-class")
			issuerName, _ := cmd.Flags().GetString("issuer-name")
			size, _ := cmd.Flags().GetString("size")
			retentionPolicy, _ := cmd.Flags().GetString("retention-policy")
			wandbNamespace, _ := cmd.Flags().GetString("wandb-namespace")
			wandbVersion, _ := cmd.Flags().GetString("wandb-version")
			wandbName, _ := cmd.Flags().GetString("wandb-name")
			wandbHostname, _ := cmd.Flags().GetString("wandb-hostname")
			wait, _ := cmd.Flags().GetBool("wait")

			ctx := context.Background()

			err := processWandbCR(
				crFile,
				wandbVersion,
				wandbName,
				wandbHostname,
				gatewayClass,
				ingressClass,
				issuerName,
				addIngressAnnotations,
				license,
				licenseFile,
				telemetryMode,
				wandbNamespace,
				createCA,
				size,
				retentionPolicy,
			)
			if err != nil {
				return err
			}

			err = deployWandbCR(ctx, createCA, createAwsStorageClass, createAwsIngressClass, ingressClass)
			if err != nil {
				return err
			}

			// Step 6: Wait for CR to be ready (if requested)
			if wait {
				fmt.Println("Waiting for W&B instance to be ready...")

				if err := operator.WaitForCRReady(ctx, wandbCR.Namespace, wandbCR.Name, 30*time.Minute); err != nil {
					fmt.Println(" ✗")
					return err
				}
			}

			return nil
		},
	}

	return cmd
}

func operatorDeployCmd() *cobra.Command {
	var setupCluster bool
	var installCertManagerMode string
	var installNginxGatewayMode string
	var enableGatewayAPI bool
	var includeCR bool
	var clusterName string
	var workers int
	var operatorVersion string
	var operatorChartVersion string
	var operatorNamespace string

	cmd := &cobra.Command{
		Use:   "operator",
		Short: "Deploy the v2 operator",
		Long:  `Deploy the v2 operator with specified versions and configuration`,
		RunE: func(cmd *cobra.Command, args []string) error {
			crFile, _ := cmd.Flags().GetString("cr-file")
			createCA, _ := cmd.Flags().GetBool("create-ca")
			createAwsIngressClass, _ := cmd.Flags().GetBool("create-aws-ingress-class")
			createAwsStorageClass, _ := cmd.Flags().GetBool("create-aws-storage-class")
			addIngressAnnotations, _ := cmd.Flags().GetBool("add-ingress-annotations")
			gatewayClass, _ := cmd.Flags().GetString("gateway-class")
			ingressClass, _ := cmd.Flags().GetString("ingress-class")
			issuerName, _ := cmd.Flags().GetString("issuer-name")
			size, _ := cmd.Flags().GetString("size")
			retentionPolicy, _ := cmd.Flags().GetString("retention-policy")
			license, _ := cmd.Flags().GetString("license")
			licenseFile, _ := cmd.Flags().GetString("license-file")
			telemetryMode, _ := cmd.Flags().GetString("observability-mode")
			wandbNamespace, _ := cmd.Flags().GetString("wandb-namespace")
			wandbVersion, _ := cmd.Flags().GetString("wandb-version")
			wandbName, _ := cmd.Flags().GetString("wandb-name")
			wandbHostname, _ := cmd.Flags().GetString("wandb-hostname")
			wait, _ := cmd.Flags().GetBool("wait")

			err := processWandbCR(
				crFile,
				wandbVersion,
				wandbName,
				wandbHostname,
				gatewayClass,
				ingressClass,
				issuerName,
				addIngressAnnotations,
				license,
				licenseFile,
				telemetryMode,
				wandbNamespace,
				createCA,
				size,
				retentionPolicy,
			)
			if err != nil {
				return err
			}

			// Perform the deployment
			deployStart := time.Now()
			if err := performDeploy(
				setupCluster,
				installCertManagerMode,
				installNginxGatewayMode,
				enableGatewayAPI,
				includeCR,
				wait,
				clusterName,
				telemetryMode,
				workers,
				operatorChartVersion,
				operatorVersion,
				operatorNamespace,
				createCA,
				createAwsStorageClass,
				createAwsIngressClass,
				ingressClass,
			); err != nil {
				fmt.Printf("\n✗ Deployment failed: %v\n", err)
				return err
			}

			// Success summary
			totalTime := time.Since(deployStart).Round(time.Second)
			fmt.Printf("\n✓ Deployment complete! (%s total)\n\n", totalTime)

			if includeCR {
				fmt.Println("Access your W&B instance:")
				if setupCluster {
					fmt.Printf("  • Kubectl context: kind-%s\n", clusterName)
				}
				fmt.Printf("  • Namespace: %s\n", wandbNamespace)
				fmt.Printf("  • Status: kubectl get wandb -n %s\n", wandbNamespace)
				fmt.Println()
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&setupCluster, "setup-k8s-cluster", false, "Setup a Kind cluster before deploying")
	cmd.Flags().StringVar(&clusterName, "cluster-name", "kind", "Name of the Kind cluster (only used with --setup-k8s-cluster)")
	cmd.Flags().IntVar(&workers, "workers", 0, "Number of worker nodes (only used with --setup-k8s-cluster)")

	//TODO Decide whether to expose this or have it depend on the chart version
	cmd.Flags().StringVar(&operatorVersion, "operator-version", "", "Operator image version (e.g., v2.0.0) - defaults to value in the chart")
	cmd.Flags().StringVar(&operatorChartVersion, "operator-chart-version", "2.0.0-alpha.1", "Operator Chart version (e.g., v2.0.0)")
	cmd.Flags().StringVar(&operatorNamespace, "operator-namespace", "wandb-operators", "Namespace for operator")
	cmd.Flags().StringVar(&installCertManagerMode, "install-cert-manager", certManagerInstallModeAuto, "Cert-manager install mode: auto (detect and reuse existing), true (force install flow), false (skip installation)")
	cmd.Flags().StringVar(&installNginxGatewayMode, "install-nginx-gateway", nginxGatewayInstallModeAuto, "Nginx-gateway-fabric install mode: auto (detect and reuse existing), true (force install flow), false (skip installation)")

	cmd.Flags().BoolVar(&enableGatewayAPI, "enable-gateway-api", true, "Disables Gateway API support for cert-manager")
	cmd.Flags().BoolVar(&includeCR, "include-cr", false, "Include the Wandb CR in the operator deployment")
	return cmd
}

func performDeploy(
	setupCluster bool,
	installCertManagerMode string,
	installNginxGatewayMode string,
	enableGatewayAPI bool,
	includeCR bool,
	wait bool,
	clusterName string,
	telemetryMode string,
	workers int,
	operatorChartVersion string,
	operatorVersion string,
	operatorNamespace string,
	createCA bool,
	createAwsStorageClass bool,
	createAwsIngressClass bool,
	ingressClass string,
) error {
	ctx := context.Background()
	installNginxGatewayMode = strings.ToLower(strings.TrimSpace(installNginxGatewayMode))
	installCertManagerMode = strings.ToLower(strings.TrimSpace(installCertManagerMode))

	// Calculate total steps based on flags
	totalSteps := 2 // Always: ensure cert-manager, deploy operator
	if setupCluster {
		totalSteps++
	}
	if installNginxGatewayMode != nginxGatewayInstallModeFalse {
		totalSteps++
	}
	if includeCR {
		totalSteps++
	}
	if wait {
		totalSteps++
	}
	currentStep := 1

	// Step 1: Setup K8s cluster if requested
	if setupCluster {
		fmt.Printf("[%d/%d] Setting up cluster (%d workers)...", currentStep, totalSteps, workers)
		start := time.Now()

		err := performCreateCluster(ctx, clusterName, workers, 8080, 8443)
		if err != nil {
			return err
		}

		fmt.Printf(" ✓ (%s)\n", time.Since(start).Round(time.Second))
		currentStep++
	} else {
		_ = clusterName
	}

	// Step: Ensure nginx-gateway-fabric
	if installNginxGatewayMode != nginxGatewayInstallModeFalse {
		fmt.Printf("[%d/%d] Ensuring nginx-gateway-fabric...", currentStep, totalSteps)
		start := time.Now()

		switch installNginxGatewayMode {
		case nginxGatewayInstallModeAuto:
			if err := operator.InstallNginxGateway(ctx, true); err != nil {
				fmt.Println(" ✗")
				return err
			}
		case nginxGatewayInstallModeTrue:
			if err := operator.InstallNginxGateway(ctx, false); err != nil {
				fmt.Println(" ✗")
				return err
			}
		}

		if err := operator.WaitForNginxGateway(ctx, 5*time.Minute); err != nil {
			fmt.Println(" ✗")
			return err
		}

		fmt.Printf(" ✓ (%s)\n", time.Since(start).Round(time.Second))
		currentStep++
	}

	// Step: Ensure cert-manager
	if installCertManagerMode != certManagerInstallModeFalse {
		fmt.Printf("[%d/%d] Ensuring cert-manager...", currentStep, totalSteps)
		start := time.Now()

		switch installCertManagerMode {
		case certManagerInstallModeAuto:
			if err := operator.InstallCertManager(ctx, enableGatewayAPI, true); err != nil {
				fmt.Println(" ✗")
				return err
			}
		case certManagerInstallModeTrue:
			if err := operator.InstallCertManager(ctx, enableGatewayAPI, false); err != nil {
				fmt.Println(" ✗")
				return err
			}
		case certManagerInstallModeFalse:
			// Skip installation and only verify cert-manager readiness below.
		default:
			fmt.Println(" ✗")
			return fmt.Errorf("invalid --install-cert-manager value %q (expected: auto, true, false)", installCertManagerMode)
		}

		if err := operator.WaitForCertManager(ctx, 5*time.Minute); err != nil {
			fmt.Println(" ✗")
			if installCertManagerMode == certManagerInstallModeFalse {
				return fmt.Errorf("cert-manager is not ready and installation is disabled (--install-cert-manager=false): %w", err)
			}
			return err
		}

		fmt.Printf(" ✓ (%s)\n", time.Since(start).Round(time.Second))
		currentStep++
	}

	// Step: Create infra-operators wandbNamespace
	if err := operator.CreateNamespace(ctx, operatorNamespace); err != nil {
		return err
	}

	// Step 4: Deploy W&B operator
	fmt.Printf("[%d/%d] Deploying Required operators...", currentStep, totalSteps)
	start := time.Now()

	if err := operator.DeployOperator(ctx, operatorNamespace, operatorChartVersion, operatorVersion, telemetryMode); err != nil {
		fmt.Println(" ✗")
		return err
	}

	if err := operator.WaitForOperator(ctx, operatorNamespace, 5*time.Minute); err != nil {
		fmt.Println(" ✗")
		return err
	}

	markers := "cert-manager,operator"
	if installNginxGatewayMode != nginxGatewayInstallModeFalse {
		markers += ",nginx-gateway"
	}
	if err := kubectl.CreateDeploymentMarker(ctx, "", operatorNamespace, markers); err != nil {
		fmt.Println(" ✗")
		return err
	}

	fmt.Printf(" ✓ (%s)\n", time.Since(start).Round(time.Second))
	currentStep++

	if includeCR {
		// Step 5: Create W&B instance
		fmt.Printf("[%d/%d] Creating W&B instance...", currentStep, totalSteps)
		start = time.Now()

		err := deployWandbCR(ctx, createCA, createAwsStorageClass, createAwsIngressClass, ingressClass)
		if err != nil {
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
	}

	return nil
}

func destroyWandbCR(ctx context.Context, name string, namespace string) error {
	hasMarker, err := kubectl.HasDeploymentMarker(ctx, namespace, "wandb-cr")
	if err != nil {
		return err
	}

	if !hasMarker {
		return errors.New("no wsm deployment marker found - W&B instance may not be managed by wsm")
	}

	if err := kubectl.DeleteCR(ctx, name, namespace); err != nil {
		return fmt.Errorf("failed to delete W&B CR: %w", err)
	}

	if err := kubectl.DeleteDeploymentMarker(ctx, namespace, "wandb-cr"); err != nil {
		return fmt.Errorf("failed to delete deployment marker: %w", err)
	}

	return nil
}

func deployWandbCR(ctx context.Context, createCA bool, createAwsStorageClass, createAwsIngressClass bool, ingressClass string) error {
	if err := operator.CreateNamespace(ctx, wandbCR.Namespace); err != nil {
		return err
	}

	if createCA {
		err := createCAIssuer(ctx, wandbCR.Name, wandbCR.Namespace)
		if err != nil {
			return fmt.Errorf("failed to create CA issuer: %w", err)
		}
	}

	if createAwsIngressClass {
		err := createDefaultAwsIngressClass(ctx, ingressClass)
		if err != nil {
			return fmt.Errorf("failed to create AWS ingress class: %w", err)
		}
	}

	if createAwsStorageClass {
		err := createDefaultAwsStorageClass(ctx)
		if err != nil {
			return fmt.Errorf("failed to create AWS storage class: %w", err)
		}
	}

	if err := operator.ApplyCR(ctx, wandbCR); err != nil {
		fmt.Println(" ✗")
		return err
	}

	if err := kubectl.CreateDeploymentMarker(ctx, "", wandbCR.Namespace, "wandb-cr"); err != nil {
		fmt.Println(" ✗")
		return err
	}
	return nil
}

func performCreateCluster(ctx context.Context, clusterName string, workers int, httpPort int32, httpsPort int32) error {
	exists, err := kind.ClusterExists(ctx, clusterName)
	if err != nil {
		fmt.Println(" ✗")
		return fmt.Errorf("failed to check if cluster exists: %w", err)
	}

	if !exists {
		if err := kind.CreateCluster(ctx, clusterName, workers, httpPort, httpsPort); err != nil {
			fmt.Println(" ✗")
			return err
		}

		kind.SetKubectlContext(ctx, clusterName)
		kubectl.ResetClients()

		if err := kubectl.CreateDeploymentMarker(ctx, clusterName, "default", "kind-cluster"); err != nil {
			fmt.Println(" ✗")
			return err
		}
	} else {
		kind.SetKubectlContext(ctx, clusterName)
		kubectl.ResetClients()
	}
	return nil
}

func createCAIssuer(ctx context.Context, wandbName string, namespace string) error {
	selfSignedIssuerName := wandbName + "-selfsigned-issuer"
	rootCertName := wandbName + "-root-cert"
	caIssuerName := wandbName + "-ca-issuer"

	selfSignedIssuer := certmanagerv1.Issuer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      selfSignedIssuerName,
			Namespace: namespace,
		},
		Spec: certmanagerv1.IssuerSpec{
			IssuerConfig: certmanagerv1.IssuerConfig{
				SelfSigned: &certmanagerv1.SelfSignedIssuer{},
			},
		},
	}
	rootCert := certmanagerv1.Certificate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rootCertName,
			Namespace: namespace,
		},
		Spec: certmanagerv1.CertificateSpec{
			SecretName: rootCertName,
			IsCA:       true,
			CommonName: "wandb-ca",
			Duration:   &metav1.Duration{Duration: 8760 * 24 * time.Hour},
			IssuerRef: certmanagermetav1.ObjectReference{
				Name:  selfSignedIssuerName,
				Kind:  "Issuer",
				Group: "cert-manager.io",
			},
		},
	}
	caIssuer := certmanagerv1.Issuer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caIssuerName,
			Namespace: namespace,
		},
		Spec: certmanagerv1.IssuerSpec{
			IssuerConfig: certmanagerv1.IssuerConfig{
				CA: &certmanagerv1.CAIssuer{
					SecretName: rootCertName,
				},
			},
		},
	}

	if err := kubectl.ApplyIssuer(ctx, &selfSignedIssuer); err != nil {
		return err
	}
	if err := kubectl.ApplyCertificate(ctx, &rootCert); err != nil {
		return err
	}
	if err := kubectl.ApplyIssuer(ctx, &caIssuer); err != nil {
		return err
	}

	return nil
}

func createDefaultAwsIngressClass(ctx context.Context, ingressClass string) error {
	awsIngressClassParams := map[string]interface{}{
		"apiVersion": "eks.amazonaws.com/v1",
		"kind":       "IngressClassParams",
		"metadata": map[string]interface{}{
			"name": ingressClass,
		},
		"spec": map[string]interface{}{
			"scheme": "internet-facing",
		},
	}
	awsIngressClassParamsYaml, err := yaml.Marshal(awsIngressClassParams)
	if err != nil {
		return fmt.Errorf("failed to marshal AWS ingress class params YAML: %w", err)
	}

	awsIngressClass := networkingv1.IngressClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: ingressClass,
			Annotations: map[string]string{
				"ingressclass.kubernetes.io/is-default-class": "true",
			},
		},
		Spec: networkingv1.IngressClassSpec{
			Controller: "eks.amazonaws.com/alb",
			Parameters: &networkingv1.IngressClassParametersReference{
				APIGroup: ptr.String("eks.amazonaws.com"),
				Kind:     "IngressClassParams",
				Name:     ingressClass,
			},
		},
	}

	err = kubectl.ApplyYAML(ctx, awsIngressClassParamsYaml)
	if err != nil {
		return fmt.Errorf("failed to apply AWS ingress class params YAML: %w", err)
	}

	return kubectl.ApplyIngressClass(ctx, awsIngressClass)
}

func createDefaultAwsStorageClass(ctx context.Context) error {
	storageClass := storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gp3",
			Annotations: map[string]string{
				"storageclass.kubernetes.io/is-default-class": "true",
			},
		},
		Provisioner: "ebs.csi.eks.amazonaws.com",
		Parameters: map[string]string{
			"type":      "gp3",
			"encrypted": "true",
		},
		ReclaimPolicy:        nil,
		MountOptions:         nil,
		AllowVolumeExpansion: ptr.Bool(true),
		VolumeBindingMode:    nil,
		AllowedTopologies: []corev1.TopologySelectorTerm{
			{
				MatchLabelExpressions: []corev1.TopologySelectorLabelRequirement{
					{
						Key:    "eks.amazonaws.com/compute-type",
						Values: []string{"auto"},
					},
				},
			},
		},
	}

	return kubectl.ApplyStorageClass(ctx, &storageClass)
}

func processWandbCR(
	crFile string,
	wandbVersion string,
	wandbName string,
	wandbHostname string,
	gatewayClass string,
	ingressClass string,
	issuerName string,
	addIngressAnnotations bool,
	license string,
	licenseFile string,
	telemetryMode string,
	wandbNamespace string,
	createCA bool,
	size string,
	retentionPolicy string,
) error {
	if crFile != "" {
		var err error
		wandbCR, err = readCRFile(crFile)
		if err != nil {
			fmt.Printf("failed to read CR file: %v\n", err)
			return err
		}
	}

	wandbCR.Name = wandbName
	wandbCR.Spec.Wandb.Hostname = wandbHostname
	wandbCR.Spec.Size = v2.Size(size)
	wandbCR.Spec.RetentionPolicy.OnDelete = v2.OnDeletePolicy(retentionPolicy)

	if wandbCR.Spec.Wandb.Version == "" && wandbVersion == "" {
		wandbCR.Spec.Wandb.Version = defaultWandbVersion
	}

	if wandbVersion != "" {
		wandbCR.Spec.Wandb.Version = wandbVersion
	}

	if license != "" && licenseFile != "" {
		return fmt.Errorf("cannot specify both license and license file")
	}

	if gatewayClass != "" && ingressClass != "" {
		return fmt.Errorf("cannot specify both gatewayClass and ingressClass")
	}

	if license != "" || licenseFile != "" {
		if license != "" {
			wandbCR.Spec.Wandb.License = license
		}
		if licenseFile != "" {
			licenseData, err := os.ReadFile(licenseFile)
			if err != nil {
				fmt.Printf("failed to read license file: %v\n", err)
				return err
			}
			wandbCR.Spec.Wandb.License = strings.TrimSpace(string(licenseData))
		}
	}

	if gatewayClass != "" {
		wandbCR.Spec.Networking.Mode = "gateway"
		wandbCR.Spec.Networking.GatewayAPI = &v2.GatewayAPIConfig{
			Gateway: v2.GatewayConfig{
				Managed:          true,
				GatewayClassName: &gatewayClass,
			},
		}
	}

	if ingressClass != "" {
		wandbCR.Spec.Networking.Mode = "ingress"
		wandbCR.Spec.Networking.Ingress = &v2.IngressConfig{
			IngressClassName: &ingressClass,
		}
	}

	if addIngressAnnotations {
		wandbCR.Spec.Networking.GatewayAPI.Gateway.InfrastructureAnnotations = map[string]string{
			"service.beta.kubernetes.io/aws-load-balancer-scheme": "internet-facing",
		}
	}

	if strings.HasPrefix(wandbCR.Spec.Wandb.Hostname, "https") {
		if createCA {
			wandbCR.Spec.Networking.TLS = &v2.TLSConfig{
				SecretName: wandbName + "-tls-secret",
				CertManager: &v2.CertManagerConfig{
					Issuer: wandbName + "-ca-issuer",
				},
			}
		} else if issuerName != "" {
			wandbCR.Spec.Networking.TLS = &v2.TLSConfig{
				SecretName: wandbName + "-tls-secret",
				CertManager: &v2.CertManagerConfig{
					Issuer: issuerName,
				},
			}
		} else {
			return fmt.Errorf("cannot specify TLS without a CA or issuer")
		}
	}

	if telemetryMode != "" && telemetryMode != "off" {
		if wandbCR.Spec.MySQL.ManagedMysql != nil {
			wandbCR.Spec.MySQL.ManagedMysql.Telemetry.Enabled = true
		}
		if wandbCR.Spec.Kafka.ManagedKafka != nil {
			wandbCR.Spec.Kafka.ManagedKafka.Telemetry.Enabled = true
		}
		if wandbCR.Spec.ClickHouse.ManagedClickHouse != nil {
			wandbCR.Spec.ClickHouse.ManagedClickHouse.Telemetry.Enabled = true
		}
		if wandbCR.Spec.Redis.ManagedRedis != nil {
			wandbCR.Spec.Redis.ManagedRedis.Telemetry.Enabled = true
		}
		if wandbCR.Spec.ObjectStore.ManagedObjectStore != nil {
			wandbCR.Spec.ObjectStore.ManagedObjectStore.Telemetry.Enabled = true
		}
	}

	wandbCR.Namespace = wandbNamespace
	return nil
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

// ClusterCmd returns the cluster command with subcommands
func ClusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage Kubernetes clusters",
		Long:  `Create and delete Kubernetes clusters for local development`,
	}

	cmd.AddCommand(clusterCreateCmd())
	cmd.AddCommand(clusterDestroyCmd())
	cmd.AddCommand(clusterCleanupCmd())

	return cmd
}

func clusterCreateCmd() *cobra.Command {
	var clusterName string
	var workers int
	var httpPort, httpsPort int32
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new kind cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := performCreateCluster(context.Background(), clusterName, workers, httpPort, httpsPort); err != nil {
				fmt.Printf("✗ Cluster Create failed: %v\n", err)
				return err
			}
			fmt.Printf("✓ Kind cluster '%s' created successfully\n", clusterName)
			return nil
		},
	}

	cmd.Flags().StringVar(&clusterName, "cluster-name", "kind", "Name of the Kind cluster")
	cmd.Flags().IntVar(&workers, "workers", 0, "Number of worker nodes")
	cmd.Flags().Int32Var(&httpPort, "http-port", 8080, "HTTP port for Kind cluster ingress")
	cmd.Flags().Int32Var(&httpsPort, "https-port", 8443, "HTTPS port for Kind cluster ingress")

	return cmd
}

func clusterDestroyCmd() *cobra.Command {
	var clusterName string
	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "Destroy Kind cluster and cleanup",
		Long:  `Delete the Kind cluster and cleanup resources`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			hasMarker, err := kubectl.HasDeploymentMarker(ctx, "default", "kind-cluster")
			if err != nil {
				return err
			}

			if !hasMarker {
				return fmt.Errorf("no wsm deployment marker found - cluster may not be managed by wsm")
			}

			fmt.Printf("→ Deleting Kind cluster '%s'...\n", clusterName)
			if err := kind.DeleteCluster(ctx, clusterName); err != nil {
				return err
			}

			if err := kind.CleanupDistDirectory(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
			}

			fmt.Printf("✓ Kind cluster '%s' deleted successfully\n", clusterName)
			return nil
		},
	}

	cmd.Flags().StringVar(&clusterName, "cluster-name", "kind", "Name of the Kind cluster to delete")

	return cmd
}

func performCleanup() error {
	ctx := context.Background()

	// 1. Delete W&B CRs in all namespaces that have the marker
	wandbNamespaces, err := kubectl.FindNamespacesWithMarker(ctx, "wandb-cr")
	if err != nil {
		return err
	}

	if len(wandbNamespaces) == 0 {
		fmt.Println("! No wsm-managed W&B deployments found.")
	}

	for _, ns := range wandbNamespaces {
		crs, err := operator.ListCRs(ctx, ns)
		if err != nil {
			fmt.Printf("✗ Failed to list W&B CRs in namespace %s: %v\n", ns, err)
			continue
		}

		for _, cr := range crs {
			fmt.Printf("→ Deleting W&B CR '%s' in namespace '%s'...\n", cr, ns)
			if err := operator.DeleteCR(ctx, cr, ns); err != nil {
				fmt.Printf("  ✗ Failed to delete W&B CR %s/%s: %v\n", ns, cr, err)
			}
		}

		fmt.Printf("→ Removing deployment marker in namespace '%s'...\n", ns)
		if err := kubectl.DeleteDeploymentMarker(ctx, ns, "wandb-cr"); err != nil {
			fmt.Printf("  ✗ Failed to delete deployment marker in %s: %v\n", ns, err)
		}
	}

	// 2. Delete W&B operators in all namespaces that have the marker
	operatorNamespaces, err := kubectl.FindNamespacesWithMarker(ctx, "operator")
	if err != nil {
		return err
	}

	if len(operatorNamespaces) == 0 {
		fmt.Println("! No wsm-managed W&B operators found.")
	}

	for _, ns := range operatorNamespaces {
		fmt.Printf("→ Deleting W&B operator in namespace '%s'...\n", ns)
		if err := operator.DeleteOperator(ctx, ns); err != nil {
			fmt.Printf("  ✗ Failed to delete operator in %s: %v\n", ns, err)
			return err
		}

		fmt.Printf("→ Removing deployment marker in namespace '%s'...\n", ns)
		if err := kubectl.DeleteDeploymentMarker(ctx, ns, "operator"); err != nil {
			fmt.Printf("  ✗ Failed to delete deployment marker in %s: %v\n", ns, err)
			return err
		}
	}

	// 3. Delete cert-manager
	fmt.Println("→ Deleting cert-manager...")
	if err := operator.DeleteCertManager(ctx); err != nil {
		fmt.Printf("  ✗ Failed to delete cert-manager: %v\n", err)
	}

	cmNamespaces, err := kubectl.FindNamespacesWithMarker(ctx, "cert-manager")
	if err == nil {
		for _, ns := range cmNamespaces {
			if err := kubectl.DeleteDeploymentMarker(ctx, ns, "cert-manager"); err != nil {
				fmt.Printf("  ✗ Failed to remove cert-manager marker in %s: %v\n", ns, err)
			}
		}
	}

	// 4. Delete nginx-gateway
	fmt.Println("→ Deleting nginx-gateway...")
	if err := operator.DeleteNginxGateway(ctx); err != nil {
		fmt.Printf("  ✗ Failed to delete nginx-gateway: %v\n", err)
	}

	ngNamespaces, err := kubectl.FindNamespacesWithMarker(ctx, "nginx-gateway")
	if err == nil {
		for _, ns := range ngNamespaces {
			if err := kubectl.DeleteDeploymentMarker(ctx, ns, "nginx-gateway"); err != nil {
				fmt.Printf("  ✗ Failed to remove nginx-gateway marker in %s: %v\n", ns, err)
			}
		}
	}

	return nil
}

func clusterCleanupCmd() *cobra.Command {
	var kubeContext string

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Cleanup wsm-managed resources",
		Long:  `Delete all resources deployed by wsm, including operator and CR`,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if kubeContext == "" {
				return errors.New("context is required")
			}

			kubectl.SetContext(kubeContext)
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := performCleanup(); err != nil {
				fmt.Printf("✗ Cleanup failed: %v\n", err)
				return err
			}

			fmt.Println("✓ Cleanup completed successfully")
			return nil
		},
	}

	cmd.Flags().StringVar(&kubeContext, "context", "", "name of the kubeconfig context to use (Required)")

	return cmd
}
