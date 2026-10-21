package supportbundle

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

// Result is Lumen's v1 collection result, read from the collector's termination message.
type Result struct {
	Version             string             `json:"version"`
	Artifact            *Artifact          `json:"artifact,omitempty"`
	Failure             *Failure           `json:"failure,omitempty"`
	Partial             bool               `json:"partial,omitempty"`
	Collectors          []CollectorOutcome `json:"collectors,omitempty"`
	CollectorsTruncated bool               `json:"collectorsTruncated,omitempty"`
}

type Artifact struct {
	Location  string `json:"location"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

type Failure struct {
	Stage   string `json:"stage"`
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}

// CollectorOutcome status is collected, partial, or failed.
type CollectorOutcome struct {
	Name    string          `json:"name"`
	Status  string          `json:"status"`
	Exports []ExportOutcome `json:"exports,omitempty"`
}

type ExportOutcome struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// ParseResult decodes a termination message.
func ParseResult(message string) (*Result, error) {
	var result Result
	if err := json.Unmarshal([]byte(message), &result); err != nil {
		return nil, fmt.Errorf("decode support-bundle result: %w", err)
	}
	if result.Version != "v1" {
		return nil, fmt.Errorf("unsupported support-bundle result version %q", result.Version)
	}
	return &result, nil
}

// PodResult returns the result from a finished collector pod, or nil when
// the container hasn't terminated or left no message.
func PodResult(pod *corev1.Pod, container string) (*Result, error) {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != container || status.State.Terminated == nil {
			continue
		}
		if message := strings.TrimSpace(status.State.Terminated.Message); message != "" {
			return ParseResult(message)
		}
	}
	return nil, nil
}

// CollectorContainer is the name of the collector container in the template.
func CollectorContainer(job *batchv1.Job) string {
	return job.Spec.Template.Spec.Containers[0].Name
}

// WaitForCollectorPod waits until the Job's collector container is running or
// has terminated, so its logs can be followed. It returns a nil pod when the
// Job finishes first (e.g. the pod never started before the deadline), and
// calls onWaiting whenever the reason the pod isn't running yet changes.
func WaitForCollectorPod(ctx context.Context, client kubernetes.Interface, job *batchv1.Job, onWaiting func(reason string)) (*corev1.Pod, error) {
	container := CollectorContainer(job)
	var found *corev1.Pod
	lastReason := ""
	err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		current, err := client.BatchV1().Jobs(job.Namespace).Get(ctx, job.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("get support bundle %s in namespace: %s: %w", job.Name, job.Namespace, err)
		}
		pod, err := CollectorPod(ctx, client, job)
		if err != nil {
			return false, err
		}
		if pod != nil {
			for _, status := range pod.Status.ContainerStatuses {
				if status.Name == container && (status.State.Running != nil || status.State.Terminated != nil) {
					found = pod
					return true, nil
				}
			}
		}
		if jobFinished(current) {
			return true, nil
		}
		if reason := waitingReason(pod, container); reason != "" && reason != lastReason && onWaiting != nil {
			lastReason = reason
			onWaiting(reason)
		}
		return false, nil
	})
	return found, err
}

// waitingReason explains why a collector pod isn't running yet, or "".
func waitingReason(pod *corev1.Pod, container string) string {
	if pod == nil {
		return ""
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == container && status.State.Waiting != nil {
			return strings.TrimSpace(status.State.Waiting.Reason + " " + status.State.Waiting.Message)
		}
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
			return strings.TrimSpace(condition.Reason + " " + condition.Message)
		}
	}
	return ""
}

// CollectorPod returns the newest pod of a collection Job, or nil.
func CollectorPod(ctx context.Context, client kubernetes.Interface, job *batchv1.Job) (*corev1.Pod, error) {
	pods, err := client.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{LabelSelector: batchv1.JobNameLabel + "=" + job.Name})
	if err != nil {
		return nil, fmt.Errorf("list pods for support bundle %s: %w", job.Name, err)
	}
	var newest *corev1.Pod
	for i := range pods.Items {
		if newest == nil || pods.Items[i].CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = &pods.Items[i]
		}
	}
	return newest, nil
}

// FormatLogLine turns a Lumen JSON log line into "LEVEL message key=value".
// Lines that aren't JSON are returned unchanged.
func FormatLogLine(line string) string {
	var fields map[string]any
	if err := json.Unmarshal([]byte(line), &fields); err != nil {
		return line
	}
	message, _ := fields["msg"].(string)
	level, _ := fields["level"].(string)
	out := strings.TrimSpace(level + " " + strings.TrimPrefix(message, "[support-bundle] "))
	for _, key := range []string{"uri", "path", "source", "destination", "error"} {
		if value, ok := fields[key]; ok {
			out += fmt.Sprintf(" %s=%v", key, value)
		}
	}
	return out
}
