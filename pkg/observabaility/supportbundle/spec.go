package supportbundle

import (
	"fmt"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Source names as they appear in the bundle and in `status` output.
const (
	SourceClusterResources = "cluster-resources"
	SourceClusterInfo      = "cluster-info"
	SourcePodLogs          = "pod-logs"
	SourceVictoriaStack    = "victoria-stack"
)

// redactors mirror the operator branch's support-bundle template.
var redactors = []map[string]any{{
	"name": "hide-sensitive-resource-values",
	"fileSelector": map[string]any{"files": []string{
		"cluster-resources/*.json",
		"cluster-resources/*/*.json",
		"cluster-resources/*/*/*.json",
		"victoria-stack/*.jsonl",
	}},
	"removals": map[string]any{"regex": []map[string]string{
		{"redactor": `(?i)("(?:[^"]*secret[^"]*|[^"]*password[^"]*|[^"]*(?:key|token|license|cert))"\s*:\s*")(?P<mask>(?:\\.|[^"\\])*)(")`},
		{"redactor": `(?i)(\\"(?:[^"\\]*secret[^"\\]*|[^"\\]*password[^"\\]*|[^"\\]*(?:key|token|license|cert))\\"\s*:\s*\\")(?P<mask>(?:\\\\.|[^"\\])*)(\\")`},
		{"redactor": `(?i)((?:[A-Za-z0-9_./{}$-]*(?:secret|password)[A-Za-z0-9_./{}$-]*|[A-Za-z0-9_./{}$-]*(?:key|token|license|cert))[ \t]*:[ \t]*)(?P<mask>.*?)(\\n)`},
	}},
}}

// BuildCollectionSpec renders the Lumen Support-Bundle YAML and returns the
// sources it configures. The dns collector is omitted: it needs pod creation
// in `default` and a public image, and any collector error fails the bundle.
func BuildCollectionSpec(inst *Installation, window time.Duration, includeTelemetry bool) ([]byte, []string, error) {
	if window <= 0 {
		return nil, nil, fmt.Errorf("collection window must be positive")
	}
	lookback := formatDuration(window)

	collectors := []map[string]any{
		{"clusterResources": map[string]any{"namespaces": []string{inst.Namespace}}},
		{"clusterInfo": map[string]any{}},
		{"logs": map[string]any{
			"name":      SourcePodLogs,
			"namespace": inst.Namespace,
			"selector":  []string{},
			"limits":    map[string]any{"maxAge": lookback, "maxBytes": 50000000},
		}},
	}
	sources := []string{SourceClusterResources, SourceClusterInfo, SourcePodLogs}

	if includeTelemetry && inst.Telemetry != nil {
		victoria := map[string]any{
			"name":      SourceVictoriaStack,
			"namespace": inst.Telemetry.Namespace,
			"lookback":  lookback,
			"timeout":   "5m",
		}
		for key, ep := range map[string]string{
			"metrics": inst.Telemetry.MetricsEndpoint,
			"logs":    inst.Telemetry.LogsEndpoint,
			"traces":  inst.Telemetry.TracesEndpoint,
		} {
			if ep != "" {
				victoria[key] = map[string]string{"endpoint": ep}
			}
		}
		collectors = append(collectors, map[string]any{"victoriaStack": victoria})
		sources = append(sources, SourceVictoriaStack)
	}

	doc := map[string]any{
		"apiVersion": "lumen.wandb.ai/v1alpha1",
		"kind":       "Support-Bundle",
		"metadata":   map[string]string{"name": "support-bundle"},
		"spec": map[string]any{
			"collectors": collectors,
			"redactors":  redactors,
		},
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, nil, fmt.Errorf("render support-bundle spec: %w", err)
	}
	return out, sources, nil
}

// formatDuration renders 1h0m0s as 1h, 90m as 1h30m.
func formatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
