package supportbundle

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

var userinfoPattern = regexp.MustCompile(`://[^/\s@]+@`)

// Watch polls the bundle until it reaches a terminal phase or ctx is
// cancelled, printing phase changes, startup problems and collector log
// messages. Cancelling ctx only stops watching; the Job keeps running.
func (m *Manager) Watch(ctx context.Context, id string, out io.Writer, interval time.Duration) (*Record, error) {
	var (
		lastPhase   Phase
		lastMessage string
		seen        = map[string]bool{}
		following   bool
		rec         *Record
	)
	err := wait.PollUntilContextCancel(ctx, interval, true, func(ctx context.Context) (bool, error) {
		var obs Observation
		var err error
		rec, obs, err = m.Get(ctx, id)
		if err != nil {
			return false, err
		}
		if obs.Phase != lastPhase || obs.Message != lastMessage {
			lastPhase, lastMessage = obs.Phase, obs.Message
			_, _ = fmt.Fprintf(out, "%s %s", time.Now().UTC().Format("15:04:05"), obs.Phase)
			if obs.Message != "" {
				_, _ = fmt.Fprintf(out, ": %s", obs.Message)
			}
			_, _ = fmt.Fprintln(out)
		}
		for _, p := range obs.Problems {
			if !seen[p] {
				seen[p] = true
				_, _ = fmt.Fprintf(out, "  ⚠ %s\n", p)
			}
		}
		if obs.Phase == PhaseRunning && !following {
			following = true
			if pod, err := m.CollectorPod(ctx, rec); err == nil && pod != "" {
				go m.followLogs(ctx, pod, out)
			}
		}
		return rec.Terminal(), nil
	})
	return rec, err
}

// followLogs prints the msg field of Lumen's JSON log lines. Logs are shown
// for progress only; nothing is derived from them.
func (m *Manager) followLogs(ctx context.Context, pod string, out io.Writer) {
	stream, err := m.Client.CoreV1().Pods(m.Namespace).GetLogs(pod, &corev1.PodLogOptions{Container: containerName, Follow: true}).Stream(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(out, "  (collector logs unavailable: %v)\n", err)
		return
	}
	defer func() { _ = stream.Close() }()
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if line := formatLogLine(scanner.Bytes()); line != "" {
			_, _ = fmt.Fprintf(out, "  · %s\n", line)
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		_, _ = fmt.Fprintf(out, "  (collector log stream ended: %v)\n", err)
	}
}

func formatLogLine(raw []byte) string {
	var entry struct {
		Level string `json:"level"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil || entry.Msg == "" {
		return ""
	}
	msg := userinfoPattern.ReplaceAllString(entry.Msg, "://[redacted]@")
	if level := strings.ToUpper(entry.Level); level != "" && level != "INFO" {
		return level + " " + msg
	}
	return msg
}
