package supportbundle

import (
	"context"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func testRunOptions() RunOptions {
	return RunOptions{
		ID: "sb-abc123", Image: "reg/wandb/lumen:abc", ServiceAccount: DefaultServiceAccountName,
		Created: time.Date(2026, 8, 10, 14, 12, 0, 0, time.UTC), Window: time.Hour,
		Timeout: 30 * time.Minute, Retention: 24 * time.Hour, Sources: []string{SourcePodLogs},
	}
}

func TestBuildJobUsesOnlySecretRefs(t *testing.T) {
	inst := testInstallation("")
	owner := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "wsm-sb-sb-abc123", UID: "uid-1"}}
	job := BuildJob(inst, testRunOptions(), owner)

	if job.Name != "support-bundle-abc123" || len(job.OwnerReferences) != 1 || job.OwnerReferences[0].UID != "uid-1" {
		t.Errorf("job identity/owner wrong: %s %v", job.Name, job.OwnerReferences)
	}
	spec := job.Spec
	if *spec.BackoffLimit != 0 || *spec.ActiveDeadlineSeconds != 1800 || *spec.TTLSecondsAfterFinished != 86400 {
		t.Errorf("retry/deadline/ttl wrong: %v %v %v", *spec.BackoffLimit, *spec.ActiveDeadlineSeconds, *spec.TTLSecondsAfterFinished)
	}
	c := spec.Template.Spec.Containers[0]
	if c.Command[0] != "/app/bin/collector" || c.Args[0] != "agent" || !contains(c.Args, "--lumen-collection=false") || !contains(c.Args, "--support-bundle") {
		t.Errorf("command/args wrong: %v %v", c.Command, c.Args)
	}
	if c.TerminationMessagePolicy != corev1.TerminationMessageReadFile || c.Resources.Limits.Memory().IsZero() {
		t.Error("termination policy or limits missing")
	}
	for _, e := range c.Env {
		if strings.HasPrefix(e.Name, "WANDB_DATA") || e.Name == "AWS_REGION" {
			if e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
				t.Errorf("%s must be a secretKeyRef, got %+v", e.Name, e)
			}
		}
	}
	if spec.Template.Spec.ServiceAccountName != DefaultServiceAccountName || spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Error("pod identity/restart policy wrong")
	}
	if job.Labels[LabelBundleID] != "sb-abc123" || !ownedBy(job.Labels, installationKey("wandb", "wandb")) {
		t.Errorf("labels wrong: %v", job.Labels)
	}
}

func TestRBACHasNoSecretsOrWildcards(t *testing.T) {
	inst := testInstallation("forward")
	roles, bindings, clusterRole, clusterBinding := BuildRBAC(inst, SetupOptions{ServiceAccount: "wsm-lumen", IncludeTelemetry: true})
	if len(roles) != 2 || roles[1].Namespace != "wandb-telemetry" {
		t.Fatalf("expected namespace + telemetry roles, got %d", len(roles))
	}
	all := append([]rbacv1.PolicyRule{}, clusterRole.Rules...)
	for _, r := range roles {
		all = append(all, r.Rules...)
	}
	for _, rule := range all {
		for _, res := range rule.Resources {
			if res == "secrets" || res == "*" {
				t.Errorf("rule grants %q: %+v", res, rule)
			}
		}
		for _, g := range rule.APIGroups {
			if g == "*" {
				t.Errorf("wildcard api group: %+v", rule)
			}
		}
	}
	for _, b := range append(bindings, &rbacv1.RoleBinding{Subjects: clusterBinding.Subjects}) {
		if b.Subjects[0].Name != "wsm-lumen" || b.Subjects[0].Namespace != "wandb" {
			t.Errorf("binding subject wrong: %+v", b.Subjects)
		}
	}
}

func TestEnsureSetupIsIdempotentAndRefusesForeignObjects(t *testing.T) {
	ctx := context.Background()
	inst := testInstallation("")
	opts := SetupOptions{ServiceAccount: DefaultServiceAccountName, CreateServiceAccount: true}
	cs := fake.NewSimpleClientset()

	if err := EnsureSetup(ctx, cs, inst, opts); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSetup(ctx, cs, inst, opts); err != nil {
		t.Fatalf("second (concurrent/repeat) run must succeed: %v", err)
	}
	sa, _ := cs.CoreV1().ServiceAccounts("wandb").Get(ctx, DefaultServiceAccountName, metav1.GetOptions{})
	if sa.Annotations["eks.amazonaws.com/role-arn"] == "" || len(sa.ImagePullSecrets) != 1 {
		t.Errorf("service account missing identity/pull secrets: %+v", sa)
	}

	foreign := fake.NewSimpleClientset(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: DefaultServiceAccountName, Namespace: "wandb"}})
	err := EnsureSetup(ctx, foreign, inst, opts)
	if err == nil || !strings.Contains(err.Error(), "refusing to modify") {
		t.Fatalf("unlabelled existing SA must not be adopted, got %v", err)
	}

	other := testInstallation("")
	other.Name = "other"
	if err := EnsureSetup(ctx, cs, other, opts); err == nil {
		t.Fatal("SA owned by another installation must not be adopted")
	}
}

func TestEnsureSetupWithExistingServiceAccount(t *testing.T) {
	ctx := context.Background()
	inst := testInstallation("")
	opts := SetupOptions{ServiceAccount: "preauthorized"}
	if err := EnsureSetup(ctx, fake.NewSimpleClientset(), inst, opts); err == nil {
		t.Fatal("missing pre-authorized SA must error")
	}
	cs := fake.NewSimpleClientset(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "preauthorized", Namespace: "wandb"}})
	if err := EnsureSetup(ctx, cs, inst, opts); err != nil {
		t.Fatal(err)
	}
	sa, _ := cs.CoreV1().ServiceAccounts("wandb").Get(ctx, "preauthorized", metav1.GetOptions{})
	if len(sa.Labels) != 0 {
		t.Error("user-supplied SA must not be modified")
	}
}

func TestPreflightReportsAllMissing(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		attrs := review.Spec.ResourceAttributes
		review.Status.Allowed = attrs.Resource != "clusterroles" && attrs.Subresource != "log"
		return true, review, nil
	})
	err := Preflight(context.Background(), cs, CreateAccess(testInstallation(""), SetupOptions{CreateServiceAccount: true}))
	if err == nil {
		t.Fatal("expected missing permissions")
	}
	for _, want := range []string{"create clusterroles.rbac.authorization.k8s.io cluster-wide", "get pods/log in namespace wandb"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
