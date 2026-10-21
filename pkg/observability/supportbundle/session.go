package supportbundle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	v2 "github.com/wandb/operator/api/v2"
	"github.com/wandb/wsm/pkg/kubectl"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Variables so tests can shorten them.
var (
	startupTimeout = 5 * time.Minute
	pollInterval   = 2 * time.Second
	cleanupTimeout = 30 * time.Second
	slowStart      = 30 * time.Second
)

// managerLifetime bounds each manager pod; it sleeps this long, then exits.
const managerLifetime = 15 * time.Minute

// Executor runs a command in a pod's container.
type Executor interface {
	Exec(ctx context.Context, namespace, pod, container string, command []string, stdout, stderr io.Writer) error
}

// KubeExecutor execs through the Kubernetes API.
type KubeExecutor struct {
	Config *rest.Config
	Client kubernetes.Interface
}

func (e KubeExecutor) Exec(ctx context.Context, namespace, pod, container string, command []string, stdout, stderr io.Writer) error {
	return kubectl.ExecInPod(ctx, e.Config, e.Client, namespace, pod, container, command, stdout, stderr)
}

// SessionOptions configures StartSession.
type SessionOptions struct {
	Lifetime time.Duration
	Progress func(string)
	Suffix   string // tests only
}

// Session is a running manager pod that bundle-manager commands exec into.
type Session struct {
	client    kubernetes.Interface
	executor  Executor
	Job       *batchv1.Job
	Pod       string
	Container string
	Image     string
}

// ErrBundleManagerMissing means the template's image has no bundle-manager.
var ErrBundleManagerMissing = errors.New("bundle-manager not found in image")

// StartSession creates a manager Job and waits for its container. On failure
// the Job is deleted, since the caller gets no session to Close.
func StartSession(ctx context.Context, client kubernetes.Interface, executor Executor, cr *v2.WeightsAndBiases, template *batchv1.CronJob, op Operation, opts SessionOptions) (*Session, error) {
	suffix := opts.Suffix
	if suffix == "" {
		var err error
		if suffix, err = NewSuffix(); err != nil {
			return nil, err
		}
	}
	job, err := BuildManagerJob(cr, template, op, opts.Lifetime, suffix)
	if err != nil {
		return nil, err
	}
	created, err := client.BatchV1().Jobs(cr.Namespace).Create(ctx, job, metav1.CreateOptions{FieldManager: "wsm"})
	if err != nil {
		return nil, fmt.Errorf("create manager Job %s in namespace: %s: %w", job.Name, cr.Namespace, err)
	}
	session := &Session{
		client:    client,
		executor:  executor,
		Job:       created,
		Container: job.Spec.Template.Spec.Containers[0].Name,
		Image:     job.Spec.Template.Spec.Containers[0].Image,
	}
	pod, err := session.waitForContainer(ctx, opts.Progress)
	if err != nil {
		if ctx.Err() != nil {
			err = errors.New("interrupted")
		}
		if cleanupErr := session.Close(); cleanupErr != nil {
			return nil, errors.Join(err, cleanupErr)
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("interrupted - removed manager Job %s", created.Name)
		}
		return nil, err
	}
	session.Pod = pod
	return session, nil
}

func (s *Session) waitForContainer(ctx context.Context, progress func(string)) (string, error) {
	var (
		podName string
		last    string
		started = time.Now()
	)
	report := func(msg string) {
		if msg != last {
			last = msg
			if progress != nil {
				progress(msg)
			}
		}
	}
	err := wait.PollUntilContextTimeout(ctx, pollInterval, startupTimeout, true, func(ctx context.Context) (bool, error) {
		pods, err := s.client.CoreV1().Pods(s.Job.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: batchv1.JobNameLabel + "=" + s.Job.Name,
		})
		if err != nil {
			return false, fmt.Errorf("list manager pods: %w", err)
		}
		if len(pods.Items) == 0 {
			report(s.noPodReason(ctx, time.Since(started)))
			return false, nil
		}
		pod := &pods.Items[0]
		state := containerState(pod, s.Container)
		if state.terminal != nil {
			return false, fmt.Errorf("manager %w before wsm could use it", state.terminal)
		}
		if state.ready {
			podName = pod.Name
			return true, nil
		}
		report(state.progress)
		return false, nil
	})
	if err != nil {
		if wait.Interrupted(err) && ctx.Err() == nil {
			return "", fmt.Errorf("manager pod for %s did not start within %s (last state: %s)", s.Image, startupTimeout, last)
		}
		return "", err
	}
	return podName, nil
}

