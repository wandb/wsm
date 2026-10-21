package supportbundle

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func shortStartup(t *testing.T) {
	t.Helper()
	oldStartup, oldPoll := startupTimeout, pollInterval
	startupTimeout, pollInterval = 200*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { startupTimeout, pollInterval = oldStartup, oldPoll })
}

func managerPod(jobName string, phase corev1.PodPhase, status *corev1.ContainerStatus) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: jobName + "-pod", Namespace: "wandb", Labels: map[string]string{batchv1.JobNameLabel: jobName}},
		Status:     corev1.PodStatus{Phase: phase},
	}
	if status != nil {
		status.Name = "collector"
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{*status}
	}
	return pod
}

func waiting(reason string) *corev1.ContainerStatus {
	return &corev1.ContainerStatus{Image: "lumen:0.4.10", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}}
}

func running(started bool) *corev1.ContainerStatus {
	return &corev1.ContainerStatus{Started: &started, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
}

func TestContainerState(t *testing.T) {
	terminated := &corev1.ContainerStatus{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}}
	unschedulable := managerPod("job", corev1.PodPending, nil)
	unschedulable.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable, Message: "0/1 nodes"}}

	tests := []struct {
		name     string
		pod      *corev1.Pod
		ready    bool
		terminal bool
		progress string
	}{
		{"running and started", managerPod("job", corev1.PodRunning, running(true)), true, false, ""},
		{"running pod, container not started", managerPod("job", corev1.PodRunning, running(false)), false, false, "starting"},
		{"unschedulable recovers", unschedulable, false, false, "Unschedulable"},
		{"image pull error recovers", managerPod("job", corev1.PodPending, waiting("ErrImagePull")), false, false, "ErrImagePull"},
		{"image pull backoff recovers", managerPod("job", corev1.PodPending, waiting("ImagePullBackOff")), false, false, "ImagePullBackOff"},
		{"config error recovers", managerPod("job", corev1.PodPending, waiting("CreateContainerConfigError")), false, false, "CreateContainerConfigError"},
		{"invalid image is terminal", managerPod("job", corev1.PodPending, waiting("InvalidImageName")), false, true, ""},
		{"terminated container is terminal", managerPod("job", corev1.PodRunning, terminated), false, true, ""},
		{"failed pod is terminal", managerPod("job", corev1.PodFailed, nil), false, true, ""},
		{"succeeded pod is terminal", managerPod("job", corev1.PodSucceeded, nil), false, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := containerState(tt.pod, "collector")
			if state.ready != tt.ready || (state.terminal != nil) != tt.terminal {
				t.Fatalf("state = %+v", state)
			}
			if !strings.Contains(state.progress, tt.progress) {
				t.Errorf("progress = %q, want it to mention %q", state.progress, tt.progress)
			}
		})
	}
}

func assertJobDeleted(t *testing.T, client *fake.Clientset, name string) {
	t.Helper()
	_, err := client.BatchV1().Jobs("wandb").Get(context.Background(), name, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("manager Job %s still exists (err=%v)", name, err)
	}
}

