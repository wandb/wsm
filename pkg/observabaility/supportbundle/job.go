package supportbundle

import (
	"time"

	v2 "github.com/wandb/operator/api/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	specMountDir   = "/etc/support-bundle"
	specFileName   = "support-bundle.yaml"
	stagingDir     = "/staging"
	collectorPath  = "/app/bin/collector"
	terminationLog = "/dev/termination-log"
)

// RunOptions describe one collection run.
type RunOptions struct {
	ID             string
	Image          string
	ServiceAccount string
	PullSecrets    []corev1.LocalObjectReference
	Created        time.Time
	Window         time.Duration
	Timeout        time.Duration
	Retention      time.Duration
	Sources        []string
}

// BuildSpecConfigMap holds the rendered collection YAML (no credentials).
func BuildSpecConfigMap(inst *Installation, opts RunOptions, specYAML []byte, owner *corev1.ConfigMap) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            specName(opts.ID),
			Namespace:       inst.Namespace,
			Labels:          bundleLabels(inst.Namespace, inst.Name, opts.ID, roleSpec),
			OwnerReferences: ownerRefs(owner),
		},
		Data: map[string]string{specFileName: string(specYAML)},
	}
}

// BuildJob constructs the collector Job. Bucket credentials are injected
// only as live secretKeyRefs copied from the CR status.
func BuildJob(inst *Installation, opts RunOptions, owner *corev1.ConfigMap) *batchv1.Job {
	labels := bundleLabels(inst.Namespace, inst.Name, opts.ID, roleJob)

	env := []corev1.EnvVar{
		{Name: "LUMEN_STAGING_ROOT", Value: "file://" + stagingDir},
		{Name: ResultPathEnv, Value: terminationLog},
	}
	for _, pair := range []struct {
		name  string
		value v2.ValueOrSecret
	}{
		{"WANDB_DATA_ROOT", inst.ObjectStore.URL},
		{"WANDB_DATA_ACCESS_KEY", inst.ObjectStore.AccessKey},
		{"WANDB_DATA_SECRET_KEY", inst.ObjectStore.SecretKey},
		{"AWS_REGION", inst.ObjectStore.Region},
	} {
		if ref := pair.value.SecretKeyRef(); ref != nil {
			env = append(env, corev1.EnvVar{Name: pair.name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ref.DeepCopy()}})
		} else if pair.name == "AWS_REGION" && pair.value.Value != "" {
			env = append(env, corev1.EnvVar{Name: pair.name, Value: pair.value.Value})
		}
	}

	container := corev1.Container{
		Name:    containerName,
		Image:   opts.Image,
		Command: []string{collectorPath},
		Args: []string{
			"agent",
			"--lumen-collection=false",
			"--support-bundle",
			"--support-bundle-spec=" + specMountDir + "/" + specFileName,
			"--staging-root=file://" + stagingDir,
			"--log-format=json",
		},
		Env: env,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:              resource.MustParse("1"),
				corev1.ResourceMemory:           resource.MustParse("1Gi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "spec", MountPath: specMountDir, ReadOnly: true},
			{Name: "staging", MountPath: stagingDir},
			{Name: "tmp", MountPath: "/tmp"},
		},
		TerminationMessagePath:   terminationLog,
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: new(false),
			ReadOnlyRootFilesystem:   new(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
	}

	stagingLimit := resource.MustParse("20Gi")
	tmpLimit := resource.MustParse("1Gi")
	pod := corev1.PodSpec{
		ServiceAccountName:           opts.ServiceAccount,
		AutomountServiceAccountToken: new(true),
		RestartPolicy:                corev1.RestartPolicyNever,
		ImagePullSecrets:             opts.PullSecrets,
		Affinity:                     inst.Affinity,
		Tolerations:                  inst.Tolerations,
		Containers:                   []corev1.Container{container},
		Volumes: []corev1.Volume{
			{Name: "spec", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: specName(opts.ID)},
			}}},
			{Name: "staging", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &stagingLimit}}},
			{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &tmpLimit}}},
		},
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            jobName(opts.ID),
			Namespace:       inst.Namespace,
			Labels:          labels,
			OwnerReferences: ownerRefs(owner),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            new(int32(0)),
			ActiveDeadlineSeconds:   new(int64(opts.Timeout.Seconds())),
			TTLSecondsAfterFinished: new(int32(opts.Retention.Seconds())),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       pod,
			},
		},
	}
}

// ownerRefs makes the record ConfigMap the GC root of a run, so deleting the
// record removes its spec ConfigMap and Job even if wsm exited mid-create.
func ownerRefs(owner *corev1.ConfigMap) []metav1.OwnerReference {
	if owner == nil || owner.UID == "" {
		return nil
	}
	return []metav1.OwnerReference{{
		APIVersion: "v1",
		Kind:       "ConfigMap",
		Name:       owner.Name,
		UID:        owner.UID,
	}}
}
