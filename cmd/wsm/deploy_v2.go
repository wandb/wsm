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
const defaultWandbVersion = "0.81.0"

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
	cmd.PersistentFlags().String("object-store-storage-size", "", "Override the managed object store (SeaweedFS) storage size, e.g. 20Gi. Must be < 30Gi: the operator derives SeaweedFS volumeSizeLimitMB from this and the master rejects a limit >= 30000. Leave empty to use the size preset's default.")
	cmd.PersistentFlags().String("wandb-hostname", "http://localhost:8080", "Hostname to use for the W&B instance")
	cmd.PersistentFlags().String("wandb-name", "wandb", "Name of the W&B instance")
	cmd.PersistentFlags().String("wandb-version", "", "Server manifest version (e.g., 0.76.1)")
	cmd.PersistentFlags().String("manifest-repository", "", "OCI repository for the server manifest (e.g. oci://harbor.corp/wandb/server-manifest). Defaults to oci://<mirror>/wandb/server-manifest when --mirror-registry is set, else the operator default.")
	cmd.PersistentFlags().String("wandb-namespace", "wandb", "Namespace for CR")
	cmd.PersistentFlags().String("oidc-client-id", "", "OIDC client ID as <secret-name>:<key> (spec.wandb.oidc.clientId; optional)")
	cmd.PersistentFlags().String("oidc-client-secret", "", "OIDC client secret as <secret-name>:<key> (spec.wandb.oidc.clientSecret; optional)")
	cmd.PersistentFlags().String("oidc-issuer-url", "", "OIDC issuer URL as <secret-name>:<key> (spec.wandb.oidc.issuerUrl; optional)")
	cmd.PersistentFlags().String("oidc-auth-method", "", "OIDC auth method as <secret-name>:<key> (spec.wandb.oidc.authMethod; optional)")
	// TODO(operator-bump): add --oidc-session-length once OidcSpec.SessionLength
	// exists upstream; set it in processWandbCR (maps to app env
	// GORILLA_SESSION_LENGTH, operator default 720h).
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
			ingressName, _ := cmd.Flags().GetString("ingress-name")
			issuerName, _ := cmd.Flags().GetString("issuer-name")
			size, _ := cmd.Flags().GetString("size")
			retentionPolicy, _ := cmd.Flags().GetString("retention-policy")
			wandbNamespace, _ := cmd.Flags().GetString("wandb-namespace")
			wandbVersion, _ := cmd.Flags().GetString("wandb-version")
			wandbName, _ := cmd.Flags().GetString("wandb-name")
			wandbHostname, _ := cmd.Flags().GetString("wandb-hostname")
			manifestRepo, _ := cmd.Flags().GetString("manifest-repository")
			objectStoreStorageSize, _ := cmd.Flags().GetString("object-store-storage-size")
			mirrorRegistry, _ := cmd.Flags().GetString("mirror-registry")
			oidcClientID, _ := cmd.Flags().GetString("oidc-client-id")
			oidcClientSecret, _ := cmd.Flags().GetString("oidc-client-secret")
			oidcIssuerURL, _ := cmd.Flags().GetString("oidc-issuer-url")
			oidcAuthMethod, _ := cmd.Flags().GetString("oidc-auth-method")
			wait, _ := cmd.Flags().GetBool("wait")

			if err := validateObservabilityMode(telemetryMode); err != nil {
				return err
			}
			if err := validateNetworkingFlags(cmd.Flags().Changed("gateway-class"), gatewayClass, ingressClass); err != nil {
				return err
			}

			// When installing from a mirror, default the server-manifest source to
			// the mirror so the operator pulls the manifest (and every app image it
			// references) offline. 'wsm registry mirror --wandb-version' pushes it to
			// exactly this path.
			if manifestRepo == "" && mirrorRegistry != "" {
				manifestRepo = "oci://" + strings.TrimRight(mirrorRegistry, "/") + "/wandb/server-manifest"
			}

			ctx := context.Background()

			err := processWandbCR(
				crFile,
				wandbVersion,
				wandbName,
				wandbHostname,
				gatewayClass,
				ingressClass,
				ingressName,
				issuerName,
				addIngressAnnotations,
				license,
				licenseFile,
				telemetryMode,
				wandbNamespace,
				createCA,
				size,
				retentionPolicy,
				manifestRepo,
				objectStoreStorageSize,
				oidcClientID,
				oidcClientSecret,
				oidcIssuerURL,
				oidcAuthMethod,
			)
			if err != nil {
				return err
			}

			// Retarget the managed data-plane images (ClickHouse/MySQL/Redis/object
			// store) to the mirror. The operator reads spec.global.imageRegistry and
			// host-replaces each hardcoded image ref, so no node containerd config is
			// needed for them. (The managed-service operators and the Kafka broker are
			// already retargeted at operator-install time via Helm values — see
			// DeployOperator.)
			if mirrorRegistry != "" {
				wandbCR.Spec.Global.ImageRegistry = strings.TrimRight(mirrorRegistry, "/")
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

	cmd.Flags().String("mirror-registry", "", "Install the W&B instance from this mirror registry (e.g. harbor.corp:5443). Defaults --manifest-repository to oci://<mirror>/wandb/server-manifest. Populate it first with 'wsm registry mirror'.")
	return cmd
}

