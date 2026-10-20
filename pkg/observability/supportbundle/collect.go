package supportbundle

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	v2 "github.com/wandb/operator/api/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

// Labels the operator puts on the collector template and its Jobs.
const (
	WandbNameLabel      = "weightsandbiases.apps.wandb.com/name"
	WandbNamespaceLabel = "weightsandbiases.apps.wandb.com/namespace"
	WandbComponentLabel = "weightsandbiases.apps.wandb.com/component"
	CollectorComponent  = "support-bundle"
)

const specArgPrefix = "--support-bundle-spec="

func selectorLabels(cr *v2.WeightsAndBiases) map[string]string {
	return map[string]string{
		WandbComponentLabel: CollectorComponent,
		WandbNameLabel:      cr.Name,
		WandbNamespaceLabel: cr.Namespace,
	}
}

// Template returns the operator's collector CronJob for a CR, or why it can't be used.
func Template(ctx context.Context, client kubernetes.Interface, cr *v2.WeightsAndBiases) (*batchv1.CronJob, error) {
	if enabled := cr.Spec.SupportBundle.Enabled; enabled == nil || !*enabled {
		return nil, fmt.Errorf("support bundles are disabled on namespace: %s and instance: %s; run `wsm support-bundle --enabled=true`", cr.Namespace, cr.Name)
	}
	status := cr.Status.SupportBundle
	if status == nil {
		return nil, fmt.Errorf("WeightsAndBiases namespace: %s and instance: %s reports no support-bundle status; the operator predates support bundles or hasn't reconciled yet", cr.Namespace, cr.Name)
	}
	if !status.Ready || status.CronJob == "" {
		return nil, fmt.Errorf("the support-bundle template for namespace: %s and instance: %s is not ready yet", cr.Namespace, cr.Name)
	}
	cronJob, err := client.BatchV1().CronJobs(cr.Namespace).Get(ctx, status.CronJob, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("support-bundle CronJob %s in namespace: %s does not exist", status.CronJob, cr.Namespace)
	}
	if err != nil {
		return nil, fmt.Errorf("get support-bundle CronJob %s in namespace: %s: %w", status.CronJob, cr.Namespace, err)
	}
	if !labels.SelectorFromSet(selectorLabels(cr)).Matches(labels.Set(cronJob.Spec.JobTemplate.Labels)) {
		return nil, fmt.Errorf("CronJob %s in namespace: %s is not the support-bundle template for namespace: %s and instance: %s", cronJob.Name, cr.Namespace, cr.Namespace, cr.Name)
	}
	return cronJob, nil
}

// Collections lists the collection Jobs for a CR, finished or not.
func Collections(ctx context.Context, client kubernetes.Interface, cr *v2.WeightsAndBiases) ([]batchv1.Job, error) {
	jobs, err := client.BatchV1().Jobs(cr.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(selectorLabels(cr)).String(),
	})
	if err != nil {
		return nil, fmt.Errorf("list support-bundle Jobs in %s: %w", cr.Namespace, err)
	}
	return jobs.Items, nil
}

// RunningCollections returns the unfinished collection Jobs for a CR.
func RunningCollections(ctx context.Context, client kubernetes.Interface, cr *v2.WeightsAndBiases) ([]batchv1.Job, error) {
	jobs, err := Collections(ctx, client, cr)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(jobs, func(job batchv1.Job) bool { return jobFinished(&job) }), nil
}

// Running returns the name of an unfinished collection Job for a CR, or "".
func Running(ctx context.Context, client kubernetes.Interface, cr *v2.WeightsAndBiases) (string, error) {
	jobs, err := RunningCollections(ctx, client, cr)
	if err != nil || len(jobs) == 0 {
		return "", err
	}
	return jobs[0].Name, nil
}

// NewCollectionJob builds a Job from the collector template the way
// `kubectl create job --from=cronjob/...` does, applying opts.
func NewCollectionJob(cronJob *batchv1.CronJob, name string, opts CollectOptions) (*batchv1.Job, error) {
	template := cronJob.Spec.JobTemplate
	annotations := map[string]string{"cronjob.kubernetes.io/instantiate": "manual"}
	for k, v := range template.Annotations {
		annotations[k] = v
	}
	job := &batchv1.Job{
		APIVersion: "batch/v1", Kind: "Job",
		Name:            name,
		Namespace:       cronJob.Namespace,
		Labels:          template.Labels,
		Annotations:     annotations,
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cronJob, batchv1.SchemeGroupVersion.WithKind("CronJob"))},
		Spec:            *template.Spec.DeepCopy(),
	}
	if err := setPreset(job, opts.preset()); err != nil {
		return nil, err
	}
	return job, nil
}

