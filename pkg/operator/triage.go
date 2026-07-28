package operator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wandb/wsm/pkg/kubectl"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

const (
	// DefaultTriageAction is selected when a caller does not specify an action.
	DefaultTriageAction = "default"

	triageRunManagedByLabel = "app.kubernetes.io/managed-by"
	triageRunManagedByValue = "wsm"
	triageRunNameSuffix     = "-triage-"

	// Leave room below the Kubernetes 253-character object-name limit for the
	// API server's generated suffix.
	maxTriageRunGenerateNameLength = 240
)

var triageRunsV2GVR = schema.GroupVersionResource{
	Group:    "apps.wandb.com",
	Version:  "v2",
	Resource: "triageruns",
}

// TriageRunRequest describes one immutable request to diagnose an Application.
// Namespace and ApplicationName are required; Action defaults to "default".
type TriageRunRequest struct {
	Namespace       string `json:"namespace"`
	ApplicationName string `json:"applicationName"`
	Action          string `json:"action,omitempty"`
}

// TriageRunRef identifies the newly created TriageRun. It is intentionally
// small so Watchtower and other SDK consumers do not need to import operator
// API types just to trigger a run.
type TriageRunRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// CreateTriageRun creates a fresh TriageRun through wsm's configured dynamic
// Kubernetes client. It always uses generateName: repeated calls represent
// distinct execution requests rather than updates or retries of an earlier run.
func CreateTriageRun(ctx context.Context, request TriageRunRequest) (TriageRunRef, error) {
	_, dynamicClient, err := kubectl.GetDynamicClientset()
	if err != nil {
		return TriageRunRef{}, err
	}
	return createTriageRun(ctx, dynamicClient, request)
}

func createTriageRun(
	ctx context.Context,
	dynamicClient dynamic.Interface,
	request TriageRunRequest,
) (TriageRunRef, error) {
	if err := validateTriageRunRequest(request); err != nil {
		return TriageRunRef{}, err
	}
	if request.Action == "" {
		request.Action = DefaultTriageAction
	}

	created, err := dynamicClient.Resource(triageRunsV2GVR).Namespace(request.Namespace).Create(
		ctx,
		newTriageRun(request),
		metav1.CreateOptions{FieldManager: "wsm"},
	)
	if err != nil {
		return TriageRunRef{}, fmt.Errorf(
			"failed to create TriageRun for Application %s/%s: %w",
			request.Namespace,
			request.ApplicationName,
			err,
		)
	}

	return TriageRunRef{
		Namespace: created.GetNamespace(),
		Name:      created.GetName(),
	}, nil
}

func validateTriageRunRequest(request TriageRunRequest) error {
	if request.Namespace == "" {
		return errors.New("namespace is required to create a TriageRun")
	}
	if problems := validation.IsDNS1123Label(request.Namespace); len(problems) > 0 {
		return fmt.Errorf("invalid TriageRun namespace %q: %s", request.Namespace, strings.Join(problems, "; "))
	}
	if request.ApplicationName == "" {
		return errors.New("application name is required to create a TriageRun")
	}
	if problems := validation.IsDNS1123Subdomain(request.ApplicationName); len(problems) > 0 {
		return fmt.Errorf("invalid Application name %q: %s", request.ApplicationName, strings.Join(problems, "; "))
	}
	return nil
}

func newTriageRun(request TriageRunRequest) *unstructured.Unstructured {
	action := request.Action
	if action == "" {
		action = DefaultTriageAction
	}

	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps.wandb.com/v2",
		"kind":       "TriageRun",
		"metadata": map[string]any{
			"generateName": triageRunGenerateName(request.ApplicationName),
			"namespace":    request.Namespace,
			"labels": map[string]any{
				triageRunManagedByLabel: triageRunManagedByValue,
			},
		},
		"spec": map[string]any{
			"applicationRef": map[string]any{
				"name": request.ApplicationName,
			},
			"action": action,
		},
	}}
}

func triageRunGenerateName(applicationName string) string {
	maxApplicationLength := maxTriageRunGenerateNameLength - len(triageRunNameSuffix)
	if len(applicationName) > maxApplicationLength {
		applicationName = strings.TrimRight(applicationName[:maxApplicationLength], "-.")
	}
	return applicationName + triageRunNameSuffix
}
