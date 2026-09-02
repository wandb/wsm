package telemetry

import (
	"context"
	"time"

	"github.com/wandb/wsm/pkg/kubectl"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	ModeOff     = "off"
	ModeFull    = "full"
	ModeForward = "forward"
)

func ValidMode(mode string) bool {
	switch mode {
	case ModeOff, ModeFull, ModeForward:
		return true
	default:
		return false
	}
}

// Config bundles the operator-chart telemetry values wsm sets. Every field
// except Mode is optional; an empty/zero field is omitted so the chart default
// for that key is preserved.
type Config struct {
	Mode              string
	ForwardEndpoint   string
	OtelSecretName    string
	OtelProtocol      string
	OtelServiceName   string
	OtelResourceAttrs string
	ForwardProtocol   string
	ForwardHeaders    map[string]string
	// The operator chart defaults telemetry.scrape.kubeStateMetrics to false; wsm
	// flips it on when it's managing KSM under full mode, else the installed KSM
	// emits kube_* metrics that nothing scrapes.
	ScrapeKubeStateMetrics bool
}

// BuildValues renders a Config into the operator chart's telemetry.* values
// block. ParseValues is its inverse.
func BuildValues(t Config) map[string]interface{} {
	values := map[string]interface{}{"mode": t.Mode}

	otel := map[string]interface{}{}
	if t.OtelSecretName != "" {
		otel["secretName"] = t.OtelSecretName
	}
	if t.OtelProtocol != "" {
		otel["protocol"] = t.OtelProtocol
	}
	if t.OtelServiceName != "" {
		otel["serviceName"] = t.OtelServiceName
	}
	if t.OtelResourceAttrs != "" {
		otel["resourceAttributes"] = t.OtelResourceAttrs
	}
	if len(otel) > 0 {
		values["otel"] = otel
	}

	if t.ScrapeKubeStateMetrics {
		values["scrape"] = map[string]interface{}{"kubeStateMetrics": true}
	}

	if crds := crdToggles(t.Mode); crds != nil {
		values["crds"] = crds
	}

	if t.Mode == ModeForward {
		otlp := map[string]interface{}{"endpoint": t.ForwardEndpoint}
		if t.ForwardProtocol != "" {
			otlp["protocol"] = t.ForwardProtocol
		}
		if len(t.ForwardHeaders) > 0 {
			// Helm validates values as map[string]interface{}; map[string]string trips its type detector.
			headers := make(map[string]interface{}, len(t.ForwardHeaders))
			for k, v := range t.ForwardHeaders {
				headers[k] = v
			}
			otlp["headers"] = headers
		}
		values["forwarding"] = map[string]interface{}{"otlp": otlp}
	}
	return values
}

// crdToggles is the telemetry.crds block for a mode; nil for off.
func crdToggles(mode string) map[string]interface{} {
	switch mode {
	case ModeFull:
		return map[string]interface{}{"victoriaMetrics": true, "grafana": true}
	case ModeForward:
		return map[string]interface{}{"victoriaMetrics": true}
	}
	return nil
}

// PrepValues is the base values with telemetry off and its operators disabled
// but the CRDs enabled — the CRD-preparation upgrade that runs before the stack
// is turned on.
func PrepValues(base map[string]interface{}, mode string) map[string]interface{} {
	prep := make(map[string]interface{}, len(base))
	for k, v := range base {
		prep[k] = v
	}
	telemetry := map[string]interface{}{"mode": ModeOff}
	if crds := crdToggles(mode); crds != nil {
		telemetry["crds"] = crds
	}
	if bt, ok := base["telemetry"].(map[string]interface{}); ok {
		if ns, ok := bt["namespace"]; ok {
			telemetry["namespace"] = ns
		}
	}
	prep["telemetry"] = telemetry
	prep["victoria-metrics-operator"] = map[string]interface{}{"enabled": false}
	prep["grafana-operator"] = map[string]interface{}{"enabled": false}
	return prep
}