func operatorDeployCmd() *cobra.Command {
	var setupCluster bool
	var installCertManagerMode string
	var installNginxGatewayMode string
	var enableGatewayAPI bool
	var clusterName string
	var workers int
	var kindNodeImage string
	var operatorChartVersion string
	var operatorNamespace string
	var mirrorRegistry string
	var insecureRegistry bool
	var registryCAFile string
	var gatewayCRDURL string
	var skipGatewayCRDs bool
	var allowUnsupportedArch bool

	cmd := &cobra.Command{
		Use:   "operator",
		Short: "Deploy the v2 operator",
		Long:  `Deploy the v2 operator with specified versions and configuration`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Phase 1 installs only the operator stack (cert-manager,
			// nginx-gateway, the wandb-operator + the managed-service operators).
			// The W&B instance itself is a separate phase — `wsm deploy-v2 wandb
			// deploy` — so none of the CR/app flags are read here.
			gatewayClass, _ := cmd.Flags().GetString("gateway-class")
			ingressClass, _ := cmd.Flags().GetString("ingress-class")
			telemetryMode, _ := cmd.Flags().GetString("observability-mode")
			telemetryForwardEndpoint, _ := cmd.Flags().GetString("observability-forward-endpoint")
			otelSecret, _ := cmd.Flags().GetString("observability-otel-secret")
			otelProtocol, _ := cmd.Flags().GetString("observability-otel-protocol")
			otelServiceName, _ := cmd.Flags().GetString("observability-otel-service-name")
			otelResourceAttrs, _ := cmd.Flags().GetString("observability-otel-resource-attributes")
			forwardProtocol, _ := cmd.Flags().GetString("observability-forward-protocol")
			forwardHeaders, _ := cmd.Flags().GetStringToString("observability-forward-headers")
			wandbNamespace, _ := cmd.Flags().GetString("wandb-namespace")

			if mirrorRegistry != "" && insecureRegistry {
				fmt.Println("⚠ --insecure-registry: the operator stack will install from the mirror, but the W&B instance will NOT reconcile — the operator fetches the server manifest over HTTPS and cannot use a plain-HTTP registry. For a full offline install, serve the mirror over HTTPS and pass --registry-ca-file. See docs/deployment/on-prem.md.")
			}

			if err := validateObservabilityMode(telemetryMode); err != nil {
				return err
			}
			if telemetryMode == operator.TelemetryModeForward && telemetryForwardEndpoint == "" {
				return fmt.Errorf("--observability-mode=forward requires --observability-forward-endpoint")
			}
			if err := validateNetworkingFlags(cmd.Flags().Changed("gateway-class"), gatewayClass, ingressClass); err != nil {
				return err
			}

			telemetry := operator.TelemetryConfig{
				Mode:              telemetryMode,
				ForwardEndpoint:   telemetryForwardEndpoint,
				OtelSecretName:    otelSecret,
				OtelProtocol:      otelProtocol,
				OtelServiceName:   otelServiceName,
				OtelResourceAttrs: otelResourceAttrs,
				ForwardProtocol:   forwardProtocol,
				ForwardHeaders:    forwardHeaders,
			}

			// Perform the deployment
			deployStart := time.Now()
			if err := performDeploy(
				setupCluster,
				installCertManagerMode,
				installNginxGatewayMode,
				enableGatewayAPI,
				clusterName,
				telemetry,
				wandbNamespace,
				workers,
				operatorChartVersion,
				operatorNamespace,
				kindNodeImage,
				mirrorRegistry,
				insecureRegistry,
				registryCAFile,
				gatewayCRDURL,
				skipGatewayCRDs,
				allowUnsupportedArch,
			); err != nil {
				fmt.Printf("\n✗ Operator install failed: %v\n", err)
				return err
			}

			totalTime := time.Since(deployStart).Round(time.Second)
			fmt.Printf("\n✓ Operator stack installed! (%s total)\n\n", totalTime)
			fmt.Println("Next: install the W&B instance with 'wsm deploy-v2 wandb deploy'", "(add --mirror-registry for an air-gapped install).")
			return nil
		},
	}

	cmd.Flags().BoolVar(&setupCluster, "setup-k8s-cluster", false, "Setup a Kind cluster before deploying")
	cmd.Flags().StringVar(&clusterName, "cluster-name", "kind", "Name of the Kind cluster (only used with --setup-k8s-cluster)")
	cmd.Flags().IntVar(&workers, "workers", 0, "Number of worker nodes (only used with --setup-k8s-cluster)")
	cmd.Flags().StringVar(&kindNodeImage, "kind-node-image", "", "Kind node image to use, e.g. myreg.example.com/kindest/node:v1.35.1@sha256:... (defaults to the upstream pinned image; only used with --setup-k8s-cluster)")

	cmd.Flags().StringVar(&operatorChartVersion, "operator-chart-version", "2.0.0-alpha.2", "Operator Chart version (e.g., v2.0.0)")
	cmd.Flags().StringVar(&operatorNamespace, "operator-namespace", "wandb-operators", "Namespace for operator")
	cmd.Flags().StringVar(&installCertManagerMode, "install-cert-manager", certManagerInstallModeAuto, "Cert-manager install mode: auto (detect and reuse existing), true (force install flow), false (skip installation)")
	cmd.Flags().StringVar(&installNginxGatewayMode, "install-nginx-gateway", nginxGatewayInstallModeAuto, "Nginx-gateway-fabric install mode: auto (detect and reuse existing), true (force install flow), false (skip installation)")

	cmd.Flags().BoolVar(&enableGatewayAPI, "enable-gateway-api", true, "Disables Gateway API support for cert-manager")
	cmd.Flags().StringVar(&mirrorRegistry, "mirror-registry", "", "Pull every chart and image from this registry (e.g. harbor.corp:5443). Populate it first with 'wsm registry mirror --to <same-host>'.")
	cmd.Flags().BoolVar(&insecureRegistry, "insecure-registry", false, "Use plain HTTP / skip TLS verification when fetching from --mirror-registry (installs the operator stack offline, but the W&B instance needs an HTTPS mirror — see --registry-ca-file)")
	cmd.Flags().StringVar(&registryCAFile, "registry-ca-file", "", "PEM CA bundle to trust for an HTTPS --mirror-registry with a self-signed / internal-CA cert. wsm uses it for chart pulls and mounts it into the operator so its server-manifest fetch trusts the registry. For an auth'd registry, run 'docker login' and 'helm registry login' first.")
	cmd.Flags().StringVar(&gatewayCRDURL, "gateway-api-crd-url", "", "Fetch the Gateway API CRDs from this URL instead of the GitHub default (use a mirrored copy for air-gapped installs)")
	cmd.Flags().BoolVar(&skipGatewayCRDs, "skip-gateway-api-crds", false, "Assume the Gateway API CRDs are already installed; fail instead of fetching them from the internet")
	cmd.Flags().BoolVar(&allowUnsupportedArch, "allow-unsupported-arch", false, "Deploy even if the cluster has non-amd64 nodes. The wandb-operator image is published amd64-only and will crash under emulation on arm64 (e.g. Kind on Apple Silicon); set this only if you know your operator image is multi-arch.")
	return cmd
}

