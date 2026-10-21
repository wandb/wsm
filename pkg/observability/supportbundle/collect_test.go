package supportbundle

import (
	"context"
	"strings"
	"testing"

	v2 "github.com/wandb/operator/api/v2"
	"github.com/wandb/wsm/pkg/operator"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func testCR() *v2.WeightsAndBiases {
	cr := &v2.WeightsAndBiases{ObjectMeta: metav1.ObjectMeta{Name: "wandb", Namespace: "wandb"}}
	cr.Spec.SupportBundle.Enabled = new(true)
	cr.Status.SupportBundle = &v2.SupportBundleStatusSummary{Ready: true, CronJob: "wandb-support-bundle-collector"}
	return cr
}

func testCronJob(cr *v2.WeightsAndBiases) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "wandb-support-bundle-collector", Namespace: cr.Namespace, UID: "cron-uid"},
		Spec: batchv1.CronJobSpec{
			Suspend: new(true),
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: selectorLabels(cr), Annotations: map[string]string{"a": "b"}},
				Spec: batchv1.JobSpec{
					BackoffLimit: new(int32(0)),
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: "collector",
						Args: []string{"agent", "--support-bundle-spec=/etc/lumen/collection/full.yaml", "--support-bundle-id=$(SUPPORT_BUNDLE_ID)"},
					}}}},
				},
			},
		},
	}
}

func TestNewCollectionJob(t *testing.T) {
	cr := testCR()
	cronJob := testCronJob(cr)
	job, err := NewCollectionJob(cronJob, "support-bundle-abc123", CollectOptions{Preset: "pod-logs"})
	if err != nil {
		t.Fatal(err)
	}
	if job.Name != "support-bundle-abc123" || job.Namespace != "wandb" {
		t.Errorf("job = %s/%s", job.Namespace, job.Name)
	}
	if job.Labels[WandbComponentLabel] != CollectorComponent || job.Annotations["a"] != "b" || job.Annotations["cronjob.kubernetes.io/instantiate"] != "manual" {
		t.Errorf("metadata = %v %v", job.Labels, job.Annotations)
	}
	owner := job.OwnerReferences[0]
	if owner.Kind != "CronJob" || owner.UID != "cron-uid" || owner.Controller == nil || !*owner.Controller {
		t.Errorf("owner = %+v", owner)
	}
	args := job.Spec.Template.Spec.Containers[0].Args
	want := []string{"agent", "--support-bundle-spec=/etc/lumen/collection/pod-logs.yaml", "--support-bundle-id=$(SUPPORT_BUNDLE_ID)"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v", args)
	}
	if got := cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Args[1]; got != "--support-bundle-spec=/etc/lumen/collection/full.yaml" {
		t.Errorf("template mutated: %s", got)
	}
}

func TestTemplateGuards(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*v2.WeightsAndBiases, *batchv1.CronJob)
		want   string
	}{
		{"ready", func(*v2.WeightsAndBiases, *batchv1.CronJob) {}, ""},
		{"disabled", func(cr *v2.WeightsAndBiases, _ *batchv1.CronJob) { cr.Spec.SupportBundle.Enabled = new(false) }, "wsm support-bundle --enabled=true"},
		{"no status", func(cr *v2.WeightsAndBiases, _ *batchv1.CronJob) { cr.Status.SupportBundle = nil }, "predates support bundles"},
		{"not ready", func(cr *v2.WeightsAndBiases, _ *batchv1.CronJob) { cr.Status.SupportBundle.Ready = false }, "not ready"},
		{"missing cronjob", func(_ *v2.WeightsAndBiases, c *batchv1.CronJob) { c.Name = "other" }, "does not exist"},
		{"foreign cronjob", func(_ *v2.WeightsAndBiases, c *batchv1.CronJob) { c.Spec.JobTemplate.Labels[WandbNameLabel] = "other" }, "not the support-bundle template"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := testCR()
			cronJob := testCronJob(cr)
			tt.mutate(cr, cronJob)
			_, err := Template(context.Background(), fake.NewClientset(cronJob), cr)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCreate(t *testing.T) {
	cr := testCR()
	cronJob := testCronJob(cr)

	running := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "support-bundle-old", Namespace: "wandb", Labels: selectorLabels(cr)}}
	if _, err := Create(context.Background(), fake.NewClientset(cronJob, running), cr, CollectOptions{}); err == nil || !strings.Contains(err.Error(), "support-bundle-old is still collecting") {
		t.Fatalf("err = %v", err)
	}

	finished := running.DeepCopy()
	finished.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	client := fake.NewClientset([]runtime.Object{cronJob, finished}...)
	job, err := Create(context.Background(), client, cr, CollectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(job.Name, "support-bundle-") || len(job.Name) != len("support-bundle-")+6 {
		t.Errorf("name = %s", job.Name)
	}
	if _, err := client.BatchV1().Jobs("wandb").Get(context.Background(), job.Name, metav1.GetOptions{}); err != nil {
		t.Errorf("job not created: %v", err)
	}
	if got := job.Spec.Template.Spec.Containers[0].Args[1]; got != "--support-bundle-spec=/etc/lumen/collection/full.yaml" {
		t.Errorf("zero options should run the default preset, got %s", got)
	}
}

func TestCreateLosesRace(t *testing.T) {
	for _, tt := range []struct {
		rival   string
		wantErr bool
	}{
		{"support-bundle-000000", true},  // rival sorts first: ours deletes itself
		{"support-bundle-zzzzzz", false}, // ours sorts first: ours stays
	} {
		t.Run(tt.rival, func(t *testing.T) {
			cr := testCR()
			client := fake.NewClientset(testCronJob(cr))
			rival := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: tt.rival, Namespace: "wandb", Labels: selectorLabels(cr)}}
			// Simulate a second wsm creating its Job between our Running check and our Create.
			client.PrependReactor("create", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
				_ = client.Tracker().Add(rival)
				return false, nil, nil
			})
			job, err := Create(context.Background(), client, cr, CollectOptions{})
			jobs, _ := client.BatchV1().Jobs("wandb").List(context.Background(), metav1.ListOptions{})
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), tt.rival+" started at the same time") {
					t.Fatalf("err = %v", err)
				}
				if len(jobs.Items) != 1 || jobs.Items[0].Name != tt.rival {
					t.Errorf("only the rival should remain, got %d jobs", len(jobs.Items))
				}
				return
			}
			if err != nil || job == nil || len(jobs.Items) != 2 {
				t.Fatalf("job=%v err=%v jobs=%d", job, err, len(jobs.Items))
			}
		})
	}
}

func TestSelectInstallation(t *testing.T) {
	refs := []operator.CRRef{{Namespace: "a", Name: "wandb"}, {Namespace: "b", Name: "wandb"}}
	if _, err := SelectInstallation(refs, "", ""); err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Errorf("ambiguous: %v", err)
	}
	if got, err := SelectInstallation(refs, "b", ""); err != nil || got.Namespace != "b" {
		t.Errorf("by namespace: %v %v", got, err)
	}
	if _, err := SelectInstallation(refs, "", "nope"); err == nil || !strings.Contains(err.Error(), `named "nope"`) {
		t.Errorf("not found: %v", err)
	}
}
