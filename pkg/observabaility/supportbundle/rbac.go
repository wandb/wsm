package supportbundle

import (
	"context"
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

// PermissionsDoc is where authorization failures point users.
const PermissionsDoc = "docs/operations/support-bundles.md#permissions"

// namespaceRules are what Lumen's clusterResources and logs collectors read in
// the installation namespace. Secrets are deliberately absent.
var namespaceRules = []rbacv1.PolicyRule{
	{APIGroups: []string{""}, Resources: []string{
		"pods", "events", "services", "endpoints", "configmaps", "serviceaccounts",
		"persistentvolumeclaims", "limitranges", "resourcequotas", "replicationcontrollers",
	}, Verbs: []string{"get", "list"}},
	{APIGroups: []string{""}, Resources: []string{"pods/log"}, Verbs: []string{"get"}},
	{APIGroups: []string{"apps"}, Resources: []string{"deployments", "replicasets", "statefulsets", "daemonsets"}, Verbs: []string{"get", "list"}},
	{APIGroups: []string{"batch"}, Resources: []string{"jobs", "cronjobs"}, Verbs: []string{"get", "list"}},
	{APIGroups: []string{"autoscaling"}, Resources: []string{"horizontalpodautoscalers"}, Verbs: []string{"list"}},
	{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingresses", "networkpolicies"}, Verbs: []string{"list"}},
	{APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"list"}},
	{APIGroups: []string{"policy"}, Resources: []string{"poddisruptionbudgets"}, Verbs: []string{"list"}},
	{APIGroups: []string{"rbac.authorization.k8s.io"}, Resources: []string{"roles", "rolebindings"}, Verbs: []string{"list"}},
	{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: []string{"list"}},
	{APIGroups: []string{"apps.wandb.com"}, Resources: []string{"weightsandbiases"}, Verbs: []string{"get", "list"}},
}

// clusterRules are the cluster-scoped reads of clusterResources/clusterInfo
// plus the access reviews Lumen runs as a preflight.
var clusterRules = []rbacv1.PolicyRule{
	{APIGroups: []string{""}, Resources: []string{"namespaces", "nodes", "persistentvolumes"}, Verbs: []string{"get", "list"}},
	{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"storageclasses", "volumeattachments"}, Verbs: []string{"list"}},
	{APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}, Verbs: []string{"list"}},
	{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingressclasses"}, Verbs: []string{"list"}},
	{APIGroups: []string{"scheduling.k8s.io"}, Resources: []string{"priorityclasses"}, Verbs: []string{"list"}},
	{APIGroups: []string{"rbac.authorization.k8s.io"}, Resources: []string{"clusterroles", "clusterrolebindings"}, Verbs: []string{"list"}},
	{APIGroups: []string{"admissionregistration.k8s.io"}, Resources: []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"}, Verbs: []string{"list"}},
	{APIGroups: []string{"certificates.k8s.io"}, Resources: []string{"certificatesigningrequests"}, Verbs: []string{"list"}},
	{APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"selfsubjectaccessreviews", "selfsubjectrulesreviews"}, Verbs: []string{"create"}},
}

// telemetryRules let the victoriaStack collector run its curl helper pod.
var telemetryRules = []rbacv1.PolicyRule{
	{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"create", "get", "list", "delete"}},
	{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create"}},
}

// SetupOptions selects the collector identity.
type SetupOptions struct {
	// ServiceAccount is the SA the Job runs as.
	ServiceAccount string
	// CreateServiceAccount is false when the user supplied a pre-authorized SA.
	CreateServiceAccount bool
	IncludeTelemetry     bool
}

// BuildServiceAccount returns the dedicated collector ServiceAccount.
func BuildServiceAccount(inst *Installation, name string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   inst.Namespace,
			Labels:      setupLabels(inst.Namespace, inst.Name),
			Annotations: inst.IdentityAnnotations,
		},
		ImagePullSecrets: inst.ImagePullSecrets,
	}
}

func subjects(inst *Installation, sa string) []rbacv1.Subject {
	return []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa, Namespace: inst.Namespace}}
}

// BuildRBAC returns the Roles/Bindings for the collector identity.
func BuildRBAC(inst *Installation, opts SetupOptions) ([]*rbacv1.Role, []*rbacv1.RoleBinding, *rbacv1.ClusterRole, *rbacv1.ClusterRoleBinding) {
	labels := setupLabels(inst.Namespace, inst.Name)
	nsName := fitName("wsm-lumen-"+inst.Name, 63)

	roles := []*rbacv1.Role{{
		ObjectMeta: metav1.ObjectMeta{Name: nsName, Namespace: inst.Namespace, Labels: labels},
		Rules:      namespaceRules,
	}}
	bindings := []*rbacv1.RoleBinding{{
		ObjectMeta: metav1.ObjectMeta{Name: nsName, Namespace: inst.Namespace, Labels: labels},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: nsName},
		Subjects:   subjects(inst, opts.ServiceAccount),
	}}

	if opts.IncludeTelemetry && inst.Telemetry != nil {
		telName := fitName("wsm-lumen-"+inst.Name+"-telemetry", 63)
		roles = append(roles, &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: telName, Namespace: inst.Telemetry.Namespace, Labels: labels},
			Rules:      telemetryRules,
		})
		bindings = append(bindings, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: telName, Namespace: inst.Telemetry.Namespace, Labels: labels},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: telName},
			Subjects:   subjects(inst, opts.ServiceAccount),
		})
	}

	clusterName := clusterScopedName(inst.Namespace, inst.Name)
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName, Labels: labels},
		Rules:      clusterRules,
	}
	clusterBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName, Labels: labels},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterName},
		Subjects:   subjects(inst, opts.ServiceAccount),
	}
	return roles, bindings, clusterRole, clusterBinding
}

