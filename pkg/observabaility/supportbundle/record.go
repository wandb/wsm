package supportbundle

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Phase is the user-facing state of a bundle.
type Phase string

const (
	PhasePending   Phase = "Pending"
	PhaseRunning   Phase = "Running"
	PhaseReady     Phase = "Ready"
	PhaseCompleted Phase = "Completed" // Job succeeded but no artifact was reported
	PhaseFailed    Phase = "Failed"
	PhaseExpired   Phase = "Expired"
	PhaseDeleted   Phase = "Deleted"
	PhaseUnknown   Phase = "Unknown"
	PhaseCancelled Phase = "Cancelled"
)

// ResultUnavailable explains a successful Job without a v1 result.
const ResultUnavailable = "artifact location reporting unavailable: the Lumen image did not write a v1 result to " + ResultPathEnv

// Record is the durable, credential-free state of one bundle, stored in the
// wsm-sb-<id> ConfigMap. It outlives the Job (which has a TTL).
type Record struct {
	ID             string    `json:"id"`
	Installation   string    `json:"installation"`
	Created        time.Time `json:"created"`
	WindowStart    time.Time `json:"windowStart"`
	WindowEnd      time.Time `json:"windowEnd"`
	Expires        time.Time `json:"expires"`
	JobName        string    `json:"jobName"`
	Image          string    `json:"image"`
	ServiceAccount string    `json:"serviceAccount"`
	Sources        []string  `json:"sources"`
	Health         []string  `json:"health,omitempty"`

	Phase     Phase      `json:"phase"`
	Message   string     `json:"message,omitempty"`
	Completed *time.Time `json:"completed,omitempty"`
	Result    *Result    `json:"result,omitempty"`
}

const recordKey = "record.json"

// Terminal reports whether the run has finished and its outcome is stored.
func (r *Record) Terminal() bool {
	switch r.Phase {
	case PhaseReady, PhaseCompleted, PhaseFailed, PhaseDeleted, PhaseUnknown, PhaseCancelled:
		return true
	}
	return false
}

// DisplayPhase overlays expiry onto the stored phase.
func (r *Record) DisplayPhase(now time.Time) Phase {
	if r.Phase != PhaseDeleted && !r.Expires.IsZero() && now.After(r.Expires) && r.Terminal() {
		return PhaseExpired
	}
	return r.Phase
}

// Artifact returns the reported artifact, if any.
func (r *Record) Artifact() *Artifact {
	if r.Result == nil {
		return nil
	}
	return r.Result.Artifact
}

// NewRecord builds the initial record for a run.
func NewRecord(inst *Installation, opts RunOptions) *Record {
	health := []string{fmt.Sprintf("installation ready=%t", inst.Ready), fmt.Sprintf("object store ready=%t", inst.ObjectStoreReady)}
	if inst.Telemetry != nil {
		health = append(health, fmt.Sprintf("telemetry (%s) ready=%t", inst.Telemetry.Mode, inst.Telemetry.Ready))
	}
	health = append(health, inst.Conditions...)
	created := opts.Created.UTC().Truncate(time.Second)
	return &Record{
		ID:             opts.ID,
		Installation:   inst.Namespace + "/" + inst.Name,
		Created:        created,
		WindowStart:    created.Add(-opts.Window),
		WindowEnd:      created,
		Expires:        created.Add(opts.Retention),
		JobName:        jobName(opts.ID),
		Image:          opts.Image,
		ServiceAccount: opts.ServiceAccount,
		Sources:        opts.Sources,
		Health:         health,
		Phase:          PhasePending,
	}
}

// ToConfigMap encodes the record.
func (r *Record) ToConfigMap(namespace, name string) (*corev1.ConfigMap, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      recordName(r.ID),
			Namespace: namespace,
			Labels:    bundleLabels(namespace, name, r.ID, roleRecord),
		},
		Data: map[string]string{recordKey: string(data)},
	}, nil
}

// RecordFromConfigMap decodes a record, verifying wsm ownership.
func RecordFromConfigMap(cm *corev1.ConfigMap, namespace, name string) (*Record, error) {
	if !ownedBy(cm.Labels, installationKey(namespace, name)) || cm.Labels[LabelRole] != roleRecord {
		return nil, fmt.Errorf("ConfigMap %s/%s is not a wsm support-bundle record for this installation", cm.Namespace, cm.Name)
	}
	var r Record
	if err := json.Unmarshal([]byte(cm.Data[recordKey]), &r); err != nil {
		return nil, fmt.Errorf("decode support-bundle record %s/%s: %w", cm.Namespace, cm.Name, err)
	}
	return &r, nil
}