var crdGVR = schema.GroupVersionResource{
	Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions",
}

var victoriaMetricsCRDNames = []string{
	"vmagents.operator.victoriametrics.com",
	"vmalerts.operator.victoriametrics.com",
	"vmrules.operator.victoriametrics.com",
	"vmnodescrapes.operator.victoriametrics.com",
	"vmservicescrapes.operator.victoriametrics.com",
	"vmpodscrapes.operator.victoriametrics.com",
	"vmsingles.operator.victoriametrics.com",
	"vlsingles.operator.victoriametrics.com",
	"vtsingles.operator.victoriametrics.com",
}

var grafanaCRDNames = []string{
	"grafanas.grafana.integreatly.org",
	"grafanadatasources.grafana.integreatly.org",
	"grafanadashboards.grafana.integreatly.org",
}

// crdNames are all CRDs instantiated by the telemetry chart for a mode. Keep
// this list aligned with the chart resources so the final Helm upgrade cannot
// race an API that the CRD-preparation upgrade has not Established yet.
func crdNames(mode string) []string {
	switch mode {
	case ModeForward:
		return append([]string(nil), victoriaMetricsCRDNames...)
	case ModeFull:
		names := append([]string(nil), victoriaMetricsCRDNames...)
		return append(names, grafanaCRDNames...)
	}
	return nil
}

func crdEstablished(obj map[string]interface{}) bool {
	conditions, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if ok && cond["type"] == "Established" && cond["status"] == "True" {
			return true
		}
	}
	return false
}

// CRDsEstablished reports whether every telemetry CRD for the mode is Established.
func CRDsEstablished(ctx context.Context, mode string) (bool, error) {
	names := crdNames(mode)
	if len(names) == 0 {
		return true, nil
	}
	_, dyn, err := kubectl.GetDynamicClientset()
	if err != nil {
		return false, err
	}
	for _, name := range names {
		crd, err := dyn.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil || !crdEstablished(crd.Object) {
			return false, nil
		}
	}
	return true, nil
}

// WaitForCRDs blocks until every telemetry CRD for the mode is Established.
func WaitForCRDs(ctx context.Context, mode string, timeout time.Duration) error {
	names := crdNames(mode)
	if len(names) == 0 {
		return nil
	}
	_, dyn, err := kubectl.GetDynamicClientset()
	if err != nil {
		return err
	}
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		for _, name := range names {
			crd, err := dyn.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
			if err != nil || !crdEstablished(crd.Object) {
				return false, nil
			}
		}
		return true, nil
	})
}

// ParseValues is the inverse of BuildValues, reading a telemetry.* values block
// back into a Config.
func ParseValues(values map[string]interface{}) Config {
	t := Config{Mode: ModeOff}
	if values == nil {
		return t
	}
	if mode, ok := values["mode"].(string); ok {
		t.Mode = mode
	}
	if otel, ok := values["otel"].(map[string]interface{}); ok {
		t.OtelSecretName, _ = otel["secretName"].(string)
		t.OtelProtocol, _ = otel["protocol"].(string)
		t.OtelServiceName, _ = otel["serviceName"].(string)
		t.OtelResourceAttrs, _ = otel["resourceAttributes"].(string)
	}
	if scrape, ok := values["scrape"].(map[string]interface{}); ok {
		t.ScrapeKubeStateMetrics, _ = scrape["kubeStateMetrics"].(bool)
	}
	if fwd, ok := values["forwarding"].(map[string]interface{}); ok {
		if otlp, ok := fwd["otlp"].(map[string]interface{}); ok {
			t.ForwardEndpoint, _ = otlp["endpoint"].(string)
			t.ForwardProtocol, _ = otlp["protocol"].(string)
			if headers, ok := otlp["headers"].(map[string]interface{}); ok {
				t.ForwardHeaders = make(map[string]string, len(headers))
				for k, v := range headers {
					if s, ok := v.(string); ok {
						t.ForwardHeaders[k] = s
					}
				}
			}
		}
	}
	return t
}
