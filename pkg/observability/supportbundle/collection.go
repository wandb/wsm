// Package supportbundle triggers the Lumen support-bundle collections a v2 W&B
// install runs from the operator's suspended collector CronJob.
package supportbundle

// CollectorName mirrors the collectors the operator can put in a preset.
type CollectorName string

const (
	ClusterInfo      CollectorName = "cluster-info"
	ClusterResources CollectorName = "cluster-resources"
	PodLogs          CollectorName = "pod-logs"
	DNS              CollectorName = "dns"
	VictoriaMetrics  CollectorName = "victoria-metrics"
	VictoriaLogs     CollectorName = "victoria-logs"
	VictoriaTraces   CollectorName = "victoria-traces"
)

// Preset is a key "<preset>.yaml" in the operator's collection ConfigMap.
type Preset string

// DefaultPreset runs every collector; it is the only preset the operator ships today.
const DefaultPreset Preset = "full"

// CollectOptions picks what a collection gathers. The zero value runs DefaultPreset.
// Options the operator's spec doesn't expose yet (e.g. lookback windows) belong here.
type CollectOptions struct {
	Preset Preset
}

func (o CollectOptions) preset() Preset {
	if o.Preset == "" {
		return DefaultPreset
	}
	return o.Preset
}
