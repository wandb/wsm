package kubectl

import (
	"bufio"
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// FollowPodLogs streams a container's logs, calling onLine for each line until
// the container exits or ctx is cancelled.
func FollowPodLogs(ctx context.Context, client kubernetes.Interface, namespace, pod, container string, onLine func(string)) error {
	stream, err := client.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{Container: container, Follow: true}).Stream(ctx)
	if err != nil {
		return fmt.Errorf("stream logs for pod %s/%s: %w", namespace, pod, err)
	}
	defer func() { _ = stream.Close() }()

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		onLine(scanner.Text())
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("read logs for pod %s/%s: %w", namespace, pod, err)
	}
	return ctx.Err()
}
