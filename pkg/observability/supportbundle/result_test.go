package supportbundle

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPodResult(t *testing.T) {
	message := `{"version":"v1","artifact":{"location":"s3://b/agent/service-bundles/x.tar.gz","sizeBytes":42,"sha256":"abc"},"partial":true,
		"collectors":[{"name":"pod-logs","status":"collected"},{"name":"victoria-stack","status":"partial","exports":[{"name":"logs","status":"empty"}]}]}`
	pod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name:  "collector",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: message}},
	}}}}
	result, err := PodResult(pod, "collector")
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifact.SizeBytes != 42 || !result.Partial || len(result.Collectors) != 2 || result.Collectors[1].Exports[0].Status != "empty" {
		t.Errorf("result = %+v", result)
	}

	running := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name: "collector", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}}}
	if result, err := PodResult(running, "collector"); result != nil || err != nil {
		t.Errorf("running pod: %v %v", result, err)
	}
	if _, err := ParseResult(`{"version":"v2"}`); err == nil {
		t.Error("expected unsupported version error")
	}
}

func TestFormatLogLine(t *testing.T) {
	got := FormatLogLine(`{"time":"t","level":"INFO","msg":"[support-bundle] upload complete","destination":"s3://b/x.tar.gz"}`)
	if got != "INFO upload complete destination=s3://b/x.tar.gz" {
		t.Errorf("got %q", got)
	}
	if got := FormatLogLine("plain text"); got != "plain text" {
		t.Errorf("got %q", got)
	}
}

func TestWaitForCollectorPodJobFailed(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "support-bundle-x", Namespace: "wandb"},
		Spec:       batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "collector"}}}}},
		Status:     batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded"}}},
	}
	pending := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "wandb", Labels: map[string]string{batchv1.JobNameLabel: job.Name}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "collector", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
		}}},
	}
	var reasons []string
	pod, err := WaitForCollectorPod(context.Background(), fake.NewClientset(job, pending), job, func(r string) { reasons = append(reasons, r) })
	if err != nil || pod != nil {
		t.Fatalf("a failed Job should end the wait without a pod: pod=%v err=%v", pod, err)
	}
	if got := waitingReason(pending, "collector"); got != "ImagePullBackOff" {
		t.Errorf("waitingReason = %q", got)
	}
}
