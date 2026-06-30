package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/kubectl"
	"github.com/wandb/wsm/pkg/operator"
)

func init() {
	rootCmd.AddCommand(UpgradeCmd())
}

func UpgradeCmd() *cobra.Command {
	var (
		kubeContext    string
		wandbName      string
		wandbNamespace string
		wandbVersion   string
		wait           bool
		timeout        time.Duration
		force          bool
		dryRun         bool
	)

	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade an existing wsm-managed W&B instance to a new version",
		Long:  `Patch spec.wandb.version on a WeightsAndBiases CR that wsm previously deployed.`,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if kubeContext == "" {
				return errors.New("--context is required")
			}
			if wandbVersion == "" {
				return errors.New("--wandb-version is required")
			}
			kubectl.SetContext(kubeContext)
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			hasMarker, err := kubectl.HasDeploymentMarker(ctx, wandbNamespace, "wandb-cr")
			if err != nil {
				return err
			}
			if !hasMarker {
				return fmt.Errorf("no wsm deployment marker found in namespace %q — refusing to upgrade an install wsm did not deploy", wandbNamespace)
			}

			currentCR, err := operator.GetCR(ctx, wandbName, wandbNamespace)
			currentCR.ManagedFields = nil
			currentCR.ResourceVersion = ""
			if err != nil {
				return fmt.Errorf("failed to read current CR: %w", err)
			}

			currentVersion := currentCR.Spec.Wandb.Version
			if currentVersion == wandbVersion {
				fmt.Printf("✓ %s/%s is already at version %s, nothing to do.\n", wandbNamespace, wandbName, wandbVersion)
				return nil
			}

			if !force {
				down, cmpErr := isDowngrade(currentVersion, wandbVersion)
				if cmpErr != nil {
					return fmt.Errorf("%w (pass --force to proceed anyway)", cmpErr)
				}
				if down {
					return fmt.Errorf("refusing to downgrade %s → %s (pass --force to override)", currentVersion, wandbVersion)
				}
			}

			fmt.Printf("Upgrade plan for %s/%s:\n", wandbNamespace, wandbName)
			fmt.Printf("  spec.wandb.version: %s → %s\n", currentVersion, wandbVersion)

			if dryRun {
				fmt.Println("(dry-run) no changes applied.")
				return nil
			}

			currentCR.Spec.Wandb.Version = wandbVersion

			start := time.Now()
			fmt.Print("→ Applying upgrade...")
			if err := operator.ApplyCR(ctx, currentCR); err != nil {
				return fmt.Errorf("failed to apply upgrade: %w", err)
			}
			fmt.Printf(" (%s)\n", time.Since(start).Round(time.Second))

			if wait {
				fmt.Printf("→ Waiting for %s/%s to be ready (timeout %s)...\n", wandbNamespace, wandbName, timeout)
				if err != nil {
					return err
				}
				if err := operator.WaitForCRReady(ctx, wandbNamespace, wandbName, timeout); err != nil {
					return fmt.Errorf("instance did not become ready: %w", err)
				}
				fmt.Println("Upgrade complete.")
				// only save CR as good if WaitForCRReady was called otherwise can't trust that it's valid
				operator.SaveCheckpoint(ctx, wandbNamespace, wandbName, currentCR, 0, true)
			} else {
				fmt.Printf("Upgrade applied. Check status with: kubectl get wandb -n %s %s\n", wandbNamespace, wandbName)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&kubeContext, "context", "", "name of the kubeconfig context to use (required)")
	cmd.Flags().StringVar(&wandbName, "wandb-name", "wandb", "Name of the W&B instance")
	cmd.Flags().StringVar(&wandbNamespace, "wandb-namespace", "wandb", "Namespace of the W&B instance")
	cmd.Flags().StringVar(&wandbVersion, "wandb-version", "", "Target server manifest version (e.g., 0.78.0) (required)")
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait for the W&B instance to be ready after applying")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "Timeout when --wait is set")
	cmd.Flags().BoolVar(&force, "force", false, "Allow downgrades and unparseable versions")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would change without applying")

	return cmd
}

func isDowngrade(current, target string) (bool, error) {
	cur, err := semver.NewVersion(current)
	if err != nil {
		return false, fmt.Errorf("current version %q is not semver: %w", current, err)
	}
	tgt, err := semver.NewVersion(target)
	if err != nil {
		return false, fmt.Errorf("target version %q is not semver: %w", target, err)
	}
	return tgt.LessThan(cur), nil
}
