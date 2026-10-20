package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	oppreflight "github.com/wandb/operator/pkg/preflight"
	"github.com/wandb/wsm/pkg/kubectl"
	"github.com/wandb/wsm/pkg/preflight"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func init() {
	rootCmd.AddCommand(PreflightCmd())
}

// PreflightCmd runs the operator's preflight checks client-side against a v2 CR
// file via pkg/preflight. Which checks run is driven by the operator's registry:
// every registered CRField present in the CR triggers its check, so new upstream
// checks land on `go get`.
func PreflightCmd() *cobra.Command {
	var (
		kubeContext string
		namespace   string
		offline     bool
	)

	cmd := &cobra.Command{
		Use:   "preflight <cr-file>",
		Short: "Run the operator's preflight checks against a v2 WeightsAndBiases CR file",
		Args:  cobra.ExactArgs(1),
		Long: `Run the operator's preflight checks client-side against a v2 WeightsAndBiases CR file.

		  Checks are selected from the operator's preflight registry: each registered CR field
		  present in the file triggers its check. The checks found are listed with their type
		  (static, cluster, connection) and the access they need before any of them runs.

		  Checks that create resources (resourceCreate) ask for confirmation first; declining
		  exits without running any check. With --offline only static checks run, so neither
		  the cluster nor external services are contacted.

		  Checks run from the machine running wsm, not from inside the cluster. An endpoint
		  reachable only from the cluster network (e.g. *.svc, private VPC hosts) may FAIL here
		  even though W&B pods can reach it.

		  Exits non-zero if any check fails or is inconclusive (unknown). Skip checks with the
		  skipPreflights annotation (comma-separated check names).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if kubeContext != "" {
				kubectl.SetContext(kubeContext)
			}
			data, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("failed to read CR file: %w", err)
			}
			cr, err := preflight.ReadCR(data)
			if err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			// Mirrors deploy-v2, which places the CR in --wandb-namespace regardless of
			// the file's metadata.namespace, so Secrets resolve where they will live.
			if namespace != "" {
				cr.SetNamespace(namespace)
			}
			runs, err := runPreflight(cmd.Context(), cr, preflight.ExecOptions{Offline: offline})
			if err != nil {
				return err
			}
			printPreflightResults(runs)
			return preflight.Err(runs)
		},
	}

	cmd.Flags().StringVar(&kubeContext, "context", "", "Kubeconfig context used to resolve Secret-backed values (valueFrom.secretKeyRef)")
	cmd.Flags().StringVar(&namespace, "wandb-namespace", "", "Namespace the CR will be deployed to; overrides metadata.namespace and is where referenced Secrets are read (default: the CR's metadata.namespace)")
	cmd.Flags().BoolVar(&offline, "offline", false, "Only run static checks that inspect the CR; skip checks that contact the cluster or external services")

	return cmd
}

var errPreflightDeclined = errors.New("preflight aborted: checks that create resources were not confirmed; no checks were run")

// runPreflight lists the checks found and, before running anything, asks once
// for all checks that create resources. Declining aborts the whole run.
func runPreflight(ctx context.Context, cr *unstructured.Unstructured, opts preflight.ExecOptions) ([]preflight.CheckRun, error) {
	plan := preflight.PlanChecks(cr)
	printPreflightPlan(plan, opts)

	if needs := preflight.RequiringConfirmation(plan, opts); len(needs) > 0 {
		fmt.Println("These checks create resources in the cluster:")
		for _, c := range needs {
			fmt.Printf("  %s (%s)\n", c.Name, joinResourceReqs(c.ResourceReqs))
		}
		if !confirmOnStdin("Run them?") {
			return nil, errPreflightDeclined
		}
		fmt.Println()
	}

	return preflight.ExecuteChecks(ctx, cr, plan, opts), nil
}

func printPreflightPlan(plan []preflight.PlannedRun, opts preflight.ExecOptions) {
	if len(plan) == 0 {
		return
	}
	header := "Preflight checks found:"
	if opts.Offline {
		header = "Preflight checks found (offline: only static checks run):"
	}
	fmt.Println(header)
	for _, p := range plan {
		fmt.Printf("  %s (%s) [type: %s, access: %s]\n", p.Check.Name, p.FieldPath, p.Check.Type, joinResourceReqs(p.Check.ResourceReqs))
	}
	fmt.Println()
}

func joinResourceReqs(reqs []oppreflight.ResourceReq) string {
	parts := make([]string, len(reqs))
	for i, r := range reqs {
		parts[i] = string(r)
	}
	return strings.Join(parts, ", ")
}

func printPreflightResults(runs []preflight.CheckRun) {
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
