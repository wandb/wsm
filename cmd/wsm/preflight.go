package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/kubectl"
	"github.com/wandb/wsm/pkg/preflight"
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
	)

	cmd := &cobra.Command{
		Use:   "preflight <cr-file>",
		Short: "Run the operator's preflight checks against a v2 WeightsAndBiases CR file",
		Args:  cobra.ExactArgs(1),
		Long: `Run the operator's preflight checks client-side against a v2 WeightsAndBiases CR file.
		
		  Checks are selected from the operator's preflight registry: each registered CR field
		  present in the file triggers its check. Currently only spec.mysql.<instance>.externalMysql
		  is checked; other external services report "No preflight checks apply".
		
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
			runs := preflight.RunChecks(cmd.Context(), cr, nil)
			printPreflightResults(runs)
			return preflight.Err(runs)
		},
	}

	cmd.Flags().StringVar(&kubeContext, "context", "", "Kubeconfig context used to resolve Secret-backed values (valueFrom.secretKeyRef)")
	cmd.Flags().StringVar(&namespace, "wandb-namespace", "", "Namespace the CR will be deployed to; overrides metadata.namespace and is where referenced Secrets are read (default: the CR's metadata.namespace)")

	return cmd
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
