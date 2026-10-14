package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	v2 "github.com/wandb/operator/api/v2"
	oppreflight "github.com/wandb/operator/pkg/preflight"
	"github.com/wandb/wsm/pkg/kubectl"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"
)

// CheckRun is one registered check run against one CR field it matched.
type CheckRun struct {
	FieldPath string
	Result    oppreflight.Result
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

// RunChecks runs every registered check whose CRField is present in cr, so new
// upstream checks land on `go get`. An empty namespace defaults to the CR's
// metadata.namespace; a nil resolve reads Secrets from it via wsm's kubectl client.
func RunChecks(ctx context.Context, cr *unstructured.Unstructured, namespace string, resolve oppreflight.ValueResolver) []CheckRun {
	if namespace == "" {
		namespace = cr.GetNamespace()
	}
	if resolve == nil {
		resolve = SecretResolver(namespace)
	}
	skip := oppreflight.ParseSkipList(cr.GetAnnotations()[oppreflight.SkipPreflightsAnnotation])

	names := make([]string, 0, len(oppreflight.Checks))
	for name := range oppreflight.Checks {
		names = append(names, name)
	}
	sort.Strings(names)

	var runs []CheckRun
	for _, name := range names {
		check := oppreflight.Checks[name]
		for _, m := range matchCRField(cr.Object, strings.Split(check.CRField, "."), "") {
			run := CheckRun{FieldPath: m.path}
			switch spec := check.NewSpec(); {
			case skip[name]:
				run.Result = oppreflight.Result{Check: name, Outcome: oppreflight.OutcomeSkipped}
			case decodeInto(m.value, spec) != nil:
				run.Result = oppreflight.Result{Check: name, Outcome: oppreflight.OutcomeUnknown, Message: fmt.Sprintf("cannot decode %s", m.path)}
			default:
				run.Result = check.Run(ctx, spec, resolve)
			}
			runs = append(runs, run)
		}
	}
	return runs
}

// SecretResolver mirrors the operator's external.ResolveValue (internal, not
// importable): a literal wins, otherwise the Secret key is read from namespace.
// Each Secret is fetched once, since a connection's fields usually share one.
func SecretResolver(namespace string) oppreflight.ValueResolver {
	cache := map[string]map[string][]byte{}
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
		data, ok := cache[ref.Name]
		if !ok {
			_, cs, err := kubectl.GetClientset()
			if err != nil {
				return "", err
			}
			// ctx, not context.Background() as in kubectl.GetSecretData, so the
			// caller's timeout also bounds the Secret read.
			secret, err := cs.CoreV1().Secrets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			data = secret.Data
			cache[ref.Name] = data
		}
		value, ok := data[ref.Key]
		if !ok {
			return "", fmt.Errorf("secret %s/%s does not contain key %q", namespace, ref.Name, ref.Key)
		}
		return string(value), nil
	}
}

// Err reports failed and inconclusive (unknown) runs; nil means the CR passed.
func Err(runs []CheckRun) error {
	var failed, inconclusive []string
	for _, r := range runs {
		label := r.Result.Check + " (" + r.FieldPath + ")"
		switch r.Result.Outcome {
		case oppreflight.OutcomeFail:
			failed = append(failed, label)
		case oppreflight.OutcomeUnknown:
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
