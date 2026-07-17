package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/docker/go-connections/tlsconfig"
	appsv1 "github.com/wandb/operator/api/v1"
	v2 "github.com/wandb/operator/api/v2"
	"github.com/wandb/wsm/pkg/kubectl"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/cli"
	"helm.sh/helm/v4/pkg/registry"
	v1 "helm.sh/helm/v4/pkg/release/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// CreateNamespace creates a namespace if it doesn't exist
func CreateNamespace(ctx context.Context, namespace string) error {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return err
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}

	_, err = cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("failed to create namespace: %w", err)
	}

	return nil
}

const (
	certManagerNamespace      = "cert-manager"
	certManagerDeploymentName = "cert-manager"
	certManagerReleaseName    = "cert-manager"
	certManagerChartRef       = "oci://quay.io/jetstack/charts/cert-manager"
	// CertManagerVersion is the cert-manager chart and image tag wsm installs.
	// Exported so `wsm registry mirror` can build a mirroring plan that matches
	// exactly what `wsm deploy-v2 operator` will request.
	CertManagerVersion = "v1.20.2"

	nginxGatewayNamespace      = "nginx-gateway"
	nginxGatewayDeploymentName = "nginx-gateway-nginx-gateway-fabric"
	nginxGatewayReleaseName    = "nginx-gateway"
	nginxGatewayChartRef       = "oci://ghcr.io/nginx/charts/nginx-gateway-fabric"
	// NginxGatewayVersion is the nginx-gateway-fabric chart and image tag wsm
	// installs. Exported for the same reason as CertManagerVersion.
	NginxGatewayVersion = "2.5.1"

	gatewayApiCRDURL                     = "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.0/standard-install.yaml"
	completeServerAPIsDiscoveryErrSubstr = "unable to retrieve the complete list of server APIs"
)

var (
	apiDiscoveryRetryInterval = 2 * time.Second
	apiDiscoveryRetryTimeout  = 2 * time.Minute
)

// setNested walks (and creates) a chain of map[string]any keys, then sets the
// leaf value at `path[len(path)-1]` to `value`. Mirrors apimachinery's nested
// helpers but works on `map[string]any` (Helm release values) instead of
// Unstructured objects.
func setNested(obj map[string]any, value any, path ...string) {
	m := obj
	for _, k := range path[:len(path)-1] {
		next, ok := m[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[k] = next
		}
		m = next
	}
	m[path[len(path)-1]] = value
}

// MirrorConfig points the install functions at a customer-controlled registry
// instead of the upstream defaults baked into wsm. When non-nil, every OCI
// chart reference and container image repository is rewritten to live under
// Host (using wsm's path conventions; see registry_mirror.go for the matching
// push targets). Populate the mirror with `wsm registry mirror --to <Host>`
// before installing.
type MirrorConfig struct {
	Host     string // hostname[:port] of the mirror, e.g. "local-registry:5000"
	Insecure bool   // plain HTTP / skip TLS verify; passed to Helm's OCI client
	CAFile   string // path to a PEM CA bundle to trust for an HTTPS mirror (self-signed / internal CA)
}

// InstallCertManager installs cert-manager.
// When skipIfPresent is true, installation is skipped if the cert-manager deployment already exists.
func InstallCertManager(ctx context.Context, enableGatewayAPI bool, skipIfPresent bool, mirror *MirrorConfig) error {
	if skipIfPresent {
		deploymentExists, err := certManagerDeploymentExists(ctx)
		if err != nil {
			return err
		}
		if deploymentExists {
			return nil
		}
	}

	if err := CreateNamespace(ctx, certManagerNamespace); err != nil {
		return err
	}

	// Initialize Helm settings
	settings := cli.New()
	settings.SetNamespace(certManagerNamespace)
	settings.KubeContext = kubectl.GetContext()

	// Initialize action configuration
	actionConfig, err := initActionConfig(settings)
	if err != nil {
		return fmt.Errorf("failed to initialize action config: %w", err)
	}

	// Create registry client. Plain-HTTP / TLS-skip needed for self-hosted
	// mirrors that don't have a real cert (e.g. a local registry:2).
	plainHTTP := mirror != nil && mirror.Insecure
	registryClient, err := newRegistryClient(settings, "", "", mirrorCAFile(mirror), plainHTTP, plainHTTP)
	if err != nil {
		return fmt.Errorf("failed to create registry client: %w", err)
	}
	actionConfig.RegistryClient = registryClient

	// Check if release already exists
	releaseExists, err := checkReleaseExists(actionConfig, certManagerReleaseName)
	if err != nil {
		return fmt.Errorf("failed to check if release exists: %w", err)
	}

	chartRef := certManagerChartRef
	if mirror != nil {
		chartRef = "oci://" + mirror.Host + "/jetstack/charts/cert-manager"
	}

	releaseValues := map[string]interface{}{
		"crds": map[string]interface{}{
			"enabled": true,
		},
		"config": map[string]interface{}{
			"enableGatewayAPI": enableGatewayAPI,
		},
		"startupapicheck": map[string]interface{}{
			"enabled": false,
		},
	}

	if mirror != nil {
		// cert-manager v1.20 composes per-component image refs as
		// <imageRegistry>/<imageNamespace>/cert-manager-<component>:<tag>.
		// Setting both makes all 5 component images resolve to the mirror.
		releaseValues["imageRegistry"] = mirror.Host
		releaseValues["imageNamespace"] = "jetstack"
	}

	if releaseExists {
		// Create upgrade action
		upgradeClient := action.NewUpgrade(actionConfig)
		upgradeClient.Namespace = certManagerNamespace
		upgradeClient.Version = CertManagerVersion
		upgradeClient.WaitStrategy = "hookOnly"
		upgradeClient.ForceConflicts = true
		//upgradeClient.ResetValues = true

		// Get the chart
		cp, err := upgradeClient.LocateChart(chartRef, settings)
		if err != nil {
			return fmt.Errorf("failed to locate cert-manager chart: %w", err)
		}

		// Load the chart
		chartRequested, err := loader.Load(cp)
		if err != nil {
			return fmt.Errorf("failed to load cert-manager chart: %w", err)
		}

		// Run the upgrade
		_, err = upgradeClient.RunWithContext(ctx, certManagerReleaseName, chartRequested, releaseValues)
		if err != nil {
			return fmt.Errorf("failed to upgrade cert-manager: %w", err)
		}
	} else {
		// Create install action
		installClient := action.NewInstall(actionConfig)
		installClient.Namespace = certManagerNamespace
		installClient.ReleaseName = certManagerReleaseName
		installClient.Version = CertManagerVersion
		installClient.WaitStrategy = "hookOnly"

		// Get the chart
		cp, err := installClient.LocateChart(chartRef, settings)
		if err != nil {
			return fmt.Errorf("failed to locate cert-manager chart: %w", err)
		}

		// Load the chart
		chartRequested, err := loader.Load(cp)
		if err != nil {
			return fmt.Errorf("failed to load cert-manager chart: %w", err)
		}

		// Run the install
		_, err = installClient.RunWithContext(ctx, chartRequested, releaseValues)
		if err != nil {
			return fmt.Errorf("failed to install cert-manager: %w", err)
		}
	}

	return nil
}

