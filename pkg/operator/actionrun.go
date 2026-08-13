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
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

const (
	DefaultActionName = "default"

	actionRunManagedByLabel = "app.kubernetes.io/managed-by"
	actionRunManagedByValue = "wsm"

	// Leave room below the Kubernetes 253-character object-name limit for the
	// API server's generated suffix.
	maxActionRunGenerateNameLength = 240
)

type ActionType string

const (
	ActionTypeTriage      ActionType = "triage"
	ActionTypeMaintenance ActionType = "maintenance"
)

var ErrActionRunNotTerminal = errors.New("ActionRun is not terminal")

var actionRunsV2GVR = schema.GroupVersionResource{
	Group:    "apps.wandb.com",
	Version:  "v2",
	Resource: "actionruns",
}

var applicationsV2GVR = schema.GroupVersionResource{
	Group:    "apps.wandb.com",
	Version:  "v2",
	Resource: "applications",
}

// Action is one action advertised by an Application catalog.
type Action struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ActionReference selects an advertised Application action by name.
type ActionReference struct {
	Name string `json:"name"`
}

// ActionRunRequest describes one immutable request to execute one Application
// action. Namespace, ApplicationName, and Type are required. An empty action
// name selects "default".
type ActionRunRequest struct {
	Namespace       string          `json:"namespace"`
	ApplicationName string          `json:"applicationName"`
	Type            ActionType      `json:"type"`
	Action          ActionReference `json:"action"`
}

// ActionRunRef identifies a newly created ActionRun.
type ActionRunRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// ListActionRunsRequest scopes run history to one namespace and optionally to
// one action type and Application.
type ListActionRunsRequest struct {
	Namespace       string     `json:"namespace"`
	Type            ActionType `json:"type,omitempty"`
	ApplicationName string     `json:"applicationName,omitempty"`
}

// ActionRunSummary contains aggregate result counts reported by the operator.
type ActionRunSummary struct {
	Total           int64  `json:"total,omitempty"`
	Pass            int64  `json:"pass,omitempty"`
	Warn            int64  `json:"warn,omitempty"`
	Fail            int64  `json:"fail,omitempty"`
	Error           int64  `json:"error,omitempty"`
	OverallSeverity string `json:"overallSeverity,omitempty"`
}

