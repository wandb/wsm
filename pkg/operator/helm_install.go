package operator

import (
	"context"
	"fmt"

	"github.com/wandb/wsm/pkg/kubectl"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/cli"
)

// installHelmChart creates the namespace, wires a mirror-aware registry client, and
// installs or upgrades an OCI chart.
func installHelmChart(ctx context.Context, namespace, releaseName, chartRef, version string, values map[string]any, mirror *MirrorConfig) error {
	if err := CreateNamespace(ctx, namespace); err != nil {
		return err
	}

	settings := cli.New()
	settings.SetNamespace(namespace)
	settings.KubeContext = kubectl.GetContext()

	actionConfig, err := initActionConfig(settings)
	if err != nil {
		return fmt.Errorf("failed to initialize action config: %w", err)
	}

	// Plain-HTTP / TLS-skip needed for self-hosted mirrors without a real cert.
	plainHTTP := mirror != nil && mirror.Insecure
	registryClient, err := newRegistryClient(settings, "", "", mirrorCAFile(mirror), plainHTTP, plainHTTP)
	if err != nil {
		return fmt.Errorf("failed to create registry client: %w", err)
	}
	actionConfig.RegistryClient = registryClient

	releaseExists, err := checkReleaseExists(actionConfig, releaseName)
	if err != nil {
		return fmt.Errorf("failed to check if release exists: %w", err)
	}

	if releaseExists {
		upgradeClient := action.NewUpgrade(actionConfig)
		upgradeClient.Namespace = namespace
		upgradeClient.Version = version
		upgradeClient.WaitStrategy = "hookOnly"
		upgradeClient.ForceConflicts = true
		upgradeClient.SkipSchemaValidation = true

		cp, err := upgradeClient.LocateChart(chartRef, settings)
		if err != nil {
			return fmt.Errorf("failed to locate chart %q: %w", chartRef, err)
		}
		chartRequested, err := loader.Load(cp)
		if err != nil {
			return fmt.Errorf("failed to load chart %q: %w", chartRef, err)
		}
		if err := runUpgradeWithWebhookRaceRetry(ctx, upgradeClient, releaseName, chartRequested, values); err != nil {
			return fmt.Errorf("failed to upgrade %q: %w", releaseName, err)
		}
		return nil
	}

	installClient := action.NewInstall(actionConfig)
	installClient.Namespace = namespace
	installClient.ReleaseName = releaseName
	installClient.Version = version
	installClient.WaitStrategy = "hookOnly"
	installClient.SkipSchemaValidation = true

	cp, err := installClient.LocateChart(chartRef, settings)
	if err != nil {
		return fmt.Errorf("failed to locate chart %q: %w", chartRef, err)
	}
	chartRequested, err := loader.Load(cp)
	if err != nil {
		return fmt.Errorf("failed to load chart %q: %w", chartRef, err)
	}
	if err := runInstallWithWebhookRaceRetry(ctx, installClient, chartRequested, values); err != nil {
		return fmt.Errorf("failed to install %q: %w", releaseName, err)
	}
	return nil
}
