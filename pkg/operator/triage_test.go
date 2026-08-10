package operator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCreateTriageRun(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	client.PrependReactor("create", "triageruns", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction := action.(k8stesting.CreateAction)
		obj := createAction.GetObject().(*unstructured.Unstructured).DeepCopy()

		if got := action.GetNamespace(); got != "wandb" {
			t.Fatalf("namespace = %q, want wandb", got)
		}
		if got := obj.GetGenerateName(); got != "weave-trace-triage-" {
			t.Fatalf("generateName = %q, want weave-trace-triage-", got)
		}
		if got := obj.GetLabels()[triageRunManagedByLabel]; got != triageRunManagedByValue {
			t.Fatalf("managed-by label = %q, want %q", got, triageRunManagedByValue)
		}
		if got, _, _ := unstructured.NestedString(
			obj.Object, "spec", "applicationRef", "name",
		); got != "weave-trace" {
			t.Fatalf("applicationRef.name = %q, want weave-trace", got)
		}
		got, _, _ := parseTriageActionReferences(obj.Object, "spec", "actions")
		if len(got) != 1 || got[0].Name != "default" {
			t.Fatalf("actions = %#v, want [default]", got)
		}

		obj.SetName(obj.GetGenerateName() + "abcde")
		return true, obj, nil
	})

	ref, err := createTriageRun(context.Background(), client, TriageRunRequest{
		Namespace:       "wandb",
		ApplicationName: "weave-trace",
	})
	if err != nil {
		t.Fatalf("create TriageRun: %v", err)
	}
	if ref.Namespace != "wandb" || ref.Name != "weave-trace-triage-abcde" {
		t.Fatalf("ref = %#v", ref)
	}
}

func TestCreateTriageRunPreservesExplicitActions(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	client.PrependReactor("create", "triageruns", func(action k8stesting.Action) (bool, runtime.Object, error) {
		obj := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		got, _, _ := parseTriageActionReferences(obj.Object, "spec", "actions")
		if len(got) != 2 || got[0].Name != "dependencies" || got[1].Name != "deep" {
			t.Fatalf("actions = %#v, want [dependencies deep]", got)
		}
		obj.SetName("weave-trace-triage-explicit")
		return true, obj, nil
	})

	_, err := createTriageRun(context.Background(), client, TriageRunRequest{
		Namespace:       "wandb",
		ApplicationName: "weave-trace",
		Actions: []TriageActionReference{
			{Name: "dependencies"},
			{Name: "deep"},
		},
	})
	if err != nil {
		t.Fatalf("create TriageRun: %v", err)
	}
}

