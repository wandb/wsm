package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/operator"
	sigsyaml "sigs.k8s.io/yaml"
)

func init() {
	rootCmd.AddCommand(ConvertV2Cmd())
}

var roundTripAnnotations = []string{
	"legacy.operator.wandb.com/v1-chart",
	"legacy.operator.wandb.com/v1-values",
}

// ConvertV2Cmd returns the convert-v2 command: a read-only preview of the v1→v2
// CR conversion, emitted either as a unified diff or as two YAML files for a
// side-by-side compare in an editor.
func ConvertV2Cmd() *cobra.Command {
	var (
		wandbName      string
		wandbNamespace string
		outputPath     string
		format         string
		stripRoundTrip bool
	)

	cmd := &cobra.Command{
		Use:   "convert-v2",
		Short: "Diff the live v1 WeightsAndBiases CR against the v2 CR the conversion webhook would produce",
		Long: `Read the live apps.wandb.com/v1 WeightsAndBiases CR from the cluster, run the
operator's conversion-webhook logic client-side, and show how it converts to the
apps.wandb.com/v2 CR it would produce.

No operator or conversion webhook needs to be installed: the conversion code is
compiled in from the operator version wsm is built against, so the output matches
what the in-cluster webhook would emit after upgrade.

Output formats (--format) optional:
  unified  a unified diff (default). Tip: write it to a .diff/.patch file so
           your editor colorizes it (a .yaml extension is parsed as YAML and
           won't highlight the +/- lines).
  split    two YAML files, <base>-v1.yaml and <base>-v2.yaml, for a side-by-side
           compare in your editor (VS Code: select both, right-click > Compare).
           v1 and v2 use different shapes (v1's spec.values blob vs v2's typed
           spec), so side-by-side is usually far more legible than a line diff.`,

		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "unified" && format != "split" {
				return fmt.Errorf("invalid --format %q (expected: unified, split)", format)
			}

			ctx := context.Background()

			v1CR, v2CR, err := operator.ConvertV1CRToV2(ctx, wandbName, wandbNamespace)
			if err != nil {
				return err
			}

			if stripRoundTrip {
				anns := v2CR.GetAnnotations()
				for _, k := range roundTripAnnotations {
					delete(anns, k)
				}
				v2CR.SetAnnotations(anns)
			}

			v1YAML, err := sigsyaml.Marshal(v1CR)
			if err != nil {
				return fmt.Errorf("failed to marshal v1 CR to YAML: %w", err)
			}
			v2YAML, err := sigsyaml.Marshal(v2CR)
			if err != nil {
				return fmt.Errorf("failed to marshal v2 CR to YAML: %w", err)
			}

			if format == "split" {
				return writeSplit(outputPath, wandbName, v1YAML, v2YAML)
			}

			diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
				A:        difflib.SplitLines(string(v1YAML)),
				B:        difflib.SplitLines(string(v2YAML)),
				FromFile: fmt.Sprintf("%s/%s (apps.wandb.com/v1)", wandbNamespace, wandbName),
				ToFile:   fmt.Sprintf("%s/%s (apps.wandb.com/v2)", wandbNamespace, wandbName),
				Context:  3,
			})
			if err != nil {
				return fmt.Errorf("failed to build diff: %w", err)
			}

			if diff == "" {
				diff = "# no differences between the v1 CR and the converted v2 CR\n"
			}

			if outputPath != "" {
				if err := os.WriteFile(outputPath, []byte(diff), 0644); err != nil {
					return fmt.Errorf("failed to write diff to %q: %w", outputPath, err)
				}
				fmt.Printf("Wrote v1→v2 CR diff to %s\n", outputPath)
				return nil
			}

			fmt.Print(diff)
			return nil
		},
	}

	cmd.Flags().StringVar(&wandbName, "wandb-name", "wandb", "Name of the W&B CR to convert")
	cmd.Flags().StringVar(&wandbNamespace, "wandb-namespace", "default", "Namespace of the W&B CR to convert")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output path: the diff file (unified) or the base path for the two files (split)")
	cmd.Flags().StringVar(&format, "format", "unified", "Output format: unified (diff) or split (two YAML files for side-by-side compare)")
	cmd.Flags().BoolVar(&stripRoundTrip, "strip-roundtrip", false, "Drop the legacy v1-chart/v1-values round-trip annotations that bloat the output")

	return cmd
}

// writeSplit writes the v1 and v2 YAML to <base>-v1.yaml and <base>-v2.yaml.
// base derives from outputPath (extension trimmed) or from wandbName when no
// output path is given.
func writeSplit(outputPath, wandbName string, v1YAML, v2YAML []byte) error {
	base := wandbName
	if outputPath != "" {
		base = strings.TrimSuffix(outputPath, filepath.Ext(outputPath))
	}

	v1Path := base + "-v1.yaml"
	v2Path := base + "-v2.yaml"

	if err := os.WriteFile(v1Path, v1YAML, 0644); err != nil {
		return fmt.Errorf("failed to write v1 CR to %q: %w", v1Path, err)
	}
	if err := os.WriteFile(v2Path, v2YAML, 0644); err != nil {
		return fmt.Errorf("failed to write v2 CR to %q: %w", v2Path, err)
	}

	fmt.Printf("Wrote %s and %s — open both and compare side-by-side in your editor.\n", v1Path, v2Path)
	return nil
}