// WaitForCertManager waits for cert-manager to be ready
func WaitForCertManager(ctx context.Context, timeout time.Duration) error {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return err
	}

	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		deploy, err := cs.AppsV1().Deployments(certManagerNamespace).Get(ctx, certManagerDeploymentName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}

		isAvailable := false
		for _, cond := range deploy.Status.Conditions {
			if cond.Type == "Available" && cond.Status == corev1.ConditionTrue {
				isAvailable = true
				break
			}
		}
		return isAvailable, nil
	})
}

func certManagerDeploymentExists(ctx context.Context) (bool, error) {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return false, err
	}

	_, err = cs.AppsV1().Deployments(certManagerNamespace).Get(ctx, certManagerDeploymentName, metav1.GetOptions{})
	if err == nil {
		return true, nil
	}
	if errors.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("failed to check cert-manager deployment %q: %w", certManagerDeploymentName, err)
}

// DeleteCertManager deletes the cert-manager resources
func DeleteCertManager(ctx context.Context) error {
	settings := cli.New()
	settings.SetNamespace(certManagerNamespace)
	settings.KubeContext = kubectl.GetContext()

	actionConfig, err := initActionConfig(settings)
	if err != nil {
		return fmt.Errorf("failed to initialize action config: %w", err)
	}

	uninstallClient := action.NewUninstall(actionConfig)
	uninstallClient.WaitStrategy = "hookOnly"
	uninstallClient.Timeout = 5 * time.Minute

	_, err = uninstallClient.Run(certManagerReleaseName)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil
		}
		return fmt.Errorf("failed to uninstall cert-manager: %w", err)
	}

	return nil
}