// ActionResult is one structured result reported by the application-owned
// action command.
type ActionResult struct {
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

// ActionRun is the caller-facing view of an immutable ActionRun and its latest
// operator-reported status.
type ActionRun struct {
	Namespace       string            `json:"namespace"`
	Name            string            `json:"name"`
	Type            ActionType        `json:"type"`
	ApplicationName string            `json:"applicationName"`
	Action          ActionReference   `json:"action"`
	Phase           string            `json:"phase,omitempty"`
	JobName         string            `json:"jobName,omitempty"`
	CreatedAt       string            `json:"createdAt,omitempty"`
	StartedAt       string            `json:"startedAt,omitempty"`
	CompletedAt     string            `json:"completedAt,omitempty"`
	Summary         *ActionRunSummary `json:"summary,omitempty"`
	Results         []ActionResult    `json:"results,omitempty"`
}

// CreateActionRun creates a fresh ActionRun through wsm's configured dynamic
// Kubernetes client. It always uses generateName so repeated calls represent
// distinct execution requests.
func CreateActionRun(ctx context.Context, request ActionRunRequest) (ActionRunRef, error) {
	_, dynamicClient, err := kubectl.GetDynamicClientset()
	if err != nil {
		return ActionRunRef{}, err
	}
	return createActionRun(ctx, dynamicClient, request)
}

// ListActions returns sorted action metadata from the selected Application
// catalog. For example, type "triage" reads spec.triage.actions.
func ListActions(
	ctx context.Context,
	namespace string,
	applicationName string,
	actionType ActionType,
) ([]Action, error) {
	_, dynamicClient, err := kubectl.GetDynamicClientset()
	if err != nil {
		return nil, err
	}
	return listActionsWithClient(ctx, dynamicClient, namespace, applicationName, actionType)
}

// ListActionRuns returns newest-first run history in one namespace. Type and
// ApplicationName use CRD selectable fields and are also checked client-side.
func ListActionRuns(ctx context.Context, request ListActionRunsRequest) ([]ActionRun, error) {
	_, dynamicClient, err := kubectl.GetDynamicClientset()
	if err != nil {
		return nil, err
	}
	return listActionRuns(ctx, dynamicClient, request)
}

// GetActionRun returns one run and its latest status.
func GetActionRun(ctx context.Context, namespace, name string) (ActionRun, error) {
	_, dynamicClient, err := kubectl.GetDynamicClientset()
	if err != nil {
		return ActionRun{}, err
	}
	return getActionRun(ctx, dynamicClient, namespace, name)
}

// DeleteActionRun removes a completed run. Pending and Running runs are not
// deleted because deletion would implicitly cancel their owned Job.
func DeleteActionRun(ctx context.Context, namespace, name string) error {
	_, dynamicClient, err := kubectl.GetDynamicClientset()
	if err != nil {
		return err
	}
	return deleteActionRun(ctx, dynamicClient, namespace, name)
}

func createActionRun(
	ctx context.Context,
	dynamicClient dynamic.Interface,
	request ActionRunRequest,
) (ActionRunRef, error) {
	if err := validateActionRunRequest(request); err != nil {
		return ActionRunRef{}, err
	}
	request.Action = normalizedAction(request.Action)

	created, err := dynamicClient.Resource(actionRunsV2GVR).Namespace(request.Namespace).Create(
		ctx,
		newActionRun(request),
		metav1.CreateOptions{FieldManager: "wsm"},
	)
	if err != nil {
		return ActionRunRef{}, fmt.Errorf(
			"failed to create ActionRun for Application %s/%s: %w",
			request.Namespace,
			request.ApplicationName,
			err,
		)
	}

	return ActionRunRef{Namespace: created.GetNamespace(), Name: created.GetName()}, nil
}

func listActionsWithClient(
	ctx context.Context,
	dynamicClient dynamic.Interface,
	namespace string,
	applicationName string,
	actionType ActionType,
) ([]Action, error) {
	if err := validateActionRunNamespace(namespace); err != nil {
		return nil, err
	}
	if err := validateActionApplicationName(applicationName); err != nil {
		return nil, err
	}
	if err := validateActionType(actionType); err != nil {
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
		application.Object, "spec", string(actionType), "actions")
	if err != nil {
		return nil, fmt.Errorf(
			"failed to read Application %s/%s %s actions: %w",
			namespace, applicationName, actionType, err)
	}
	if !found {
		return []Action{}, nil
	}
	actionItems, ok := rawActions.([]any)
	if !ok {
		return nil, fmt.Errorf(
			"application %s/%s %s actions are %T, want array",
			namespace, applicationName, actionType, rawActions)
	}
	actions := make([]Action, 0, len(actionItems))
	for i, item := range actionItems {
		actionMap, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf(
				"application %s/%s %s action %d is %T, want object",
				namespace, applicationName, actionType, i, item)
		}
		name, _, nameErr := unstructured.NestedString(actionMap, "name")
		if nameErr != nil {
			return nil, fmt.Errorf(
				"application %s/%s %s action %d has invalid name: %w",
				namespace, applicationName, actionType, i, nameErr)
		}
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf(
				"application %s/%s %s action %d has an empty name",
				namespace, applicationName, actionType, i)
		}
		description, _, descriptionErr := unstructured.NestedString(actionMap, "description")
		if descriptionErr != nil {
			return nil, fmt.Errorf(
				"application %s/%s %s action %q has invalid description: %w",
				namespace, applicationName, actionType, name, descriptionErr)
		}
		actions = append(actions, Action{Name: name, Description: description})
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].Name < actions[j].Name })
	return actions, nil
}

