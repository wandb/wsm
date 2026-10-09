package supportbundle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const validResult = `{"version":"v1","artifact":{"location":"s3://bucket/agent/service-bundles/support-bundle-x.tar.gz","sizeBytes":42,"sha256":"` +
	"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `"}}`

func TestParseResult(t *testing.T) {
	if r, err := ParseResult(""); r != nil || err != nil {
		t.Error("empty message means no result")
	}
	r, err := ParseResult(validResult)
	if err != nil || r.Artifact.SizeBytes != 42 {
		t.Fatalf("valid result rejected: %v", err)
	}
	for _, bad := range []string{
		`{"version":"v2","artifact":{"location":"s3://b/k.tar.gz"}}`,
		`{"version":"v1","artifact":{"location":"s3://ak:sk@b/k.tar.gz"}}`,
		`{"version":"v1","artifact":{"location":"s3://b/k.tar.gz?X-Amz-Signature=1"}}`,
		`{"version":"v1","artifact":{"location":"https://b/k.tar.gz"}}`,
		`{"version":"v1","artifact":{"location":"s3://b/k.tar.gz","sha256":"xyz"}}`,
		`{"version":"v1","failure":{"stage":"Upload","reason":"bad reason!"}}`,
		`{"version":"v1"}`,
		`not json`,
	} {
		if _, err := ParseResult(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	r, err = ParseResult(`{"version":"v1","failure":{"stage":"Upload","reason":"UploadFailed","message":"PUT s3://ak:sk@host failed"}}`)
	if err != nil || strings.Contains(r.Failure.Message, "sk@") {
		t.Errorf("failure message must be sanitized: %v %+v", err, r)
	}
}

func jobWith(cond batchv1.JobConditionType, reason string) *batchv1.Job {
	job := &batchv1.Job{}
	if cond != "" {
		job.Status.Conditions = []batchv1.JobCondition{{Type: cond, Status: corev1.ConditionTrue, Reason: reason}}
	}
	return job
}

func podWith(state corev1.ContainerState) *corev1.Pod {
	return &corev1.Pod{
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: containerName, Image: "reg/wandb/lumen:abc"}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: containerName, State: state}}},
	}
}

func TestObserve(t *testing.T) {
	terminated := func(msg string, code int32) corev1.ContainerState {
		return corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: msg, ExitCode: code}}
	}
	cases := []struct {
		name    string
		job     *batchv1.Job
		pod     *corev1.Pod
		phase   Phase
		problem string
		message string
	}{
		{"no pod yet", jobWith("", ""), nil, PhasePending, "", "created"},
		{"image pull", jobWith("", ""), podWith(corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}), PhasePending, "cannot pull", ""},
		{"running", jobWith("", ""), podWith(corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}), PhaseRunning, "", ""},
		{"exited before job condition", jobWith("", ""), podWith(terminated("", 0)), PhaseRunning, "", "waiting for the Job"},
		{"success with result", jobWith(batchv1.JobComplete, ""), podWith(terminated(validResult, 0)), PhaseReady, "", ""},
		{"success without result", jobWith(batchv1.JobComplete, ""), podWith(terminated("", 0)), PhaseCompleted, "", "reporting unavailable"},
		{"success with bad result", jobWith(batchv1.JobComplete, ""), podWith(terminated(`{"version":"v1","artifact":{"location":"s3://a:b@c/d.tar.gz"}}`, 0)), PhaseCompleted, "", "rejected"},
		{"deadline", jobWith(batchv1.JobFailed, "DeadlineExceeded"), nil, PhaseFailed, "", "Timeout"},
		{"collector failure", jobWith(batchv1.JobFailed, "BackoffLimitExceeded"), podWith(terminated(`{"version":"v1","failure":{"stage":"Collection","reason":"Forbidden"}}`, 1)), PhaseFailed, "", "Collection: Forbidden"},
		{"crash without result", jobWith(batchv1.JobFailed, "BackoffLimitExceeded"), podWith(terminated("", 137)), PhaseFailed, "", "code 137"},
		{"job gone", nil, nil, PhaseUnknown, "", "no longer exists"},
	}
	for _, tc := range cases {
		obs := Observe(tc.job, tc.pod)
		if obs.Phase != tc.phase {
			t.Errorf("%s: phase %s, want %s", tc.name, obs.Phase, tc.phase)
		}
		if tc.problem != "" && (len(obs.Problems) == 0 || !strings.Contains(obs.Problems[0], tc.problem)) {
			t.Errorf("%s: problems %v", tc.name, obs.Problems)
		}
		if tc.message != "" && !strings.Contains(obs.Message, tc.message) {
			t.Errorf("%s: message %q", tc.name, obs.Message)
		}
		if obs.Phase == PhaseCompleted && obs.Result != nil {
			t.Errorf("%s: completed without a valid artifact must not carry a result", tc.name)
		}
	}
}

func newTestManager(t *testing.T, now time.Time) (*Manager, *fake.Clientset) {
	t.Helper()
	cs := fake.NewSimpleClientset()
	// The fake does not assign UIDs; the API server does.
	cs.PrependReactor("create", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		obj := action.(k8stesting.CreateAction).GetObject().(metav1.Object)
		if obj.GetUID() == "" {
			obj.SetUID(types.UID("uid-" + obj.GetName()))
		}
		return false, nil, nil
	})
	return &Manager{Client: cs, Namespace: "wandb", Name: "wandb", Now: func() time.Time { return now }}, cs
}

func TestCreateSyncAndReconnect(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 10, 14, 12, 0, 0, time.UTC)
	m, cs := newTestManager(t, now)
	inst := testInstallation("")
	opts := testRunOptions()
	opts.Created = now

	if _, err := m.Create(ctx, inst, opts, []byte("spec")); err != nil {
		t.Fatal(err)
	}
	spec, err := cs.CoreV1().ConfigMaps("wandb").Get(ctx, "support-bundle-abc123-spec", metav1.GetOptions{})
	if err != nil || len(spec.OwnerReferences) != 1 {
		t.Fatalf("spec ConfigMap must be owned by the record: %v", err)
	}

	// A second, independent Get (reconnect) sees the same pending run.
	rec, obs, err := m.Get(ctx, "sb-abc123")
	if err != nil || rec.Phase != PhasePending || obs.Phase != PhasePending {
		t.Fatalf("pending run: %v %+v", err, rec)
	}

	job, _ := cs.BatchV1().Jobs("wandb").Get(ctx, "support-bundle-abc123", metav1.GetOptions{})
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	_, _ = cs.BatchV1().Jobs("wandb").UpdateStatus(ctx, job, metav1.UpdateOptions{})
	pod := podWith(corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: validResult}})
	pod.Name, pod.Namespace, pod.Labels = "support-bundle-abc123-x", "wandb", map[string]string{"job-name": "support-bundle-abc123"}
	_, _ = cs.CoreV1().Pods("wandb").Create(ctx, pod, metav1.CreateOptions{})

	rec, _, err = m.Get(ctx, "sb-abc123")
	if err != nil || rec.Phase != PhaseReady || rec.Artifact() == nil {
		t.Fatalf("ready run: %v %+v", err, rec)
	}

	// The outcome is persisted, so it survives the Job's TTL.
	_ = cs.BatchV1().Jobs("wandb").Delete(ctx, "support-bundle-abc123", metav1.DeleteOptions{})
	rec, _, err = m.Get(ctx, "sb-abc123")
	if err != nil || rec.Phase != PhaseReady {
		t.Fatalf("persisted outcome lost: %v %+v", err, rec)
	}
}

func TestDeleteAndPruneOnlyTouchOwnedRecords(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	m, cs := newTestManager(t, now)
	inst := testInstallation("")

	expired := NewRecord(inst, testRunOptions())
	expired.Phase = PhaseReady
	expired.Result, _ = ParseResult(validResult)
	cm, _ := expired.ToConfigMap("wandb", "wandb")
	_, _ = cs.CoreV1().ConfigMaps("wandb").Create(ctx, cm, metav1.CreateOptions{})

	fresh := NewRecord(inst, RunOptions{ID: "sb-fff000", Created: now, Window: time.Hour, Retention: 24 * time.Hour})
	fresh.Phase = PhaseFailed
	cm, _ = fresh.ToConfigMap("wandb", "wandb")
	_, _ = cs.CoreV1().ConfigMaps("wandb").Create(ctx, cm, metav1.CreateOptions{})

	// Unlabelled ConfigMap with a record-like name must be ignored.
	_, _ = cs.CoreV1().ConfigMaps("wandb").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "wsm-sb-sb-999999", Namespace: "wandb"}}, metav1.CreateOptions{})

	records, err := m.List(ctx)
	if err != nil || len(records) != 2 || records[0].ID != "sb-fff000" {
		t.Fatalf("list must return owned records newest first: %v %d", err, len(records))
	}
	if _, _, err := m.Get(ctx, "sb-999999"); err == nil {
		t.Error("unlabelled record must not be readable as a bundle")
	}

	toPrune := m.Expired(records)
	if len(toPrune) != 1 || toPrune[0].ID != "sb-abc123" {
		t.Fatalf("expired = %v", toPrune)
	}

	// Object delete failure keeps the record.
	failing := func(context.Context, *Artifact) error { return errors.New("boom") }
	if err := m.Delete(ctx, toPrune[0], failing); err == nil {
		t.Fatal("expected object delete failure")
	}
	if _, err := cs.CoreV1().ConfigMaps("wandb").Get(ctx, "wsm-sb-sb-abc123", metav1.GetOptions{}); err != nil {
		t.Fatal("record must be kept when the object could not be deleted")
	}

	var deleted string
	if err := m.Delete(ctx, toPrune[0], func(_ context.Context, a *Artifact) error { deleted = a.Location; return nil }); err != nil {
		t.Fatal(err)
	}
	if deleted == "" {
		t.Error("object delete not called")
	}
	if _, err := cs.CoreV1().ConfigMaps("wandb").Get(ctx, "wsm-sb-sb-999999", metav1.GetOptions{}); err != nil {
		t.Error("unrelated ConfigMap must survive")
	}
}

func TestCancelRunningBundle(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	m, cs := newTestManager(t, now)
	opts := testRunOptions()
	opts.Created = now
	rec, err := m.Create(ctx, testInstallation(""), opts, []byte("spec"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, rec, nil); !errors.Is(err, ErrStillRunning) {
		t.Fatalf("delete of a running bundle must be refused, got %v", err)
	}
	if _, err := cs.BatchV1().Jobs("wandb").Get(ctx, rec.JobName, metav1.GetOptions{}); err != nil {
		t.Fatal("a refused delete must leave the Job running")
	}

	if err := m.Cancel(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.BatchV1().Jobs("wandb").Get(ctx, rec.JobName, metav1.GetOptions{}); err == nil {
		t.Error("cancel must delete the Job")
	}
	got, _, err := m.Get(ctx, rec.ID)
	if err != nil || got.Phase != PhaseCancelled || got.Completed == nil {
		t.Fatalf("cancel must keep a Cancelled record: %v %+v", err, got)
	}
	if err := m.Cancel(ctx, got); err == nil {
		t.Error("cancelling a finished bundle must error")
	}

	if err := m.Delete(ctx, got, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Get(ctx, rec.ID); err == nil {
		t.Error("record must be gone after delete")
	}
}

func TestFitNameAndIDs(t *testing.T) {
	long := strings.Repeat("a", 80)
	if n := fitName(long, 63); len(n) != 63 || fitName(long+"b", 63) == n {
		t.Errorf("fitName must cap length and stay distinct: %s", n)
	}
	id, err := NewBundleID()
	if err != nil || !ValidBundleID(id) {
		t.Errorf("bad id %q", id)
	}
	for _, bad := range []string{"sb-xyz123", "sb-abc12", "../etc"} {
		if ValidBundleID(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}