func TestCreateTriageRunValidatesRequestBeforeCallingCluster(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request TriageRunRequest
		want    string
	}{
		{
			name:    "namespace required",
			request: TriageRunRequest{ApplicationName: "weave-trace"},
			want:    "namespace is required",
		},
		{
			name:    "namespace valid",
			request: TriageRunRequest{Namespace: "Not Valid", ApplicationName: "weave-trace"},
			want:    "invalid TriageRun namespace",
		},
		{
			name:    "application required",
			request: TriageRunRequest{Namespace: "wandb"},
			want:    "application name is required",
		},
		{
			name:    "application valid",
			request: TriageRunRequest{Namespace: "wandb", ApplicationName: "Not Valid"},
			want:    "invalid Application name",
		},
		{
			name: "explicit empty actions",
			request: TriageRunRequest{
				Namespace: "wandb", ApplicationName: "weave-trace",
				Actions: []TriageActionReference{},
			},
			want: "at least one action",
		},
		{
			name: "whitespace action name",
			request: TriageRunRequest{
				Namespace: "wandb", ApplicationName: "weave-trace",
				Actions: []TriageActionReference{{Name: "  "}},
			},
			want: "must not be empty",
		},
		{
			name: "duplicate actions",
			request: TriageRunRequest{
				Namespace: "wandb", ApplicationName: "weave-trace",
				Actions: []TriageActionReference{{Name: "default"}, {Name: "default"}},
			},
			want: "selected more than once",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := createTriageRun(context.Background(), nil, test.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestCreateTriageRunWrapsCreateError(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	wantErr := errors.New("triageruns.apps.wandb.com not found")
	client.PrependReactor("create", "triageruns", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, wantErr
	})

	_, err := createTriageRun(context.Background(), client, TriageRunRequest{
		Namespace:       "wandb",
		ApplicationName: "weave-trace",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped %v", err, wantErr)
	}
}

func TestTriageRunGenerateNameIsBounded(t *testing.T) {
	t.Parallel()

	name := strings.Repeat("a", 252)
	got := triageRunGenerateName(name)
	if len(got) != maxTriageRunGenerateNameLength {
		t.Fatalf("generateName length = %d, want %d", len(got), maxTriageRunGenerateNameLength)
	}
	if !strings.HasSuffix(got, triageRunNameSuffix) {
		t.Fatalf("generateName = %q, want suffix %q", got, triageRunNameSuffix)
	}
}

func TestTriageRunGVR(t *testing.T) {
	t.Parallel()

	want := schema.GroupVersionResource{
		Group:    "apps.wandb.com",
		Version:  "v2",
		Resource: "triageruns",
	}
	if triageRunsV2GVR != want {
		t.Fatalf("GVR = %#v, want %#v", triageRunsV2GVR, want)
	}
}

func TestListTriageActionsReturnsSortedApplicationActions(t *testing.T) {
	t.Parallel()

	application := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps.wandb.com/v2",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":      "weave-trace",
			"namespace": "wandb",
		},
		"spec": map[string]any{
			"triage": map[string]any{
				"actions": []any{
					map[string]any{"name": "default", "description": "Run standard diagnostics"},
					map[string]any{"name": "deep", "description": "Run deeper diagnostics"},
				},
			},
		},
	}}
	application.SetGroupVersionKind(schema.GroupVersionKind{
		Group: applicationsV2GVR.Group, Version: applicationsV2GVR.Version, Kind: "Application",
	})
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), application)

	actions, err := listTriageActionsWithClient(context.Background(), client, "wandb", "weave-trace")
	if err != nil {
		t.Fatalf("list triage actions: %v", err)
	}
	if len(actions) != 2 || actions[0].Name != "deep" || actions[1].Name != "default" ||
		actions[1].Description != "Run standard diagnostics" {
		t.Fatalf("actions = %#v, want [deep default]", actions)
	}
}

func TestListTriageActionsRejectsInvalidCatalogs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		rawActions any
		want       string
	}{
		{
			name: "map shaped catalog",
			rawActions: map[string]any{
				"default": map[string]any{"description": "Run standard diagnostics"},
			},
			want: "want array",
		},
		{
			name: "invalid description",
			rawActions: []any{
				map[string]any{"name": "default", "description": int64(1)},
			},
			want: "invalid description",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			application := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "apps.wandb.com/v2",
				"kind":       "Application",
				"metadata": map[string]any{
					"name":      "weave-trace",
					"namespace": "wandb",
				},
				"spec": map[string]any{
					"triage": map[string]any{"actions": test.rawActions},
				},
			}}
			application.SetGroupVersionKind(schema.GroupVersionKind{
				Group: applicationsV2GVR.Group, Version: applicationsV2GVR.Version, Kind: "Application",
			})
			client := fake.NewSimpleDynamicClient(runtime.NewScheme(), application)

			_, err := listTriageActionsWithClient(context.Background(), client, "wandb", "weave-trace")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestParseTriageActionReferencesRejectsInvalidSelections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		rawActions any
	}{
		{name: "empty array", rawActions: []any{}},
		{name: "empty object", rawActions: []any{map[string]any{}}},
		{name: "empty string", rawActions: ""},
		{name: "string item", rawActions: []any{"default"}},
		{name: "whitespace name", rawActions: []any{map[string]any{"name": "  "}}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			object := map[string]any{
				"spec": map[string]any{"actions": test.rawActions},
			}
			if _, _, err := parseTriageActionReferences(object, "spec", "actions"); err == nil {
				t.Fatalf("parse actions %#v: expected error", test.rawActions)
			}
		})
	}
}

func TestNewTriageRunHasNamespacedMetadata(t *testing.T) {
	t.Parallel()

	obj := newTriageRun(TriageRunRequest{
		Namespace:       "wandb",
		ApplicationName: "weave-trace",
		Actions:         []TriageActionReference{{Name: "default"}},
	})
	if obj.GetNamespace() != "wandb" || obj.GetCreationTimestamp() != (metav1.Time{}) {
		t.Fatalf("unexpected metadata: %#v", obj.Object["metadata"])
	}
}

