package supportbundle

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	v2 "github.com/wandb/operator/api/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Collection-only parts of the collector pod; bucket env and Secret refs pass through.
var (
	collectionOnlyEnv     = []string{"LUMEN_SUPPORT_BUNDLE_RESULT_PATH", "SUPPORT_BUNDLE_ID", "SELF_POD_NAME", "SELF_POD_NAMESPACE"}
	collectionOnlyVolumes = []string{"collection", "staging"}
)

const (
	managerTTLSeconds int32 = 300
	deadlineSlack           = 6 * time.Minute
)

// BuildManagerJob copies the collector pod into a Job that sleeps for
// lifetime, so wsm can exec bundle-manager in it.
func BuildManagerJob(cr *v2.WeightsAndBiases, template *batchv1.CronJob, op Operation, lifetime time.Duration, suffix string) (*batchv1.Job, error) {
	if lifetime < time.Second {
		return nil, fmt.Errorf("manager Job lifetime must be at least 1s, got %s", lifetime)
	}
	sourcePod := template.Spec.JobTemplate.Spec.Template.Spec
	if len(sourcePod.Containers) != 1 {
		return nil, fmt.Errorf("support-bundle template has %d containers, expected 1", len(sourcePod.Containers))
	}

	pod := *sourcePod.DeepCopy()
	pod.RestartPolicy = corev1.RestartPolicyNever
	pod.Volumes = slices.DeleteFunc(pod.Volumes, func(v corev1.Volume) bool {
		return slices.Contains(collectionOnlyVolumes, v.Name)
	})

	container := &pod.Containers[0]
	container.Command = []string{"sleep", strconv.FormatInt(int64(lifetime.Seconds()), 10)}
	container.Args = nil
	container.Env = slices.DeleteFunc(container.Env, func(e corev1.EnvVar) bool {
		return slices.Contains(collectionOnlyEnv, e.Name)
	})
	container.VolumeMounts = slices.DeleteFunc(container.VolumeMounts, func(m corev1.VolumeMount) bool {
		return slices.Contains(collectionOnlyVolumes, m.Name)
	})

	labels := ManagerLabels(cr, op)
	deadline := int64((lifetime + deadlineSlack).Seconds())
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ManagerJobName(op, suffix),
			Namespace: cr.Namespace,
			Labels:    labels,
			// Not a controller ref, so the CronJob controller ignores it, but it's
			// still garbage-collected with the template.
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "batch/v1",
				Kind:               "CronJob",
				Name:               template.Name,
				UID:                template.UID,
				Controller:         new(false),
				BlockOwnerDeletion: new(false),
			}},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            new(int32(0)),
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: new(managerTTLSeconds),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       pod,
			},
		},
	}, nil
}