// InstallNginxGateway installs nginx-gateway-fabric.
// When skipIfPresent is true, installation is skipped if the nginx-gateway-fabric deployment already exists.
func InstallNginxGateway(ctx context.Context, skipIfPresent bool, mirror *MirrorConfig, gatewayCRDURL string, skipGatewayCRDs bool) error {
	if skipIfPresent {
		deploymentExists, err := nginxGatewayDeploymentExists(ctx)
		if err != nil {
			return err
		}
		if deploymentExists {
			return nil
		}
	}

	exists, err := gatewayApiCRDsExist(ctx)
	if err != nil {
		return fmt.Errorf("failed to check if gateway api crds exist: %w", err)
	}
	if !exists {
		// In an air-gapped network the default URL is unreachable. --skip-gateway-api-crds
		// lets the customer pre-apply the CRDs (e.g. from a mirrored copy) and have wsm
		// assume they're present; --gateway-api-crd-url points the fetch at an internal host.
		if skipGatewayCRDs {
			return fmt.Errorf("gateway API CRDs are not installed and --skip-gateway-api-crds was set; apply them first (kubectl apply -f <gateway-api standard-install.yaml>)")
		}
		crdURL := gatewayCRDURL
		if crdURL == "" {
			crdURL = gatewayApiCRDURL
		}
		if err := installGatewayApiCRDs(ctx, crdURL); err != nil {
			return fmt.Errorf("failed to install gateway api crds: %w", err)
		}
	}

	if err := CreateNamespace(ctx, nginxGatewayNamespace); err != nil {
		return err
	}

	// Initialize Helm settings
	settings := cli.New()
	settings.SetNamespace(nginxGatewayNamespace)
	settings.KubeContext = kubectl.GetContext()

	// Initialize action configuration
	actionConfig, err := initActionConfig(settings)
	if err != nil {
		return fmt.Errorf("failed to initialize action config: %w", err)
	}

	// Create registry client. Plain-HTTP / TLS-skip needed for self-hosted
	// mirrors that don't have a real cert (e.g. a local registry:2).
	plainHTTP := mirror != nil && mirror.Insecure
	registryClient, err := newRegistryClient(settings, "", "", mirrorCAFile(mirror), plainHTTP, plainHTTP)
	if err != nil {
		return fmt.Errorf("failed to create registry client: %w", err)
	}
	actionConfig.RegistryClient = registryClient

	// Check if release already exists
	releaseExists, err := checkReleaseExists(actionConfig, nginxGatewayReleaseName)
	if err != nil {
		return fmt.Errorf("failed to check if release exists: %w", err)
	}

	chartRef := nginxGatewayChartRef
	if mirror != nil {
		chartRef = "oci://" + mirror.Host + "/nginx/charts/nginx-gateway-fabric"
	}

	releaseValues := map[string]any{}

	if strings.HasPrefix(kubectl.GetContext(), "kind-") {
		releaseValues["nginx"] = map[string]any{
			"service": map[string]any{
				"type": "NodePort",
				"nodePorts": []map[string]any{
					{"port": 31437, "listenerPort": 8080},
					{"port": 30478, "listenerPort": 8443},
				},
			},
		}
	}

	if mirror != nil {
		// nginx-gateway-fabric has no global imageRegistry — each component
		// repository is set independently. Merge with any existing nginx.*
		// values (the Kind NodePort block above).
		setNested(releaseValues, mirror.Host+"/nginx/nginx-gateway-fabric", "nginxGateway", "image", "repository")
		setNested(releaseValues, mirror.Host+"/nginx/nginx-gateway-fabric/nginx", "nginx", "image", "repository")
	}

	if releaseExists {
		// Create upgrade action
		upgradeClient := action.NewUpgrade(actionConfig)
		upgradeClient.Namespace = nginxGatewayNamespace
		upgradeClient.Version = NginxGatewayVersion
		upgradeClient.WaitStrategy = "hookOnly"
		upgradeClient.ForceConflicts = true
		upgradeClient.SkipSchemaValidation = true

		// Get the chart
		cp, err := upgradeClient.LocateChart(chartRef, settings)
		if err != nil {
			return fmt.Errorf("failed to locate nginx-gateway chart: %w", err)
		}

		// Load the chart
		chartRequested, err := loader.Load(cp)
		if err != nil {
			return fmt.Errorf("failed to load nginx-gateway chart: %w", err)
		}

		// Run the upgrade
		_, err = upgradeClient.RunWithContext(ctx, nginxGatewayReleaseName, chartRequested, releaseValues)
		if err != nil {
			return fmt.Errorf("failed to upgrade nginx-gateway: %w", err)
		}
	} else {
		// Create install action
		installClient := action.NewInstall(actionConfig)
		installClient.Namespace = nginxGatewayNamespace
		installClient.ReleaseName = nginxGatewayReleaseName
		installClient.Version = NginxGatewayVersion
		installClient.WaitStrategy = "hookOnly"
		installClient.SkipSchemaValidation = true

		// Get the chart
		cp, err := installClient.LocateChart(chartRef, settings)
		if err != nil {
			return fmt.Errorf("failed to locate nginx-gateway chart: %w", err)
		}

		// Load the chart
		chartRequested, err := loader.Load(cp)
		if err != nil {
			return fmt.Errorf("failed to load nginx-gateway chart: %w", err)
		}

		// Run the install
		_, err = installClient.RunWithContext(ctx, chartRequested, releaseValues)
		if err != nil {
			return fmt.Errorf("failed to install nginx-gateway: %w", err)
		}
	}

	return nil
}

// WaitForNginxGateway waits for nginx-gateway-fabric to be ready
func WaitForNginxGateway(ctx context.Context, timeout time.Duration) error {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return err
	}

	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		deploy, err := cs.AppsV1().Deployments(nginxGatewayNamespace).Get(ctx, nginxGatewayDeploymentName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}

		isAvailable := false
		for _, cond := range deploy.Status.Conditions {
			if cond.Type == "Available" && cond.Status == corev1.ConditionTrue {
				isAvailable = true
				break
			}
		}
		return isAvailable, nil
	})
}

func nginxGatewayDeploymentExists(ctx context.Context) (bool, error) {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return false, err
	}

	_, err = cs.AppsV1().Deployments(nginxGatewayNamespace).Get(ctx, nginxGatewayDeploymentName, metav1.GetOptions{})
	if err == nil {
		return true, nil
	}
	if errors.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("failed to check nginx-gateway deployment %q: %w", nginxGatewayDeploymentName, err)
}

