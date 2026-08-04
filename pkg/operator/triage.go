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

var applicationsV2GVR = schema.GroupVersionResource{
	Group:    "apps.wandb.com",
	Version:  "v2",
	Resource: "applications",
}

// TriageAction is one action advertised by an Application.
type TriageAction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// TriageActionReference selects an advertised Application action by name.
type TriageActionReference struct {
	Name string `json:"name"`
}

// TriageRunRequest describes one immutable request to diagnose an Application.
// Namespace and ApplicationName are required; Actions defaults to [{name:
// "default"}].
type TriageRunRequest struct {
	Namespace       string                  `json:"namespace"`
	ApplicationName string                  `json:"applicationName"`
	Actions         []TriageActionReference `json:"actions,omitempty"`

	// Action is retained for source compatibility with clients of the initial
	// single-action SDK. New callers should use Actions; setting both is invalid.
	Action string `json:"action,omitempty"`
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

// TriageActionStatus contains the execution and results for one selected
// Application action.
type TriageActionStatus struct {
	Action      string              `json:"action"`
	Phase       string              `json:"phase,omitempty"`
	JobName     string              `json:"jobName,omitempty"`
	StartedAt   string              `json:"startedAt,omitempty"`
	CompletedAt string              `json:"completedAt,omitempty"`
	Summary     *TriageRunSummary   `json:"summary,omitempty"`
	Results     []TriageCheckResult `json:"results,omitempty"`
}

// TriageRun is the caller-facing view of an immutable TriageRun and its latest
// operator-reported status.
type TriageRun struct {
	Namespace       string                  `json:"namespace"`
	Name            string                  `json:"name"`
	ApplicationName string                  `json:"applicationName"`
	Actions         []TriageActionReference `json:"actions"`
	// Action mirrors the selected action for legacy single-action consumers.
	Action         string               `json:"action,omitempty"`
	Phase          string               `json:"phase,omitempty"`
	CreatedAt      string               `json:"createdAt,omitempty"`
	StartedAt      string               `json:"startedAt,omitempty"`
	CompletedAt    string               `json:"completedAt,omitempty"`
	Summary        *TriageRunSummary    `json:"summary,omitempty"`
	ActionStatuses []TriageActionStatus `json:"actionStatuses,omitempty"`
	// Results is a flattened compatibility view of all action results.
	Results []TriageCheckResult `json:"results,omitempty"`
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

// ListTriageActions returns the sorted action metadata declared by one
// Application.
func ListTriageActions(ctx context.Context, namespace, applicationName string) ([]TriageAction, error) {
	_, dynamicClient, err := kubectl.GetDynamicClientset()
	if err != nil {
		return nil, err
	}
	return listTriageActions(ctx, dynamicClient, namespace, applicationName)
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
	request.Actions = normalizedTriageActions(request)

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

func listTriageActions(
	ctx context.Context,
	dynamicClient dynamic.Interface,
	namespace string,
	applicationName string,
) ([]TriageAction, error) {
	if err := validateTriageRunNamespace(namespace); err != nil {
		return nil, err
	}
	if err := validateTriageApplicationName(applicationName); err != nil {
		return nil, err
	}
	application, err := dynamicClient.Resource(applicationsV2GVR).Namespace(namespace).Get(
		ctx,
		applicationName,
		metav1.GetOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get Application %s/%s: %w", namespace, applicationName, err)
	}
	rawActions, found, err := unstructured.NestedFieldNoCopy(
		application.Object, "spec", "triage", "actions")
	if err != nil {
		return nil, fmt.Errorf(
			"failed to read Application %s/%s triage actions: %w", namespace, applicationName, err)
	}
	if !found {
		return []TriageAction{}, nil
	}
	actions := []TriageAction{}
	switch value := rawActions.(type) {
	case []any:
		actions = make([]TriageAction, 0, len(value))
		for i, item := range value {
			actionMap, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf(
					"Application %s/%s triage action %d is %T, want object",
					namespace, applicationName, i, item)
			}
			name, _, nameErr := unstructured.NestedString(actionMap, "name")
			if nameErr != nil {
				return nil, fmt.Errorf(
					"Application %s/%s triage action %d has invalid name: %w",
					namespace, applicationName, i, nameErr)
			}
			if strings.TrimSpace(name) == "" {
				return nil, fmt.Errorf(
					"Application %s/%s triage action %d has an empty name",
					namespace, applicationName, i)
			}
			description, _, descriptionErr := unstructured.NestedString(actionMap, "description")
			if descriptionErr != nil {
				return nil, fmt.Errorf(
					"Application %s/%s triage action %q has invalid description: %w",
					namespace, applicationName, name, descriptionErr)
			}
			actions = append(actions, TriageAction{Name: name, Description: description})
		}
	case map[string]any:
		// Read the original map-shaped catalog during rolling upgrades.
		actions = make([]TriageAction, 0, len(value))
		for name, item := range value {
			description := ""
			if actionMap, ok := item.(map[string]any); ok {
				description, _, _ = unstructured.NestedString(actionMap, "description")
			}
			actions = append(actions, TriageAction{Name: name, Description: description})
		}
	default:
		return nil, fmt.Errorf(
			"Application %s/%s triage actions are %T, want array", namespace, applicationName, rawActions)
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].Name < actions[j].Name })
	return actions, nil
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
	if phase == "" {
		phase = "Pending"
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
	actions, foundActions, err := parseTriageActionReferences(obj.Object, "spec", "actions")
	if err != nil {
		return TriageRun{}, fmt.Errorf("failed to read TriageRun %s/%s actions: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	if !foundActions {
		legacyAction, _, legacyErr := unstructured.NestedString(obj.Object, "spec", "action")
		if legacyErr != nil {
			return TriageRun{}, fmt.Errorf(
				"failed to read TriageRun %s/%s legacy action: %w", obj.GetNamespace(), obj.GetName(), legacyErr)
		}
		if legacyAction == "" {
			legacyAction = DefaultTriageAction
		}
		actions = []TriageActionReference{{Name: legacyAction}}
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
		Actions:         actions,
		Phase:           phase,
		CreatedAt:       creationTimestamp.UTC().Format("2006-01-02T15:04:05Z07:00"),
		StartedAt:       startedAt,
		CompletedAt:     completedAt,
	}
	if len(actions) == 1 {
		run.Action = actions[0].Name
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

	actionStatusItems, found, err := unstructured.NestedSlice(obj.Object, "status", "actionStatuses")
	if err != nil {
		return TriageRun{}, fmt.Errorf("failed to read TriageRun %s/%s action statuses: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	if found {
		run.ActionStatuses = make([]TriageActionStatus, 0, len(actionStatusItems))
		for i, item := range actionStatusItems {
			statusMap, ok := item.(map[string]any)
			if !ok {
				return TriageRun{}, fmt.Errorf(
					"TriageRun %s/%s action status %d is %T, want object",
					obj.GetNamespace(), obj.GetName(), i, item)
			}
			status, err := parseTriageActionStatus(statusMap)
			if err != nil {
				return TriageRun{}, fmt.Errorf(
					"failed to parse TriageRun %s/%s action status %d: %w",
					obj.GetNamespace(), obj.GetName(), i, err)
			}
			run.ActionStatuses = append(run.ActionStatuses, status)
			run.Results = append(run.Results, status.Results...)
		}
	} else {
		legacyResults, resultErr := parseTriageCheckResults(obj.Object, "status", "results")
		if resultErr != nil {
			return TriageRun{}, fmt.Errorf(
				"failed to parse TriageRun %s/%s legacy results: %w",
				obj.GetNamespace(), obj.GetName(), resultErr)
		}
		run.Results = legacyResults
	}
	return run, nil
}

func parseTriageActionReferences(
	object map[string]any,
	fields ...string,
) ([]TriageActionReference, bool, error) {
	items, found, err := unstructured.NestedSlice(object, fields...)
	if err != nil || !found {
		return nil, found, err
	}
	actions := make([]TriageActionReference, 0, len(items))
	for i, item := range items {
		switch value := item.(type) {
		case map[string]any:
			name, _, nameErr := unstructured.NestedString(value, "name")
			if nameErr != nil {
				return nil, true, fmt.Errorf("action %d name: %w", i, nameErr)
			}
			actions = append(actions, TriageActionReference{Name: name})
		case string:
			// Read the original string-shaped selection during rolling upgrades.
			actions = append(actions, TriageActionReference{Name: value})
		default:
			return nil, true, fmt.Errorf("action %d is %T, want object", i, item)
		}
	}
	return actions, true, nil
}

func parseTriageActionStatus(item map[string]any) (TriageActionStatus, error) {
	status := TriageActionStatus{}
	var err error
	if status.Action, _, err = unstructured.NestedString(item, "action"); err != nil {
		return TriageActionStatus{}, err
	}
	if status.Phase, _, err = unstructured.NestedString(item, "phase"); err != nil {
		return TriageActionStatus{}, err
	}
	if status.JobName, _, err = unstructured.NestedString(item, "jobRef", "name"); err != nil {
		return TriageActionStatus{}, err
	}
	if status.StartedAt, _, err = unstructured.NestedString(item, "startedAt"); err != nil {
		return TriageActionStatus{}, err
	}
	if status.CompletedAt, _, err = unstructured.NestedString(item, "completedAt"); err != nil {
		return TriageActionStatus{}, err
	}
	if summary, found, summaryErr := unstructured.NestedMap(item, "summary"); summaryErr != nil {
		return TriageActionStatus{}, summaryErr
	} else if found {
		status.Summary, err = parseTriageRunSummary(summary)
		if err != nil {
			return TriageActionStatus{}, err
		}
	}
	status.Results, err = parseTriageCheckResults(item, "results")
	if err != nil {
		return TriageActionStatus{}, err
	}
	return status, nil
}

func parseTriageCheckResults(object map[string]any, fields ...string) ([]TriageCheckResult, error) {
	items, found, err := unstructured.NestedSlice(object, fields...)
	if err != nil || !found {
		return nil, err
	}
	results := make([]TriageCheckResult, 0, len(items))
	for i, item := range items {
		resultMap, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("result %d is %T, want object", i, item)
		}
		result, err := parseTriageCheckResult(resultMap)
		if err != nil {
			return nil, fmt.Errorf("result %d: %w", i, err)
		}
		results = append(results, result)
	}
	return results, nil
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
	if err := validateTriageApplicationName(request.ApplicationName); err != nil {
		return err
	}
	if request.Action != "" && len(request.Actions) > 0 {
		return errors.New("set either action or actions, not both")
	}
	seen := make(map[string]struct{})
	for i, action := range normalizedTriageActions(request) {
		if strings.TrimSpace(action.Name) == "" {
			return fmt.Errorf("triage action %d must not be empty", i)
		}
		if _, exists := seen[action.Name]; exists {
			return fmt.Errorf("triage action %q is selected more than once", action.Name)
		}
		seen[action.Name] = struct{}{}
	}
	return nil
}

func normalizedTriageActions(request TriageRunRequest) []TriageActionReference {
	if len(request.Actions) > 0 {
		return append([]TriageActionReference(nil), request.Actions...)
	}
	if request.Action != "" {
		return []TriageActionReference{{Name: request.Action}}
	}
	return []TriageActionReference{{Name: DefaultTriageAction}}
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
	actions := normalizedTriageActions(request)
	unstructuredActions := make([]any, len(actions))
	for i := range actions {
		unstructuredActions[i] = map[string]any{"name": actions[i].Name}
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
			"actions": unstructuredActions,
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
