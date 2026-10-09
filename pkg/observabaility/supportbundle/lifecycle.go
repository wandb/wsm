package supportbundle

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

// creationGrace is how long a record may exist without its Job before the
// run is considered abandoned (wsm exited between creating the two).
const creationGrace = 2 * time.Minute

// Manager operates on the support bundles of one installation.
type Manager struct {
	Client    kubernetes.Interface
	Namespace string
	Name      string
	Now       func() time.Time
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Create writes the record, spec ConfigMap and Job, in that order. The record
// owns the other two, so a partial create is cleaned up by deleting it.
func (m *Manager) Create(ctx context.Context, inst *Installation, opts RunOptions, specYAML []byte) (*Record, error) {
	rec := NewRecord(inst, opts)
	cm, err := rec.ToConfigMap(m.Namespace, m.Name)
	if err != nil {
		return nil, err
	}
	created, err := m.Client.CoreV1().ConfigMaps(m.Namespace).Create(ctx, cm, metav1.CreateOptions{FieldManager: fieldManager})
	if err != nil {
		return nil, apiError("create", "ConfigMap", m.Namespace, cm.Name, err)
	}

	spec := BuildSpecConfigMap(inst, opts, specYAML, created)
	if _, err := m.Client.CoreV1().ConfigMaps(m.Namespace).Create(ctx, spec, metav1.CreateOptions{FieldManager: fieldManager}); err != nil {
		return rec, apiError("create", "ConfigMap", m.Namespace, spec.Name, err)
	}
	job := BuildJob(inst, opts, created)
	if _, err := m.Client.BatchV1().Jobs(m.Namespace).Create(ctx, job, metav1.CreateOptions{FieldManager: fieldManager}); err != nil {
		return rec, apiError("create", "Job", m.Namespace, job.Name, err)
	}
	return rec, nil
}

// Get loads a record and refreshes it from the live Job/Pod.
func (m *Manager) Get(ctx context.Context, id string) (*Record, Observation, error) {
	if !ValidBundleID(id) {
		return nil, Observation{}, fmt.Errorf("invalid bundle ID %q (expected sb-xxxxxx)", id)
	}
	cm, err := m.Client.CoreV1().ConfigMaps(m.Namespace).Get(ctx, recordName(id), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, Observation{}, fmt.Errorf("bundle %s not found in %s/%s", id, m.Namespace, m.Name)
		}
		return nil, Observation{}, apiError("get", "ConfigMap", m.Namespace, recordName(id), err)
	}
	return m.sync(ctx, cm)
}

// List returns this installation's records, newest first.
func (m *Manager) List(ctx context.Context) ([]*Record, error) {
	cms, err := m.Client.CoreV1().ConfigMaps(m.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selectorFor(m.Namespace, m.Name, roleRecord)})
	if err != nil {
		return nil, apiError("list", "ConfigMaps", m.Namespace, "", err)
	}
	records := make([]*Record, 0, len(cms.Items))
	for i := range cms.Items {
		rec, _, err := m.sync(ctx, &cms.Items[i])
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Created.After(records[j].Created) })
	return records, nil
}

// sync observes the Job/Pod for a non-terminal record and persists the
// outcome so it survives the Job's TTL.
func (m *Manager) sync(ctx context.Context, cm *corev1.ConfigMap) (*Record, Observation, error) {
	rec, err := RecordFromConfigMap(cm, m.Namespace, m.Name)
	if err != nil {
		return nil, Observation{}, err
	}
	if rec.Terminal() {
		return rec, Observation{Phase: rec.Phase, Message: rec.Message, Result: rec.Result, Done: true}, nil
	}

	job, pod, err := m.jobAndPod(ctx, rec)
	if err != nil {
		return nil, Observation{}, err
	}
	if job == nil && m.now().Sub(rec.Created) < creationGrace {
		return rec, Observation{Phase: rec.Phase, Message: "waiting for the Job to be created"}, nil
	}
	obs := Observe(job, pod)
	if rec.Apply(obs, m.now()) {
		if err := m.saveRecord(ctx, rec); err != nil {
			return nil, Observation{}, err
		}
	}
	return rec, obs, nil
}

func (m *Manager) jobAndPod(ctx context.Context, rec *Record) (*batchv1.Job, *corev1.Pod, error) {
	job, err := m.Client.BatchV1().Jobs(m.Namespace).Get(ctx, rec.JobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, apiError("get", "Job", m.Namespace, rec.JobName, err)
	}
	if !ownedBy(job.Labels, installationKey(m.Namespace, m.Name)) {
		return nil, nil, fmt.Errorf("job %s/%s is not managed by wsm for this installation", m.Namespace, rec.JobName)
	}
	pods, err := m.Client.CoreV1().Pods(m.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + rec.JobName})
	if err != nil {
		return nil, nil, apiError("list", "Pods", m.Namespace, "", err)
	}
	var pod *corev1.Pod
	for i := range pods.Items {
		if pod == nil || pods.Items[i].CreationTimestamp.After(pod.CreationTimestamp.Time) {
			pod = &pods.Items[i]
		}
	}
	return job, pod, nil
}