// DeleteNginxGateway deletes the nginx-gateway-fabric resources
func DeleteNginxGateway(ctx context.Context) error {
	settings := cli.New()
	settings.SetNamespace(nginxGatewayNamespace)
	settings.KubeContext = kubectl.GetContext()

	actionConfig, err := initActionConfig(settings)
	if err != nil {
		return fmt.Errorf("failed to initialize action config: %w", err)
	}

	uninstallClient := action.NewUninstall(actionConfig)
	uninstallClient.WaitStrategy = "hookOnly"
	uninstallClient.Timeout = 5 * time.Minute

	_, err = uninstallClient.Run(nginxGatewayReleaseName)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil
		}
		return fmt.Errorf("failed to uninstall nginx-gateway: %w", err)
	}

	return nil
}

// gatewayApiCRDsExist checks if Gateway API CRDs exist in the cluster
func gatewayApiCRDsExist(ctx context.Context) (bool, error) {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return false, err
	}

	resources, err := discoverServerGroupsAndResourcesWithRetry(ctx, func() ([]*metav1.APIResourceList, error) {
		_, resourceLists, discoveryErr := cs.Discovery().ServerGroupsAndResources()
		return resourceLists, discoveryErr
	})
	if err != nil && resources == nil {
		return false, err
	}

	foundGateways := false
	foundHTTPRoutes := false
	foundGRPCRoutes := false

	for _, list := range resources {
		gv, _ := schema.ParseGroupVersion(list.GroupVersion)
		if gv.Group != "gateway.networking.k8s.io" {
			continue
		}
		for _, resource := range list.APIResources {
			switch resource.Name {
			case "gateways":
				foundGateways = true
			case "httproutes":
				foundHTTPRoutes = true
			case "grpcroutes":
				foundGRPCRoutes = true
			}
		}
	}

	if foundGateways && foundHTTPRoutes && foundGRPCRoutes {
		return true, nil
	}

	return false, err
}

func discoverServerGroupsAndResourcesWithRetry(
	ctx context.Context,
	discoverFn func() ([]*metav1.APIResourceList, error),
) ([]*metav1.APIResourceList, error) {
	var resources []*metav1.APIResourceList
	var discoveryErr error

	pollErr := wait.PollUntilContextTimeout(ctx, apiDiscoveryRetryInterval, apiDiscoveryRetryTimeout, true, func(context.Context) (bool, error) {
		resources, discoveryErr = discoverFn()
		if discoveryErr != nil && resources == nil {
			if isRetryableServerAPIDiscoveryError(discoveryErr) {
				return false, nil
			}
			return false, discoveryErr
		}

		return true, nil
	})
	if pollErr != nil {
		return nil, pollErr
	}

	return resources, discoveryErr
}

func isRetryableServerAPIDiscoveryError(err error) bool {
	return strings.Contains(err.Error(), completeServerAPIsDiscoveryErrSubstr)
}

// installGatewayApiCRDs installs Gateway API CRDs from the given YAML URL.
func installGatewayApiCRDs(ctx context.Context, crdURL string) error {
	resp, err := http.Get(crdURL)
	if err != nil {
		return fmt.Errorf("failed to fetch Gateway API CRDs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch Gateway API CRDs: status %s", resp.Status)
	}

	yamlContent, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read Gateway API CRDs: %w", err)
	}

	if err := kubectl.ApplyYAML(ctx, yamlContent); err != nil {
		return fmt.Errorf("failed to apply Gateway API CRDs: %w", err)
	}

	return nil
}

