package operator

// kube-state-metrics is installed as its own dependency (like cert-manager), not
// bundled in the operator chart. The full-observability gating lives in the caller.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wandb/wsm/pkg/kubectl"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/cli"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	kubeStateMetricsNamespace      = "kube-state-metrics"
	kubeStateMetricsDeploymentName = "kube-state-metrics"
	kubeStateMetricsReleaseName    = "kube-state-metrics"
	kubeStateMetricsChartRef       = "oci://ghcr.io/prometheus-community/charts/kube-state-metrics"
	// The operator's VMServiceScrape keys off this same label.
	kubeStateMetricsSelector = "app.kubernetes.io/name=kube-state-metrics"

	// Chart version + its image tag (chart appVersion prefixed with "v", the chart's
	// own default). Bump together per release. Exported so `wsm registry mirror`
	// mirrors what we install.
	KubeStateMetricsVersion  = "5.27.0"
	KubeStateMetricsImageTag = "v2.14.0"
)

// Install modes for --install-kube-state-metrics, exported for external consumers.
const (
	KubeStateMetricsInstallModeAuto  = "auto"
	KubeStateMetricsInstallModeTrue  = "true"
	KubeStateMetricsInstallModeFalse = "false"
)

// ValidKubeStateMetricsInstallMode reports whether mode is a known install mode.
func ValidKubeStateMetricsInstallMode(mode string) bool {
	switch mode {
	case KubeStateMetricsInstallModeAuto, KubeStateMetricsInstallModeTrue, KubeStateMetricsInstallModeFalse:
		return true
	default:
		return false
	}
}

// InstallKubeStateMetrics installs KSM into its own namespace. installed is false when
// skipIfPresent found an existing KSM to reuse, letting the caller decide on ownership.
func InstallKubeStateMetrics(ctx context.Context, skipIfPresent bool, mirror *MirrorConfig, openshift bool) (installed bool, err error) {
	if skipIfPresent {
		exists, err := kubeStateMetricsExists(ctx)
		if err != nil {
			return false, err
		}
		if exists {
			return false, nil
		}
	}

	// Pin image.tag so the installed image matches what `wsm registry mirror` pushes.
	values := map[string]any{
		"image": map[string]any{"tag": KubeStateMetricsImageTag},
	}

	chartRef := kubeStateMetricsChartRef
	if mirror != nil {
		chartRef = "oci://" + mirror.Host + "/prometheus-community/charts/kube-state-metrics"
		// Only the registry moves; the repository path already matches the mirror layout.
		setNested(values, mirror.Host, "image", "registry")
	}

	if openshift {
		// Null the chart's hardcoded 65534 IDs so restricted-v2 assigns valid ones.
		setNested(values, nil, "securityContext", "runAsUser")
		setNested(values, nil, "securityContext", "runAsGroup")
		setNested(values, nil, "securityContext", "fsGroup")
	}

	if err := installHelmChart(ctx, kubeStateMetricsNamespace, kubeStateMetricsReleaseName, chartRef, KubeStateMetricsVersion, values, mirror); err != nil {
		return false, err
	}
	return true, nil
}

// WaitForKubeStateMetrics waits for the wsm-installed KSM deployment to be ready.
func WaitForKubeStateMetrics(ctx context.Context, timeout time.Duration) error {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return err
	}

	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		deploy, err := cs.AppsV1().Deployments(kubeStateMetricsNamespace).Get(ctx, kubeStateMetricsDeploymentName, metav1.GetOptions{})
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
}

// kubeStateMetricsExists reports whether any KSM exists cluster-wide (Deployment or
// StatefulSet); a duplicate install would double-count every kube_* series.
func kubeStateMetricsExists(ctx context.Context) (bool, error) {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return false, err
	}

	sel := metav1.ListOptions{LabelSelector: kubeStateMetricsSelector}
	deploys, err := cs.AppsV1().Deployments("").List(ctx, sel)
	if err != nil {
		return false, fmt.Errorf("failed to list kube-state-metrics deployments: %w", err)
	}
	if len(deploys.Items) > 0 {
		return true, nil
	}
	sts, err := cs.AppsV1().StatefulSets("").List(ctx, sel)
	if err != nil {
		return false, fmt.Errorf("failed to list kube-state-metrics statefulsets: %w", err)
	}
	return len(sts.Items) > 0, nil
}

