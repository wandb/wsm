package supportbundle

import (
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestBuildManagerJob(t *testing.T) {
	template := testTemplate()
	original := template.DeepCopy()

	job, err := BuildManagerJob(testCR(), template, OperationRetrieve, 15*time.Minute, "abc123")
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(template, original) {
		t.Fatal("BuildManagerJob modified the template")
	}
	if job.Name != "support-bundle-retrieve-abc123" || job.Namespace != "wandb" {
		t.Fatalf("job identity = %s/%s", job.Namespace, job.Name)
	}
	if job.ResourceVersion != "" || job.UID != "" {
		t.Fatal("job copied identity metadata from the template")
	}

	wantLabels := map[string]string{
		ManagedByLabel:      "wsm",
		ComponentLabel:      ManagerComponent,
		PartOfLabel:         "wandb",
		WandbNameLabel:      "wandb",
		WandbNamespaceLabel: "wandb",
		OperationLabel:      "retrieve",
	}
	for name, labels := range map[string]map[string]string{"job": job.Labels, "pod": job.Spec.Template.Labels} {
		if !reflect.DeepEqual(labels, wantLabels) {
			t.Errorf("%s labels = %v, want %v", name, labels, wantLabels)
		}
		if _, ok := labels[WandbComponentLabel]; ok {
			t.Errorf("%s carries the operator's component label, so the operator would count it as a collection", name)
		}
	}

	if len(job.OwnerReferences) != 1 {
		t.Fatalf("owner references = %v", job.OwnerReferences)
	}
	owner := job.OwnerReferences[0]
	if owner.Kind != "CronJob" || owner.Name != template.Name || owner.UID != template.UID {
		t.Errorf("owner = %+v, want the template CronJob", owner)
	}
	if owner.Controller == nil || *owner.Controller || owner.BlockOwnerDeletion == nil || *owner.BlockOwnerDeletion {
		t.Errorf("owner must be non-controller and non-blocking: %+v", owner)
	}

	if *job.Spec.BackoffLimit != 0 {
		t.Errorf("backoffLimit = %d, want 0", *job.Spec.BackoffLimit)
	}
	if *job.Spec.ActiveDeadlineSeconds != int64((21 * time.Minute).Seconds()) {
		t.Errorf("activeDeadlineSeconds = %d", *job.Spec.ActiveDeadlineSeconds)
	}
	if *job.Spec.TTLSecondsAfterFinished != managerTTLSeconds {
		t.Errorf("ttlSecondsAfterFinished = %d", *job.Spec.TTLSecondsAfterFinished)
	}

	pod := job.Spec.Template.Spec
	sourcePod := original.Spec.JobTemplate.Spec.Template.Spec
	if pod.ServiceAccountName != sourcePod.ServiceAccountName || !*pod.AutomountServiceAccountToken {
		t.Error("pod must keep the template's ServiceAccount and token mount")
	}
	if !reflect.DeepEqual(pod.ImagePullSecrets, sourcePod.ImagePullSecrets) ||
		!reflect.DeepEqual(pod.SecurityContext, sourcePod.SecurityContext) ||
		!reflect.DeepEqual(pod.Tolerations, sourcePod.Tolerations) {
		t.Error("pod must keep pull secrets, security context, and scheduling")
	}
	if pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %s", pod.RestartPolicy)
	}
	if len(pod.Volumes) != 1 || pod.Volumes[0].Name != "tmp" {
		t.Errorf("volumes = %v, want only tmp", pod.Volumes)
	}

	container := pod.Containers[0]
	if !reflect.DeepEqual(container.Command, []string{"sleep", "900"}) || container.Args != nil {
		t.Errorf("command = %v args = %v", container.Command, container.Args)
	}
	if container.Image != sourcePod.Containers[0].Image || container.Name != "collector" {
		t.Errorf("container = %s %s", container.Name, container.Image)
	}
	if !reflect.DeepEqual(container.SecurityContext, sourcePod.Containers[0].SecurityContext) {
		t.Error("container must keep the template's security context")
	}
	var envNames []string
	for _, e := range container.Env {
		envNames = append(envNames, e.Name)
	}
	if !reflect.DeepEqual(envNames, []string{"WANDB_DATA_ROOT", "AWS_REGION"}) {
		t.Errorf("env = %v, want only bucket env", envNames)
	}
	if !reflect.DeepEqual(container.Env[0], sourcePod.Containers[0].Env[0]) {
		t.Error("bucket Secret ref must pass through unchanged")
	}
	if len(container.VolumeMounts) != 1 || container.VolumeMounts[0].Name != "tmp" {
		t.Errorf("mounts = %v, want only tmp", container.VolumeMounts)
	}
}

func TestBuildManagerJobRejectsBadInput(t *testing.T) {
	if _, err := BuildManagerJob(testCR(), testTemplate(), OperationList, 0, "abc123"); err == nil {
		t.Error("zero lifetime accepted")
	}
	template := testTemplate()
	pod := &template.Spec.JobTemplate.Spec.Template.Spec
	pod.Containers = append(pod.Containers, corev1.Container{Name: "extra"})
	if _, err := BuildManagerJob(testCR(), template, OperationList, time.Minute, "abc123"); err == nil {
		t.Error("two-container template accepted")
	}
}