// DeployOperator deploys the W&B operator chart version specified.  The chart is called operator and is available in oci://us-docker.pkg.dev/wandb-production/public/wandb/charts
func DeployOperator(
	ctx context.Context,
	namespace string,
	chartVersion string,
	mirror *MirrorConfig,
	telemetry TelemetryConfig,
	wandbNamespace string,
) error {
	const chartName = "operator"
	const releaseName = "wandb-operator"

	repositoryURL := "oci://us-docker.pkg.dev/wandb-production/public/wandb/charts"
	if mirror != nil {
		repositoryURL = "oci://" + mirror.Host + "/wandb/charts"
	}
	chartRef := repositoryURL + "/" + chartName

	// Initialize Helm settings
	settings := cli.New()
	settings.SetNamespace(namespace)
	settings.KubeContext = kubectl.GetContext()

	// Initialize action configuration
	actionConfig, err := initActionConfig(settings)
	if err != nil {
		return fmt.Errorf("failed to initialize action config: %w", err)
	}

	// Create registry client. Plain-HTTP / TLS-skip needed for self-hosted
	// mirrors that don't have a real cert (e.g. a local registry:2).
	plainHTTP := mirror != nil && mirror.Insecure
	registryClient, err := newRegistryClient(settings, "", "", mirrorCAFile(mirror), plainHTTP, plainHTTP)
	if err != nil {
		return fmt.Errorf("failed to create registry client: %w", err)
	}
	actionConfig.RegistryClient = registryClient

	// Check if release already exists
	releaseExists, err := checkReleaseExists(actionConfig, releaseName)
	if err != nil {
		return fmt.Errorf("failed to check if release exists: %w", err)
	}

	operatorImage := map[string]interface{}{
		"pullPolicy": "Always",
	}
	if mirror != nil {
		operatorImage["repository"] = mirror.Host + "/wandb/operator"
	}

	telemetryValues := buildTelemetryValues(telemetry)
	releaseValues := map[string]interface{}{
		"wandb": map[string]interface{}{
			"install": false,
		},
		"wandb-operator": map[string]interface{}{
			"image": operatorImage,
		},
		"telemetry": telemetryValues,
	}

	// Point the bundled managed-service operator subcharts at the mirror. Each
	// third-party subchart exposes images differently (no single global knob), so
	// we set each chart's own registry/repository key — all resolving to
	// <mirror>/<host-stripped path>, matching where `wsm registry mirror` pushes
	// them (translate() in registry_mirror.go). Helm deep-merges these over the
	// chart's values.yaml, so image tags and unrelated keys are preserved.
	// Kafka (Bufstream) has no subchart operator here and no Helm image knob; the
	// operator emits its data-plane images with upstream refs, so they reach the
	// mirror via each node's container-runtime registry mirror — not a Helm value
	// and not spec.global.imageRegistry (which --mirror-registry does not set).
	if mirror != nil {
		// moco injects three sidecar images into every MySQLCluster via the
		// controller's --agent-image / --fluent-bit-image / mysqld_exporter args
		// (driven by these chart values), so all four must be retargeted — not
		// just the controller image. Verified by `helm template`.
		releaseValues["moco"] = map[string]interface{}{
			"image":          map[string]interface{}{"repository": mirror.Host + "/cybozu-go/moco"},
			"agent":          map[string]interface{}{"image": map[string]interface{}{"repository": mirror.Host + "/cybozu-go/moco-agent"}},
			"fluentbit":      map[string]interface{}{"image": map[string]interface{}{"repository": mirror.Host + "/cybozu-go/moco/fluent-bit"}},
			"mysqldExporter": map[string]interface{}{"image": map[string]interface{}{"repository": mirror.Host + "/cybozu-go/moco/mysqld_exporter"}},
		}
		releaseValues["redis-operator"] = map[string]interface{}{
			"redisOperator": map[string]interface{}{"imageName": mirror.Host + "/opstree/redis-operator"},
		}
		releaseValues["seaweedfs-operator"] = map[string]interface{}{
			"image": map[string]interface{}{
				"registry":   mirror.Host,
				"repository": "chrislusf/seaweedfs-operator",
			},
		}
		releaseValues["altinity-clickhouse-operator"] = map[string]interface{}{
			"operator": map[string]interface{}{
				"image": map[string]interface{}{"registry": mirror.Host},
			},
			"metrics": map[string]interface{}{
				"image": map[string]interface{}{"registry": mirror.Host},
			},
			"crdHook": map[string]interface{}{
				"image": map[string]interface{}{"repository": mirror.Host + "/alpine/k8s"},
			},
		}
	}

	// The operator chart's telemetry-validation requires the caller to opt the
	// victoria-metrics-operator and grafana-operator dependencies in when
	// telemetry is enabled — their chart defaults are false (helm dependency
	// conditions are boolean-only). "full" runs the in-cluster Victoria stack
	// plus local Grafana; "forward" runs the Victoria stack and forwards OTLP
	// data to telemetry.forwarding.otlp.endpoint.
	if telemetry.Mode == TelemetryModeFull || telemetry.Mode == TelemetryModeForward {
		// The telemetry subchart deploys into the telemetry namespace (the W&B
		// namespace), not the operator's release namespace. It must already
		// exist — the chart does not create it — so ensure it here and pin
		// telemetry.namespace to match the CR's namespace.
		telemetryValues["namespace"] = wandbNamespace
		if err := CreateNamespace(ctx, wandbNamespace); err != nil {
			return fmt.Errorf("failed to ensure telemetry namespace %q: %w", wandbNamespace, err)
		}
	}
	// Enable the telemetry subchart dependencies (chart defaults are false). The
	// forwarding.otlp.* values for "forward" are already set by buildTelemetryValues.
	switch telemetry.Mode {
	case TelemetryModeFull:
		releaseValues["victoria-metrics-operator"] = map[string]interface{}{"enabled": true}
		releaseValues["grafana-operator"] = map[string]interface{}{"enabled": true}
	case TelemetryModeForward:
		releaseValues["victoria-metrics-operator"] = map[string]interface{}{"enabled": true}
	}

	if releaseExists {
		// Create upgrade action
		upgradeClient := action.NewUpgrade(actionConfig)
		upgradeClient.Namespace = namespace
		upgradeClient.Version = chartVersion
		upgradeClient.WaitStrategy = "hookOnly"
		upgradeClient.ForceConflicts = true

		// Get the chart
		cp, err := upgradeClient.LocateChart(chartRef, settings)
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
		installClient.Version = chartVersion
		installClient.WaitStrategy = "hookOnly"

		// Get the chart
		cp, err := installClient.LocateChart(chartRef, settings)
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

// WaitForOperator waits for operator to be ready by checking webhook CA bundle injection and deployment
func WaitForOperator(ctx context.Context, namespace string, timeout time.Duration) error {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return err
	}

	// Wait for webhook CA bundle to be injected
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		webhook, err := cs.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, "wandb-operator-mutating-webhook-configuration", metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if len(webhook.Webhooks) > 0 && len(webhook.Webhooks[0].ClientConfig.CABundle) > 0 {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("operator webhook CA bundle not ready: %w", err)
	}

	// Also wait for the deployment to be available
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		deploy, err := cs.AppsV1().Deployments(namespace).Get(ctx, "wandb-operator", metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		for _, cond := range deploy.Status.Conditions {
			if cond.Type == "Available" && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("operator deployment not ready: %w", err)
	}

	// Wait for webhook pods to be ready
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name=wandb-operator",
		})
		if err != nil {
			return false, nil
		}
		if len(pods.Items) == 0 {
			return false, nil
		}
		for _, pod := range pods.Items {
			podReady := false
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					podReady = true
					break
				}
			}
			if !podReady {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("operator pods not ready: %w", err)
	}

	// Give webhook service a few more seconds to be fully ready
	time.Sleep(10 * time.Second)

	return nil
}