// setPreset points the collector's --support-bundle-spec at <preset>.yaml.
func setPreset(job *batchv1.Job, preset Preset) error {
	for c := range job.Spec.Template.Spec.Containers {
		args := job.Spec.Template.Spec.Containers[c].Args
		for i, arg := range args {
			if spec, ok := strings.CutPrefix(arg, specArgPrefix); ok {
				args[i] = specArgPrefix + path.Join(path.Dir(spec), string(preset)+".yaml")
				return nil
			}
		}
	}
	return fmt.Errorf("support-bundle template has no %s argument", strings.TrimSuffix(specArgPrefix, "="))
}

// Create starts a collection. The returned Job's name is the bundle ID.
func Create(ctx context.Context, client kubernetes.Interface, cr *v2.WeightsAndBiases, opts CollectOptions) (*batchv1.Job, error) {
	cronJob, err := Template(ctx, client, cr)
	if err != nil {
		return nil, err
	}
	running, err := Running(ctx, client, cr)
	if err != nil {
		return nil, err
	}
	if running != "" {
		return nil, fmt.Errorf("support bundle %s is still collecting; wait for it to finish", running)
	}
	suffix, err := NewSuffix()
	if err != nil {
		return nil, err
	}
	job, err := NewCollectionJob(cronJob, "support-bundle-"+suffix, opts)
	if err != nil {
		return nil, err
	}
	created, err := client.BatchV1().Jobs(cr.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create support-bundle Job: %w", err)
	}

	// Two creates can both pass the Running check. The older Job wins; the
	// newer one deletes itself. The check is best-effort: if it can't list,
	// keep the Job so the caller still gets its ID.
	winner, err := olderCollection(ctx, client, cr, created)
	if err != nil || winner == "" {
		return created, nil
	}
	propagation := metav1.DeletePropagationBackground
	if err := client.BatchV1().Jobs(cr.Namespace).Delete(ctx, created.Name, metav1.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("support bundle %s started at the same time and is still collecting, and the duplicate %s couldn't be removed; run `wsm support-bundle cancel %s`: %w", winner, created.Name, created.Name, err)
	}
	return nil, fmt.Errorf("support bundle %s started at the same time and is still collecting; wait for it to finish", winner)
}

// olderCollection returns another unfinished collection created before job, or "".
func olderCollection(ctx context.Context, client kubernetes.Interface, cr *v2.WeightsAndBiases, job *batchv1.Job) (string, error) {
	jobs, err := RunningCollections(ctx, client, cr)
	if err != nil {
		return "", err
	}
	for _, other := range jobs {
		if other.Name == job.Name {
			continue
		}
		created, mine := other.CreationTimestamp.Time, job.CreationTimestamp.Time
		if created.Before(mine) || (created.Equal(mine) && other.Name < job.Name) {
			return other.Name, nil
		}
	}
	return "", nil
}

// Cancel stops a running collection by deleting its Job and pod; bundles
// already uploaded are kept. It reports false when the collection had already finished.
func Cancel(ctx context.Context, client kubernetes.Interface, cr *v2.WeightsAndBiases, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("a support bundle ID is required")
	}
	job, err := collectionJob(ctx, client, cr, id)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, fmt.Errorf("support bundle %s not found in namespace: %s", id, cr.Namespace)
	}
	if jobFinished(job) {
		return false, nil
	}
	propagation := metav1.DeletePropagationBackground
	if err := client.BatchV1().Jobs(cr.Namespace).Delete(ctx, id, metav1.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("cancel support bundle %s: %w", id, err)
	}
	return true, nil
}

// collectionJob returns the collection Job for a bundle ID, or nil if it's gone.
func collectionJob(ctx context.Context, client kubernetes.Interface, cr *v2.WeightsAndBiases, id string) (*batchv1.Job, error) {
	job, err := client.BatchV1().Jobs(cr.Namespace).Get(ctx, id, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get support bundle %s: %w", id, err)
	}
	if !labels.SelectorFromSet(selectorLabels(cr)).Matches(labels.Set(job.Labels)) {
		return nil, fmt.Errorf("job %s is not a support bundle for namespace: %s and instance: %s", id, cr.Namespace, cr.Name)
	}
	return job, nil
}

// Wait polls a collection Job until it finishes. The template's deadline bounds it.
func Wait(ctx context.Context, client kubernetes.Interface, namespace, name string) (*batchv1.Job, error) {
	var job *batchv1.Job
	err := wait.PollUntilContextCancel(ctx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		var err error
		job, err = client.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("get support bundle %s in namespace: %s: %w", name, namespace, err)
		}
		return jobFinished(job), nil
	})
	if err != nil {
		return nil, err
	}
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return job, fmt.Errorf("support bundle %s failed: %s: %s", name, c.Reason, c.Message)
		}
	}
	return job, nil
}

func jobFinished(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
