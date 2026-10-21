package kubectl

import (
	"errors"
	"fmt"
	"testing"

	utilexec "k8s.io/client-go/util/exec"
)

func TestExitCode(t *testing.T) {
	code, ok := ExitCode(fmt.Errorf("wrapped: %w", utilexec.CodeExitError{Err: errors.New("exit 3"), Code: 3}))
	if !ok || code != 3 {
		t.Fatalf("ExitCode(remote exit 3) = %d, %v; want 3, true", code, ok)
	}
	if _, ok := ExitCode(errors.New("connection reset by peer")); ok {
		t.Fatal("ExitCode(transport error) reported an exit status")
	}
	if _, ok := ExitCode(nil); ok {
		t.Fatal("ExitCode(nil) reported an exit status")
	}
}
