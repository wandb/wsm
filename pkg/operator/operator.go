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
	"k8s.io/apimachinery/pkg/types"
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

// InstallCertManager installs cert-manager using kubernetes client
func InstallCertManager(ctx context.Context) error {
	const certManagerURL = "https://github.com/cert-manager/cert-manager/releases/download/v1.19.2/cert-manager.yaml"

	resp, err := http.Get(certManagerURL)
	if err != nil {
		return fmt.Errorf("failed to download cert-manager manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download cert-manager manifest: status code %d", resp.StatusCode)
	}

	manifest, err := io.ReadAll(resp.Body)

	err = kubectl.ApplyYAML(ctx, manifest)
	if err != nil {
		return fmt.Errorf("failed to apply cert-manager manifest: %w", err)
	}

	return nil
}

// WaitForCertManager waits for cert-manager to be ready
func WaitForCertManager(ctx context.Context, timeout time.Duration) error {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return err
	}

	deployments := []string{"cert-manager", "cert-manager-webhook", "cert-manager-cainjector"}

	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		for _, name := range deployments {
			deploy, err := cs.AppsV1().Deployments("cert-manager").Get(ctx, name, metav1.GetOptions{})
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
			if !isAvailable {
				return false, nil
			}
		}
		return true, nil
	})
}

// DeleteCertManager deletes the cert-manager resources
func DeleteCertManager(ctx context.Context) error {
	const manifestURL = "https://github.com/cert-manager/cert-manager/releases/download/v1.19.2/cert-manager.yaml"

	resp, err := http.Get(manifestURL)
	if err != nil {
		return fmt.Errorf("failed to download cert-manager manifest for deletion: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download cert-manager manifest for deletion: status code %d", resp.StatusCode)
	}

	manifest, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read cert-manager manifest for deletion: %w", err)
	}

	// We don't have a DeleteYAML in kubectl, but we can use ApplyYAML with a trick or add DeleteYAML.
	// For now, let's just use kubectl.DeleteNamespace for cert-manager if we know it.
	// Actually, the manifest contains many resources.
	// Let's add DeleteYAML to kubectl.
	return kubectl.DeleteYAML(ctx, manifest)
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

// ApplyCR applies a WeightsAndBiases CR to the cluster (idempotent)
func ApplyCR(ctx context.Context, wandbCR *v2.WeightsAndBiases) error {
	_, dyn, err := kubectl.GetDynamicClientset()
	if err != nil {
		return err
	}

	mapper, err := kubectl.GetRESTMapper()
	if err != nil {
		return err
	}

	gvk := wandbCR.GroupVersionKind()
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		// If mapping fails, try refreshing the mapper as CRDs might have been just installed
		if refreshedMapper, refreshErr := kubectl.RefreshRESTMapper(); refreshErr == nil {
			mapping, err = refreshedMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		}

		if err != nil {
			return fmt.Errorf("failed to get mapping for %s: %w", gvk, err)
		}
	}

	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(wandbCR)
	if err != nil {
		return fmt.Errorf("failed to convert to unstructured: %w", err)
	}
	obj := &unstructured.Unstructured{Object: data}

	// Ensure GVK is set on the unstructured object
	obj.SetGroupVersionKind(gvk)

	raw, err := obj.MarshalJSON()
	if err != nil {
		return fmt.Errorf("failed to marshal CR: %w", err)
	}

	dr := dyn.Resource(mapping.Resource).Namespace(wandbCR.Namespace)

	if _, err := dr.Patch(ctx, wandbCR.Name, types.ApplyPatchType, raw, metav1.PatchOptions{
		FieldManager: "wsm",
	}); err != nil {
		return fmt.Errorf("failed to apply CR: %w", err)
	}

	return nil
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
