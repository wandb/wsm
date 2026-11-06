package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	v2 "github.com/wandb/operator/api/v2"
	operatorPreflight "github.com/wandb/operator/pkg/preflight"
	"github.com/wandb/operator/pkg/wandb/manifest"
	"github.com/wandb/wsm/pkg/kubectl"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	"oras.land/oras-go/v2/registry/remote/credentials"
	sigsyaml "sigs.k8s.io/yaml"
)

// CheckRun is one registered check run against one CR field it matched.
type CheckRun struct {
	FieldPath string
	Result    operatorPreflight.Result
}

// ReadCR parses CR YAML and requires apps.wandb.com/v2. Callers reading from a
// file add the path to the error.
func ReadCR(data []byte) (*unstructured.Unstructured, error) {
	obj := map[string]any{}
	if err := sigsyaml.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("failed to parse CR YAML: %w", err)
	}
	cr := &unstructured.Unstructured{Object: obj}
	if cr.GetAPIVersion() != "apps.wandb.com/v2" {
		return nil, fmt.Errorf("CR is %q, preflight only supports apps.wandb.com/v2", cr.GetAPIVersion())
	}
	return cr, nil
}

// PlannedRun is a registered check matched to one CR field, before it runs.
type PlannedRun struct {
	Check     operatorPreflight.Check
	FieldPath string
	value     any
}

// ExecOptions controls how ExecuteChecks runs a plan.
type ExecOptions struct {
	// Resolve resolves ValueOrSecret fields. Nil reads Secrets from the CR's
	// metadata.namespace via wsm's kubectl client, or rejects Secret references
	// when Offline is set.
	Resolve operatorPreflight.ValueResolver
	// Offline skips TypeCluster checks and never reads Secrets, so the cluster is
	// never contacted. Static and connection checks still run from this machine.
	Offline bool
	// Mapper answers TypeCluster checks. Nil uses wsm's kubectl discovery when the
	// plan has a cluster check to run.
	Mapper meta.RESTMapper
	// OperatorVersion is compared against the server manifest's
	// requiredOperatorVersion; empty skips the comparison.
	OperatorVersion string
	// RegistryAuth pulls the server manifest from a private registry; nil pulls
	// anonymously.
	RegistryAuth *manifest.RegistryAuth
}

// PlanChecks lists every registered check whose CRField is present in cr without
// running any, so new upstream checks land on `go get` and callers can show users
// what will run before anything touches the cluster.
func PlanChecks(cr *unstructured.Unstructured) []PlannedRun {
	names := make([]string, 0, len(operatorPreflight.Checks))
	for name := range operatorPreflight.Checks {
		names = append(names, name)
	}
	sort.Strings(names)

	var plan []PlannedRun
	for _, name := range names {
		check := operatorPreflight.Checks[name]
		for _, m := range matchCRField(cr.Object, strings.Split(check.CRField, "."), "") {
			plan = append(plan, PlannedRun{Check: check, FieldPath: m.path, value: m.value})
		}
	}
	return plan
}

// NeedsConfirmation reports whether wsm must ask the user before running a check.
func NeedsConfirmation(c operatorPreflight.Check) bool {
	return c.Requires(operatorPreflight.ReqResourceCreate)
}

// RequiringConfirmation returns the distinct checks in plan that would run under
// opts and need confirmation, in plan order.
func RequiringConfirmation(cr *unstructured.Unstructured, plan []PlannedRun, opts ExecOptions) []operatorPreflight.Check {
	skip := skipList(cr)
	seen := map[string]bool{}
	var out []operatorPreflight.Check
	for _, p := range plan {
		if seen[p.Check.Name] || skip[p.Check.Name] || !willRun(p.Check, opts) || !NeedsConfirmation(p.Check) {
			continue
		}
		seen[p.Check.Name] = true
		out = append(out, p.Check)
	}
	return out
}

func willRun(c operatorPreflight.Check, opts ExecOptions) bool {
	return !opts.Offline || c.Type != operatorPreflight.TypeCluster
}

// plansClusterCheck avoids discovery (and a cluster connection) when no cluster
// check will run.
func plansClusterCheck(plan []PlannedRun, opts ExecOptions) bool {
	for _, p := range plan {
		if p.Check.Type == operatorPreflight.TypeCluster && willRun(p.Check, opts) {
			return true
		}
	}
	return false
}

// ExecuteChecks runs a plan from PlanChecks. Confirmation is the caller's job:
// ExecuteChecks runs every check opts allows.
func ExecuteChecks(ctx context.Context, cr *unstructured.Unstructured, plan []PlannedRun, opts ExecOptions) []CheckRun {
	resolve := opts.Resolve
	switch {
	case resolve != nil:
	case opts.Offline:
		resolve = offlineResolver
	default:
		resolve = SecretResolver(cr.GetNamespace())
	}
	env := operatorPreflight.Env{
		Resolve:         resolve,
		Mapper:          opts.Mapper,
		OperatorVersion: opts.OperatorVersion,
		RegistryAuth:    opts.RegistryAuth,
	}
	if env.Mapper == nil && plansClusterCheck(plan, opts) {
		// Left nil on error: cluster checks then report unknown instead of aborting the run.
		if m, err := kubectl.RefreshRESTMapper(); err == nil {
			env.Mapper = m
		}
	}
	skip := skipList(cr)

	runs := make([]CheckRun, 0, len(plan))
	for _, p := range plan {
		name := p.Check.Name
		run := CheckRun{FieldPath: p.FieldPath}
		switch spec := p.Check.NewSpec(); {
		case skip[name]:
			run.Result = operatorPreflight.Result{Check: name, Outcome: operatorPreflight.OutcomeSkipped}
		case !willRun(p.Check, opts):
			run.Result = operatorPreflight.Result{Check: name, Outcome: operatorPreflight.OutcomeSkipped, Message: fmt.Sprintf("%s check not run offline", p.Check.Type)}
		case decodeInto(p.value, spec) != nil:
			run.Result = operatorPreflight.Result{Check: name, Outcome: operatorPreflight.OutcomeUnknown, Message: fmt.Sprintf("cannot decode %s", p.FieldPath)}
		default:
			run.Result = p.Check.Run(ctx, spec, env)
		}
		runs = append(runs, run)
	}
	return runs
}

