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

func TestCreateActionRun(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	client.PrependReactor("create", "actionruns", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction := action.(k8stesting.CreateAction)
		obj := createAction.GetObject().(*unstructured.Unstructured).DeepCopy()
		if got := action.GetNamespace(); got != "wandb" {
			t.Fatalf("namespace = %q, want wandb", got)
		}
		if got := obj.GetGenerateName(); got != "weave-trace-triage-" {
			t.Fatalf("generateName = %q, want weave-trace-triage-", got)
		}
		if got := obj.GetLabels()[actionRunManagedByLabel]; got != actionRunManagedByValue {
			t.Fatalf("managed-by label = %q, want %q", got, actionRunManagedByValue)
		}
		if got, _, _ := unstructured.NestedString(obj.Object, "spec", "type"); got != "triage" {
			t.Fatalf("spec.type = %q, want triage", got)
		}
		if got, _, _ := unstructured.NestedString(
			obj.Object, "spec", "applicationRef", "name",
		); got != "weave-trace" {
			t.Fatalf("applicationRef.name = %q, want weave-trace", got)
		}
		if got, _, _ := unstructured.NestedString(obj.Object, "spec", "action", "name"); got != "default" {
			t.Fatalf("action.name = %q, want default", got)
		}
		obj.SetName(obj.GetGenerateName() + "abcde")
		return true, obj, nil
	})

	ref, err := createActionRun(context.Background(), client, ActionRunRequest{
		Namespace:       "wandb",
		ApplicationName: "weave-trace",
		Type:            ActionTypeTriage,
	})
	if err != nil {
		t.Fatalf("create ActionRun: %v", err)
	}
	if ref.Namespace != "wandb" || ref.Name != "weave-trace-triage-abcde" {
		t.Fatalf("ref = %#v", ref)
	}
}

func TestCreateActionRunPreservesExplicitAction(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	client.PrependReactor("create", "actionruns", func(action k8stesting.Action) (bool, runtime.Object, error) {
		obj := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		got, _, _ := unstructured.NestedString(obj.Object, "spec", "action", "name")
		if got != "dependencies" {
			t.Fatalf("action.name = %q, want dependencies", got)
		}
		obj.SetName("weave-trace-triage-explicit")
		return true, obj, nil
	})

	_, err := createActionRun(context.Background(), client, ActionRunRequest{
		Namespace:       "wandb",
		ApplicationName: "weave-trace",
		Type:            ActionTypeTriage,
		Action:          ActionReference{Name: "dependencies"},
	})
	if err != nil {
		t.Fatalf("create ActionRun: %v", err)
	}
}

