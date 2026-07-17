package operator

import (
	"context"
	"fmt"

	"github.com/wandb/wsm/pkg/kubectl"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/cli"
	v1 "helm.sh/helm/v4/pkg/release/v1"
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

// TelemetryConfig bundles the operator-chart telemetry values wsm sets. Every
// field except Mode is optional; an empty/zero field is omitted from the chart
// values so the operator chart's own default for that key is preserved.
type TelemetryConfig struct {
	Mode              string
	ForwardEndpoint   string
	OtelSecretName    string
	OtelProtocol      string
	OtelServiceName   string
	OtelResourceAttrs string
	ForwardProtocol   string
	ForwardHeaders    map[string]string
}

// buildTelemetryValues renders a TelemetryConfig into the operator chart's telemetry.* values block.
// It is the single definition of that layout; parseTelemetryValues is its inverse, so the write path
// (DeployOperator) and the read path (GetOperatorTelemetryConfig) cannot drift.
func buildTelemetryValues(t TelemetryConfig) map[string]interface{} {
	values := map[string]interface{}{"mode": t.Mode}

	// Omit empty otel keys so the chart default for each is preserved.
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

	if t.Mode == TelemetryModeForward {
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

// parseTelemetryValues is the inverse of buildTelemetryValues, reading a telemetry.* values block
// (as stored in the operator's Helm release) back into a TelemetryConfig.
func parseTelemetryValues(values map[string]interface{}) TelemetryConfig {
	t := TelemetryConfig{Mode: TelemetryModeOff}
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

// GetOperatorTelemetryConfig reads back the telemetry configuration wsm installed into the operator's
// Helm release, returning wsm's domain type. It round-trips against DeployOperator: what you install
// is what you read back. Callers get the mode without embedding a Helm client or knowing the values layout.
func GetOperatorTelemetryConfig(ctx context.Context, namespace string) (TelemetryConfig, error) {
	const releaseName = "wandb-operator"

	settings := cli.New()
	settings.SetNamespace(namespace)
	settings.KubeContext = kubectl.GetContext()

	actionConfig, err := initActionConfig(settings)
	if err != nil {
		return TelemetryConfig{}, fmt.Errorf("failed to initialize action config: %w", err)
	}

	rel, err := action.NewGet(actionConfig).Run(releaseName)
	if err != nil {
		return TelemetryConfig{}, fmt.Errorf("operator not installed in namespace %q: %w", namespace, err)
	}
	release, ok := rel.(*v1.Release)
	if !ok {
		return TelemetryConfig{}, fmt.Errorf("unexpected release type for %q", releaseName)
	}

	values, _ := release.Config["telemetry"].(map[string]interface{})
	return parseTelemetryValues(values), nil
}
