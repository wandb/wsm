package operator

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		if got, _, _ := unstructured.NestedString(obj.Object, "spec", "action"); got != "default" {
			t.Fatalf("action = %q, want default", got)
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

func TestCreateTriageRunPreservesExplicitAction(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	client.PrependReactor("create", "triageruns", func(action k8stesting.Action) (bool, runtime.Object, error) {
		obj := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		got, _, _ := unstructured.NestedString(obj.Object, "spec", "action")
		if got != "dependencies" {
			t.Fatalf("action = %q, want dependencies", got)
		}
		obj.SetName("weave-trace-triage-explicit")
		return true, obj, nil
	})

	_, err := createTriageRun(context.Background(), client, TriageRunRequest{
		Namespace:       "wandb",
		ApplicationName: "weave-trace",
		Action:          "dependencies",
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

func TestNewTriageRunHasNamespacedMetadata(t *testing.T) {
	t.Parallel()

	obj := newTriageRun(TriageRunRequest{
		Namespace:       "wandb",
		ApplicationName: "weave-trace",
		Action:          "default",
	})
	if obj.GetNamespace() != "wandb" || obj.GetCreationTimestamp() != (metav1.Time{}) {
		t.Fatalf("unexpected metadata: %#v", obj.Object["metadata"])
	}
}