func listActionRuns(
	ctx context.Context,
	dynamicClient dynamic.Interface,
	request ListActionRunsRequest,
) ([]ActionRun, error) {
	if err := validateActionRunNamespace(request.Namespace); err != nil {
		return nil, err
	}
	selectors := make([]fields.Selector, 0, 2)
	if request.Type != "" {
		if err := validateActionType(request.Type); err != nil {
			return nil, err
		}
		selectors = append(selectors, fields.OneTermEqualSelector("spec.type", string(request.Type)))
	}
	if request.ApplicationName != "" {
		if err := validateActionApplicationName(request.ApplicationName); err != nil {
			return nil, err
		}
		selectors = append(selectors,
			fields.OneTermEqualSelector("spec.applicationRef.name", request.ApplicationName))
	}
	options := metav1.ListOptions{}
	if len(selectors) > 0 {
		options.FieldSelector = fields.AndSelectors(selectors...).String()
	}

	list, err := dynamicClient.Resource(actionRunsV2GVR).Namespace(request.Namespace).List(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("failed to list ActionRuns in namespace %s: %w", request.Namespace, err)
	}

	runs := make([]ActionRun, 0, len(list.Items))
	for i := range list.Items {
		run, err := parseActionRun(&list.Items[i])
		if err != nil {
			return nil, err
		}
		if request.Type != "" && run.Type != request.Type {
			continue
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

func getActionRun(
	ctx context.Context,
	dynamicClient dynamic.Interface,
	namespace string,
	name string,
) (ActionRun, error) {
	if err := validateActionRunIdentity(namespace, name); err != nil {
		return ActionRun{}, err
	}
	obj, err := dynamicClient.Resource(actionRunsV2GVR).Namespace(namespace).Get(
		ctx,
		name,
		metav1.GetOptions{},
	)
	if err != nil {
		return ActionRun{}, fmt.Errorf("failed to get ActionRun %s/%s: %w", namespace, name, err)
	}
	return parseActionRun(obj)
}

func deleteActionRun(
	ctx context.Context,
	dynamicClient dynamic.Interface,
	namespace string,
	name string,
) error {
	if err := validateActionRunIdentity(namespace, name); err != nil {
		return err
	}
	obj, err := dynamicClient.Resource(actionRunsV2GVR).Namespace(namespace).Get(
		ctx,
		name,
		metav1.GetOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to get ActionRun %s/%s before deletion: %w", namespace, name, err)
	}
	phase, _, err := unstructured.NestedString(obj.Object, "status", "phase")
	if err != nil {
		return fmt.Errorf("failed to read ActionRun %s/%s phase: %w", namespace, name, err)
	}
	if phase == "" {
		phase = "Pending"
	}
	if phase != "Succeeded" && phase != "Failed" {
		return fmt.Errorf("%w: %s/%s has phase %q", ErrActionRunNotTerminal, namespace, name, phase)
	}

	uid := obj.GetUID()
	if err := dynamicClient.Resource(actionRunsV2GVR).Namespace(namespace).Delete(
		ctx,
		name,
		metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}},
	); err != nil {
		return fmt.Errorf("failed to delete ActionRun %s/%s: %w", namespace, name, err)
	}
	return nil
}

func parseActionRun(obj *unstructured.Unstructured) (ActionRun, error) {
	actionType, _, err := unstructured.NestedString(obj.Object, "spec", "type")
	if err != nil {
		return ActionRun{}, fmt.Errorf("failed to read ActionRun %s/%s type: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	if err := validateActionType(ActionType(actionType)); err != nil {
		return ActionRun{}, fmt.Errorf("failed to read ActionRun %s/%s type: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	applicationName, _, err := unstructured.NestedString(
		obj.Object, "spec", "applicationRef", "name")
	if err != nil {
		return ActionRun{}, fmt.Errorf("failed to read ActionRun %s/%s application: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	actionName, _, err := unstructured.NestedString(obj.Object, "spec", "action", "name")
	if err != nil {
		return ActionRun{}, fmt.Errorf("failed to read ActionRun %s/%s action: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	if strings.TrimSpace(actionName) == "" {
		return ActionRun{}, fmt.Errorf("failed to read ActionRun %s/%s action: name is required", obj.GetNamespace(), obj.GetName())
	}
	phase, _, err := unstructured.NestedString(obj.Object, "status", "phase")
	if err != nil {
		return ActionRun{}, fmt.Errorf("failed to read ActionRun %s/%s phase: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	if phase == "" {
		phase = "Pending"
	}
	jobName, _, err := unstructured.NestedString(obj.Object, "status", "jobRef", "name")
	if err != nil {
		return ActionRun{}, fmt.Errorf("failed to read ActionRun %s/%s job: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	startedAt, _, err := unstructured.NestedString(obj.Object, "status", "startedAt")
	if err != nil {
		return ActionRun{}, fmt.Errorf("failed to read ActionRun %s/%s startedAt: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	completedAt, _, err := unstructured.NestedString(obj.Object, "status", "completedAt")
	if err != nil {
		return ActionRun{}, fmt.Errorf("failed to read ActionRun %s/%s completedAt: %w", obj.GetNamespace(), obj.GetName(), err)
	}

	creationTimestamp := obj.GetCreationTimestamp()
	run := ActionRun{
		Namespace:       obj.GetNamespace(),
		Name:            obj.GetName(),
		Type:            ActionType(actionType),
		ApplicationName: applicationName,
		Action:          ActionReference{Name: actionName},
		Phase:           phase,
		JobName:         jobName,
		CreatedAt:       creationTimestamp.UTC().Format("2006-01-02T15:04:05Z07:00"),
		StartedAt:       startedAt,
		CompletedAt:     completedAt,
	}
	if creationTimestamp.IsZero() {
		run.CreatedAt = ""
	}

	summaryMap, found, err := unstructured.NestedMap(obj.Object, "status", "summary")
	if err != nil {
		return ActionRun{}, fmt.Errorf("failed to read ActionRun %s/%s summary: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	if found {
		run.Summary, err = parseActionRunSummary(summaryMap)
		if err != nil {
			return ActionRun{}, fmt.Errorf("failed to parse ActionRun %s/%s summary: %w", obj.GetNamespace(), obj.GetName(), err)
		}
	}
	run.Results, err = parseActionResults(obj.Object, "status", "results")
	if err != nil {
		return ActionRun{}, fmt.Errorf("failed to parse ActionRun %s/%s results: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	return run, nil
}

func parseActionResults(object map[string]any, fields ...string) ([]ActionResult, error) {
	items, found, err := unstructured.NestedSlice(object, fields...)
	if err != nil || !found {
		return nil, err
	}
	results := make([]ActionResult, 0, len(items))
	for i, item := range items {
		resultMap, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("result %d is %T, want object", i, item)
		}
		result, err := parseActionResult(resultMap)
		if err != nil {
			return nil, fmt.Errorf("result %d: %w", i, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func parseActionRunSummary(summary map[string]any) (*ActionRunSummary, error) {
	result := &ActionRunSummary{}
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

func parseActionResult(item map[string]any) (ActionResult, error) {
	result := ActionResult{}
	var err error
	if result.Name, _, err = unstructured.NestedString(item, "name"); err != nil {
		return ActionResult{}, err
	}
	if result.Umbrella, _, err = unstructured.NestedString(item, "umbrella"); err != nil {
		return ActionResult{}, err
	}
	if result.Severity, _, err = unstructured.NestedString(item, "severity"); err != nil {
		return ActionResult{}, err
	}
	if result.Message, _, err = unstructured.NestedString(item, "message"); err != nil {
		return ActionResult{}, err
	}
	if result.Remediation, _, err = unstructured.NestedString(item, "remediation"); err != nil {
		return ActionResult{}, err
	}
	if result.StartedAt, _, err = unstructured.NestedString(item, "startedAt"); err != nil {
		return ActionResult{}, err
	}
	if result.EndedAt, _, err = unstructured.NestedString(item, "endedAt"); err != nil {
		return ActionResult{}, err
	}
	if result.DurationMilliseconds, _, err = unstructured.NestedInt64(item, "durationMs"); err != nil {
		return ActionResult{}, err
	}
	if evidence, ok := item["evidence"]; ok {
		result.Evidence = evidence
	}
	return result, nil
}

func validateActionRunRequest(request ActionRunRequest) error {
	if err := validateActionRunNamespace(request.Namespace); err != nil {
		return err
	}
	if err := validateActionApplicationName(request.ApplicationName); err != nil {
		return err
	}
	if err := validateActionType(request.Type); err != nil {
		return err
	}
	if request.Action.Name != "" && strings.TrimSpace(request.Action.Name) == "" {
		return errors.New("action name must not be empty")
	}
	return nil
}

func normalizedAction(action ActionReference) ActionReference {
	if action.Name == "" {
		action.Name = DefaultActionName
	}
	return action
}

func validateActionType(actionType ActionType) error {
	switch actionType {
	case ActionTypeTriage, ActionTypeMaintenance:
		return nil
	case "":
		return errors.New("action type is required")
	default:
		return fmt.Errorf("unsupported action type %q", actionType)
	}
}

func validateActionRunIdentity(namespace, name string) error {
	if err := validateActionRunNamespace(namespace); err != nil {
		return err
	}
	if name == "" {
		return errors.New("ActionRun name is required")
	}
	if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
		return fmt.Errorf("invalid ActionRun name %q: %s", name, strings.Join(problems, "; "))
	}
	return nil
}

func validateActionRunNamespace(namespace string) error {
	if namespace == "" {
		return errors.New("namespace is required to access ActionRuns")
	}
	if problems := validation.IsDNS1123Label(namespace); len(problems) > 0 {
		return fmt.Errorf("invalid ActionRun namespace %q: %s", namespace, strings.Join(problems, "; "))
	}
	return nil
}

func validateActionApplicationName(applicationName string) error {
	if applicationName == "" {
		return errors.New("application name is required to access ActionRuns")
	}
	if problems := validation.IsDNS1123Subdomain(applicationName); len(problems) > 0 {
		return fmt.Errorf("invalid Application name %q: %s", applicationName, strings.Join(problems, "; "))
	}
	return nil
}

func newActionRun(request ActionRunRequest) *unstructured.Unstructured {
	action := normalizedAction(request.Action)
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps.wandb.com/v2",
		"kind":       "ActionRun",
		"metadata": map[string]any{
			"generateName": actionRunGenerateName(request.ApplicationName, request.Type),
			"namespace":    request.Namespace,
			"labels": map[string]any{
				actionRunManagedByLabel: actionRunManagedByValue,
			},
		},
		"spec": map[string]any{
			"type": string(request.Type),
			"applicationRef": map[string]any{
				"name": request.ApplicationName,
			},
			"action": map[string]any{"name": action.Name},
		},
	}}
}

func actionRunGenerateName(applicationName string, actionType ActionType) string {
	suffix := "-" + string(actionType) + "-"
	maxApplicationLength := maxActionRunGenerateNameLength - len(suffix)
	if len(applicationName) > maxApplicationLength {
		applicationName = strings.TrimRight(applicationName[:maxApplicationLength], "-.")
	}
	return applicationName + suffix
}