// checkOperatorArch fails fast when the target cluster has nodes the
// wandb-operator image can't run on. The operator image is published amd64-only
// today; scheduling it onto an arm64 node (e.g. a Kind cluster on an Apple
// Silicon Mac) runs the crd-installer under qemu emulation, where it SIGSEGVs
// and wedges the install with no obvious cause. We inspect the live nodes
// (rather than the host arch) so the common "arm64 laptop → remote amd64
// cluster" workflow is not blocked. A probe failure is non-fatal: if we can't
// read the nodes we let the install proceed rather than guess.
func checkOperatorArch(ctx context.Context, operatorImageTag string, allowUnsupported bool) error {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return nil
	}
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		return nil
	}

	var unsupported []string
	for _, n := range nodes.Items {
		arch := n.Labels["kubernetes.io/arch"]
		if arch == "" {
			arch = n.Status.NodeInfo.Architecture
		}
		if arch != "" && arch != "amd64" {
			unsupported = append(unsupported, fmt.Sprintf("%s (%s)", n.Name, arch))
		}
	}
	if len(unsupported) == 0 {
		return nil
	}

	msg := fmt.Sprintf(
		"the wandb-operator image (%s) is published amd64-only, but the cluster has non-amd64 node(s): %s.\n"+
			"  On arm64 (e.g. Kind on an Apple Silicon Mac) the operator runs under qemu emulation and crashes (SIGSEGV in crd-installer).\n"+
			"  Use an amd64 host/cluster, or run against a remote amd64 cluster.",
		operatorImageTag, strings.Join(unsupported, ", "))

	if allowUnsupported {
		fmt.Printf("⚠ %s\n  Continuing anyway because --allow-unsupported-arch was set.\n", msg)
		return nil
	}
	return fmt.Errorf("%s\n  Pass --allow-unsupported-arch to override (only if you know your operator image is multi-arch)", msg)
}

