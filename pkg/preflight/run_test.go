//go:build ignore

package preflight

import (
	"context"
	"strings"
	"testing"

	v2 "github.com/wandb/operator/api/v2"
	oppreflight "github.com/wandb/operator/pkg/preflight"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestReadCR(t *testing.T) {
	cr, err := ReadCR([]byte("apiVersion: apps.wandb.com/v2\nkind: WeightsAndBiases\nmetadata:\n  name: wandb\n  namespace: wandb\n"))
	if err != nil {
		t.Fatalf("ReadCR: %v", err)
	}
	if cr.GetNamespace() != "wandb" {
		t.Errorf("namespace = %q, want wandb", cr.GetNamespace())
	}

	if _, err := ReadCR([]byte("apiVersion: apps.wandb.com/v1\nkind: WeightsAndBiases\n")); err == nil {
		t.Error("ReadCR accepted a v1 CR")
	}
	if _, err := ReadCR([]byte("spec: [")); err == nil {
		t.Error("ReadCR accepted invalid YAML")
	}
}

func TestMatchCRField(t *testing.T) {
	obj := map[string]any{
		"spec": map[string]any{
			"mysql": map[string]any{
				"replica": map[string]any{"externalMysql": map[string]any{"host": "b"}},
				"default": map[string]any{"externalMysql": map[string]any{"host": "a"}},
				"managed": map[string]any{"managedMysql": map[string]any{}},
				"bad":     "not-a-map",
			},
		},
	}

	got := matchCRField(obj, strings.Split("spec.mysql.*.externalMysql", "."), "")
	want := []string{"spec.mysql.default.externalMysql", "spec.mysql.replica.externalMysql"}
	if len(got) != len(want) {
		t.Fatalf("got %d matches, want %d: %+v", len(got), len(want), got)
	}
	for i, m := range got {
		if m.path != want[i] {
			t.Errorf("match %d path = %q, want %q", i, m.path, want[i])
		}
	}

	if got := matchCRField(obj, strings.Split("spec.redis.*.externalRedis", "."), ""); len(got) != 0 {
		t.Errorf("absent field matched: %+v", got)
	}
}

func TestRunChecksSkipAnnotation(t *testing.T) {
	cr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps.wandb.com/v2",
		"kind":       "WeightsAndBiases",
		"metadata": map[string]any{
			"name":        "wandb",
			"namespace":   "wandb",
			"annotations": map[string]any{oppreflight.SkipPreflightsAnnotation: oppreflight.ExternalMysqlCheck},
		},
		"spec": map[string]any{
			"mysql": map[string]any{
				"default": map[string]any{"externalMysql": map[string]any{"host": map[string]any{"value": "127.0.0.1"}}},
			},
		},
	}}

	// The resolver must not be reached for a skipped check.
	resolve := func(context.Context, v2.ValueOrSecret) (string, error) {
		t.Fatal("resolver called for a skipped check")
		return "", nil
	}
	runs := RunChecks(context.Background(), cr, resolve)
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1: %+v", len(runs), runs)
	}
	if runs[0].Result.Outcome != oppreflight.OutcomeSkipped {
		t.Errorf("outcome = %q, want skipped", runs[0].Result.Outcome)
	}
	if runs[0].FieldPath != "spec.mysql.default.externalMysql" {
		t.Errorf("field path = %q", runs[0].FieldPath)
	}
	if err := Err(runs); err != nil {
		t.Errorf("Err on a skipped run = %v, want nil", err)
	}
}

func TestErr(t *testing.T) {
	result := func(o oppreflight.Outcome) CheckRun {
		return CheckRun{FieldPath: "spec.mysql.default.externalMysql", Result: oppreflight.Result{Check: "c", Outcome: o}}
	}

	if err := Err(nil); err != nil {
		t.Errorf("Err(nil) = %v", err)
	}
	if err := Err([]CheckRun{result(oppreflight.OutcomePass), result(oppreflight.OutcomeSkipped)}); err != nil {
		t.Errorf("Err(pass, skipped) = %v", err)
	}

	err := Err([]CheckRun{result(oppreflight.OutcomeFail), result(oppreflight.OutcomeUnknown)})
	if err == nil {
		t.Fatal("Err(fail, unknown) = nil")
	}
	for _, s := range []string{"1 failed", "1 inconclusive"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("Err message %q missing %q", err, s)
		}
	}
}

func TestSecretResolverWithoutNamespace(t *testing.T) {
	resolve := SecretResolver("")

	if v, err := resolve(context.Background(), v2.ValueOrSecret{Value: "literal"}); err != nil || v != "literal" {
		t.Errorf("literal = (%q, %v), want (literal, nil)", v, err)
	}

	ref := v2.ValueOrSecret{ValueFrom: &v2.SecretValueSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "mysql"},
		Key:                  "password",
	}}}
	if _, err := resolve(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "metadata.namespace") {
		t.Errorf("secret ref with no namespace: err = %v, want a metadata.namespace error", err)
	}
}
