package operator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/docker/go-connections/tlsconfig"
	v2 "github.com/wandb/operator/api/v2"
	"github.com/wandb/wsm/pkg/kubectl"
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
	"k8s.io/apimachinery/pkg/util/wait"
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
	certManagerVersion        = "v1.20.2"

	nginxGatewayNamespace      = "nginx-gateway"
	nginxGatewayDeploymentName = "nginx-gateway-nginx-gateway-fabric"
	nginxGatewayReleaseName    = "nginx-gateway"
	nginxGatewayChartRef       = "oci://ghcr.io/nginx/charts/nginx-gateway-fabric"
	nginxGatewayVersion        = "2.5.1"

	gatewayApiCRDURL                     = "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.0/standard-install.yaml"
	completeServerAPIsDiscoveryErrSubstr = "unable to retrieve the complete list of server APIs"
)

const (
	TelemetryModeOff     = "off"
	TelemetryModeFull    = "full"
	TelemetryModeForward = "forward"
)

func ValidTelemetryMode(mode string) bool {
	switch mode {
	case TelemetryModeOff, TelemetryModeFull, TelemetryModeForward:
		return true
	default:
		return false
	}
}

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
	Insecure bool   // plain HTTP / self-signed; passed to Helm's OCI client
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
	registryClient, err := newRegistryClient(settings, "", "", "", plainHTTP, plainHTTP)
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
		upgradeClient.Version = certManagerVersion
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
		installClient.Version = certManagerVersion
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
func InstallNginxGateway(ctx context.Context, skipIfPresent bool, mirror *MirrorConfig) error {
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
		if err := installGatewayApiCRDs(ctx); err != nil {
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
	registryClient, err := newRegistryClient(settings, "", "", "", plainHTTP, plainHTTP)
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
		upgradeClient.Version = nginxGatewayVersion
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
		installClient.Version = nginxGatewayVersion
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

// installGatewayApiCRDs installs Gateway API CRDs from the official YAML URL
func installGatewayApiCRDs(ctx context.Context) error {
	resp, err := http.Get(gatewayApiCRDURL)
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
	telemetryMode string,
	mirror *MirrorConfig,
	telemetryForwardEndpoint string,
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
	registryClient, err := newRegistryClient(settings, "", "", "", plainHTTP, plainHTTP)
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

	telemetryValues := map[string]interface{}{
		"mode": telemetryMode,
	}
	releaseValues := map[string]interface{}{
		"wandb": map[string]interface{}{
			"install": false,
		},
		"wandb-operator": map[string]interface{}{
			"image": operatorImage,
		},
		"telemetry": telemetryValues,
	}

	// The operator chart's telemetry-validation requires the caller to opt the
	// victoria-metrics-operator and grafana-operator dependencies in when
	// telemetry is enabled — their chart defaults are false (helm dependency
	// conditions are boolean-only). "full" runs the in-cluster Victoria stack
	// plus local Grafana; "forward" runs the Victoria stack and forwards OTLP
	// data to telemetry.forwarding.otlp.endpoint.
	if telemetryMode == TelemetryModeFull || telemetryMode == TelemetryModeForward {
		// The telemetry subchart deploys into the telemetry namespace (the W&B
		// namespace), not the operator's release namespace. It must already
		// exist — the chart does not create it — so ensure it here and pin
		// telemetry.namespace to match the CR's namespace.
		telemetryValues["namespace"] = wandbNamespace
		if err := CreateNamespace(ctx, wandbNamespace); err != nil {
			return fmt.Errorf("failed to ensure telemetry namespace %q: %w", wandbNamespace, err)
		}
	}
	switch telemetryMode {
	case TelemetryModeFull:
		releaseValues["victoria-metrics-operator"] = map[string]interface{}{"enabled": true}
		releaseValues["grafana-operator"] = map[string]interface{}{"enabled": true}
	case TelemetryModeForward:
		releaseValues["victoria-metrics-operator"] = map[string]interface{}{"enabled": true}
		telemetryValues["forwarding"] = map[string]interface{}{
			"otlp": map[string]interface{}{
				"endpoint": telemetryForwardEndpoint,
			},
		}
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

func ApplyCR(ctx context.Context, wandbCR *v2.WeightsAndBiases) error {
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

	if err := kubectl.ApplyUnstructured(ctx, obj); err != nil {
		return fmt.Errorf("failed to apply CR: %w", err)
	}

	return nil
}

// stripFieldsNotInCRDSchema removes fields the Go API types include but the
// currently-deployed CRD schema doesn't declare. Keep this list narrow — every
// entry is technically a chart/types version-skew workaround. When the operator
// chart catches up (or wsm pins an older API), the corresponding line can go.
func stripFieldsNotInCRDSchema(obj *unstructured.Unstructured) {
	// .status is always operator-owned; never SSA from the client side.
	unstructured.RemoveNestedField(obj.Object, "status")

	// OidcSpec is a by-value struct in the v2 Go API, so Go's `omitempty`
	// does not drop a zero value — wsm would emit oidc.{clientId,clientSecret,
	// issuerUrl,authMethod}: {"key": ""} on every apply even when no OIDC is
	// configured. Drop the whole oidc block; wsm has no flag for it, so a
	// user with OIDC needs --cr-file regardless.
	unstructured.RemoveNestedField(obj.Object, "spec", "wandb", "oidc")
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
