package operator

import (
	"context"
	"fmt"

	"github.com/wandb/wsm/pkg/kubectl"
	"github.com/wandb/wsm/pkg/telemetry"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/cli"
	v1 "helm.sh/helm/v4/pkg/release/v1"
)

// GetOperatorTelemetryConfig reads back the telemetry config wsm installed into
// the operator's Helm release.
func GetOperatorTelemetryConfig(ctx context.Context, namespace string) (telemetry.Config, error) {
	const releaseName = "wandb-operator"

	settings := cli.New()
	settings.SetNamespace(namespace)
	settings.KubeContext = kubectl.GetContext()

	actionConfig, err := initActionConfig(settings)
	if err != nil {
		return telemetry.Config{}, fmt.Errorf("failed to initialize action config: %w", err)
	}

	rel, err := action.NewGet(actionConfig).Run(releaseName)
	if err != nil {
		return telemetry.Config{}, fmt.Errorf("operator not installed in namespace %q: %w", namespace, err)
	}
	release, ok := rel.(*v1.Release)
	if !ok {
		return telemetry.Config{}, fmt.Errorf("unexpected release type for %q", releaseName)
	}

	values, _ := release.Config["telemetry"].(map[string]interface{})
	return telemetry.ParseValues(values), nil
}