// DeleteOperator uninstalls the W&B operator Helm release
func DeleteOperator(ctx context.Context, namespace string) error {
	const releaseName = "wandb-operator"

	settings := cli.New()
	settings.SetNamespace(namespace)
	settings.KubeContext = kubectl.GetContext()

	actionConfig, err := initActionConfig(settings)
	if err != nil {
		return fmt.Errorf("failed to initialize action config: %w", err)
	}

	uninstallClient := action.NewUninstall(actionConfig)
	uninstallClient.WaitStrategy = "hookOnly"
	uninstallClient.Timeout = 5 * time.Minute

	_, err = uninstallClient.Run(releaseName)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil
		}
		return fmt.Errorf("failed to uninstall operator: %w", err)
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
		release, ok := r.(*v1.Release)
		if !ok {
			continue
		}
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

// mirrorCAFile returns the CA bundle path for an HTTPS mirror with a self-signed
// or internal-CA certificate, or "" if none is configured.
func mirrorCAFile(mirror *MirrorConfig) string {
	if mirror == nil {
		return ""
	}
	return mirror.CAFile
}

// InjectRegistryCAIntoOperator makes the wandb-operator pod trust an HTTPS
// mirror's CA. The operator fetches the server manifest from inside the cluster
// with its own TLS-verifying client (not containerd), so a self-signed / internal
// CA must be present in the pod's trust store. We mount the CA as a secret and
// point Go's SSL_CERT_FILE at it — no operator image or chart change needed.
func InjectRegistryCAIntoOperator(ctx context.Context, namespace, caFile string) error {
	caData, err := os.ReadFile(caFile)
	if err != nil {
		return fmt.Errorf("read registry CA file %q: %w", caFile, err)
	}

	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return err
	}

	const secretName = "wsm-registry-ca"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
		Data:       map[string][]byte{"ca.crt": caData},
	}
	if _, err := cs.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("create registry CA secret: %w", err)
		}
		if _, err := cs.CoreV1().Secrets(namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update registry CA secret: %w", err)
		}
	}

	const deployName = "wandb-operator"
	dep, err := cs.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get operator deployment: %w", err)
	}
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("operator deployment %s/%s has no containers", namespace, deployName)
	}
	containerName := dep.Spec.Template.Spec.Containers[0].Name

	// Strategic-merge patch: lists merge by name, so this adds the volume, mount,
	// and env without disturbing what the chart already set, and is idempotent.
	patch := fmt.Sprintf(`{"spec":{"template":{"spec":{`+
		`"volumes":[{"name":%q,"secret":{"secretName":%q}}],`+
		`"containers":[{"name":%q,`+
		`"volumeMounts":[{"name":%q,"mountPath":"/etc/wsm-ca","readOnly":true}],`+
		`"env":[{"name":"SSL_CERT_FILE","value":"/etc/wsm-ca/ca.crt"}]}]}}}}`,
		secretName, secretName, containerName, secretName)

	return kubectl.PatchDeployment(ctx, deployName, namespace, types.StrategicMergePatchType, []byte(patch))
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

func ApplyCR(ctx context.Context, wandbCR *v2.WeightsAndBiases, overrides []CROverride) error {
	gvk := wandbCR.GroupVersionKind()
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(wandbCR)
	if err != nil {
		return fmt.Errorf("failed to convert to unstructured: %w", err)
	}
	obj := &unstructured.Unstructured{Object: data}

	obj.SetGroupVersionKind(gvk)
	obj.SetManagedFields(nil)
	obj.SetResourceVersion("")

	stripFieldsNotInCRDSchema(obj)

	// --cr-set overrides are applied last — after the template, --cr-file, typed
	// flags, and the strip — so a set field always wins and is never removed. The
	// CRD validates the result server-side on apply.
	for _, o := range overrides {
		if err := unstructured.SetNestedField(obj.Object, o.Value, o.Path...); err != nil {
			return fmt.Errorf("failed to apply --cr-set %s: %w", strings.Join(o.Path, "."), err)
		}
	}

	if err := kubectl.ApplyUnstructured(ctx, obj); err != nil {
		return fmt.Errorf("failed to apply CR: %w", err)
	}

	return nil
}

// CROverride is a parsed `--cr-set path=value` entry applied to the CR just
// before it is sent to the apiserver. Path is the dotted field path split into
// segments; Value is a JSON-compatible scalar/list/map inferred from the RHS.
type CROverride struct {
	Path  []string
	Value interface{}
}

