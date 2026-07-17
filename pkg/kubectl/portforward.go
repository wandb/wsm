package kubectl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// PortForwardSession is a live port-forward to a pod backing a Service. Close it to tear it down.
type PortForwardSession struct {
	LocalPort int
	stopCh    chan struct{}
	errCh     chan error
}

// Done receives the terminal result of the forward: nil on clean shutdown (Close), or the error that
// tore it down (e.g. the pod died). Callers select on it alongside their own cancellation.
func (s *PortForwardSession) Done() <-chan error {
	return s.errCh
}

// Close stops the forward. Safe to call more than once.
func (s *PortForwardSession) Close() error {
	if s == nil || s.stopCh == nil {
		return nil
	}
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
	return nil
}

// PortForward resolves the Service to a ready backing pod, maps remotePort to that pod's target port,
// and establishes a native (client-go) port-forward. It blocks until the forward is ready, then returns
// a live session. localPort 0 lets the OS choose. Callers pass an explicit config/clientset (e.g. from
// GetClientset) so this stays a pure library function rather than depending on the package singleton.
func PortForward(ctx context.Context, cfg *rest.Config, cs *kubernetes.Clientset, namespace, service string, remotePort, localPort int) (*PortForwardSession, error) {
	if remotePort < 1 || remotePort > 65535 {
		return nil, fmt.Errorf("invalid remote port %d (must be 1-65535)", remotePort)
	}
	if localPort < 0 || localPort > 65535 {
		return nil, fmt.Errorf("invalid local port %d (must be 0-65535)", localPort)
	}

	podName, podPort, err := pickPodAndPort(ctx, cs, namespace, service, int32(remotePort))
	if err != nil {
		return nil, err
	}

	reqURL := cs.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(podName).SubResource("portforward").URL()
	dialer, err := portForwardDialer(cfg, reqURL)
	if err != nil {
		return nil, fmt.Errorf("failed to build port-forward transport: %w", err)
	}

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	fw, err := portforward.New(dialer, []string{fmt.Sprintf("%d:%d", localPort, podPort)}, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return nil, err
	}

	errCh := make(chan error, 1)
	go func() { errCh <- fw.ForwardPorts() }()

	select {
	case <-readyCh:
	case err := <-errCh:
		return nil, fmt.Errorf("port-forward to %s/%s failed: %w", namespace, service, err)
	case <-time.After(15 * time.Second):
		close(stopCh)
		return nil, fmt.Errorf("timed out establishing port-forward to %s/%s", namespace, service)
	}

	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stopCh)
		return nil, fmt.Errorf("could not determine local port for %s/%s", namespace, service)
	}
	return &PortForwardSession{LocalPort: int(ports[0].Local), stopCh: stopCh, errCh: errCh}, nil
}

// portForwardDialer prefers WebSocket (SPDY-over-WebSocket) and falls back to plain SPDY on upgrade
// failure, matching kubectl — so it works against both classic and WebSocket-only API servers.
func portForwardDialer(cfg *rest.Config, reqURL *url.URL) (httpstream.Dialer, error) {
	roundTripper, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return nil, err
	}
	spdyDialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost, reqURL)
	wsDialer, err := portforward.NewSPDYOverWebsocketDialer(reqURL, cfg)
	if err != nil {
		return nil, err
	}
	return portforward.NewFallbackDialer(wsDialer, spdyDialer, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	}), nil
}

func pickPodAndPort(ctx context.Context, cs *kubernetes.Clientset, namespace, service string, remotePort int32) (string, int32, error) {
	svc, err := cs.CoreV1().Services(namespace).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return "", 0, fmt.Errorf("service %q not found in namespace %q: %w", service, namespace, err)
	}
	if len(svc.Spec.Selector) == 0 {
		return "", 0, fmt.Errorf("service %q has no selector to resolve pods", service)
	}

	var targetPortName string
	var targetPortNum int32
	var found bool
	var available []int32
	for _, p := range svc.Spec.Ports {
		available = append(available, p.Port)
		if p.Port == remotePort {
			if p.TargetPort.Type == intstr.Int {
				targetPortNum = p.TargetPort.IntVal
			} else {
				targetPortName = p.TargetPort.StrVal
			}
			found = true
			break
		}
	}
	if !found {
		return "", 0, fmt.Errorf("service %q in namespace %q has no port %d (available: %v)", service, namespace, remotePort, available)
	}

	sel := labels.SelectorFromSet(svc.Spec.Selector).String()
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return "", 0, fmt.Errorf("failed to list pods for service %q: %w", service, err)
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase != corev1.PodRunning || !podReady(pod) {
			continue
		}
		port := targetPortNum
		if targetPortName != "" {
			resolved, ok := namedContainerPort(pod, targetPortName)
			if !ok {
				continue
			}
			port = resolved
		}
		if port == 0 {
			continue
		}
		return pod.Name, port, nil
	}
	return "", 0, fmt.Errorf("no ready pod backing service %q in namespace %q", service, namespace)
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func namedContainerPort(pod *corev1.Pod, name string) (int32, bool) {
	for _, c := range pod.Spec.Containers {
		for _, p := range c.Ports {
			if p.Name == name {
				return p.ContainerPort, true
			}
		}
	}
	return 0, false
}