// DeleteKubeStateMetrics uninstalls the wsm-installed KSM release (namespaced, so it
// can't touch a customer's KSM elsewhere). removed is false when there was none.
func DeleteKubeStateMetrics(ctx context.Context) (removed bool, err error) {
	settings := cli.New()
	settings.SetNamespace(kubeStateMetricsNamespace)
	settings.KubeContext = kubectl.GetContext()

	actionConfig, err := initActionConfig(settings)
	if err != nil {
		return false, fmt.Errorf("failed to initialize action config: %w", err)
	}

	uninstallClient := action.NewUninstall(actionConfig)
	uninstallClient.WaitStrategy = "hookOnly"
	uninstallClient.Timeout = 5 * time.Minute

	if _, err = uninstallClient.Run(kubeStateMetricsReleaseName); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, fmt.Errorf("failed to uninstall kube-state-metrics: %w", err)
	}
	return true, nil
}

// KubeStateMetricsInstance describes one KSM workload found in the cluster.
type KubeStateMetricsInstance struct {
	Namespace string
	Name      string
	Image     string
	Ready     bool
	Replicas  string // "ready/desired", e.g. "1/1"
}

// KubeStateMetricsStatus reports every KSM in the cluster; more than one is a duplicate.
type KubeStateMetricsStatus struct {
	Installed bool
	Instances []KubeStateMetricsInstance
}

// GetKubeStateMetricsStatus reports every KSM in the cluster (Deployments and
// StatefulSets), across all namespaces.
func GetKubeStateMetricsStatus(ctx context.Context) (*KubeStateMetricsStatus, error) {
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		return nil, err
	}

	sel := metav1.ListOptions{LabelSelector: kubeStateMetricsSelector}
	status := &KubeStateMetricsStatus{}

	deploys, err := cs.AppsV1().Deployments("").List(ctx, sel)
	if err != nil {
		return nil, fmt.Errorf("failed to list kube-state-metrics deployments: %w", err)
	}
	for _, d := range deploys.Items {
		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		status.Instances = append(status.Instances, KubeStateMetricsInstance{
			Namespace: d.Namespace,
			Name:      d.Name,
			Image:     kubeStateMetricsContainerImage(d.Spec.Template.Spec.Containers),
			Ready:     desired > 0 && d.Status.ReadyReplicas == desired,
			Replicas:  fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, desired),
		})
	}

	sts, err := cs.AppsV1().StatefulSets("").List(ctx, sel)
	if err != nil {
		return nil, fmt.Errorf("failed to list kube-state-metrics statefulsets: %w", err)
	}
	for _, s := range sts.Items {
		desired := int32(1)
		if s.Spec.Replicas != nil {
			desired = *s.Spec.Replicas
		}
		status.Instances = append(status.Instances, KubeStateMetricsInstance{
			Namespace: s.Namespace,
			Name:      s.Name,
			Image:     kubeStateMetricsContainerImage(s.Spec.Template.Spec.Containers),
			Ready:     desired > 0 && s.Status.ReadyReplicas == desired,
			Replicas:  fmt.Sprintf("%d/%d", s.Status.ReadyReplicas, desired),
		})
	}

	status.Installed = len(status.Instances) > 0
	return status, nil
}

// kubeStateMetricsContainerImage returns the image of the kube-state-metrics container,
// not containers[0] — the chart can add a kubeRBACProxy sidecar.
func kubeStateMetricsContainerImage(containers []corev1.Container) string {
	for _, c := range containers {
		if c.Name == kubeStateMetricsDeploymentName {
			return c.Image
		}
	}
	if len(containers) > 0 {
		return containers[0].Image
	}
	return ""
}
