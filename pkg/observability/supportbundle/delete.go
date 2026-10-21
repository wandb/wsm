package supportbundle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	v2 "github.com/wandb/operator/api/v2"
	"github.com/wandb/wsm/pkg/kubectl"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

var cancelWaitTimeout = 2 * time.Minute

// ErrDeleteOutcomeUnknown means the stream ended without an exit status.
var ErrDeleteOutcomeUnknown = errors.New("deletion outcome unknown")

// Delete runs bundle-manager delete for one exact location. A dropped stream
// wraps ErrDeleteOutcomeUnknown rather than claiming the delete failed.
func (s *Session) Delete(ctx context.Context, location string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := s.BundleManager(ctx, io.Discard, nil, "delete", "--location", location)
	if err == nil {
		return nil
	}
	if _, exited := kubectl.ExitCode(err); exited || errors.Is(err, ErrBundleManagerMissing) {
		return fmt.Errorf("delete %s failed: %w", location, err)
	}
	return fmt.Errorf("%w for %s (%v) - run `wsm support-bundle list` to check whether it is still stored", ErrDeleteOutcomeUnknown, location, err)
}

// CancelAndWait cancels a running collection, deletes its pods, and waits for
// their containers to stop, so an upload in flight has either finished or
// stopped. It returns false when the collection had already finished or is gone.
func CancelAndWait(ctx context.Context, client kubernetes.Interface, cr *v2.WeightsAndBiases, id string) (bool, error) {
	job, err := collectionJob(ctx, client, cr, id)
	if err != nil || job == nil || jobFinished(job) {
		return false, err
	}
	propagation := metav1.DeletePropagationBackground
	if err := client.BatchV1().Jobs(cr.Namespace).Delete(ctx, id, metav1.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("cancel support bundle %s: %w", id, err)
	}
	selector := metav1.ListOptions{LabelSelector: batchv1.JobNameLabel + "=" + id}
	pods, err := client.CoreV1().Pods(cr.Namespace).List(ctx, selector)
	if err != nil {
		return true, fmt.Errorf("cancelled %s, but couldn't list its pod: %w", id, err)
	}
	for _, pod := range pods.Items {
		if err := client.CoreV1().Pods(cr.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return true, fmt.Errorf("cancelled %s, but couldn't delete its pod: %w", id, err)
		}
	}
	err = wait.PollUntilContextTimeout(ctx, pollInterval, cancelWaitTimeout, true, func(ctx context.Context) (bool, error) {
		pods, err := client.CoreV1().Pods(cr.Namespace).List(ctx, selector)
		if err != nil {
			return false, err
		}
		return !slices.ContainsFunc(pods.Items, podRunning), nil
	})
	if wait.Interrupted(err) && ctx.Err() == nil {
		return true, fmt.Errorf("cancelled %s, but its pod hasn't stopped after %s", id, cancelWaitTimeout)
	}
	if err != nil {
		return true, err
	}
	return true, nil
}

func podRunning(pod corev1.Pod) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Terminated == nil {
			return true
		}
	}
	return len(pod.Status.ContainerStatuses) == 0
}
