package supportbundle

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// testTemplate mirrors the collector CronJob the operator renders.
func testTemplate() *batchv1.CronJob {
	selector := map[string]string{
		WandbNameLabel:      "wandb",
		WandbNamespaceLabel: "wandb",
		WandbComponentLabel: CollectorComponent,
		PartOfLabel:         "wandb",
	}
	// What the Application controller puts on the CronJob in a real cluster.
	objectLabels := map[string]string{
		"app.kubernetes.io/instance": "wandb",
		ManagedByLabel:               "application-controller",
		"app.kubernetes.io/name":     "wandb-support-bundle",
		"app.kubernetes.io/version":  "0.4.10",
	}
	secretRef := func(name, key string) *corev1.EnvVarSource {
		return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key,
		}}
	}
	fieldRef := func(path string) *corev1.EnvVarSource {
		return &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: path}}
	}
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wandb-support-bundle-collector",
			Namespace:       "wandb",
			UID:             types.UID("cronjob-uid"),
			ResourceVersion: "42",
			Labels:          objectLabels,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps.wandb.com/v2", Kind: "Application", Name: "wandb-support-bundle", UID: "app-uid", Controller: new(true)}},
		},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 0 * * *",
			Suspend:  new(true),
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: selector},
				Spec: batchv1.JobSpec{
					BackoffLimit:            new(int32(0)),
					ActiveDeadlineSeconds:   new(int64(1800)),
					TTLSecondsAfterFinished: new(int32(86400)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: selector},
						Spec: corev1.PodSpec{
							RestartPolicy:                corev1.RestartPolicyNever,
							ServiceAccountName:           "wandb-support-bundle",
							AutomountServiceAccountToken: new(true),
							ImagePullSecrets:             []corev1.LocalObjectReference{{Name: "regcred"}},
							SecurityContext:              &corev1.PodSecurityContext{RunAsNonRoot: new(true), RunAsUser: new(int64(65532))},
							Tolerations:                  []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists}},
							Containers: []corev1.Container{{
								Name:       "collector",
								Image:      "us-docker.pkg.dev/wandb-production/public/wandb/lumen:0.4.10",
								Command:    []string{"/app/bin/collector"},
								Args:       []string{"--log-format=json", "agent", "--support-bundle"},
								WorkingDir: "/app",
								Env: []corev1.EnvVar{
									{Name: "WANDB_DATA_ROOT", ValueFrom: secretRef("wandb-objectstore-connection", "url")},
									{Name: "LUMEN_SUPPORT_BUNDLE_RESULT_PATH", Value: "/dev/termination-log"},
									{Name: "SUPPORT_BUNDLE_ID", ValueFrom: fieldRef("metadata.labels['batch.kubernetes.io/job-name']")},
									{Name: "SELF_POD_NAME", ValueFrom: fieldRef("metadata.name")},
									{Name: "SELF_POD_NAMESPACE", ValueFrom: fieldRef("metadata.namespace")},
									{Name: "AWS_REGION", ValueFrom: secretRef("wandb-objectstore-connection", "Region")},
								},
								VolumeMounts: []corev1.VolumeMount{
									{Name: "collection", MountPath: "/etc/support-bundle", ReadOnly: true},
									{Name: "staging", MountPath: "/staging"},
									{Name: "tmp", MountPath: "/tmp"},
								},
								TerminationMessagePath:   "/dev/termination-log",
								TerminationMessagePolicy: corev1.TerminationMessageReadFile,
								SecurityContext:          &corev1.SecurityContext{ReadOnlyRootFilesystem: new(true), AllowPrivilegeEscalation: new(false)},
							}},
							Volumes: []corev1.Volume{
								{Name: "collection", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "wandb-support-bundle"}}}},
								{Name: "staging", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
								{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
							},
						},
					},
				},
			},
		},
	}
}
