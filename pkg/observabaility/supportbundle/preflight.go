package supportbundle

import (
	"context"
	"fmt"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Access is one permission the person running wsm needs.
type Access struct {
	Namespace string
	Verb      string
	Group     string
	Resource  string
}

func (a Access) String() string {
	resource := a.Resource
	if a.Group != "" {
		resource += "." + a.Group
	}
	scope := "cluster-wide"
	if a.Namespace != "" {
		scope = "in namespace " + a.Namespace
	}
	return fmt.Sprintf("%s %s %s", a.Verb, resource, scope)
}

// CreateAccess lists what `support-bundle create` needs from its caller.
func CreateAccess(inst *Installation, opts SetupOptions) []Access {
	ns := inst.Namespace
	var out []Access
	add := func(namespace, group, resource string, verbs ...string) {
		for _, v := range verbs {
			out = append(out, Access{Namespace: namespace, Verb: v, Group: group, Resource: resource})
		}
	}
	add(ns, "apps.wandb.com", "weightsandbiases", "get")
	if opts.CreateServiceAccount {
		add(ns, "", "serviceaccounts", "create", "get", "update")
	} else {
		add(ns, "", "serviceaccounts", "get")
	}
	add(ns, "rbac.authorization.k8s.io", "roles", "create", "get", "update")
	add(ns, "rbac.authorization.k8s.io", "rolebindings", "create", "get", "update")
	add("", "rbac.authorization.k8s.io", "clusterroles", "create", "get", "update")
	add("", "rbac.authorization.k8s.io", "clusterrolebindings", "create", "get", "update")
	if opts.IncludeTelemetry && inst.Telemetry != nil {
		add(inst.Telemetry.Namespace, "rbac.authorization.k8s.io", "roles", "create", "get", "update")
		add(inst.Telemetry.Namespace, "rbac.authorization.k8s.io", "rolebindings", "create", "get", "update")
	}
	add(ns, "", "configmaps", "create", "get", "list", "update", "delete")
	add(ns, "batch", "jobs", "create", "get", "delete")
	add(ns, "", "pods", "list")
	add(ns, "", "pods/log", "get")
	return out
}

// Preflight checks every Access with SelfSubjectAccessReview and returns an
// error listing all missing permissions at once.
func Preflight(ctx context.Context, cs kubernetes.Interface, required []Access) error {
	var missing []string
	for _, a := range required {
		resource, sub, _ := strings.Cut(a.Resource, "/")
		review := &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: a.Namespace, Verb: a.Verb, Group: a.Group, Resource: resource, Subresource: sub,
			},
		}}
		resp, err := cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("check permissions: %w", err)
		}
		if !resp.Status.Allowed {
			missing = append(missing, "  - "+a.String())
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("your Kubernetes identity is missing permissions needed for support-bundle collection (see %s):\n%s",
			PermissionsDoc, strings.Join(missing, "\n"))
	}
	return nil
}
