package preflight

import (
	"context"
	"strings"
	"testing"

	v2 "github.com/wandb/operator/api/v2"
	operatorPreflight "github.com/wandb/operator/pkg/preflight"
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
			"annotations": map[string]any{operatorPreflight.SkipPreflightsAnnotation: operatorPreflight.ExternalMysqlCheck},
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
	if runs[0].Result.Outcome != operatorPreflight.OutcomeSkipped {
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
	result := func(o operatorPreflight.Outcome) CheckRun {
		return CheckRun{FieldPath: "spec.mysql.default.externalMysql", Result: operatorPreflight.Result{Check: "c", Outcome: o}}
	}

	if err := Err(nil); err != nil {
		t.Errorf("Err(nil) = %v", err)
	}
	if err := Err([]CheckRun{result(operatorPreflight.OutcomePass), result(operatorPreflight.OutcomeSkipped)}); err != nil {
		t.Errorf("Err(pass, skipped) = %v", err)
	}

	err := Err([]CheckRun{result(operatorPreflight.OutcomeFail), result(operatorPreflight.OutcomeUnknown)})
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

func fakeCheck(name string, typ operatorPreflight.CheckType, ran *[]string, reqs ...operatorPreflight.ResourceReq) operatorPreflight.Check {
	return operatorPreflight.Check{
		Name:         name,
		Type:         typ,
		ResourceReqs: reqs,
		NewSpec:      func() any { return &map[string]any{} },
		Run: func(context.Context, any, operatorPreflight.Env) operatorPreflight.Result {
			*ran = append(*ran, name)
			return operatorPreflight.Result{Check: name, Outcome: operatorPreflight.OutcomePass}
		},
	}
}

func TestExecuteChecksOfflineRunsOnlyStatic(t *testing.T) {
	var ran []string
	plan := []PlannedRun{
		{Check: fakeCheck("static", operatorPreflight.TypeStatic, &ran, operatorPreflight.ReqReadOnly), FieldPath: "spec.a", value: map[string]any{}},
		{Check: fakeCheck("cluster", operatorPreflight.TypeCluster, &ran, operatorPreflight.ReqReadOnly), FieldPath: "spec.b", value: map[string]any{}},
		{Check: fakeCheck("conn", operatorPreflight.TypeConnection, &ran, operatorPreflight.ReqReadOnly), FieldPath: "spec.c", value: map[string]any{}},
	}
	cr := &unstructured.Unstructured{Object: map[string]any{}}

	runs := ExecuteChecks(context.Background(), cr, plan, ExecOptions{Offline: true})

	if len(ran) != 1 || ran[0] != "static" {
		t.Fatalf("ran = %v, want only [static]", ran)
	}
	want := []operatorPreflight.Outcome{operatorPreflight.OutcomePass, operatorPreflight.OutcomeSkipped, operatorPreflight.OutcomeSkipped}
	for i, r := range runs {
		if r.Result.Outcome != want[i] {
			t.Errorf("%s outcome = %q, want %q", r.Result.Check, r.Result.Outcome, want[i])
		}
	}
}

func TestRequiringConfirmation(t *testing.T) {
	var ran []string
	create := fakeCheck("createsPods", operatorPreflight.TypeConnection, &ran, operatorPreflight.ReqReadWrite, operatorPreflight.ReqResourceCreate)
	plan := []PlannedRun{
		{Check: fakeCheck("readOnly", operatorPreflight.TypeConnection, &ran, operatorPreflight.ReqReadOnly), FieldPath: "spec.a", value: map[string]any{}},
		{Check: create, FieldPath: "spec.objectStore.default.externalObjectStore", value: map[string]any{}},
		{Check: create, FieldPath: "spec.objectStore.other.externalObjectStore", value: map[string]any{}},
	}
	cr := &unstructured.Unstructured{Object: map[string]any{}}

	got := RequiringConfirmation(cr, plan, ExecOptions{})
	if len(got) != 1 || got[0].Name != "createsPods" {
		t.Errorf("online: got %v, want one createsPods", got)
	}
	// Offline never runs non-static checks, so there is nothing to confirm.
	if got := RequiringConfirmation(cr, plan, ExecOptions{Offline: true}); len(got) != 0 {
		t.Errorf("offline: got %v, want none", got)
	}

	// A skipped create check needs no confirmation, and the runnable check beside it still runs.
	skipped := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{operatorPreflight.SkipPreflightsAnnotation: "createsPods"},
		},
	}}
	if got := RequiringConfirmation(skipped, plan, ExecOptions{}); len(got) != 0 {
		t.Errorf("skipped: got %v, want none", got)
	}
	runs := ExecuteChecks(context.Background(), skipped, plan, ExecOptions{})
	if len(ran) != 1 || ran[0] != "readOnly" {
		t.Errorf("skipped: ran = %v, want only [readOnly]", ran)
	}
	for _, r := range runs[1:] {
		if r.Result.Outcome != operatorPreflight.OutcomeSkipped {
			t.Errorf("%s (%s) outcome = %q, want skipped", r.Result.Check, r.FieldPath, r.Result.Outcome)
		}
	}
}

func TestOfflineResolverRejectsSecretRefs(t *testing.T) {
	if v, err := offlineResolver(context.Background(), v2.ValueOrSecret{Value: "literal"}); err != nil || v != "literal" {
		t.Errorf("literal = (%q, %v), want (literal, nil)", v, err)
	}
	ref := v2.ValueOrSecret{ValueFrom: &v2.SecretValueSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "mysql"},
		Key:                  "password",
	}}}
	if _, err := offlineResolver(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("secret ref offline: err = %v, want an offline error", err)
	}
}
