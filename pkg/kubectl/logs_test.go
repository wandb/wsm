package kubectl

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestFollowPodLogs(t *testing.T) {
	client := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "wandb"}})
	var lines []string
	if err := FollowPodLogs(context.Background(), client, "wandb", "p", "c", func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	// The fake clientset always returns a single "fake logs" line.
	if len(lines) != 1 || lines[0] != "fake logs" {
		t.Errorf("lines = %q", lines)
	}
}
