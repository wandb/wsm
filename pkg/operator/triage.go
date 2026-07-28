package operator

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

var ErrTriageRunNotTerminal = errors.New("TriageRun is not terminal")

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

// ListTriageRunsRequest scopes run history to one namespace and, optionally,
// one Application.
type ListTriageRunsRequest struct {
	Namespace       string `json:"namespace"`
	ApplicationName string `json:"applicationName,omitempty"`
}

// TriageRunSummary contains the aggregate diagnostic verdict counts reported by
// the operator.
type TriageRunSummary struct {
	Total           int64  `json:"total,omitempty"`
	Pass            int64  `json:"pass,omitempty"`
	Warn            int64  `json:"warn,omitempty"`
	Fail            int64  `json:"fail,omitempty"`
	Error           int64  `json:"error,omitempty"`
	OverallSeverity string `json:"overallSeverity,omitempty"`
}

// TriageCheckResult is one structured diagnostic result reported by the
// application-owned check command.
type TriageCheckResult struct {
	Name                 string `json:"name"`
	Umbrella             string `json:"umbrella,omitempty"`
	Severity             string `json:"severity"`
	Message              string `json:"message,omitempty"`
	Evidence             any    `json:"evidence,omitempty"`
	Remediation          string `json:"remediation,omitempty"`
	StartedAt            string `json:"startedAt,omitempty"`
	EndedAt              string `json:"endedAt,omitempty"`
	DurationMilliseconds int64  `json:"durationMs,omitempty"`
}

// TriageRun is the caller-facing view of an immutable TriageRun and its latest
// operator-reported status.
type TriageRun struct {
	Namespace       string              `json:"namespace"`
	Name            string              `json:"name"`
	ApplicationName string              `json:"applicationName"`
	Action          string              `json:"action"`
	Phase           string              `json:"phase,omitempty"`
	CreatedAt       string              `json:"createdAt,omitempty"`
	StartedAt       string              `json:"startedAt,omitempty"`
	CompletedAt     string              `json:"completedAt,omitempty"`
	Summary         *TriageRunSummary   `json:"summary,omitempty"`
	Results         []TriageCheckResult `json:"results,omitempty"`
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

// ListTriageRuns returns newest-first run history in one namespace. Supplying
// ApplicationName filters the history without excluding runs created outside
// wsm.
func ListTriageRuns(ctx context.Context, request ListTriageRunsRequest) ([]TriageRun, error) {
	_, dynamicClient, err := kubectl.GetDynamicClientset()
	if err != nil {
		return nil, err
	}
	return listTriageRuns(ctx, dynamicClient, request)
}

// GetTriageRun returns one run and its latest status.
func GetTriageRun(ctx context.Context, namespace, name string) (TriageRun, error) {
	_, dynamicClient, err := kubectl.GetDynamicClientset()
	if err != nil {
		return TriageRun{}, err
	}
	return getTriageRun(ctx, dynamicClient, namespace, name)
}

// DeleteTriageRun removes a completed run. Pending and Running runs are not
// deleted because deletion would implicitly cancel their owned Job; cancellation
// needs a separate explicit contract.
func DeleteTriageRun(ctx context.Context, namespace, name string) error {
	_, dynamicClient, err := kubectl.GetDynamicClientset()
	if err != nil {
		return err
	}
	return deleteTriageRun(ctx, dynamicClient, namespace, name)
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

func listTriageRuns(
	ctx context.Context,
	dynamicClient dynamic.Interface,
	request ListTriageRunsRequest,
) ([]TriageRun, error) {
	if err := validateTriageRunNamespace(request.Namespace); err != nil {
		return nil, err
	}
	if request.ApplicationName != "" {
		if err := validateTriageApplicationName(request.ApplicationName); err != nil {
			return nil, err
		}
	}

	list, err := dynamicClient.Resource(triageRunsV2GVR).Namespace(request.Namespace).List(
		ctx,
		metav1.ListOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list TriageRuns in namespace %s: %w", request.Namespace, err)
	}

	runs := make([]TriageRun, 0, len(list.Items))
	for i := range list.Items {
		run, err := parseTriageRun(&list.Items[i])
		if err != nil {
			return nil, err
		}
		if request.ApplicationName != "" && run.ApplicationName != request.ApplicationName {
			continue
		}
		runs = append(runs, run)
	}
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].CreatedAt == runs[j].CreatedAt {
			return runs[i].Name > runs[j].Name
		}
		return runs[i].CreatedAt > runs[j].CreatedAt
	})
	return runs, nil
}