func TestListTriageRunsFiltersSortsAndParsesStatus(t *testing.T) {
	older := testTriageRun(
		t,
		"weave-trace-triage-older",
		"weave-trace",
		"Succeeded",
		"2026-07-28T18:00:00Z",
	)
	newer := testTriageRun(
		t,
		"weave-trace-triage-newer",
		"weave-trace",
		"Failed",
		"2026-07-28T19:00:00Z",
	)
	other := testTriageRun(
		t,
		"gorilla-triage-other",
		"gorilla",
		"Succeeded",
		"2026-07-28T20:00:00Z",
	)
	if err := unstructured.SetNestedSlice(newer.Object, []any{
		map[string]any{"name": "default"},
		map[string]any{"name": "deep"},
	}, "spec", "actions"); err != nil {
		t.Fatalf("set actions: %v", err)
	}
	if err := unstructured.SetNestedMap(newer.Object, map[string]any{
		"phase":       "Failed",
		"startedAt":   "2026-07-28T19:00:01Z",
		"completedAt": "2026-07-28T19:00:05Z",
		"summary": map[string]any{
			"total":           int64(2),
			"pass":            int64(1),
			"warn":            int64(0),
			"fail":            int64(1),
			"error":           int64(0),
			"overallSeverity": "fail",
		},
		"actionStatuses": []any{
			map[string]any{
				"action":  "default",
				"phase":   "Succeeded",
				"jobRef":  map[string]any{"name": "weave-trace-triage-newer-triage-0"},
				"summary": map[string]any{"total": int64(1), "pass": int64(1)},
				"results": []any{
					map[string]any{
						"name":        "clickhouse-reachable",
						"umbrella":    "clickhouse",
						"severity":    "pass",
						"message":     "connected",
						"durationMs":  int64(12),
						"evidence":    map[string]any{"host": "clickhouse"},
						"remediation": "",
					},
				},
			},
			map[string]any{
				"action":  "deep",
				"phase":   "Failed",
				"jobRef":  map[string]any{"name": "weave-trace-triage-newer-triage-1"},
				"summary": map[string]any{"total": int64(1), "fail": int64(1)},
				"results": []any{
					map[string]any{
						"name":       "kafka-reachable",
						"umbrella":   "kafka",
						"severity":   "fail",
						"message":    "connection refused",
						"durationMs": int64(8),
					},
				},
			},
		},
	}, "status"); err != nil {
		t.Fatalf("set status: %v", err)
	}

	client := newTriageRunFakeClient(older, newer, other)
	runs, err := listTriageRuns(context.Background(), client, ListTriageRunsRequest{
		Namespace:       "wandb",
		ApplicationName: "weave-trace",
	})
	if err != nil {
		t.Fatalf("list TriageRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("len(runs) = %d, want 2", len(runs))
	}
	if runs[0].Name != newer.GetName() || runs[1].Name != older.GetName() {
		t.Fatalf("run order = [%s, %s]", runs[0].Name, runs[1].Name)
	}
	if runs[0].Summary == nil || runs[0].Summary.OverallSeverity != "fail" || runs[0].Summary.Total != 2 {
		t.Fatalf("summary = %#v", runs[0].Summary)
	}
	if len(runs[0].Actions) != 2 || runs[0].Actions[0].Name != "default" ||
		runs[0].Actions[1].Name != "deep" {
		t.Fatalf("actions = %#v", runs[0].Actions)
	}
	if len(runs[0].Results) != 2 || runs[0].Results[0].Name != "clickhouse-reachable" ||
		runs[0].Results[1].Name != "kafka-reachable" {
		t.Fatalf("results = %#v", runs[0].Results)
	}
	if len(runs[0].ActionStatuses) != 2 ||
		runs[0].ActionStatuses[0].Action != "default" ||
		runs[0].ActionStatuses[0].JobName != "weave-trace-triage-newer-triage-0" ||
		runs[0].ActionStatuses[1].Action != "deep" ||
		runs[0].ActionStatuses[1].JobName != "weave-trace-triage-newer-triage-1" {
		t.Fatalf("action statuses = %#v", runs[0].ActionStatuses)
	}
	evidence, ok := runs[0].Results[0].Evidence.(map[string]any)
	if !ok || evidence["host"] != "clickhouse" {
		t.Fatalf("evidence = %#v", runs[0].Results[0].Evidence)
	}
}

func TestGetTriageRun(t *testing.T) {
	obj := testTriageRun(
		t,
		"weave-trace-triage-get",
		"weave-trace",
		"Running",
		"2026-07-28T19:00:00Z",
	)
	client := newTriageRunFakeClient(obj)

	run, err := getTriageRun(context.Background(), client, "wandb", obj.GetName())
	if err != nil {
		t.Fatalf("get TriageRun: %v", err)
	}
	if run.Name != obj.GetName() || run.ApplicationName != "weave-trace" || run.Phase != "Running" {
		t.Fatalf("run = %#v", run)
	}
}

func TestDeleteTriageRunRequiresTerminalPhase(t *testing.T) {
	running := testTriageRun(
		t,
		"weave-trace-triage-running",
		"weave-trace",
		"Running",
		"2026-07-28T19:00:00Z",
	)
	client := newTriageRunFakeClient(running)

	err := deleteTriageRun(context.Background(), client, "wandb", running.GetName())
	if !errors.Is(err, ErrTriageRunNotTerminal) {
		t.Fatalf("error = %v, want ErrTriageRunNotTerminal", err)
	}
	if _, err := client.Resource(triageRunsV2GVR).Namespace("wandb").Get(
		context.Background(),
		running.GetName(),
		metav1.GetOptions{},
	); err != nil {
		t.Fatalf("running TriageRun was deleted: %v", err)
	}
}

func TestDeleteTriageRunDefaultsEmptyPhaseToPending(t *testing.T) {
	pending := testTriageRun(
		t,
		"weave-trace-triage-pending",
		"weave-trace",
		"",
		"2026-07-28T19:00:00Z",
	)
	client := newTriageRunFakeClient(pending)

	err := deleteTriageRun(context.Background(), client, "wandb", pending.GetName())
	if !errors.Is(err, ErrTriageRunNotTerminal) {
		t.Fatalf("error = %v, want ErrTriageRunNotTerminal", err)
	}
	if !strings.Contains(err.Error(), `phase "Pending"`) {
		t.Fatalf("error = %q, want normalized Pending phase", err)
	}
	if _, err := client.Resource(triageRunsV2GVR).Namespace("wandb").Get(
		context.Background(),
		pending.GetName(),
		metav1.GetOptions{},
	); err != nil {
		t.Fatalf("pending TriageRun was deleted: %v", err)
	}
}

func TestDeleteTerminalTriageRun(t *testing.T) {
	completed := testTriageRun(
		t,
		"weave-trace-triage-completed",
		"weave-trace",
		"Succeeded",
		"2026-07-28T19:00:00Z",
	)
	client := newTriageRunFakeClient(completed)

	if err := deleteTriageRun(context.Background(), client, "wandb", completed.GetName()); err != nil {
		t.Fatalf("delete TriageRun: %v", err)
	}
	_, err := client.Resource(triageRunsV2GVR).Namespace("wandb").Get(
		context.Background(),
		completed.GetName(),
		metav1.GetOptions{},
	)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("get after delete error = %v, want NotFound", err)
	}
}

func testTriageRun(
	t *testing.T,
	name string,
	applicationName string,
	phase string,
	createdAt string,
) *unstructured.Unstructured {
	t.Helper()
	obj := newTriageRun(TriageRunRequest{
		Namespace:       "wandb",
		ApplicationName: applicationName,
		Actions:         []TriageActionReference{{Name: "default"}},
	})
	obj.SetName(name)
	obj.SetGenerateName("")
	obj.SetUID(types.UID(name + "-uid"))
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   triageRunsV2GVR.Group,
		Version: triageRunsV2GVR.Version,
		Kind:    "TriageRun",
	})
	timestamp, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		t.Fatalf("parse creation timestamp: %v", err)
	}
	obj.SetCreationTimestamp(metav1.NewTime(timestamp))
	if err := unstructured.SetNestedField(obj.Object, phase, "status", "phase"); err != nil {
		t.Fatalf("set phase: %v", err)
	}
	return obj
}

func newTriageRunFakeClient(objects ...runtime.Object) *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			triageRunsV2GVR: "TriageRunList",
		},
		objects...,
	)
}
