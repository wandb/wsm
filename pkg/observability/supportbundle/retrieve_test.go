package supportbundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	utilexec "k8s.io/client-go/util/exec"
)

const bundleLocation = "s3://bucket/agent/service-bundles/support-bundle-x.tar.gz"

// scriptedExecutor answers retrieve --stat and retrieve with fixed output.
type scriptedExecutor struct {
	size     int64
	payload  string
	err      error
	onStream func()
}

func (s *scriptedExecutor) Exec(_ context.Context, _, _, _ string, command []string, stdout, _ io.Writer) error {
	if slices.Contains(command, "--stat") {
		_, _ = fmt.Fprintf(stdout, `{"size":%d,"etag":"abc"}`, s.size)
		return nil
	}
	_, _ = io.WriteString(stdout, s.payload)
	if s.onStream != nil {
		s.onStream()
	}
	return s.err
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// leftovers lists anything in dir other than the named files.
func leftovers(t *testing.T, dir string, keep ...string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var extra []string
	for _, e := range entries {
		if !slices.Contains(keep, e.Name()) {
			extra = append(extra, e.Name())
		}
	}
	return extra
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRetrieve(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "bundle.tar.gz")
	payload := "archive-bytes"
	executor := &scriptedExecutor{size: int64(len(payload)), payload: payload}

	var written, total int64
	download, err := fakeSession(executor).Retrieve(context.Background(), bundleLocation, dest, digest(payload), func(w, t int64) { written, total = w, t })
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(len(payload)) || total != int64(len(payload)) {
		t.Errorf("progress = %d/%d", written, total)
	}
	if readFile(t, dest) != payload || download.Bytes != int64(len(payload)) || !download.Verified || download.SHA256 != digest(payload) {
		t.Errorf("download = %+v", download)
	}
	if extra := leftovers(t, dir, "bundle.tar.gz"); len(extra) != 0 {
		t.Errorf("left behind %v", extra)
	}
}

func TestRetrieveFailuresLeaveNothing(t *testing.T) {
	payload := "archive-bytes"
	tests := []struct {
		name     string
		executor *scriptedExecutor
		sha      string
		want     string
	}{
		{"short download", &scriptedExecutor{size: 100, payload: payload}, "", "expected 100"},
		{"remote non-zero exit", &scriptedExecutor{size: int64(len(payload)), payload: payload, err: utilexec.CodeExitError{Err: errors.New("exit 1"), Code: 1}}, "", "exited with code 1"},
		{"stream dropped", &scriptedExecutor{size: int64(len(payload)), payload: payload, err: errors.New("connection reset")}, "", "connection reset"},
		{"digest mismatch", &scriptedExecutor{size: int64(len(payload)), payload: payload}, digest("other"), "does not match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := fakeSession(tt.executor).Retrieve(context.Background(), bundleLocation, filepath.Join(dir, "bundle.tar.gz"), tt.sha, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
			if extra := leftovers(t, dir); len(extra) != 0 {
				t.Errorf("left behind %v", extra)
			}
		})
	}
}

func TestRetrieveRefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "bundle.tar.gz")
	if err := os.WriteFile(dest, []byte("theirs"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := &scriptedExecutor{size: 4, payload: "ours"}
	if _, err := fakeSession(executor).Retrieve(context.Background(), bundleLocation, dest, "", nil); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("err = %v, want ErrDestinationExists", err)
	}
	if readFile(t, dest) != "theirs" {
		t.Error("existing file was changed")
	}
}

func TestRetrieveDestinationAppearsDuringDownload(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "bundle.tar.gz")
	executor := &scriptedExecutor{size: 4, payload: "ours", onStream: func() {
		if err := os.WriteFile(dest, []byte("theirs"), 0o600); err != nil {
			t.Fatal(err)
		}
	}}

	_, err := fakeSession(executor).Retrieve(context.Background(), bundleLocation, dest, "", nil)
	if !errors.Is(err, ErrDestinationAppeared) {
		t.Fatalf("err = %v, want ErrDestinationAppeared", err)
	}
	if readFile(t, dest) != "theirs" {
		t.Error("the file that appeared was overwritten")
	}
	if extra := leftovers(t, dir, "bundle.tar.gz"); len(extra) != 0 {
		t.Errorf("left behind %v", extra)
	}
}

func withoutHardLinks(t *testing.T, before func()) {
	t.Helper()
	t.Cleanup(func() { linkFile = os.Link })
	linkFile = func(string, string) error {
		if before != nil {
			before()
		}
		return &os.LinkError{Op: "link", Err: syscall.EXDEV}
	}
}

func TestRetrieveCopyFallback(t *testing.T) {
	withoutHardLinks(t, nil)
	dir := t.TempDir()
	dest := filepath.Join(dir, "bundle.tar.gz")
	executor := &scriptedExecutor{size: 4, payload: "ours"}

	if _, err := fakeSession(executor).Retrieve(context.Background(), bundleLocation, dest, "", nil); err != nil {
		t.Fatal(err)
	}
	if readFile(t, dest) != "ours" {
		t.Error("fallback copy has wrong content")
	}
	if extra := leftovers(t, dir, "bundle.tar.gz"); len(extra) != 0 {
		t.Errorf("left behind %v", extra)
	}
}

func TestRetrieveCopyFallbackNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "bundle.tar.gz")
	withoutHardLinks(t, func() {
		if err := os.WriteFile(dest, []byte("theirs"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	executor := &scriptedExecutor{size: 4, payload: "ours"}

	if _, err := fakeSession(executor).Retrieve(context.Background(), bundleLocation, dest, "", nil); !errors.Is(err, ErrDestinationAppeared) {
		t.Fatalf("err = %v, want ErrDestinationAppeared", err)
	}
	if readFile(t, dest) != "theirs" {
		t.Error("the file that appeared was overwritten")
	}
	if extra := leftovers(t, dir, "bundle.tar.gz"); len(extra) != 0 {
		t.Errorf("left behind %v", extra)
	}
}

func TestRetrieveCopyFallbackFailureRemovesItsFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	withoutHardLinks(t, cancel) // cancelled after download, before the copy
	dir := t.TempDir()
	executor := &scriptedExecutor{size: 4, payload: "ours"}

	if _, err := fakeSession(executor).Retrieve(ctx, bundleLocation, filepath.Join(dir, "bundle.tar.gz"), "", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if extra := leftovers(t, dir); len(extra) != 0 {
		t.Errorf("an incomplete copy was left behind: %v", extra)
	}
}

func TestDefaultOutputName(t *testing.T) {
	if got := DefaultOutputName(bundleLocation); got != "support-bundle-x.tar.gz" {
		t.Errorf("DefaultOutputName = %q", got)
	}
	if got := DefaultOutputName("s3://bucket"); got != "support-bundle.tar.gz" {
		t.Errorf("DefaultOutputName(no key) = %q", got)
	}
	if got := DefaultOutputName("s3://bucket/a%2F.."); got != "support-bundle.tar.gz" {
		t.Errorf("DefaultOutputName(encoded ..) = %q", got)
	}
}