func getTriageRun(
	ctx context.Context,
	dynamicClient dynamic.Interface,
	namespace string,
	name string,
) (TriageRun, error) {
	if err := validateTriageRunIdentity(namespace, name); err != nil {
		return TriageRun{}, err
	}
	obj, err := dynamicClient.Resource(triageRunsV2GVR).Namespace(namespace).Get(
		ctx,
		name,
		metav1.GetOptions{},
	)
	if err != nil {
		return TriageRun{}, fmt.Errorf("failed to get TriageRun %s/%s: %w", namespace, name, err)
	}
	return parseTriageRun(obj)
}

func deleteTriageRun(
	ctx context.Context,
	dynamicClient dynamic.Interface,
	namespace string,
	name string,
) error {
	if err := validateTriageRunIdentity(namespace, name); err != nil {
		return err
	}
	obj, err := dynamicClient.Resource(triageRunsV2GVR).Namespace(namespace).Get(
		ctx,
		name,
		metav1.GetOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to get TriageRun %s/%s before deletion: %w", namespace, name, err)
	}
	phase, _, err := unstructured.NestedString(obj.Object, "status", "phase")
	if err != nil {
		return fmt.Errorf("failed to read TriageRun %s/%s phase: %w", namespace, name, err)
	}
	if phase != "Succeeded" && phase != "Failed" {
		return fmt.Errorf("%w: %s/%s has phase %q", ErrTriageRunNotTerminal, namespace, name, phase)
	}

	uid := obj.GetUID()
	if err := dynamicClient.Resource(triageRunsV2GVR).Namespace(namespace).Delete(
		ctx,
		name,
		metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &uid},
		},
	); err != nil {
		return fmt.Errorf("failed to delete TriageRun %s/%s: %w", namespace, name, err)
	}
	return nil
}