// noPodReason explains why the Job has no pod yet, from its FailedCreate
// events or, after slowStart, the likely culprit.
func (s *Session) noPodReason(ctx context.Context, waited time.Duration) string {
	events, err := s.client.CoreV1().Events(s.Job.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.kind=Job,involvedObject.name=" + s.Job.Name,
	})
	if err == nil {
		for i := len(events.Items) - 1; i >= 0; i-- {
			if e := events.Items[i]; e.Reason == "FailedCreate" && e.InvolvedObject.Name == s.Job.Name {
				return "Kubernetes can't create the manager pod: " + e.Message
			}
		}
	}
	if waited > slowStart {
		return fmt.Sprintf("Kubernetes hasn't created the manager pod after %s - check that kube-controller-manager is healthy", waited.Round(time.Second))
	}
	return "waiting for the manager pod to be created"
}

type startupState struct {
	ready    bool
	progress string
	terminal error
}

// containerState decides whether the container can take exec yet. Only
// states that can't recover are terminal.
func containerState(pod *corev1.Pod, container string) startupState {
	switch pod.Status.Phase {
	case corev1.PodFailed, corev1.PodSucceeded:
		return startupState{terminal: fmt.Errorf("pod %s ended (%s): %s", pod.Name, pod.Status.Phase, pod.Status.Message)}
	}
	var status *corev1.ContainerStatus
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == container {
			status = &pod.Status.ContainerStatuses[i]
		}
	}
	if status == nil {
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
				return startupState{progress: fmt.Sprintf("waiting for a node (%s): %s", c.Reason, c.Message)}
			}
		}
		return startupState{progress: "waiting for the container"}
	}
	if t := status.State.Terminated; t != nil {
		return startupState{terminal: fmt.Errorf("container exited (%s, code %d)", t.Reason, t.ExitCode)}
	}
	if w := status.State.Waiting; w != nil {
		switch w.Reason {
		case "InvalidImageName":
			return startupState{terminal: fmt.Errorf("invalid image %s: %s", status.Image, w.Message)}
		case "ErrImagePull", "ImagePullBackOff":
			return startupState{progress: fmt.Sprintf("pulling image %s (%s), still retrying", status.Image, w.Reason)}
		case "ContainerCreating", "PodInitializing", "":
			return startupState{progress: "starting the container"}
		default:
			return startupState{progress: fmt.Sprintf("%s: %s - still waiting", w.Reason, w.Message)}
		}
	}
	if pod.Status.Phase == corev1.PodRunning && status.State.Running != nil && status.Started != nil && *status.Started {
		return startupState{ready: true}
	}
	return startupState{progress: "starting the container"}
}

// BundleManager runs `bundle-manager args...` in the session's container.
func (s *Session) BundleManager(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	command := append([]string{"bundle-manager"}, args...)
	var captured strings.Builder
	var errWriter io.Writer = &limitedWriter{w: &captured, n: 64 << 10}
	if stderr != nil {
		errWriter = io.MultiWriter(stderr, errWriter)
	}
	err := s.executor.Exec(ctx, s.Job.Namespace, s.Pod, s.Container, command, stdout, errWriter)
	if err == nil {
		return nil
	}
	if missingExecutable(err.Error() + " " + captured.String()) {
		return fmt.Errorf("%w %s: %v", ErrBundleManagerMissing, s.Image, err)
	}
	if code, ok := kubectl.ExitCode(err); ok {
		return fmt.Errorf("bundle-manager %s exited with code %d: %s: %w", strings.Join(args, " "), code, strings.TrimSpace(captured.String()), err)
	}
	return err
}

func missingExecutable(text string) bool {
	return strings.Contains(text, "executable file not found") ||
		strings.Contains(text, `exec: "bundle-manager"`)
}

// Close deletes the manager Job with its own context, so it runs after Ctrl+C.
func (s *Session) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	propagation := metav1.DeletePropagationBackground
	err := s.client.BatchV1().Jobs(s.Job.Namespace).Delete(ctx, s.Job.Name, metav1.DeleteOptions{PropagationPolicy: &propagation})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete manager Job %s (it expires on its own): %w", s.Job.Name, err)
	}
	return nil
}

// limitedWriter keeps the first n bytes and discards the rest.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		keep := p
		if len(keep) > l.n {
			keep = keep[:l.n]
		}
		written, err := l.w.Write(keep)
		l.n -= written
		if err != nil {
			return written, err
		}
	}
	return len(p), nil
}
