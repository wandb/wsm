package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
what the in-cluster webhook would emit after upgrade.`,

		RunE: func(cmd *cobra.Command, args []string) error {

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

			return writeSplit(outputPath, wandbName, v1YAML, v2YAML)

			return nil
		},
	}

	cmd.Flags().StringVar(&wandbName, "wandb-name", "wandb", "Name of the W&B CR to convert")
	cmd.Flags().StringVar(&wandbNamespace, "wandb-namespace", "default", "Namespace of the W&B CR to convert")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output path: the diff file (unified) or the base path for the two files (split)")
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