func parseTriageRun(obj *unstructured.Unstructured) (TriageRun, error) {
	applicationName, _, err := unstructured.NestedString(
		obj.Object,
		"spec",
		"applicationRef",
		"name",
	)
	if err != nil {
		return TriageRun{}, fmt.Errorf("failed to read TriageRun %s/%s application: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	action, _, err := unstructured.NestedString(obj.Object, "spec", "action")
	if err != nil {
		return TriageRun{}, fmt.Errorf("failed to read TriageRun %s/%s action: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	if action == "" {
		action = DefaultTriageAction
	}
	phase, _, err := unstructured.NestedString(obj.Object, "status", "phase")
	if err != nil {
		return TriageRun{}, fmt.Errorf("failed to read TriageRun %s/%s phase: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	if phase == "" {
		phase = "Pending"
	}
	startedAt, _, err := unstructured.NestedString(obj.Object, "status", "startedAt")
	if err != nil {
		return TriageRun{}, fmt.Errorf("failed to read TriageRun %s/%s startedAt: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	completedAt, _, err := unstructured.NestedString(obj.Object, "status", "completedAt")
	if err != nil {
		return TriageRun{}, fmt.Errorf("failed to read TriageRun %s/%s completedAt: %w", obj.GetNamespace(), obj.GetName(), err)
	}

	creationTimestamp := obj.GetCreationTimestamp()
	run := TriageRun{
		Namespace:       obj.GetNamespace(),
		Name:            obj.GetName(),
		ApplicationName: applicationName,
		Action:          action,
		Phase:           phase,
		CreatedAt:       creationTimestamp.UTC().Format("2006-01-02T15:04:05Z07:00"),
		StartedAt:       startedAt,
		CompletedAt:     completedAt,
	}
	if creationTimestamp.IsZero() {
		run.CreatedAt = ""
	}

	summaryMap, found, err := unstructured.NestedMap(obj.Object, "status", "summary")
	if err != nil {
		return TriageRun{}, fmt.Errorf("failed to read TriageRun %s/%s summary: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	if found {
		run.Summary, err = parseTriageRunSummary(summaryMap)
		if err != nil {
			return TriageRun{}, fmt.Errorf("failed to parse TriageRun %s/%s summary: %w", obj.GetNamespace(), obj.GetName(), err)
		}
	}

	resultItems, found, err := unstructured.NestedSlice(obj.Object, "status", "results")
	if err != nil {
		return TriageRun{}, fmt.Errorf("failed to read TriageRun %s/%s results: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	if found {
		run.Results = make([]TriageCheckResult, 0, len(resultItems))
		for i, item := range resultItems {
			resultMap, ok := item.(map[string]any)
			if !ok {
				return TriageRun{}, fmt.Errorf("TriageRun %s/%s result %d is %T, want object", obj.GetNamespace(), obj.GetName(), i, item)
			}
			result, err := parseTriageCheckResult(resultMap)
			if err != nil {
				return TriageRun{}, fmt.Errorf("failed to parse TriageRun %s/%s result %d: %w", obj.GetNamespace(), obj.GetName(), i, err)
			}
			run.Results = append(run.Results, result)
		}
	}
	return run, nil
}

func parseTriageRunSummary(summary map[string]any) (*TriageRunSummary, error) {
	result := &TriageRunSummary{}
	var err error
	if result.Total, _, err = unstructured.NestedInt64(summary, "total"); err != nil {
		return nil, err
	}
	if result.Pass, _, err = unstructured.NestedInt64(summary, "pass"); err != nil {
		return nil, err
	}
	if result.Warn, _, err = unstructured.NestedInt64(summary, "warn"); err != nil {
		return nil, err
	}
	if result.Fail, _, err = unstructured.NestedInt64(summary, "fail"); err != nil {
		return nil, err
	}
	if result.Error, _, err = unstructured.NestedInt64(summary, "error"); err != nil {
		return nil, err
	}
	if result.OverallSeverity, _, err = unstructured.NestedString(summary, "overallSeverity"); err != nil {
		return nil, err
	}
	return result, nil
}

func parseTriageCheckResult(item map[string]any) (TriageCheckResult, error) {
	result := TriageCheckResult{}
	var err error
	if result.Name, _, err = unstructured.NestedString(item, "name"); err != nil {
		return TriageCheckResult{}, err
	}
	if result.Umbrella, _, err = unstructured.NestedString(item, "umbrella"); err != nil {
		return TriageCheckResult{}, err
	}
	if result.Severity, _, err = unstructured.NestedString(item, "severity"); err != nil {
		return TriageCheckResult{}, err
	}
	if result.Message, _, err = unstructured.NestedString(item, "message"); err != nil {
		return TriageCheckResult{}, err
	}
	if result.Remediation, _, err = unstructured.NestedString(item, "remediation"); err != nil {
		return TriageCheckResult{}, err
	}
	if result.StartedAt, _, err = unstructured.NestedString(item, "startedAt"); err != nil {
		return TriageCheckResult{}, err
	}
	if result.EndedAt, _, err = unstructured.NestedString(item, "endedAt"); err != nil {
		return TriageCheckResult{}, err
	}
	if result.DurationMilliseconds, _, err = unstructured.NestedInt64(item, "durationMs"); err != nil {
		return TriageCheckResult{}, err
	}
	if evidence, ok := item["evidence"]; ok {
		result.Evidence = evidence
	}
	return result, nil
}

func validateTriageRunRequest(request TriageRunRequest) error {
	if err := validateTriageRunNamespace(request.Namespace); err != nil {
		return err
	}
	return validateTriageApplicationName(request.ApplicationName)
}

func validateTriageRunIdentity(namespace, name string) error {
	if err := validateTriageRunNamespace(namespace); err != nil {
		return err
	}
	if name == "" {
		return errors.New("TriageRun name is required")
	}
	if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
		return fmt.Errorf("invalid TriageRun name %q: %s", name, strings.Join(problems, "; "))
	}
	return nil
}

func validateTriageRunNamespace(namespace string) error {
	if namespace == "" {
		return errors.New("namespace is required to access TriageRuns")
	}
	if problems := validation.IsDNS1123Label(namespace); len(problems) > 0 {
		return fmt.Errorf("invalid TriageRun namespace %q: %s", namespace, strings.Join(problems, "; "))
	}
	return nil
}

func validateTriageApplicationName(applicationName string) error {
	if applicationName == "" {
		return errors.New("application name is required to access TriageRuns")
	}
	if problems := validation.IsDNS1123Subdomain(applicationName); len(problems) > 0 {
		return fmt.Errorf("invalid Application name %q: %s", applicationName, strings.Join(problems, "; "))
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
