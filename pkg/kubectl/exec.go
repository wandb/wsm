package kubectl

import (
	"context"
	"errors"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// ExecInPod runs command in a container without a TTY, so binary stdout stays byte-exact.
func ExecInPod(ctx context.Context, cfg *rest.Config, cs kubernetes.Interface, namespace, pod, container string, command []string, stdout, stderr io.Writer) error {
	req := cs.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdout:    stdout != nil,
			Stderr:    stderr != nil,
			TTY:       false,
		}, scheme.ParameterCodec)

	websocketExec, err := remotecommand.NewWebSocketExecutor(cfg, "GET", req.URL().String())
	if err != nil {
		return fmt.Errorf("create websocket executor: %w", err)
	}
	spdyExec, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("create spdy executor: %w", err)
	}
	// Same fallback rule kubectl uses for API servers without websocket exec.
	executor, err := remotecommand.NewFallbackExecutor(websocketExec, spdyExec, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return fmt.Errorf("create executor: %w", err)
	}
	return executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: stdout, Stderr: stderr})
}

// ExitCode returns the remote exit status; ok is false for transport errors.
func ExitCode(err error) (code int, ok bool) {
	var exitErr utilexec.ExitError
	if errors.As(err, &exitErr) && exitErr.Exited() {
		return exitErr.ExitStatus(), true
	}
	return 0, false
}
