package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
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

// W&B server version policy — the two values wsm bumps per release. Both live
// here so nobody has to dig through the command wiring to find or change them.
//
//   - defaultWandbVersion: stamped into spec.wandb.version when --wandb-version
//     is unset. Bump this each W&B/operator release.
//   - minWandbVersion: the oldest server wsm will deploy. A resolved version
//     below this (via --wandb-version, --cr-file, or --cr-set spec.wandb.version)
//     is rejected up front. Raise it when dropping support for old servers.
//
// TODO once an official release publishes a manifest, we should switch to looking
// up the most recent non-dev release and not have a default.
const (
	defaultWandbVersion = "0.82.2"
	minWandbVersion     = "0.80.0"
)

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
			// Managed infra is keyed by instance name; wsm builds the single
			// reserved DefaultInstanceName instance. Kafka stays a struct.
			MySQL: map[string]v2.MySQLSpec{
				v2.DefaultInstanceName: {
					ManagedMysql: &v2.ManagedMysqlSpec{
						Telemetry: v2.Telemetry{
							Enabled: false,
						},
					},
				},
			},
			Redis: map[string]v2.RedisSpec{
				v2.DefaultInstanceName: {
					ManagedRedis: &v2.ManagedRedisSpec{
						Telemetry: v2.Telemetry{
							Enabled: false,
						},
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
			ObjectStore: map[string]v2.ObjectStoreSpec{
				v2.DefaultInstanceName: {
					ManagedObjectStore: &v2.ManagedObjectStoreSpec{
						Telemetry: v2.Telemetry{
							Enabled: false,
						},
					},
				},
			},
			ClickHouse: map[string]v2.ClickHouseSpec{
				v2.DefaultInstanceName: {
					ManagedClickHouse: &v2.ManagedClickHouseSpec{
						Telemetry: v2.Telemetry{
							Enabled: false,
						},
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
	cmd.PersistentFlags().String("ingress-name", "", "Override the generated Ingress resource name (defaults to the CR name)")
	cmd.PersistentFlags().String("gateway-class", "nginx", "Enable Gateway API support with the specified gateway class")
	cmd.PersistentFlags().String("issuer-name", "", "TLS issuer name for Gateway API")
	cmd.PersistentFlags().Bool("add-ingress-annotations", false, "Add cloud provider annotations to Ingress or Gateway API")
	cmd.PersistentFlags().String("license", "", "W&B license string (optional, injected into spec.wandb.license)")
	cmd.PersistentFlags().String("license-file", "", "Path to W&B license file (optional, injected into spec.wandb.license)")
	cmd.PersistentFlags().String("observability-mode", "off", "Enable observability for applications (off, full, forward)")
	cmd.PersistentFlags().String("observability-forward-endpoint", "", "OTLP endpoint to forward telemetry to (required when --observability-mode=forward)")
	cmd.PersistentFlags().String("observability-otel-secret", "", "Name of the OTEL connection secret (telemetry.otel.secretName; defaults to the chart's wandb-otel-connection; applied when --observability-mode=full|forward)")
	cmd.PersistentFlags().String("observability-otel-protocol", "", "OTEL exporter protocol, e.g. http/protobuf or grpc (telemetry.otel.protocol; chart default if unset)")
	cmd.PersistentFlags().String("observability-otel-service-name", "", "OTEL service.name resource attribute (telemetry.otel.serviceName; chart default if unset)")
	cmd.PersistentFlags().String("observability-otel-resource-attributes", "", "Additional OTEL resource attributes, comma-separated key=value (telemetry.otel.resourceAttributes; chart default if unset)")
	cmd.PersistentFlags().String("observability-forward-protocol", "", "OTLP forwarding protocol, e.g. http/protobuf or grpc (telemetry.forwarding.otlp.protocol; only applied when --observability-mode=forward)")
	cmd.PersistentFlags().StringToString("observability-forward-headers", nil, "OTLP forwarding headers as key=value pairs, e.g. Authorization=Bearer... (telemetry.forwarding.otlp.headers; only applied when --observability-mode=forward)")
	cmd.PersistentFlags().String("retention-policy", "detach", "Retention policy for W&B instance (detach, purge) - defaults to detach")
	cmd.PersistentFlags().String("size", "small", "W&B instance size (dev, micro, small, medium, large, xlarge, xxlarge)")
	cmd.PersistentFlags().String("wandb-hostname", "http://localhost:8080", "Hostname to use for the W&B instance")
	cmd.PersistentFlags().String("wandb-name", "wandb", "Name of the W&B instance")
	cmd.PersistentFlags().String("wandb-version", "", fmt.Sprintf("Server manifest version (defaults to %s when unset; must be >= %s)", defaultWandbVersion, minWandbVersion))
	cmd.PersistentFlags().String("wandb-namespace", "wandb", "Namespace for CR")
	cmd.PersistentFlags().String("oidc-client-id", "", "OIDC client ID as <secret-name>:<key> (spec.wandb.oidc.clientId; optional)")
	cmd.PersistentFlags().String("oidc-client-secret", "", "OIDC client secret as <secret-name>:<key> (spec.wandb.oidc.clientSecret; optional)")
	cmd.PersistentFlags().String("oidc-issuer-url", "", "OIDC issuer URL as <secret-name>:<key> (spec.wandb.oidc.issuerUrl; optional)")
	cmd.PersistentFlags().String("oidc-auth-method", "", "OIDC auth method as <secret-name>:<key> (spec.wandb.oidc.authMethod; optional)")
	cmd.PersistentFlags().String("oidc-session-length", "", "OIDC session length, e.g. 720h (spec.wandb.oidc.sessionLength; optional)")
	cmd.PersistentFlags().String("image-registry", "", "Retarget container images to this registry for air-gapped installs (spec.global.imageRegistry; optional)")
	cmd.PersistentFlags().StringArray("custom-ca-cert-file", nil, "Path to a PEM CA certificate to trust in W&B workloads; repeatable (spec.global.customCACerts; optional)")
	cmd.PersistentFlags().String("custom-ca-configmap", "", "Name of a ConfigMap holding CA certificates to trust in W&B workloads (spec.global.caCertsConfigMap; optional)")
	cmd.PersistentFlags().Int32("objectstore-copies", 0, "Managed object store replica copies (spec.objectStore.managedObjectStore.copies; optional, operator default when unset)")
	cmd.PersistentFlags().StringArray("cr-set", nil, "Set an arbitrary CR field as <path>=<value>, e.g. spec.wandb.version=0.82.2; repeatable, YAML-typed, overrides the built-in template, --cr-file, and the typed flags above")
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
			f := wandbCRFlagsFrom(cmd)
			createAwsIngressClass, _ := cmd.Flags().GetBool("create-aws-ingress-class")
			createAwsStorageClass, _ := cmd.Flags().GetBool("create-aws-storage-class")
			wait, _ := cmd.Flags().GetBool("wait")

			if err := validateObservabilityMode(f.telemetryMode); err != nil {
				return err
			}
			if err := validateNetworkingFlags(cmd.Flags().Changed("gateway-class"), f.gatewayClass, f.ingressClass); err != nil {
				return err
			}
			crOverrides, err := operator.ParseCROverrides(f.crSet)
			if err != nil {
				return err
			}
			if err := validateVersionOverride(crOverrides); err != nil {
				return err
			}

			ctx := context.Background()

			if err := processWandbCR(f); err != nil {
				return err
			}

			err = deployWandbCR(ctx, f.createCA, createAwsStorageClass, createAwsIngressClass, f.ingressClass, crOverrides)
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
	var kindNodeImage string
	var operatorChartVersion string
	var operatorNamespace string
	var mirrorRegistry string
	var insecureRegistry bool

	cmd := &cobra.Command{
		Use:   "operator",
		Short: "Deploy the v2 operator",
		Long:  `Deploy the v2 operator with specified versions and configuration`,
		RunE: func(cmd *cobra.Command, args []string) error {
			f := wandbCRFlagsFrom(cmd)
			createAwsIngressClass, _ := cmd.Flags().GetBool("create-aws-ingress-class")
			createAwsStorageClass, _ := cmd.Flags().GetBool("create-aws-storage-class")
			telemetryForwardEndpoint, _ := cmd.Flags().GetString("observability-forward-endpoint")
			otelSecret, _ := cmd.Flags().GetString("observability-otel-secret")
			otelProtocol, _ := cmd.Flags().GetString("observability-otel-protocol")
			otelServiceName, _ := cmd.Flags().GetString("observability-otel-service-name")
			otelResourceAttrs, _ := cmd.Flags().GetString("observability-otel-resource-attributes")
			forwardProtocol, _ := cmd.Flags().GetString("observability-forward-protocol")
			forwardHeaders, _ := cmd.Flags().GetStringToString("observability-forward-headers")
			wait, _ := cmd.Flags().GetBool("wait")

			if err := validateObservabilityMode(f.telemetryMode); err != nil {
				return err
			}
			if f.telemetryMode == operator.TelemetryModeForward && telemetryForwardEndpoint == "" {
				return fmt.Errorf("--observability-mode=forward requires --observability-forward-endpoint")
			}
			if err := validateNetworkingFlags(cmd.Flags().Changed("gateway-class"), f.gatewayClass, f.ingressClass); err != nil {
				return err
			}
			crOverrides, err := operator.ParseCROverrides(f.crSet)
			if err != nil {
				return err
			}
			if err := validateVersionOverride(crOverrides); err != nil {
				return err
			}

			telemetry := operator.TelemetryConfig{
				Mode:              f.telemetryMode,
				ForwardEndpoint:   telemetryForwardEndpoint,
				OtelSecretName:    otelSecret,
				OtelProtocol:      otelProtocol,
				OtelServiceName:   otelServiceName,
				OtelResourceAttrs: otelResourceAttrs,
				ForwardProtocol:   forwardProtocol,
				ForwardHeaders:    forwardHeaders,
			}

			if err := processWandbCR(f); err != nil {
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
				telemetry,
				f.wandbNamespace,
				workers,
				operatorChartVersion,
				operatorNamespace,
				f.createCA,
				createAwsStorageClass,
				createAwsIngressClass,
				f.ingressClass,
				kindNodeImage,
				mirrorRegistry,
				insecureRegistry,
				crOverrides,
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
				fmt.Printf("  • Namespace: %s\n", f.wandbNamespace)
				fmt.Printf("  • Status: kubectl get wandb -n %s\n", f.wandbNamespace)
				fmt.Println()
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&setupCluster, "setup-k8s-cluster", false, "Setup a Kind cluster before deploying")
	cmd.Flags().StringVar(&clusterName, "cluster-name", "kind", "Name of the Kind cluster (only used with --setup-k8s-cluster)")
	cmd.Flags().IntVar(&workers, "workers", 0, "Number of worker nodes (only used with --setup-k8s-cluster)")
	cmd.Flags().StringVar(&kindNodeImage, "kind-node-image", "", "Kind node image to use, e.g. myreg.example.com/kindest/node:v1.35.1@sha256:... (defaults to the upstream pinned image; only used with --setup-k8s-cluster)")

	cmd.Flags().StringVar(&operatorChartVersion, "operator-chart-version", "2.0.0-beta.1", "Operator Chart version (e.g., v2.0.0)")
	cmd.Flags().StringVar(&operatorNamespace, "operator-namespace", "wandb-operators", "Namespace for operator")
	cmd.Flags().StringVar(&installCertManagerMode, "install-cert-manager", certManagerInstallModeAuto, "Cert-manager install mode: auto (detect and reuse existing), true (force install flow), false (skip installation)")
	cmd.Flags().StringVar(&installNginxGatewayMode, "install-nginx-gateway", nginxGatewayInstallModeAuto, "Nginx-gateway-fabric install mode: auto (detect and reuse existing), true (force install flow), false (skip installation)")

	cmd.Flags().BoolVar(&enableGatewayAPI, "enable-gateway-api", true, "Disables Gateway API support for cert-manager")
	cmd.Flags().BoolVar(&includeCR, "include-cr", false, "Include the Wandb CR in the operator deployment")
	cmd.Flags().StringVar(&mirrorRegistry, "mirror-registry", "", "Pull every chart and image from this registry (e.g. harbor.corp:5443). Populate it first with 'wsm registry mirror --to <same-host>'.")
	cmd.Flags().BoolVar(&insecureRegistry, "insecure-registry", false, "Use plain HTTP / skip TLS verification when fetching from --mirror-registry")
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
	telemetry operator.TelemetryConfig,
	wandbNamespace string,
	workers int,
	operatorChartVersion string,
	operatorNamespace string,
	createCA bool,
	createAwsStorageClass bool,
	createAwsIngressClass bool,
	ingressClass string,
	kindNodeImage string,
	mirrorRegistry string,
	insecureRegistry bool,
	crOverrides []operator.CROverride,
) error {
	ctx := context.Background()
	installNginxGatewayMode = strings.ToLower(strings.TrimSpace(installNginxGatewayMode))
	installCertManagerMode = strings.ToLower(strings.TrimSpace(installCertManagerMode))

	var mirror *operator.MirrorConfig
	if mirrorRegistry != "" {
		mirror = &operator.MirrorConfig{
			Host:     strings.TrimRight(mirrorRegistry, "/"),
			Insecure: insecureRegistry,
		}
	}

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

		// When the user supplied --mirror-registry --insecure-registry on the
		// combined --setup-k8s-cluster path, treat that host as insecure for
		// the new Kind node's containerd too — otherwise kubelet would default
		// to HTTPS and ImagePullBackOff on every chart pod.
		insecureRegistryHost := ""
		if mirror != nil && mirror.Insecure {
			insecureRegistryHost = mirror.Host
		}
		err := performCreateCluster(ctx, clusterName, workers, 8080, 8443, kindNodeImage, insecureRegistryHost)
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
			if err := operator.InstallNginxGateway(ctx, true, mirror); err != nil {
				fmt.Println(" ✗")
				return err
			}
		case nginxGatewayInstallModeTrue:
			if err := operator.InstallNginxGateway(ctx, false, mirror); err != nil {
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
			if err := operator.InstallCertManager(ctx, enableGatewayAPI, true, mirror); err != nil {
				fmt.Println(" ✗")
				return err
			}
		case certManagerInstallModeTrue:
			if err := operator.InstallCertManager(ctx, enableGatewayAPI, false, mirror); err != nil {
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

	if err := operator.DeployOperator(ctx, operatorNamespace, operatorChartVersion, mirror, telemetry, wandbNamespace); err != nil {
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

		err := deployWandbCR(ctx, createCA, createAwsStorageClass, createAwsIngressClass, ingressClass, crOverrides)
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

func deployWandbCR(ctx context.Context, createCA bool, createAwsStorageClass, createAwsIngressClass bool, ingressClass string, crOverrides []operator.CROverride) error {
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

	if err := operator.ApplyCR(ctx, wandbCR, crOverrides); err != nil {
		fmt.Println(" ✗")
		return err
	}

	if err := kubectl.CreateDeploymentMarker(ctx, "", wandbCR.Namespace, "wandb-cr"); err != nil {
		fmt.Println(" ✗")
		return err
	}
	return nil
}

func performCreateCluster(ctx context.Context, clusterName string, workers int, httpPort int32, httpsPort int32, nodeImage string, insecureRegistryHost string) error {
	exists, err := kind.ClusterExists(ctx, clusterName)
	if err != nil {
		fmt.Println(" ✗")
		return fmt.Errorf("failed to check if cluster exists: %w", err)
	}

	if !exists {
		if err := kind.CreateCluster(ctx, clusterName, workers, httpPort, httpsPort, nodeImage, insecureRegistryHost); err != nil {
			fmt.Println(" ✗")
			return err
		}

		kind.SetKubectlContext(ctx, clusterName)

		if err := kind.InstallMetricsServer(ctx); err != nil {
			fmt.Println(" ✗")
			return err
		}

		if err := kubectl.CreateDeploymentMarker(ctx, clusterName, "default", "kind-cluster"); err != nil {
			fmt.Println(" ✗")
			return err
		}

		if err := waitForAllClusterPodsReady(ctx, 5*time.Minute); err != nil {
			fmt.Println(" ✗")
			return err
		}
	}
	return nil
}

func waitForAllClusterPodsReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allReady, _, err := clusterPodsReady(ctx)
		if err == nil && allReady {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}

	_, notReadyPods, err := clusterPodsReady(ctx)
	if err != nil {
		return fmt.Errorf("timed out waiting for all cluster pods to become ready within %s: %w", timeout, err)
	}

	if len(notReadyPods) == 0 {
		return fmt.Errorf("timed out waiting for all cluster pods to become ready within %s", timeout)
	}

	return fmt.Errorf(
		"timed out waiting for all cluster pods to become ready within %s; not ready pods: %s",
		timeout,
		strings.Join(notReadyPods, ", "),
	)
}

func clusterPodsReady(ctx context.Context) (bool, []string, error) {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return false, nil, err
	}

	pods, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, nil, err
	}

	notReadyPods := make([]string, 0)
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded {
			continue
		}

		if !isPodReady(pod.Status.Conditions) {
			notReadyPods = append(notReadyPods, fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))
		}
	}

	return len(notReadyPods) == 0, notReadyPods, nil
}

func isPodReady(conditions []corev1.PodCondition) bool {
	for _, condition := range conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}

	return false
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
			IssuerRef: certmanagermetav1.IssuerReference{
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

// changedInt32 returns a pointer to the flag's int32 value only when the user
// explicitly set it, so an unset flag leaves the operator's own default intact.
func changedInt32(cmd *cobra.Command, name string) *int32 {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	v, _ := cmd.Flags().GetInt32(name)
	return &v
}

// wandbCRFlags holds every flag value that shapes the WeightsAndBiases CR. Both
// `wandb deploy` and `operator` build the same CR, so they read this one set via
// wandbCRFlagsFrom — keeping the two commands in lockstep and avoiding a long
// positional argument list where adjacent same-typed values are easy to swap.
type wandbCRFlags struct {
	crFile                string
	wandbVersion          string
	wandbName             string
	wandbHostname         string
	gatewayClass          string
	ingressClass          string
	ingressName           string
	issuerName            string
	addIngressAnnotations bool
	license               string
	licenseFile           string
	telemetryMode         string
	wandbNamespace        string
	createCA              bool
	size                  string
	retentionPolicy       string
	oidcClientID          string
	oidcClientSecret      string
	oidcIssuerURL         string
	oidcAuthMethod        string
	oidcSessionLength     string
	imageRegistry         string
	customCACertFiles     []string
	customCAConfigMap     string
	objectStoreCopies     *int32
	// crSet is applied to the unstructured CR at apply time (operator.ApplyCR),
	// not by processWandbCR, since it can address any field the typed struct has.
	crSet []string
}

// wandbCRFlagsFrom reads the CR-shaping flags off cmd. Flags are declared on the
// shared persistent flag set, so both subcommands see the same names.
func wandbCRFlagsFrom(cmd *cobra.Command) wandbCRFlags {
	str := func(name string) string { v, _ := cmd.Flags().GetString(name); return v }
	boolean := func(name string) bool { v, _ := cmd.Flags().GetBool(name); return v }

	certFiles, _ := cmd.Flags().GetStringArray("custom-ca-cert-file")
	crSet, _ := cmd.Flags().GetStringArray("cr-set")
	return wandbCRFlags{
		crFile:                str("cr-file"),
		wandbVersion:          str("wandb-version"),
		wandbName:             str("wandb-name"),
		wandbHostname:         str("wandb-hostname"),
		gatewayClass:          str("gateway-class"),
		ingressClass:          str("ingress-class"),
		ingressName:           str("ingress-name"),
		issuerName:            str("issuer-name"),
		addIngressAnnotations: boolean("add-ingress-annotations"),
		license:               str("license"),
		licenseFile:           str("license-file"),
		telemetryMode:         str("observability-mode"),
		wandbNamespace:        str("wandb-namespace"),
		createCA:              boolean("create-ca"),
		size:                  str("size"),
		retentionPolicy:       str("retention-policy"),
		oidcClientID:          str("oidc-client-id"),
		oidcClientSecret:      str("oidc-client-secret"),
		oidcIssuerURL:         str("oidc-issuer-url"),
		oidcAuthMethod:        str("oidc-auth-method"),
		oidcSessionLength:     str("oidc-session-length"),
		imageRegistry:         str("image-registry"),
		customCACertFiles:     certFiles,
		customCAConfigMap:     str("custom-ca-configmap"),
		objectStoreCopies:     changedInt32(cmd, "objectstore-copies"),
		crSet:                 crSet,
	}
}

// validateWandbVersion rejects a W&B server version below minWandbVersion. The
// floor is compared on the core major.minor.patch, so a pre-release build of the
// minimum line (e.g. 0.80.0-rc.1) still passes.
func validateWandbVersion(version string) error {
	v, err := semver.NewVersion(version)
	if err != nil {
		return fmt.Errorf("wandb version %q is not valid semver: %w", version, err)
	}
	minV := semver.MustParse(minWandbVersion)
	core := semver.New(v.Major(), v.Minor(), v.Patch(), "", "")
	if core.LessThan(minV) {
		return fmt.Errorf("wandb version %q is below the minimum supported version %s", version, minWandbVersion)
	}
	return nil
}

// validateVersionOverride applies the minWandbVersion floor to a
// `--cr-set spec.wandb.version=…` override, so the escape hatch can't undercut
// the minimum supported server.
func validateVersionOverride(overrides []operator.CROverride) error {
	for _, o := range overrides {
		if strings.Join(o.Path, ".") != "spec.wandb.version" {
			continue
		}
		version, ok := o.Value.(string)
		if !ok {
			return fmt.Errorf("--cr-set spec.wandb.version must be a string, got %T", o.Value)
		}
		return validateWandbVersion(version)
	}
	return nil
}

func processWandbCR(f wandbCRFlags) error {
	if f.crFile != "" {
		var err error
		wandbCR, err = readCRFile(f.crFile)
		if err != nil {
			fmt.Printf("failed to read CR file: %v\n", err)
			return err
		}
	}

	wandbCR.Name = f.wandbName
	wandbCR.Spec.Wandb.Hostname = f.wandbHostname
	wandbCR.Spec.Size = v2.Size(f.size)
	wandbCR.Spec.RetentionPolicy.OnDelete = v2.OnDeletePolicy(f.retentionPolicy)

	if wandbCR.Spec.Wandb.Version == "" && f.wandbVersion == "" {
		wandbCR.Spec.Wandb.Version = defaultWandbVersion
	}

	if f.wandbVersion != "" {
		wandbCR.Spec.Wandb.Version = f.wandbVersion
	}

	// Enforce the minimum supported server on the resolved version (default,
	// --wandb-version, or --cr-file). --cr-set is checked separately before apply.
	if err := validateWandbVersion(wandbCR.Spec.Wandb.Version); err != nil {
		return err
	}

	if f.license != "" && f.licenseFile != "" {
		return fmt.Errorf("cannot specify both license and license file")
	}

	if f.license != "" || f.licenseFile != "" {
		if f.license != "" {
			wandbCR.Spec.Wandb.License = f.license
		}
		if f.licenseFile != "" {
			licenseData, err := os.ReadFile(f.licenseFile)
			if err != nil {
				fmt.Printf("failed to read license file: %v\n", err)
				return err
			}
			wandbCR.Spec.Wandb.License = strings.TrimSpace(string(licenseData))
		}
	}

	// Each OIDC leaf is <secret-name>:<key>. --cr-file wins: a leaf it already set
	// is left alone (the flag for it is ignored). Empty leaves stay zero and get
	// stripped by operator.ApplyCR when none is configured (stripFieldsNotInCRDSchema).
	oidcRefs := []struct {
		value string
		field *corev1.SecretKeySelector
		flag  string
	}{
		{f.oidcClientID, &wandbCR.Spec.Wandb.OIDC.ClientId, "--oidc-client-id"},
		{f.oidcClientSecret, &wandbCR.Spec.Wandb.OIDC.ClientSecret, "--oidc-client-secret"},
		{f.oidcIssuerURL, &wandbCR.Spec.Wandb.OIDC.IssuerUrl, "--oidc-issuer-url"},
		{f.oidcAuthMethod, &wandbCR.Spec.Wandb.OIDC.AuthMethod, "--oidc-auth-method"},
	}
	for _, ref := range oidcRefs {
		if ref.value == "" {
			continue
		}
		if ref.field.Name != "" || ref.field.Key != "" {
			fmt.Printf("ignoring %s: spec.wandb.oidc value already set by --cr-file\n", ref.flag)
			continue
		}
		secretName, key, ok := strings.Cut(ref.value, ":")
		if !ok || secretName == "" || key == "" {
			return fmt.Errorf("%s must be in <secret-name>:<key> form, got %q", ref.flag, ref.value)
		}
		*ref.field = corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
			Key:                  key,
		}
	}

	// sessionLength is a plain string leaf, not a selector. The W&B app consumes
	// it as a Go duration (default 720h), so reject malformed values here rather
	// than surfacing an opaque failure at reconcile. --cr-file wins.
	if f.oidcSessionLength != "" {
		if _, err := time.ParseDuration(f.oidcSessionLength); err != nil {
			return fmt.Errorf("--oidc-session-length must be a Go duration, e.g. 720h: %w", err)
		}
		if wandbCR.Spec.Wandb.OIDC.SessionLength != "" {
			fmt.Println("ignoring --oidc-session-length: spec.wandb.oidc.sessionLength already set by --cr-file")
		} else {
			wandbCR.Spec.Wandb.OIDC.SessionLength = f.oidcSessionLength
		}
	}

	// spec.global: air-gap image retarget + custom CA trust. Only set when a
	// flag is supplied; an empty GlobalSpec is stripped by operator.ApplyCR.
	if f.imageRegistry != "" {
		wandbCR.Spec.Global.ImageRegistry = f.imageRegistry
	}
	if f.customCAConfigMap != "" {
		wandbCR.Spec.Global.CACertsConfigMap = f.customCAConfigMap
	}
	for _, certFile := range f.customCACertFiles {
		pem, err := os.ReadFile(certFile)
		if err != nil {
			return fmt.Errorf("failed to read custom CA cert file %q: %w", certFile, err)
		}
		wandbCR.Spec.Global.CustomCACerts = append(wandbCR.Spec.Global.CustomCACerts, string(pem))
	}

	// --ingress-class and --gateway-class select mutually exclusive networking
	// modes. --gateway-class has a non-empty default ("nginx"), so an explicit
	// --ingress-class must take precedence and suppress the Gateway API config.
	if f.ingressClass != "" {
		wandbCR.Spec.Networking.Mode = v2.NetworkingModeIngress
		wandbCR.Spec.Networking.Ingress = &v2.IngressConfig{
			IngressClassName: &f.ingressClass,
			Name:             f.ingressName,
		}
	} else if f.gatewayClass != "" {
		wandbCR.Spec.Networking.Mode = v2.NetworkingModeGatewayAPI
		wandbCR.Spec.Networking.GatewayAPI = &v2.GatewayAPIConfig{
			Gateway: v2.GatewayConfig{
				Managed:          true,
				GatewayClassName: &f.gatewayClass,
			},
		}
	}

	if f.addIngressAnnotations {
		// InfrastructureAnnotations exist only on the managed Gateway; the CR's
		// IngressConfig has no annotations field. Apply them in Gateway API mode
		// and no-op otherwise to avoid a nil dereference.
		if wandbCR.Spec.Networking.GatewayAPI != nil {
			wandbCR.Spec.Networking.GatewayAPI.Gateway.InfrastructureAnnotations = map[string]string{
				"service.beta.kubernetes.io/aws-load-balancer-scheme": "internet-facing",
			}
		} else {
			fmt.Println("⚠ --add-ingress-annotations applies only to Gateway API mode; ignoring in Ingress mode")
		}
	}

	if strings.HasPrefix(wandbCR.Spec.Wandb.Hostname, "https") {
		if f.createCA {
			wandbCR.Spec.Networking.TLS = &v2.TLSConfig{
				SecretName: f.wandbName + "-tls-secret",
				CertManager: &v2.CertManagerConfig{
					Issuer: f.wandbName + "-ca-issuer",
				},
			}
		} else if f.issuerName != "" {
			wandbCR.Spec.Networking.TLS = &v2.TLSConfig{
				SecretName: f.wandbName + "-tls-secret",
				CertManager: &v2.CertManagerConfig{
					Issuer: f.issuerName,
				},
			}
		} else {
			return fmt.Errorf("cannot specify TLS without a CA or issuer")
		}
	}

	// Telemetry and copies mutate the single instance under DefaultInstanceName —
	// the one wsm's template builds. A --cr-file that keys managed infra under a
	// different instance name won't be touched by these flags; author such CRs
	// with telemetry/copies set directly.
	if f.telemetryMode != "" && f.telemetryMode != operator.TelemetryModeOff {
		// The map value is a struct copy, but ManagedX are pointers, so mutating
		// through them reaches the pointee — no write-back to the map needed.
		if m, ok := wandbCR.Spec.MySQL[v2.DefaultInstanceName]; ok && m.ManagedMysql != nil {
			m.ManagedMysql.Telemetry.Enabled = true
		}
		if wandbCR.Spec.Kafka.ManagedKafka != nil {
			wandbCR.Spec.Kafka.ManagedKafka.Telemetry.Enabled = true
		}
		if c, ok := wandbCR.Spec.ClickHouse[v2.DefaultInstanceName]; ok && c.ManagedClickHouse != nil {
			c.ManagedClickHouse.Telemetry.Enabled = true
		}
		if r, ok := wandbCR.Spec.Redis[v2.DefaultInstanceName]; ok && r.ManagedRedis != nil {
			r.ManagedRedis.Telemetry.Enabled = true
		}
		if o, ok := wandbCR.Spec.ObjectStore[v2.DefaultInstanceName]; ok && o.ManagedObjectStore != nil {
			o.ManagedObjectStore.Telemetry.Enabled = true
		}
	}

	if f.objectStoreCopies != nil {
		if o, ok := wandbCR.Spec.ObjectStore[v2.DefaultInstanceName]; ok && o.ManagedObjectStore != nil {
			o.ManagedObjectStore.Copies = *f.objectStoreCopies
		}
	}

	wandbCR.Namespace = f.wandbNamespace
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
	cmd.AddCommand(clusterListCmd())
	cmd.AddCommand(clusterCleanupCmd())

	return cmd
}

func clusterCreateCmd() *cobra.Command {
	var clusterName string
	var workers int
	var httpPort, httpsPort int32
	var kindNodeImage string
	var insecureRegistryHost string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new kind cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := performCreateCluster(context.Background(), clusterName, workers, httpPort, httpsPort, kindNodeImage, insecureRegistryHost); err != nil {
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
	cmd.Flags().StringVar(&kindNodeImage, "kind-node-image", "", "Kind node image to use, e.g. myreg.example.com/kindest/node:v1.35.1@sha256:... (defaults to the upstream pinned image)")
	cmd.Flags().StringVar(&insecureRegistryHost, "insecure-registry-host", "", "Configure containerd to pull from this host over plain HTTP (e.g. host.docker.internal:5000). Pairs with 'wsm registry mirror --insecure' for local-laptop testing against a plain-HTTP registry:2.")

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
			kubectl.SetContext(fmt.Sprintf("kind-%s", clusterName))

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

func clusterListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List wsm-managed Kind clusters",
		Long:  `List local Kind clusters that contain the wsm deployment marker`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			clusters, err := listWSMManagedKindClusters(ctx)
			if err != nil {
				return err
			}

			if len(clusters) == 0 {
				fmt.Println("! No wsm-managed Kind clusters found.")
				return nil
			}

			fmt.Println("WSM-managed Kind clusters:")
			for _, clusterName := range clusters {
				fmt.Printf("  • %s\n", clusterName)
			}

			return nil
		},
	}

	return cmd
}

func listWSMManagedKindClusters(ctx context.Context) ([]string, error) {
	kindClusters, err := kind.ListClusters()
	if err != nil {
		return nil, err
	}

	originalContext := kubectl.GetContext()
	defer func() {
		kubectl.SetContext(originalContext)
		kubectl.ResetClients()
	}()

	clusters := make([]string, 0, len(kindClusters))
	for _, clusterName := range kindClusters {
		kubectl.SetContext(fmt.Sprintf("kind-%s", clusterName))
		kubectl.ResetClients()

		hasMarker, err := kubectl.HasDeploymentMarker(ctx, "default", "kind-cluster")
		if err != nil {
			return nil, fmt.Errorf("failed to check deployment marker for cluster %q: %w", clusterName, err)
		}

		if hasMarker {
			clusters = append(clusters, clusterName)
		}
	}

	return clusters, nil
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