func TestCreateActionRunValidatesRequestBeforeCallingCluster(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request ActionRunRequest
		want    string
	}{
		{
			name:    "namespace required",
			request: ActionRunRequest{ApplicationName: "weave-trace", Type: ActionTypeTriage},
			want:    "namespace is required",
		},
		{
			name: "namespace valid",
			request: ActionRunRequest{
				Namespace: "Not Valid", ApplicationName: "weave-trace", Type: ActionTypeTriage,
			},
			want: "invalid ActionRun namespace",
		},
		{
			name:    "application required",
			request: ActionRunRequest{Namespace: "wandb", Type: ActionTypeTriage},
			want:    "application name is required",
		},
		{
			name: "application valid",
			request: ActionRunRequest{
				Namespace: "wandb", ApplicationName: "Not Valid", Type: ActionTypeTriage,
			},
			want: "invalid Application name",
		},
		{
			name: "type required",
			request: ActionRunRequest{
				Namespace: "wandb", ApplicationName: "weave-trace",
			},
			want: "action type is required",
		},
		{
			name: "type supported",
			request: ActionRunRequest{
				Namespace: "wandb", ApplicationName: "weave-trace", Type: "backup",
			},
			want: "unsupported action type",
		},
		{
			name: "whitespace action name",
			request: ActionRunRequest{
				Namespace: "wandb", ApplicationName: "weave-trace", Type: ActionTypeTriage,
				Action: ActionReference{Name: "  "},
			},
			want: "action name must not be empty",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := createActionRun(context.Background(), nil, test.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestCreateActionRunWrapsCreateError(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	wantErr := errors.New("actionruns.apps.wandb.com not found")
	client.PrependReactor("create", "actionruns", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, wantErr
	})

	_, err := createActionRun(context.Background(), client, ActionRunRequest{
		Namespace:       "wandb",
		ApplicationName: "weave-trace",
		Type:            ActionTypeTriage,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped %v", err, wantErr)
	}
}

func TestActionRunGenerateNameIsBounded(t *testing.T) {
	t.Parallel()

	name := strings.Repeat("a", 252)
	got := actionRunGenerateName(name, ActionTypeTriage)
	if len(got) != maxActionRunGenerateNameLength {
		t.Fatalf("generateName length = %d, want %d", len(got), maxActionRunGenerateNameLength)
	}
	if !strings.HasSuffix(got, "-triage-") {
		t.Fatalf("generateName = %q, want triage suffix", got)
	}
}

func TestActionRunGVR(t *testing.T) {
	t.Parallel()

	want := schema.GroupVersionResource{
		Group: "apps.wandb.com", Version: "v2", Resource: "actionruns",
	}
	if actionRunsV2GVR != want {
		t.Fatalf("GVR = %#v, want %#v", actionRunsV2GVR, want)
	}
}

func TestListActionsReturnsSortedApplicationActions(t *testing.T) {
	t.Parallel()

	application := testActionApplication([]any{
		map[string]any{"name": "default", "description": "Run standard diagnostics"},
		map[string]any{"name": "deep", "description": "Run deeper diagnostics"},
	})
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), application)

	actions, err := listActionsWithClient(
		context.Background(), client, "wandb", "weave-trace", ActionTypeTriage)
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	if len(actions) != 2 || actions[0].Name != "deep" || actions[1].Name != "default" ||
		actions[1].Description != "Run standard diagnostics" {
		t.Fatalf("actions = %#v, want [deep default]", actions)
	}
}

func TestListActionsRejectsInvalidCatalogs(t *testing.T) {
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
			client := fake.NewSimpleDynamicClient(runtime.NewScheme(), testActionApplication(test.rawActions))
			_, err := listActionsWithClient(
				context.Background(), client, "wandb", "weave-trace", ActionTypeTriage)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestNewActionRunHasNamespacedMetadata(t *testing.T) {
	t.Parallel()

	obj := newActionRun(ActionRunRequest{
		Namespace:       "wandb",
		ApplicationName: "weave-trace",
		Type:            ActionTypeTriage,
		Action:          ActionReference{Name: "default"},
	})
	if obj.GetNamespace() != "wandb" || obj.GetCreationTimestamp() != (metav1.Time{}) {
		t.Fatalf("unexpected metadata: %#v", obj.Object["metadata"])
	}
}

func TestListActionRunsUsesSelectorsAndParsesFlatStatus(t *testing.T) {
	older := testActionRun(t, "weave-trace-triage-older", "weave-trace", ActionTypeTriage,
		"default", "Succeeded", "2026-07-28T18:00:00Z")
	newer := testActionRun(t, "weave-trace-triage-newer", "weave-trace", ActionTypeTriage,
		"deep", "Succeeded", "2026-07-28T19:00:00Z")
	otherType := testActionRun(t, "weave-trace-maintenance-other", "weave-trace", ActionTypeMaintenance,
		"compact", "Failed", "2026-07-28T20:00:00Z")
	otherApplication := testActionRun(t, "gorilla-triage-other", "gorilla", ActionTypeTriage,
		"default", "Succeeded", "2026-07-28T21:00:00Z")
	if err := unstructured.SetNestedMap(newer.Object, map[string]any{
		"phase":       "Succeeded",
		"jobRef":      map[string]any{"name": "weave-trace-triage-newer-action"},
		"startedAt":   "2026-07-28T19:00:01Z",
		"completedAt": "2026-07-28T19:00:05Z",
		"summary": map[string]any{
			"total": int64(2), "pass": int64(1), "warn": int64(0),
			"fail": int64(1), "error": int64(0), "overallSeverity": "fail",
		},
		"results": []any{
			map[string]any{
				"name": "clickhouse-reachable", "umbrella": "clickhouse",
				"severity": "pass", "message": "connected", "durationMs": int64(12),
				"evidence": map[string]any{"host": "clickhouse"},
			},
			map[string]any{
				"name": "kafka-reachable", "umbrella": "kafka",
				"severity": "fail", "message": "connection refused", "durationMs": int64(8),
			},
		},
	}, "status"); err != nil {
		t.Fatalf("set status: %v", err)
	}

	client := newActionRunFakeClient(older, newer, otherType, otherApplication)
	var fieldSelector string
	client.PrependReactor("list", "actionruns", func(action k8stesting.Action) (bool, runtime.Object, error) {
		fieldSelector = action.(k8stesting.ListAction).GetListRestrictions().Fields.String()
		return false, nil, nil
	})
	runs, err := listActionRuns(context.Background(), client, ListActionRunsRequest{
		Namespace:       "wandb",
		Type:            ActionTypeTriage,
		ApplicationName: "weave-trace",
	})
	if err != nil {
		t.Fatalf("list ActionRuns: %v", err)
	}
	if !strings.Contains(fieldSelector, "spec.type=triage") ||
		!strings.Contains(fieldSelector, "spec.applicationRef.name=weave-trace") {
		t.Fatalf("field selector = %q", fieldSelector)
	}
	if len(runs) != 2 || runs[0].Name != newer.GetName() || runs[1].Name != older.GetName() {
		t.Fatalf("runs = %#v, want newer then older", runs)
	}
	if runs[0].Type != ActionTypeTriage || runs[0].Action.Name != "deep" ||
		runs[0].JobName != "weave-trace-triage-newer-action" {
		t.Fatalf("run = %#v", runs[0])
	}
	if runs[0].Summary == nil || runs[0].Summary.OverallSeverity != "fail" ||
		runs[0].Summary.Total != 2 {
		t.Fatalf("summary = %#v", runs[0].Summary)
	}
	if len(runs[0].Results) != 2 || runs[0].Results[0].Name != "clickhouse-reachable" {
		t.Fatalf("results = %#v", runs[0].Results)
	}
	evidence, ok := runs[0].Results[0].Evidence.(map[string]any)
	if !ok || evidence["host"] != "clickhouse" {
		t.Fatalf("evidence = %#v", runs[0].Results[0].Evidence)
	}
}

func TestGetActionRun(t *testing.T) {
	obj := testActionRun(t, "weave-trace-triage-get", "weave-trace", ActionTypeTriage,
		"default", "Running", "2026-07-28T19:00:00Z")
	client := newActionRunFakeClient(obj)

	run, err := getActionRun(context.Background(), client, "wandb", obj.GetName())
	if err != nil {
		t.Fatalf("get ActionRun: %v", err)
	}
	if run.Name != obj.GetName() || run.ApplicationName != "weave-trace" ||
		run.Type != ActionTypeTriage || run.Action.Name != "default" || run.Phase != "Running" {
		t.Fatalf("run = %#v", run)
	}
}

func TestDeleteActionRunRequiresTerminalPhase(t *testing.T) {
	running := testActionRun(t, "weave-trace-triage-running", "weave-trace", ActionTypeTriage,
		"default", "Running", "2026-07-28T19:00:00Z")
	client := newActionRunFakeClient(running)

	err := deleteActionRun(context.Background(), client, "wandb", running.GetName())
	if !errors.Is(err, ErrActionRunNotTerminal) {
		t.Fatalf("error = %v, want ErrActionRunNotTerminal", err)
	}
	if _, err := client.Resource(actionRunsV2GVR).Namespace("wandb").Get(
		context.Background(), running.GetName(), metav1.GetOptions{},
	); err != nil {
		t.Fatalf("running ActionRun was deleted: %v", err)
	}
}

func TestDeleteActionRunDefaultsEmptyPhaseToPending(t *testing.T) {
	pending := testActionRun(t, "weave-trace-triage-pending", "weave-trace", ActionTypeTriage,
		"default", "", "2026-07-28T19:00:00Z")
	client := newActionRunFakeClient(pending)

	err := deleteActionRun(context.Background(), client, "wandb", pending.GetName())
	if !errors.Is(err, ErrActionRunNotTerminal) || !strings.Contains(err.Error(), `phase "Pending"`) {
		t.Fatalf("error = %v, want pending ErrActionRunNotTerminal", err)
	}
}

func TestDeleteTerminalActionRun(t *testing.T) {
	completed := testActionRun(t, "weave-trace-triage-completed", "weave-trace", ActionTypeTriage,
		"default", "Succeeded", "2026-07-28T19:00:00Z")
	client := newActionRunFakeClient(completed)

	if err := deleteActionRun(context.Background(), client, "wandb", completed.GetName()); err != nil {
		t.Fatalf("delete ActionRun: %v", err)
	}
	_, err := client.Resource(actionRunsV2GVR).Namespace("wandb").Get(
		context.Background(), completed.GetName(), metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("get after delete error = %v, want NotFound", err)
	}
}

func testActionApplication(actions any) *unstructured.Unstructured {
	application := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps.wandb.com/v2",
		"kind":       "Application",
		"metadata": map[string]any{
			"name": "weave-trace", "namespace": "wandb",
		},
		"spec": map[string]any{
			"triage": map[string]any{"actions": actions},
		},
	}}
	application.SetGroupVersionKind(schema.GroupVersionKind{
		Group: applicationsV2GVR.Group, Version: applicationsV2GVR.Version, Kind: "Application",
	})
	return application
}

func testActionRun(
	t *testing.T,
	name string,
	applicationName string,
	actionType ActionType,
	actionName string,
	phase string,
	createdAt string,
) *unstructured.Unstructured {
	t.Helper()
	obj := newActionRun(ActionRunRequest{
		Namespace:       "wandb",
		ApplicationName: applicationName,
		Type:            actionType,
		Action:          ActionReference{Name: actionName},
	})
	obj.SetName(name)
	obj.SetGenerateName("")
	obj.SetUID(types.UID(name + "-uid"))
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: actionRunsV2GVR.Group, Version: actionRunsV2GVR.Version, Kind: "ActionRun",
	})
	timestamp, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		t.Fatalf("parse creation timestamp: %v", err)
	}
	obj.SetCreationTimestamp(metav1.NewTime(timestamp))
	if phase != "" {
		if err := unstructured.SetNestedField(obj.Object, phase, "status", "phase"); err != nil {
			t.Fatalf("set phase: %v", err)
		}
	}
	return obj
}

func newActionRunFakeClient(objects ...runtime.Object) *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{actionRunsV2GVR: "ActionRunList"},
		objects...,
	)
}
