package supportbundle

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	utilexec "k8s.io/client-go/util/exec"
)

func TestDelete(t *testing.T) {
	ctx := context.Background()

	executor := &fakeExecutor{}
	if err := fakeSession(executor).Delete(ctx, bundleLocation); err != nil {
		t.Fatalf("exit 0: %v", err)
	}
	if want := []string{"bundle-manager", "delete", "--location", bundleLocation}; !reflect.DeepEqual(executor.commands[0], want) {
		t.Errorf("command = %v, want %v", executor.commands[0], want)
	}

	err := fakeSession(&fakeExecutor{err: utilexec.CodeExitError{Err: errors.New("exit 1"), Code: 1}}).Delete(ctx, bundleLocation)
	if err == nil || errors.Is(err, ErrDeleteOutcomeUnknown) {
		t.Errorf("non-zero exit: err = %v, want a definite failure", err)
	}

	err = fakeSession(&fakeExecutor{err: errors.New("connection reset by peer")}).Delete(ctx, bundleLocation)
	if !errors.Is(err, ErrDeleteOutcomeUnknown) {
		t.Errorf("dropped stream: err = %v, want ErrDeleteOutcomeUnknown", err)
	}

	err = fakeSession(&fakeExecutor{err: context.Canceled}).Delete(ctx, bundleLocation)
	if !errors.Is(err, ErrDeleteOutcomeUnknown) {
		t.Errorf("cancelled mid-exec: err = %v, want ErrDeleteOutcomeUnknown", err)
	}

	missing := &fakeExecutor{stderr: "executable file not found in $PATH", err: errors.New("command terminated")}
	if err := fakeSession(missing).Delete(ctx, bundleLocation); !errors.Is(err, ErrBundleManagerMissing) || errors.Is(err, ErrDeleteOutcomeUnknown) {
		t.Errorf("missing binary: err = %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	notStarted := &fakeExecutor{}
	if err := fakeSession(notStarted).Delete(cancelled, bundleLocation); !errors.Is(err, context.Canceled) || len(notStarted.commands) != 0 {
		t.Errorf("cancelled before start: err = %v, commands = %v", err, notStarted.commands)
	}
}

func TestCancelAndWait(t *testing.T) {
	ctx := context.Background()
	oldPoll, oldTimeout := pollInterval, cancelWaitTimeout
	pollInterval, cancelWaitTimeout = 5*time.Millisecond, time.Second
	t.Cleanup(func() { pollInterval, cancelWaitTimeout = oldPoll, oldTimeout })

	if cancelled, err := CancelAndWait(ctx, fake.NewClientset(), testCR(), "sb-test"); cancelled || err != nil {
		t.Errorf("gone Job: cancelled = %v, err = %v", cancelled, err)
	}

	finished := fake.NewClientset(testCollectionJob(complete), collectionPod(exited(validResult, 0)))
	if cancelled, err := CancelAndWait(ctx, finished, testCR(), "sb-test"); cancelled || err != nil {
		t.Errorf("finished Job: cancelled = %v, err = %v", cancelled, err)
	}
	if _, err := finished.CoreV1().Pods("wandb").Get(ctx, "sb-test-abcde", metav1.GetOptions{}); err != nil {
		t.Errorf("finished collection was touched: %v", err)
	}

	running := fake.NewClientset(testCollectionJob(), collectionPod(corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}))
	if cancelled, err := CancelAndWait(ctx, running, testCR(), "sb-test"); !cancelled || err != nil {
		t.Errorf("running Job: cancelled = %v, err = %v", cancelled, err)
	}
	if pods, _ := running.CoreV1().Pods("wandb").List(ctx, metav1.ListOptions{}); len(pods.Items) != 0 {
		t.Errorf("collector pod left behind: %v", pods.Items)
	}
}

func TestPodRunning(t *testing.T) {
	deleting := metav1.Now()
	tests := []struct {
		name string
		pod  corev1.Pod
		want bool
	}{
		{"running container", *collectionPod(corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}), true},
		{"terminated container", *collectionPod(exited("", 137)), false},
		{"not started", corev1.Pod{}, true},
		{"deleted before starting", corev1.Pod{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &deleting}}, true},
	}
	for _, tt := range tests {
		if got := podRunning(tt.pod); got != tt.want {
			t.Errorf("%s: podRunning = %v, want %v", tt.name, got, tt.want)
		}
	}
}