// RunChecks plans and runs every matching check without confirmation.
func RunChecks(ctx context.Context, cr *unstructured.Unstructured, resolve operatorPreflight.ValueResolver) []CheckRun {
	return ExecuteChecks(ctx, cr, PlanChecks(cr), ExecOptions{Resolve: resolve})
}

// offlineResolver keeps checks off the cluster: literals resolve, and a Secret
// reference returns ErrSecretUnavailable, which checks report as skipped.
func offlineResolver(_ context.Context, v v2.ValueOrSecret) (string, error) {
	if v.Value != "" {
		return v.Value, nil
	}
	if ref := v.SecretKeyRef(); ref != nil {
		return "", fmt.Errorf("secret %s is not read offline: %w", ref.Name, operatorPreflight.ErrSecretUnavailable)
	}
	return "", nil
}

// DockerRegistryAuth uses the credentials from `docker login`, so a manifest in
// a private registry resolves the same way image pulls do on this machine. Nil
// (anonymous) when no Docker config is readable.
func DockerRegistryAuth() *manifest.RegistryAuth {
	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return nil
	}
	return &manifest.RegistryAuth{Credential: credentials.Credential(store)}
}

// SecretResolver mirrors the operator's external.ResolveValue (internal, not
// importable): a literal wins, otherwise the Secret key is read from namespace,
// and an unset field resolves to "" so the check decides whether it's required.
// Checks never read Secrets themselves; they only call the resolver they're
// handed, so new checks reuse this unchanged.
func SecretResolver(namespace string) operatorPreflight.ValueResolver {
	return resolverFor(namespace, kubectl.NewSecretReader(namespace))
}

func SecretResolverWithClient(namespace string, cs kubernetes.Interface) operatorPreflight.ValueResolver {
	return resolverFor(namespace, kubectl.NewSecretReaderWithClient(namespace, cs))
}

func resolverFor(namespace string, secrets *kubectl.SecretReader) operatorPreflight.ValueResolver {
	return func(ctx context.Context, v v2.ValueOrSecret) (string, error) {
		if v.Value != "" {
			return v.Value, nil
		}
		ref := v.SecretKeyRef()
		if ref == nil {
			return "", nil
		}
		if namespace == "" {
			return "", fmt.Errorf("secret %s is referenced but the CR has no metadata.namespace", ref.Name)
		}
		return secrets.Get(ctx, *ref)
	}
}

// Err reports failed and inconclusive (unknown) runs; nil means the CR passed.
func Err(runs []CheckRun) error {
	var failed, inconclusive []string
	for _, r := range runs {
		label := r.Result.Check + " (" + r.FieldPath + ")"
		switch r.Result.Outcome {
		case operatorPreflight.OutcomeFail:
			failed = append(failed, label)
		case operatorPreflight.OutcomeUnknown:
			inconclusive = append(inconclusive, label)
		}
	}
	var msgs []string
	if len(failed) > 0 {
		msgs = append(msgs, fmt.Sprintf("%d failed: %s", len(failed), strings.Join(failed, ", ")))
	}
	if len(inconclusive) > 0 {
		msgs = append(msgs, fmt.Sprintf("%d inconclusive: %s", len(inconclusive), strings.Join(inconclusive, ", ")))
	}
	if len(msgs) > 0 {
		return errors.New("preflight checks did not pass: " + strings.Join(msgs, "; "))
	}
	return nil
}

type crFieldMatch struct {
	path  string
	value any
}

// matchCRField walks a registry CRField path; "*" fans out over every instance
// key, so spec.mysql.*.externalMysql matches each instance that sets externalMysql.
func matchCRField(node any, segments []string, path string) []crFieldMatch {
	if len(segments) == 0 {
		if node == nil {
			return nil
		}
		return []crFieldMatch{{path: path, value: node}}
	}
	m, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	if segments[0] != "*" {
		return matchCRField(m[segments[0]], segments[1:], joinFieldPath(path, segments[0]))
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []crFieldMatch
	for _, k := range keys {
		out = append(out, matchCRField(m[k], segments[1:], joinFieldPath(path, k))...)
	}
	return out
}

func joinFieldPath(path, segment string) string {
	if path == "" {
		return segment
	}
	return path + "." + segment
}

func decodeInto(value, spec any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, spec)
}

// skipList is the CR's skipPreflights annotation parsed into check names; both
// RequiringConfirmation and ExecuteChecks use it so a skipped check is never
// confirmed nor run.
func skipList(cr *unstructured.Unstructured) map[string]bool {
	return operatorPreflight.ParseSkipList(cr.GetAnnotations()[operatorPreflight.SkipPreflightsAnnotation])
}
