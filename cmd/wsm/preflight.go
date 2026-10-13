package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	v2 "github.com/wandb/operator/api/v2"
	"github.com/wandb/operator/pkg/preflight"
	"github.com/wandb/wsm/pkg/kubectl"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"
)

func init() {
	rootCmd.AddCommand(PreflightCmd())
}

// PreflightCmd runs the operator's preflight checks client-side against a v2 CR
// file. Which checks run is driven by preflight.Checks: every registered CRField
// present in the CR triggers its check, so new upstream checks land on `go get`.
func PreflightCmd() *cobra.Command {
	var (
		kubeContext string
		namespace   string
	)

	cmd := &cobra.Command{
		Use:   "preflight <cr-file>",
		Short: "Run the operator's preflight checks against a v2 WeightsAndBiases CR file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if kubeContext != "" {
				kubectl.SetContext(kubeContext)
			}
			cr, err := readCRObject(args[0])
			if err != nil {
				return err
			}
			runs := runPreflights(cmd.Context(), cr, namespace)
			printPreflightResults(runs)
			return preflightError(runs)
		},
	}

	cmd.Flags().StringVar(&kubeContext, "context", "", "Kubeconfig context used to resolve Secret-backed values (valueFrom.secretKeyRef)")
	cmd.Flags().StringVar(&namespace, "wandb-namespace", "", "Namespace holding referenced Secrets (default: the CR's metadata.namespace)")

	return cmd
}

type preflightRun struct {
	FieldPath string
	Result    preflight.Result
}

func readCRObject(path string) (*unstructured.Unstructured, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read CR file: %w", err)
	}
	obj := map[string]any{}
	if err := sigsyaml.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("failed to parse CR YAML from %s: %w", path, err)
	}
	cr := &unstructured.Unstructured{Object: obj}
	if cr.GetAPIVersion() != "apps.wandb.com/v2" {
		return nil, fmt.Errorf("%s is %q, preflight only supports apps.wandb.com/v2", path, cr.GetAPIVersion())
	}
	return cr, nil
}

func runPreflights(ctx context.Context, cr *unstructured.Unstructured, namespace string) []preflightRun {
	if namespace == "" {
		namespace = cr.GetNamespace()
	}
	skip := preflight.ParseSkipList(cr.GetAnnotations()[preflight.SkipPreflightsAnnotation])
	resolve := secretResolver(namespace)

	names := make([]string, 0, len(preflight.Checks))
	for name := range preflight.Checks {
		names = append(names, name)
	}
	sort.Strings(names)

	var runs []preflightRun
	for _, name := range names {
		check := preflight.Checks[name]
		for _, m := range matchCRField(cr.Object, strings.Split(check.CRField, "."), "") {
			run := preflightRun{FieldPath: m.path}
			switch spec := check.NewSpec(); {
			case skip[name]:
				run.Result = preflight.Result{Check: name, Outcome: preflight.OutcomeSkipped}
			case decodeInto(m.value, spec) != nil:
				run.Result = preflight.Result{Check: name, Outcome: preflight.OutcomeUnknown, Message: fmt.Sprintf("cannot decode %s", m.path)}
			default:
				run.Result = check.Run(ctx, spec, resolve)
			}
			runs = append(runs, run)
		}
	}
	return runs
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

// secretResolver mirrors the operator's external.ResolveValue (internal, not importable):
// a literal wins, otherwise the Secret key is read from the CR's namespace.
func secretResolver(namespace string) preflight.ValueResolver {
	return func(_ context.Context, v v2.ValueOrSecret) (string, error) {
		if v.Value != "" {
			return v.Value, nil
		}
		ref := v.SecretKeyRef()
		if ref == nil {
			return "", nil
		}
		data, err := kubectl.GetSecretData(ref.Name, namespace, ref.Key)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
}

func printPreflightResults(runs []preflightRun) {
	if len(runs) == 0 {
		fmt.Println("No preflight checks apply to this CR.")
		return
	}
	for _, r := range runs {
		line := fmt.Sprintf("[%s] %s (%s)", strings.ToUpper(string(r.Result.Outcome)), r.Result.Check, r.FieldPath)
		if r.Result.Message != "" {
			line += ": " + r.Result.Message
		}
		fmt.Println(line)
	}
}

func preflightError(runs []preflightRun) error {
	var failed []string
	for _, r := range runs {
		if r.Result.Outcome == preflight.OutcomeFail {
			failed = append(failed, r.Result.Check+" ("+r.FieldPath+")")
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d preflight check(s) failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}