// ParseCROverrides parses `path=value` entries. The value is interpreted as
// YAML, so `3` becomes a number, `true` a bool, `720h` a string, and `[a,b]` a
// list. Parsing happens up front so a malformed entry fails before any cluster
// change. Paths address map/struct fields by dotted name (list indices are not
// supported — use --cr-file for those).
func ParseCROverrides(sets []string) ([]CROverride, error) {
	overrides := make([]CROverride, 0, len(sets))
	for _, s := range sets {
		path, rawValue, ok := strings.Cut(s, "=")
		if !ok || path == "" {
			return nil, fmt.Errorf("--cr-set %q must be in path=value form", s)
		}
		var value interface{}
		if err := yaml.Unmarshal([]byte(rawValue), &value); err != nil {
			return nil, fmt.Errorf("--cr-set %q: invalid value: %w", s, err)
		}
		value, err := toJSONCompatible(value)
		if err != nil {
			return nil, fmt.Errorf("--cr-set %q: %w", s, err)
		}
		overrides = append(overrides, CROverride{Path: strings.Split(path, "."), Value: value})
	}
	return overrides, nil
}

// toJSONCompatible converts a yaml.Unmarshal result into the types
// unstructured.SetNestedField accepts (int64/float64/bool/string/nil, and
// slices/maps thereof), recursing through lists and maps.
func toJSONCompatible(v interface{}) (interface{}, error) {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, e := range val {
			n, err := toJSONCompatible(e)
			if err != nil {
				return nil, err
			}
			val[k] = n
		}
		return val, nil
	case map[interface{}]interface{}:
		m := make(map[string]interface{}, len(val))
		for k, e := range val {
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("map keys must be strings, got %T", k)
			}
			n, err := toJSONCompatible(e)
			if err != nil {
				return nil, err
			}
			m[ks] = n
		}
		return m, nil
	case []interface{}:
		for i, e := range val {
			n, err := toJSONCompatible(e)
			if err != nil {
				return nil, err
			}
			val[i] = n
		}
		return val, nil
	case int:
		return int64(val), nil
	case int64, float64, bool, string, nil:
		return val, nil
	default:
		return nil, fmt.Errorf("unsupported value type %T", v)
	}
}

// stripFieldsNotInCRDSchema removes fields the Go API types include but the
// currently-deployed CRD schema doesn't declare. Keep this list narrow — every
// entry is technically a chart/types version-skew workaround. When the operator
// chart catches up (or wsm pins an older API), the corresponding line can go.
func stripFieldsNotInCRDSchema(obj *unstructured.Unstructured) {
	// .status is always operator-owned; never SSA from the client side.
	unstructured.RemoveNestedField(obj.Object, "status")

	// OidcSpec is a by-value struct, so `omitempty` can't drop its zero value
	// (serializes as oidc.*: {"key": ""}). Strip it unless a leaf is set, so
	// configured OIDC (flags or --cr-file) still reaches the CRD.
	// TODO(operator-bump): drop this once OidcSpec is a pointer upstream — the
	// alpha.2 CRD already tolerates the empty block (verified on Kind).
	if !oidcConfigured(obj) {
		unstructured.RemoveNestedField(obj.Object, "spec", "wandb", "oidc")
	}

	// GlobalSpec and WandbProbeDefaults are by-value structs, so `omitempty`
	// can't drop their zero value — they serialize as an empty `{}` block. Strip
	// when empty so an unconfigured wsm install doesn't send meaningless fields
	// (mirrors the OIDC strip above). A populated block (C1 flags / --cr-file) is
	// kept.
	if isEmptyNestedMap(obj, "spec", "global") {
		unstructured.RemoveNestedField(obj.Object, "spec", "global")
	}
	if isEmptyNestedMap(obj, "spec", "wandb", "probes") {
		unstructured.RemoveNestedField(obj.Object, "spec", "wandb", "probes")
	}
}

// isEmptyNestedMap reports whether the field at the given path is absent or an
// empty map.
func isEmptyNestedMap(obj *unstructured.Unstructured, fields ...string) bool {
	m, found, err := unstructured.NestedMap(obj.Object, fields...)
	return err != nil || !found || len(m) == 0
}

// oidcConfigured reports whether spec.wandb.oidc has any leaf selector with a
// non-empty name or key (a real reference, not the zero-value struct).
func oidcConfigured(obj *unstructured.Unstructured) bool {
	oidc, found, err := unstructured.NestedMap(obj.Object, "spec", "wandb", "oidc")
	if err != nil || !found {
		return false
	}
	// sessionLength is a plain string leaf, not a selector; a non-empty value
	// alone counts as configured so it isn't stripped with the empty block.
	if sl, _, _ := unstructured.NestedString(oidc, "sessionLength"); sl != "" {
		return true
	}
	for _, leaf := range oidc {
		selector, ok := leaf.(map[string]interface{})
		if !ok {
			continue
		}
		if name, _, _ := unstructured.NestedString(selector, "name"); name != "" {
			return true
		}
		if key, _, _ := unstructured.NestedString(selector, "key"); key != "" {
			return true
		}
	}
	return false
}

// DeleteCR deletes a WeightsAndBiases CR from the cluster
func DeleteCR(ctx context.Context, name, namespace string) error {
	_, dyn, err := kubectl.GetDynamicClientset()
	if err != nil {
		return err
	}

	gvr := schema.GroupVersionResource{
		Group:    "apps.wandb.com",
		Version:  "v2",
		Resource: "weightsandbiases",
	}

	err = dyn.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete CR: %w", err)
	}

	return nil
}