// Observation is the live state derived from the Job and its Pod.
type Observation struct {
	Phase    Phase
	Message  string
	Problems []string // actionable startup problems (image pull, config, scheduling)
	Result   *Result
	Done     bool
}

// Observe maps Job + Pod state to a phase. It never infers an artifact
// location; that only comes from a validated v1 result.
func Observe(job *batchv1.Job, pod *corev1.Pod) Observation {
	if job == nil {
		return Observation{Phase: PhaseUnknown, Message: "the Job no longer exists and its outcome was not recorded", Done: true}
	}

	terminated := containerTerminated(pod)
	result, resultErr := (*Result)(nil), error(nil)
	if terminated != nil {
		result, resultErr = ParseResult(terminated.Message)
	}

	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			obs := Observation{Phase: PhaseCompleted, Done: true, Result: result, Message: ResultUnavailable}
			switch {
			case resultErr != nil:
				obs.Message = "collector result rejected: " + resultErr.Error()
				obs.Result = nil
			case result != nil && result.Artifact != nil:
				obs.Phase, obs.Message = PhaseReady, ""
			case result != nil && result.Failure != nil:
				obs.Phase, obs.Message = PhaseFailed, failureText(result.Failure)
			}
			return obs
		case batchv1.JobFailed:
			obs := Observation{Phase: PhaseFailed, Done: true}
			switch {
			case result != nil && result.Failure != nil:
				obs.Result, obs.Message = result, failureText(result.Failure)
			case c.Reason == "DeadlineExceeded":
				obs.Message = "Timeout: collection exceeded the Job deadline (raise --timeout)"
			case terminated != nil:
				obs.Message = fmt.Sprintf("Collection: collector exited with code %d (%s)", terminated.ExitCode, terminated.Reason)
			default:
				obs.Message = fmt.Sprintf("Workload: %s %s", c.Reason, c.Message)
			}
			if resultErr != nil {
				obs.Problems = append(obs.Problems, "collector result rejected: "+resultErr.Error())
			}
			return obs
		}
	}

	if pod == nil {
		return Observation{Phase: PhasePending, Message: "waiting for the collector pod to be created"}
	}
	if terminated != nil {
		return Observation{Phase: PhaseRunning, Message: "collector exited; waiting for the Job to report its outcome"}
	}
	obs := Observation{Phase: PhasePending, Message: "waiting for the collector pod to start"}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			obs.Problems = append(obs.Problems, "unschedulable: "+c.Message)
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != containerName {
			continue
		}
		if w := cs.State.Waiting; w != nil {
			switch w.Reason {
			case "ErrImagePull", "ImagePullBackOff", "InvalidImageName":
				obs.Problems = append(obs.Problems, fmt.Sprintf("cannot pull %s: %s (check --image-registry / --image-pull-secret)", pod.Spec.Containers[0].Image, w.Reason))
			case "CreateContainerConfigError", "CreateContainerError":
				obs.Problems = append(obs.Problems, fmt.Sprintf("%s: %s", w.Reason, w.Message))
			}
		}
		if cs.State.Running != nil {
			obs.Phase, obs.Message = PhaseRunning, "collecting diagnostics"
		}
	}
	return obs
}

func containerTerminated(pod *corev1.Pod) *corev1.ContainerStateTerminated {
	if pod == nil {
		return nil
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == containerName && cs.State.Terminated != nil {
			return cs.State.Terminated
		}
	}
	return nil
}

func failureText(f *Failure) string {
	parts := []string{f.Stage + ": " + f.Reason}
	if f.Message != "" {
		parts = append(parts, f.Message)
	}
	return strings.Join(parts, " — ")
}

// Apply folds an observation into the record. It returns true if the record
// changed and should be persisted.
func (r *Record) Apply(obs Observation, now time.Time) bool {
	if r.Terminal() {
		return false
	}
	changed := r.Phase != obs.Phase || r.Message != obs.Message
	r.Phase, r.Message = obs.Phase, obs.Message
	if obs.Done {
		r.Result = obs.Result
		t := now.UTC().Truncate(time.Second)
		r.Completed = &t
		changed = true
	}
	return changed
}