func performDeploy(
	setupCluster bool,
	installCertManagerMode string,
	installNginxGatewayMode string,
	enableGatewayAPI bool,
	clusterName string,
	telemetry operator.TelemetryConfig,
	wandbNamespace string,
	workers int,
	operatorChartVersion string,
	operatorNamespace string,
	kindNodeImage string,
	mirrorRegistry string,
	insecureRegistry bool,
	registryCAFile string,
	gatewayCRDURL string,
	skipGatewayCRDs bool,
	allowUnsupportedArch bool,
) error {
	ctx := context.Background()
	installNginxGatewayMode = strings.ToLower(strings.TrimSpace(installNginxGatewayMode))
	installCertManagerMode = strings.ToLower(strings.TrimSpace(installCertManagerMode))

	var mirror *operator.MirrorConfig
	if mirrorRegistry != "" {
		mirror = &operator.MirrorConfig{
			Host:     strings.TrimRight(mirrorRegistry, "/"),
			Insecure: insecureRegistry,
			CAFile:   registryCAFile,
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
			if err := operator.InstallNginxGateway(ctx, true, mirror, gatewayCRDURL, skipGatewayCRDs); err != nil {
				fmt.Println(" ✗")
				return err
			}
		case nginxGatewayInstallModeTrue:
			if err := operator.InstallNginxGateway(ctx, false, mirror, gatewayCRDURL, skipGatewayCRDs); err != nil {
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

	// Fail fast on an arch the operator image can't run on, before we start the
	// (long) operator install only to watch its crd-installer SIGSEGV.
	if err := checkOperatorArch(ctx, operatorChartVersion, allowUnsupportedArch); err != nil {
		return err
	}

	// Step 4: Deploy W&B operator
	fmt.Printf("[%d/%d] Deploying Required operators...", currentStep, totalSteps)
	start := time.Now()

	if err := operator.DeployOperator(ctx, operatorNamespace, operatorChartVersion, mirror, telemetry, wandbNamespace); err != nil {
		fmt.Println(" ✗")
		return err
	}

	// For an HTTPS mirror with a self-signed / internal CA, mount that CA into the
	// operator so its in-cluster server-manifest fetch trusts the registry. Done
	// before WaitForOperator so the wait observes the rolled (CA-trusting) pod.
	if mirror != nil && mirror.CAFile != "" {
		if err := operator.InjectRegistryCAIntoOperator(ctx, operatorNamespace, mirror.CAFile); err != nil {
			fmt.Println(" ✗")
			return err
		}
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

func processWandbCR(
	crFile string,
	wandbVersion string,
	wandbName string,
	wandbHostname string,
	gatewayClass string,
	ingressClass string,
	ingressName string,
	issuerName string,
	addIngressAnnotations bool,
	license string,
	licenseFile string,
	telemetryMode string,
	wandbNamespace string,
	createCA bool,
	size string,
	retentionPolicy string,
	manifestRepo string,
	objectStoreStorageSize string,
	oidcClientID string,
	oidcClientSecret string,
	oidcIssuerURL string,
	oidcAuthMethod string,
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

	if manifestRepo != "" {
		wandbCR.Spec.Wandb.ManifestRepository = manifestRepo
	}

	// Override the managed object store storage size when requested. The operator
	// derives SeaweedFS's master volumeSizeLimitMB from this value (storage / 1MiB)
	// and the master refuses to start with a limit >= 30000, so the size preset's
	// default (e.g. 100Gi -> 102400) crashes SeaweedFS. Keep this under 30Gi.
	if objectStoreStorageSize != "" && wandbCR.Spec.ObjectStore.ManagedObjectStore != nil {
		wandbCR.Spec.ObjectStore.ManagedObjectStore.StorageSize = objectStoreStorageSize
	}

	if license != "" && licenseFile != "" {
		return fmt.Errorf("cannot specify both license and license file")
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

	// Each OIDC leaf is <secret-name>:<key>. --cr-file wins: a leaf it already set
	// is left alone (the flag for it is ignored). Empty leaves stay zero and get
	// stripped by operator.ApplyCR when none is configured (stripFieldsNotInCRDSchema).
	oidcRefs := []struct {
		value string
		field *corev1.SecretKeySelector
		flag  string
	}{
		{oidcClientID, &wandbCR.Spec.Wandb.OIDC.ClientId, "--oidc-client-id"},
		{oidcClientSecret, &wandbCR.Spec.Wandb.OIDC.ClientSecret, "--oidc-client-secret"},
		{oidcIssuerURL, &wandbCR.Spec.Wandb.OIDC.IssuerUrl, "--oidc-issuer-url"},
		{oidcAuthMethod, &wandbCR.Spec.Wandb.OIDC.AuthMethod, "--oidc-auth-method"},
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

	// --ingress-class and --gateway-class select mutually exclusive networking
	// modes. --gateway-class has a non-empty default ("nginx"), so an explicit
	// --ingress-class must take precedence and suppress the Gateway API config.
	if ingressClass != "" {
		wandbCR.Spec.Networking.Mode = v2.NetworkingModeIngress
		wandbCR.Spec.Networking.Ingress = &v2.IngressConfig{
			IngressClassName: &ingressClass,
			Name:             ingressName,
		}
	} else if gatewayClass != "" {
		wandbCR.Spec.Networking.Mode = v2.NetworkingModeGatewayAPI
		wandbCR.Spec.Networking.GatewayAPI = &v2.GatewayAPIConfig{
			Gateway: v2.GatewayConfig{
				Managed:          true,
				GatewayClassName: &gatewayClass,
			},
		}
	}

	if addIngressAnnotations {
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

	if telemetryMode != "" && telemetryMode != operator.TelemetryModeOff {
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