// ListCRs lists all WeightsAndBiases CRs in a namespace
func ListCRs(ctx context.Context, namespace string) ([]string, error) {
	_, dyn, err := kubectl.GetDynamicClientset()
	if err != nil {
		return nil, err
	}

	gvr := schema.GroupVersionResource{
		Group:    "apps.wandb.com",
		Version:  "v2",
		Resource: "weightsandbiases",
	}

	list, err := dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list WeightsAndBiases CRs: %w", err)
	}

	var names []string
	for _, item := range list.Items {
		names = append(names, item.GetName())
	}
	return names, nil
}

func GetCR(ctx context.Context, name, namespace string) (*v2.WeightsAndBiases, error) {
	_, dyn, err := kubectl.GetDynamicClientset()
	if err != nil {
		return nil, err
	}

	gvr := schema.GroupVersionResource{
		Group:    "apps.wandb.com",
		Version:  "v2",
		Resource: "weightsandbiases",
	}

	obj, err := dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf("WeightsAndBiases %s/%s not found", namespace, name)
		}
		return nil, fmt.Errorf("failed to get WeightsAndBiases %s/%s: %w", namespace, name, err)
	}

	cr := &v2.WeightsAndBiases{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, cr); err != nil {
		return nil, fmt.Errorf("failed to decode WeightsAndBiases: %w", err)
	}
	return cr, nil
}

func ConvertV1CRToV2(ctx context.Context, name, namespace string) (*unstructured.Unstructured, *unstructured.Unstructured, error) {
	restConfig, dynamicClient, err := kubectl.GetDynamicClientset()
	if err != nil {
		return nil, nil, err
	}

	weightsAndBiasesV1Resource := schema.GroupVersionResource{
		Group:    "apps.wandb.com",
		Version:  "v1",
		Resource: "weightsandbiases",
	}

	liveV1Unstructured, err := dynamicClient.Resource(weightsAndBiasesV1Resource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("v1 WeightsAndBiases %s/%s not found", namespace, name)
		}
		return nil, nil, fmt.Errorf("failed to get v1 WeightsAndBiases %s/%s: %w", namespace, name, err)
	}

	liveV1JSON, err := json.Marshal(liveV1Unstructured.Object)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal v1 CR: %w", err)
	}
	sourceV1CR := &appsv1.WeightsAndBiases{}
	if err := json.Unmarshal(liveV1JSON, sourceV1CR); err != nil {
		return nil, nil, fmt.Errorf("failed to decode v1 CR: %w", err)
	}

	conversionScheme := runtime.NewScheme()
	if err := corev1.AddToScheme(conversionScheme); err != nil {
		return nil, nil, fmt.Errorf("failed to build conversion scheme: %w", err)
	}
	conversionReader, err := ctrlclient.New(restConfig, ctrlclient.Options{Scheme: conversionScheme})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build conversion reader: %w", err)
	}
	appsv1.SetConversionReader(conversionReader)

	convertedV2CR := &v2.WeightsAndBiases{}
	if err := sourceV1CR.ConvertTo(convertedV2CR); err != nil {
		return nil, nil, fmt.Errorf("conversion failed: %w", err)
	}
	convertedV2CR.TypeMeta = metav1.TypeMeta{APIVersion: "apps.wandb.com/v2", Kind: "WeightsAndBiases"}

	convertedV2Object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(convertedV2CR)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to convert v2 CR to unstructured: %w", err)
	}
	cleanedV2CR := &unstructured.Unstructured{Object: convertedV2Object}
	stripServerManagedMetadata(cleanedV2CR)

	cleanedV1CR := liveV1Unstructured.DeepCopy()
	stripServerManagedMetadata(cleanedV1CR)

	return cleanedV1CR, cleanedV2CR, nil
}

// stripServerManagedMetadata removes status and the apiserver-managed metadata
// fields from obj so a v1/v2 comparison reflects only spec and user metadata.
func stripServerManagedMetadata(obj *unstructured.Unstructured) {
	unstructured.RemoveNestedField(obj.Object, "status")
	for _, field := range []string{"resourceVersion", "uid", "generation", "selfLink", "managedFields", "creationTimestamp"} {
		unstructured.RemoveNestedField(obj.Object, "metadata", field)
	}
}

// WaitForCR waits for WeightsAndBiases CR to be ready
func WaitForCR(ctx context.Context, name, namespace string, timeout time.Duration) error {
	_, dyn, err := kubectl.GetDynamicClientset()
	if err != nil {
		return err
	}

	gvr := schema.GroupVersionResource{
		Group:    "apps.wandb.com",
		Version:  "v2",
		Resource: "weightsandbiases",
	}

	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		cr, err := dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}

		ready, found, err := unstructured.NestedString(cr.Object, "status", "ready")
		if err == nil && found && ready == "true" {
			return true, nil
		}
		return false, nil
	})
}

// WaitForCRReady waits for a WeightsAndBiases CR to reach ready state
func WaitForCRReady(ctx context.Context, namespace, crName string, timeout time.Duration) error {
	return WaitForCR(ctx, crName, namespace, timeout)
}
