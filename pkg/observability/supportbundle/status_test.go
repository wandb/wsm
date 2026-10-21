package supportbundle

import (
	"context"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func testCollectionJob(conditions ...batchv1.JobCondition) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "sb-test", Namespace: "wandb", Labels: map[string]string{
			WandbNameLabel: "wandb", WandbNamespaceLabel: "wandb", WandbComponentLabel: CollectorComponent,
		}},
		Spec:   batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "collector"}}}}},
		Status: batchv1.JobStatus{Conditions: conditions},
	}
}

func collectionPod(state corev1.ContainerState) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "sb-test-abcde", Namespace: "wandb", Labels: map[string]string{batchv1.JobNameLabel: "sb-test"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "collector"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "collector", State: state}}},
	}
}

func exited(message string, code int32) corev1.ContainerState {
	return corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: message, ExitCode: code, Reason: "Completed"}}
}

const validResult = `{"version":"v1","artifact":{"location":"s3://bucket/agent/service-bundles/support-bundle-x.tar.gz","sizeBytes":42,"sha256":"abc"},"partial":true}`

var complete = batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}

func TestDescribeCollection(t *testing.T) {
	ctx := context.Background()
	inst := testCR()

	tests := []struct {
		name        string
		job         *batchv1.Job
		pod         *corev1.Pod
		phase       string
		message     string
		hasArtifact bool
	}{
		{"succeeded with artifact", testCollectionJob(complete), collectionPod(exited(validResult, 0)), PhaseSucceeded, "", true},
		{"completed but collector reported failure", testCollectionJob(complete),
			collectionPod(exited(`{"version":"v1","failure":{"stage":"Upload","reason":"UploadFailed","message":"denied"}}`, 0)), PhaseFailed, "UploadFailed denied", false},
		{"completed with no result", testCollectionJob(complete), collectionPod(exited("", 0)), PhaseSucceeded, "reported no result", false},
		{"deadline exceeded", testCollectionJob(batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded"}), nil, PhaseFailed, "deadline", false},
		{"running", testCollectionJob(), collectionPod(corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}), PhaseRunning, "", false},
		{"no pod yet", testCollectionJob(), nil, PhasePending, "waiting for the collection pod", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := []runtime.Object{tt.job}
			if tt.pod != nil {
				objects = append(objects, tt.pod)
			}
			status, err := DescribeCollection(ctx, fake.NewClientset(objects...), inst, "sb-test")
			if err != nil {
				t.Fatal(err)
			}
			if status.Phase != tt.phase || !strings.Contains(status.Message, tt.message) {
				t.Errorf("status = %s %q, want %s containing %q", status.Phase, status.Message, tt.phase, tt.message)
			}
			if (status.Result != nil && status.Result.Artifact != nil) != tt.hasArtifact {
				t.Errorf("artifact presence = %v, want %v", status.Result != nil && status.Result.Artifact != nil, tt.hasArtifact)
			}
		})
	}
}

func TestDescribeCollectionMissingOrForeignJob(t *testing.T) {
	ctx := context.Background()
	inst := testCR()

	status, err := DescribeCollection(ctx, fake.NewClientset(), inst, "sb-test")
	if err != nil || status != nil {
		t.Errorf("missing Job: %+v, %v; want nil, nil", status, err)
	}

	foreign := testCollectionJob(complete)
	foreign.Labels[WandbComponentLabel] = "something-else"
	if _, err := DescribeCollection(ctx, fake.NewClientset(foreign), inst, "sb-test"); err == nil {
		t.Error("a Job without the collector labels was accepted")
	}

	if _, err := DescribeCollection(ctx, fake.NewClientset(), inst, "Not_A_Job"); err == nil {
		t.Error("invalid ID accepted")
	}
}
