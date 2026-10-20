package supportbundle

import (
	"context"
	"fmt"
	"regexp"

	v2 "github.com/wandb/operator/api/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	PhasePending   = "Pending"
	PhaseRunning   = "Running"
	PhaseSucceeded = "Succeeded"
	PhaseFailed    = "Failed"
)

// bundleIDPattern is a Kubernetes Job name, which is what bundle IDs are.
var bundleIDPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

type CollectionStatus struct {
	ID          string       `json:"id"`
	Phase       string       `json:"phase"`
	Message     string       `json:"message,omitempty"`
	Started     *metav1.Time `json:"started,omitempty"`
	Finished    *metav1.Time `json:"finished,omitempty"`
	Result      *Result      `json:"result,omitempty"`
	ResultError string       `json:"resultError,omitempty"`
}

// Running reports whether the collection hasn't finished yet.
func (s *CollectionStatus) Running() bool {
	return s.Phase == PhasePending || s.Phase == PhaseRunning
}

// DescribeCollection reads a collection Job and its pod. It returns nil when
// the Job no longer exists.
func DescribeCollection(ctx context.Context, client kubernetes.Interface, cr *v2.WeightsAndBiases, id string) (*CollectionStatus, error) {
	if !bundleIDPattern.MatchString(id) {
		return nil, fmt.Errorf("%q is not a bundle ID", id)
	}
	job, err := collectionJob(ctx, client, cr, id)
	if err != nil || job == nil {
		return nil, err
	}
	pod, err := CollectorPod(ctx, client, job)
	if err != nil {
		return nil, err
	}
	status := describe(job, pod)
	status.ID = id
	return status, nil
}

func describe(job *batchv1.Job, pod *corev1.Pod) *CollectionStatus {
	status := &CollectionStatus{Started: job.Status.StartTime, Finished: job.Status.CompletionTime}

	var terminated *corev1.ContainerStateTerminated
	if pod != nil {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name == CollectorContainer(job) && cs.State.Terminated != nil {
				terminated = cs.State.Terminated
			}
		}
		result, err := PodResult(pod, CollectorContainer(job))
		status.Result = result
		if err != nil {
			status.ResultError = err.Error()
		}
	}
	if terminated != nil && status.Finished == nil {
		status.Finished = &terminated.FinishedAt
	}

	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			status.Phase = PhaseSucceeded
			switch {
			case status.Result != nil && status.Result.Failure != nil:
				status.Phase, status.Message = PhaseFailed, failureText(status.Result.Failure)
			case status.Result == nil && status.ResultError == "":
				status.Message = "the collector exited cleanly but reported no result"
			}
			return status
		case batchv1.JobFailed:
			status.Phase = PhaseFailed
			switch {
			case status.Result != nil && status.Result.Failure != nil:
				status.Message = failureText(status.Result.Failure)
			case c.Reason == "DeadlineExceeded":
				status.Message = "collection exceeded the Job deadline"
			case terminated != nil:
				status.Message = fmt.Sprintf("collector exited with code %d (%s)", terminated.ExitCode, terminated.Reason)
			default:
				status.Message = c.Reason + ": " + c.Message
			}
			return status
		}
	}

	switch {
	case pod == nil:
		status.Phase, status.Message = PhasePending, "waiting for the collection pod to be created"
	case terminated != nil:
		status.Phase, status.Message = PhaseRunning, "collector exited, waiting for the Job to record the outcome"
	case pod.Status.Phase == corev1.PodRunning:
		status.Phase = PhaseRunning
	default:
		status.Phase, status.Message = PhasePending, waitingReason(pod, CollectorContainer(job))
	}
	return status
}

func failureText(f *Failure) string {
	text := f.Stage + ": " + f.Reason
	if f.Message != "" {
		text += " " + f.Message
	}
	return text
}