func TestStartSessionWaitsThroughRecoverableStates(t *testing.T) {
	shortStartup(t)
	jobName := ManagerJobName(OperationList, "abc123")
	client := fake.NewClientset(managerPod(jobName, corev1.PodPending, waiting("ImagePullBackOff")))

	var progress []string
	session, err := StartSession(context.Background(), client, nil, testCR(), testTemplate(), OperationList, SessionOptions{
		Lifetime: time.Minute,
		Suffix:   "abc123",
		Progress: func(msg string) {
			progress = append(progress, msg)
			_, _ = client.CoreV1().Pods("wandb").UpdateStatus(context.Background(), managerPod(jobName, corev1.PodRunning, running(true)), metav1.UpdateOptions{})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.Pod != jobName+"-pod" || session.Container != "collector" {
		t.Fatalf("session = %+v", session)
	}
	if len(progress) != 1 || !strings.Contains(progress[0], "ImagePullBackOff") {
		t.Errorf("progress = %v", progress)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	assertJobDeleted(t, client, jobName)
}

func TestStartSessionTimeoutDeletesJob(t *testing.T) {
	shortStartup(t)
	jobName := ManagerJobName(OperationList, "abc123")
	client := fake.NewClientset(managerPod(jobName, corev1.PodPending, waiting("ImagePullBackOff")))

	session, err := StartSession(context.Background(), client, nil, testCR(), testTemplate(), OperationList, SessionOptions{Lifetime: time.Minute, Suffix: "abc123"})
	if err == nil || session != nil {
		t.Fatalf("StartSession = %v, %v; want a timeout and no session", session, err)
	}
	if !strings.Contains(err.Error(), "did not start") || !strings.Contains(err.Error(), "ImagePullBackOff") {
		t.Errorf("error %q should say it timed out and name the last reason", err)
	}
	assertJobDeleted(t, client, jobName)
}

func TestStartSessionTerminalStateDeletesJob(t *testing.T) {
	shortStartup(t)
	jobName := ManagerJobName(OperationList, "abc123")
	terminated := &corev1.ContainerStatus{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 2}}}
	client := fake.NewClientset(managerPod(jobName, corev1.PodRunning, terminated))

	session, err := StartSession(context.Background(), client, nil, testCR(), testTemplate(), OperationList, SessionOptions{Lifetime: time.Minute, Suffix: "abc123"})
	if err == nil || session != nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("StartSession = %v, %v; want a terminal error and no session", session, err)
	}
	assertJobDeleted(t, client, jobName)
}

type fakeExecutor struct {
	stdout, stderr string
	err            error
	commands       [][]string
}

func (f *fakeExecutor) Exec(_ context.Context, _, _, _ string, command []string, stdout, stderr io.Writer) error {
	f.commands = append(f.commands, command)
	if stdout != nil {
		_, _ = io.WriteString(stdout, f.stdout)
	}
	if stderr != nil {
		_, _ = io.WriteString(stderr, f.stderr)
	}
	return f.err
}

func TestBundleManagerReportsMissingBinary(t *testing.T) {
	executor := &fakeExecutor{stderr: `exec: "bundle-manager": executable file not found in $PATH`, err: errors.New("command terminated with exit code 127")}
	session := &Session{executor: executor, Job: &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "wandb"}}, Image: "lumen:custom"}
	err := session.BundleManager(context.Background(), io.Discard, nil, "list")
	if !errors.Is(err, ErrBundleManagerMissing) || !strings.Contains(err.Error(), "lumen:custom") {
		t.Fatalf("err = %v, want ErrBundleManagerMissing naming the image", err)
	}
	if got := executor.commands[0]; len(got) != 2 || got[0] != "bundle-manager" || got[1] != "list" {
		t.Errorf("command = %v", got)
	}
}

func TestStartSessionExplainsMissingPod(t *testing.T) {
	shortStartup(t)
	jobName := ManagerJobName(OperationList, "abc123")
	quota := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "quota", Namespace: "wandb"},
		InvolvedObject: corev1.ObjectReference{Kind: "Job", Name: jobName},
		Reason:         "FailedCreate",
		Message:        "exceeded quota: pods",
	}
	var progress []string
	_, err := StartSession(context.Background(), fake.NewClientset(quota), nil, testCR(), testTemplate(), OperationList, SessionOptions{
		Lifetime: time.Minute, Suffix: "abc123", Progress: func(msg string) { progress = append(progress, msg) },
	})
	if err == nil || len(progress) == 0 || !strings.Contains(progress[0], "exceeded quota") {
		t.Errorf("progress = %v, err = %v; want the FailedCreate event", progress, err)
	}

	oldSlow := slowStart
	slowStart = 0
	t.Cleanup(func() { slowStart = oldSlow })
	progress = nil
	_, _ = StartSession(context.Background(), fake.NewClientset(), nil, testCR(), testTemplate(), OperationList, SessionOptions{
		Lifetime: time.Minute, Suffix: "abc123", Progress: func(msg string) { progress = append(progress, msg) },
	})
	if len(progress) == 0 || !strings.Contains(progress[len(progress)-1], "kube-controller-manager") {
		t.Errorf("progress = %v; want the slow-start hint", progress)
	}
}

func TestStartSessionInterruptedDeletesJob(t *testing.T) {
	shortStartup(t)
	jobName := ManagerJobName(OperationList, "abc123")
	ctx, cancel := context.WithCancel(context.Background())
	client := fake.NewClientset()
	_, err := StartSession(ctx, client, nil, testCR(), testTemplate(), OperationList, SessionOptions{
		Lifetime: time.Minute, Suffix: "abc123", Progress: func(string) { cancel() },
	})
	if err == nil || err.Error() != "interrupted - removed manager Job "+jobName {
		t.Errorf("err = %v", err)
	}
	assertJobDeleted(t, client, jobName)
}