// EnsureSetup creates or updates the collector identity and RBAC. Existing
// objects are updated only when they carry this installation's wsm labels;
// anything else is refused rather than adopted.
func EnsureSetup(ctx context.Context, cs kubernetes.Interface, inst *Installation, opts SetupOptions) error {
	key := installationKey(inst.Namespace, inst.Name)

	if opts.CreateServiceAccount {
		sa := BuildServiceAccount(inst, opts.ServiceAccount)
		client := cs.CoreV1().ServiceAccounts(inst.Namespace)
		if err := ensureOwned(ctx, "ServiceAccount", key, sa, client.Create, client.Get, client.Update,
			func(dst, src *corev1.ServiceAccount) {
				dst.Annotations = mergeAnnotations(dst.Annotations, src.Annotations)
				dst.ImagePullSecrets = src.ImagePullSecrets
			}); err != nil {
			return err
		}
	} else if _, err := cs.CoreV1().ServiceAccounts(inst.Namespace).Get(ctx, opts.ServiceAccount, metav1.GetOptions{}); err != nil {
		return apiError("get", "ServiceAccount", inst.Namespace, opts.ServiceAccount, err)
	}

	roles, bindings, clusterRole, clusterBinding := BuildRBAC(inst, opts)
	for _, role := range roles {
		client := cs.RbacV1().Roles(role.Namespace)
		if err := ensureOwned(ctx, "Role", key, role, client.Create, client.Get, client.Update,
			func(dst, src *rbacv1.Role) { dst.Rules = src.Rules }); err != nil {
			return err
		}
	}
	for _, binding := range bindings {
		client := cs.RbacV1().RoleBindings(binding.Namespace)
		if err := ensureOwned(ctx, "RoleBinding", key, binding, client.Create, client.Get, client.Update,
			func(dst, src *rbacv1.RoleBinding) { dst.Subjects = src.Subjects }); err != nil {
			return err
		}
	}
	crClient := cs.RbacV1().ClusterRoles()
	if err := ensureOwned(ctx, "ClusterRole", key, clusterRole, crClient.Create, crClient.Get, crClient.Update,
		func(dst, src *rbacv1.ClusterRole) { dst.Rules = src.Rules }); err != nil {
		return err
	}
	crbClient := cs.RbacV1().ClusterRoleBindings()
	return ensureOwned(ctx, "ClusterRoleBinding", key, clusterBinding, crbClient.Create, crbClient.Get, crbClient.Update,
		func(dst, src *rbacv1.ClusterRoleBinding) { dst.Subjects = src.Subjects })
}

// mergeAnnotations keeps existing annotations (e.g. ones added by an admin)
// and overlays the identity annotations wsm manages.
func mergeAnnotations(existing, desired map[string]string) map[string]string {
	if len(desired) == 0 {
		return existing
	}
	out := map[string]string{}
	maps.Copy(out, existing)
	maps.Copy(out, desired)
	return out
}

func ensureOwned[T metav1.Object](
	ctx context.Context,
	kind, key string,
	desired T,
	create func(context.Context, T, metav1.CreateOptions) (T, error),
	get func(context.Context, string, metav1.GetOptions) (T, error),
	update func(context.Context, T, metav1.UpdateOptions) (T, error),
	copySpec func(dst, src T),
) error {
	_, err := create(ctx, desired, metav1.CreateOptions{FieldManager: fieldManager})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return apiError("create", kind, desired.GetNamespace(), desired.GetName(), err)
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing, err := get(ctx, desired.GetName(), metav1.GetOptions{})
		if err != nil {
			return apiError("get", kind, desired.GetNamespace(), desired.GetName(), err)
		}
		if !ownedBy(existing.GetLabels(), key) {
			return fmt.Errorf("%s %s already exists and is not managed by wsm for this installation; refusing to modify it",
				kind, objectRef(desired.GetNamespace(), desired.GetName()))
		}
		copySpec(existing, desired)
		if _, err := update(ctx, existing, metav1.UpdateOptions{FieldManager: fieldManager}); err != nil {
			if apierrors.IsConflict(err) {
				return err
			}
			return apiError("update", kind, desired.GetNamespace(), desired.GetName(), err)
		}
		return nil
	})
}

func objectRef(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

// apiError wraps an API error; authorization failures get an actionable hint.
func apiError(verb, kind, namespace, name string, err error) error {
	if apierrors.IsForbidden(err) {
		return fmt.Errorf("permission denied: cannot %s %s %s (see %s): %w", verb, kind, objectRef(namespace, name), PermissionsDoc, err)
	}
	return fmt.Errorf("failed to %s %s %s: %w", verb, kind, objectRef(namespace, name), err)
}