// CollectorPod returns the current collector pod name, if any.
func (m *Manager) CollectorPod(ctx context.Context, rec *Record) (string, error) {
	_, pod, err := m.jobAndPod(ctx, rec)
	if err != nil || pod == nil {
		return "", err
	}
	return pod.Name, nil
}

func (m *Manager) saveRecord(ctx context.Context, rec *Record) error {
	desired, err := rec.ToConfigMap(m.Namespace, m.Name)
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, err := m.Client.CoreV1().ConfigMaps(m.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
		if err != nil {
			return apiError("get", "ConfigMap", m.Namespace, desired.Name, err)
		}
		cm.Data = desired.Data
		_, err = m.Client.CoreV1().ConfigMaps(m.Namespace).Update(ctx, cm, metav1.UpdateOptions{FieldManager: fieldManager})
		if err != nil && !apierrors.IsConflict(err) {
			return apiError("update", "ConfigMap", m.Namespace, desired.Name, err)
		}
		return err
	})
}

// MarkDeleted records that the bucket object was removed but keeps the record.
func (m *Manager) MarkDeleted(ctx context.Context, rec *Record) error {
	rec.Phase, rec.Message = PhaseDeleted, "bundle object deleted from the bucket"
	return m.saveRecord(ctx, rec)
}

// Cancel stops a running collection by deleting its Job (and so its pod) and
// records it as Cancelled. The record is kept; use Delete to remove it.
func (m *Manager) Cancel(ctx context.Context, rec *Record) error {
	if rec.Terminal() {
		return fmt.Errorf("bundle %s has already finished (%s); nothing to cancel", rec.ID, rec.Phase)
	}
	err := m.Client.BatchV1().Jobs(m.Namespace).Delete(ctx, rec.JobName, metav1.DeleteOptions{PropagationPolicy: new(metav1.DeletePropagationBackground)})
	if err != nil && !apierrors.IsNotFound(err) {
		return apiError("delete", "Job", m.Namespace, rec.JobName, err)
	}
	t := m.now().UTC().Truncate(time.Second)
	rec.Phase, rec.Message, rec.Completed = PhaseCancelled, "cancelled; the collector pod was stopped", &t
	return m.saveRecord(ctx, rec)
}

// ErrStillRunning is returned by Delete for a bundle that is still collecting;
// callers must Cancel it first.
var ErrStillRunning = errors.New("bundle is still collecting")

// DeleteRecord removes the record; its spec ConfigMap and Job are garbage
// collected through owner references.
func (m *Manager) DeleteRecord(ctx context.Context, rec *Record) error {
	err := m.Client.CoreV1().ConfigMaps(m.Namespace).Delete(ctx, recordName(rec.ID), metav1.DeleteOptions{PropagationPolicy: new(metav1.DeletePropagationBackground)})
	if err != nil && !apierrors.IsNotFound(err) {
		return apiError("delete", "ConfigMap", m.Namespace, recordName(rec.ID), err)
	}
	return nil
}

// ObjectDeleter removes a bundle object from the bucket.
type ObjectDeleter func(ctx context.Context, artifact *Artifact) error

// Delete removes a finished bundle: the bucket object if one was reported,
// then the record. A running bundle is refused (ErrStillRunning). If the
// object cannot be deleted the record is kept so the bundle is not lost track of.
func (m *Manager) Delete(ctx context.Context, rec *Record, deleteObject ObjectDeleter) error {
	if !rec.Terminal() {
		return fmt.Errorf("%s: %w", rec.ID, ErrStillRunning)
	}
	if art := rec.Artifact(); art != nil && rec.Phase != PhaseDeleted {
		if deleteObject == nil {
			return fmt.Errorf("bundle %s has an uploaded object at %s but no bucket client is available; record kept", rec.ID, art.Location)
		}
		if err := deleteObject(ctx, art); err != nil {
			return fmt.Errorf("delete bundle object %s (record kept): %w", art.Location, err)
		}
	}
	return m.DeleteRecord(ctx, rec)
}

// Expired returns terminal records past their expiry.
func (m *Manager) Expired(records []*Record) []*Record {
	var out []*Record
	now := m.now()
	for _, rec := range records {
		if rec.DisplayPhase(now) == PhaseExpired || (rec.Phase == PhaseDeleted && now.After(rec.Expires)) {
			out = append(out, rec)
		}
	}
	return out
}

// ActiveRuns returns records that are still pending or running.
func ActiveRuns(records []*Record) []*Record {
	var out []*Record
	for _, rec := range records {
		if !rec.Terminal() {
			out = append(out, rec)
		}
	}
	return out
}
