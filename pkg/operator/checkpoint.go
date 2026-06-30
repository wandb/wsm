package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	v2 "github.com/wandb/operator/api/v2"
	"github.com/wandb/wsm/pkg/kubectl"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/cli"
	"helm.sh/helm/v4/pkg/release"
	helmcommon "helm.sh/helm/v4/pkg/release/common"
	"k8s.io/apimachinery/pkg/api/errors"
)

func checkpointConfigMapName(crName string) string {
	return "wsm-rollback-" + crName
}

// checkpointDataKey is the single ConfigMap data key holding the last good
// checkpoint; each successful save overwrites it (we keep only the latest good).
const checkpointDataKey = "checkpoint"

type Checkpoint struct {
	CreatedAt    time.Time            `json:"createdAt"`
	Good         bool                 `json:"good"`
	WandbVersion string               `json:"wandbVersion,omitempty"`
	CR           *v2.WeightsAndBiases `json:"cr"`
	HelmRevision int                  `json:"helmRevisions,omitempty"`
}

func SaveCheckpoint(ctx context.Context, namespace string, crName string, cr *v2.WeightsAndBiases, helmRevision int, good bool) error {

	if !good {
		return nil
	}

	checkpoint := Checkpoint{
		CreatedAt:    time.Now().UTC(),
		Good:         good,
		CR:           cr,
		HelmRevision: helmRevision,
	}
	if cr != nil {
		checkpoint.WandbVersion = cr.Spec.Wandb.Version
	}

	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("failed to encode checkpoint: %w", err)
	}

	// UpsertConfigMap replaces Data wholesale, so a single-entry map leaves only
	// this latest good checkpoint (and wipes any older entries).
	data := map[string]string{checkpointDataKey: string(encoded)}
	if err := kubectl.UpsertConfigMap(data, checkpointConfigMapName(crName), namespace); err != nil {
		return fmt.Errorf("failed to persist checkpoint: %w", err)
	}
	return nil
}

func GetLatestGoodCheckpoint(ctx context.Context, namespace string, crName string) (*Checkpoint, error) {
	cm, err := kubectl.GetConfigMap(ctx, checkpointConfigMapName(crName), namespace)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read checkpoint store: %w", err)
	}

	var latest *Checkpoint
	for _, raw := range cm.Data {
		var cp Checkpoint
		if err := json.Unmarshal([]byte(raw), &cp); err != nil {
			continue
		}
		if !cp.Good {
			continue
		}
		if latest == nil || cp.CreatedAt.After(latest.CreatedAt) {
			found := cp
			latest = &found
		}
	}
	return latest, nil
}

func ReleaseRevision(releaseName string, namespace string) (int, error) {
	settings := cli.New()
	settings.SetNamespace(namespace)
	settings.KubeContext = kubectl.GetContext()

	actionConfig, err := initActionConfig(settings)
	if err != nil {
		return 0, fmt.Errorf("failed to initialize action config: %w", err)
	}

	history, err := action.NewHistory(actionConfig).Run(releaseName)
	if err != nil {
		return 0, fmt.Errorf("failed to read history for release %q: %w", releaseName, err)
	}

	latestDeployed := 0
	for _, rel := range history {
		// Run returns []release.Releaser (an empty interface); go through an
		// Accessor to read status/revision.
		acc, err := release.NewAccessor(rel)
		if err != nil {
			continue
		}
		if acc.Status() == string(helmcommon.StatusDeployed) && acc.Version() > latestDeployed {
			latestDeployed = acc.Version()
		}
	}

	if latestDeployed == 0 {
		return 0, fmt.Errorf("no deployed revision found for release %q in namespace %q", releaseName, namespace)
	}
	return latestDeployed, nil
}

func RollbackRelease(releaseName string, namespace string, revision int, timeout time.Duration) error {
	settings := cli.New()
	settings.SetNamespace(namespace)
	settings.KubeContext = kubectl.GetContext()

	actionConfig, err := initActionConfig(settings)
	if err != nil {
		return fmt.Errorf("failed to initialize action config: %w", err)
	}

	client := action.NewRollback(actionConfig)
	client.Version = revision
	client.WaitStrategy = "hookOnly"
	client.Timeout = timeout

	if err := client.Run(releaseName); err != nil {
		return fmt.Errorf("failed to roll back release %q in namespace %q: %w", releaseName, namespace, err)
	}
	return nil
}
