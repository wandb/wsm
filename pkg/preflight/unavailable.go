// Package preflight wraps the operator's preflight checks. run.go holds the
// real implementation; it is excluded with //go:build ignore until the operator
// ships pkg/preflight in a release (wandb/operator#402). To restore it, delete
// this file and the build tags in run.go and run_test.go.
package preflight

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"
)

// ErrUnavailable is returned while the operator's preflight checks are unreleased.
var ErrUnavailable = errors.New("preflight checks are not available with this operator version")

type Outcome string

type Result struct {
	Check   string
	Outcome Outcome
	Message string
}

type CheckRun struct {
	FieldPath string
	Result    Result
}

func ReadCR(data []byte) (*unstructured.Unstructured, error) {
	obj := map[string]any{}
	if err := sigsyaml.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("failed to parse CR YAML: %w", err)
	}
	cr := &unstructured.Unstructured{Object: obj}
	if cr.GetAPIVersion() != "apps.wandb.com/v2" {
		return nil, fmt.Errorf("CR is %q, preflight only supports apps.wandb.com/v2", cr.GetAPIVersion())
	}
	return cr, nil
}

func RunChecks(ctx context.Context, cr *unstructured.Unstructured, resolve any) []CheckRun {
	return []CheckRun{{FieldPath: "spec", Result: Result{Check: "preflight", Outcome: "unknown", Message: ErrUnavailable.Error()}}}
}

func Err(runs []CheckRun) error {
	return ErrUnavailable
}
