package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

// portForwardAndOpen runs `kubectl port-forward` to a ClusterIP service and,
// optionally, opens the resulting localhost URL in a browser after a short
// delay. It blocks until the port-forward process exits (Ctrl+C). Shared by the
// `wsm telemetry grafana` and `wsm telemetry victoria` helpers.
func portForwardAndOpen(kubeContext, namespace, service string, localPort, remotePort int, urlPath string, openInBrowser bool) error {
	url := fmt.Sprintf("http://localhost:%d%s", localPort, urlPath)

	args := []string{}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	args = append(args,
		"port-forward",
		"-n", namespace,
		"service/"+service,
		fmt.Sprintf("%d:%d", localPort, remotePort),
	)

	fmt.Printf("→ Forwarding %s/%s to %s (Ctrl+C to stop)\n", namespace, service, url)
	if openInBrowser {
		time.AfterFunc(500*time.Millisecond, func() {
			_ = openBrowser(url)
		})
	}

	portForward := exec.Command("kubectl", args...)
	portForward.Stderr = os.Stderr
	portForward.Stdout = os.Stdout
	portForward.Stdin = os.Stdin
	return portForward.Run()
}
