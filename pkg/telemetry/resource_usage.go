package telemetry

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes"
)

// PodUsage is one pod's summed container CPU/memory usage from metrics-server.
type PodUsage struct {
	Pod         string  `json:"pod"`
	CPUCores    float64 `json:"cpuCores"`
	MemoryBytes int64   `json:"memoryBytes"`
}

// ResourceUsage is a point-in-time CPU/memory view for a namespace, read from
// metrics-server (metrics.k8s.io) — the same source as `kubectl top`. There is no
// history; each call is a live snapshot. It's queried through the API server, not
// VictoriaMetrics: the operator's telemetry stack does not collect container
// (cadvisor) metrics here, so metrics-server is the only source of pod usage.
type ResourceUsage struct {
	TotalCPUCores    float64    `json:"totalCpuCores"`
	TotalMemoryBytes int64      `json:"totalMemoryBytes"`
	Pods             []PodUsage `json:"pods"`
}

// FetchResourceUsage reads per-pod usage from metrics.k8s.io for a namespace and
// sums it. The caller's ServiceAccount needs get/list on pods.metrics.k8s.io
// (granted to Watchtower by the operator's RBAC). Returns an error only on a
// transport/decode failure; an empty namespace yields a zero snapshot.
func FetchResourceUsage(ctx context.Context, cs kubernetes.Interface, namespace string) (ResourceUsage, error) {
	raw, err := cs.CoreV1().RESTClient().Get().
		AbsPath("/apis/metrics.k8s.io/v1beta1/namespaces/" + namespace + "/pods").
		DoRaw(ctx)
	if err != nil {
		return ResourceUsage{}, fmt.Errorf("read pod metrics (is metrics-server installed and RBAC granted?): %w", err)
	}

	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Containers []struct {
				Usage struct {
					CPU    string `json:"cpu"`
					Memory string `json:"memory"`
				} `json:"usage"`
			} `json:"containers"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return ResourceUsage{}, fmt.Errorf("decode pod metrics: %w", err)
	}

	out := ResourceUsage{Pods: make([]PodUsage, 0, len(list.Items))}
	for _, it := range list.Items {
		var cores float64
		var mem int64
		for _, c := range it.Containers {
			// metrics-server reports cpu in nanocores/millicores and memory in Ki;
			// resource.Quantity parses either form.
			if q, err := resource.ParseQuantity(c.Usage.CPU); err == nil {
				cores += float64(q.MilliValue()) / 1000.0
			}
			if q, err := resource.ParseQuantity(c.Usage.Memory); err == nil {
				mem += q.Value()
			}
		}
		out.Pods = append(out.Pods, PodUsage{Pod: it.Metadata.Name, CPUCores: cores, MemoryBytes: mem})
		out.TotalCPUCores += cores
		out.TotalMemoryBytes += mem
	}
	return out, nil
}
