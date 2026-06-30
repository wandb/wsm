package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/kubectl"
	"github.com/wandb/wsm/pkg/operator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// operatorReleaseName is the Helm release name wsm installs the operator under
// (see DeployOperator in pkg/operator). Rollback targets it when a checkpoint
// recorded an operator revision.
const operatorReleaseName = "wandb-operator"

func init() {
	rootCmd.AddCommand(RollbackCmd())
}

func RollbackCmd() *cobra.Command {
	var (
		kubeContext       string
		wandbName         string
		wandbNamespace    string
		operatorNamespace string
		wait              bool
		timeout           time.Duration
		yes               bool
		dryRun            bool
	)

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Roll back a W&B instance to the last known-good checkpoint",
		Long: `Restore the WeightsAndBiases CR — and, when the checkpoint recorded one, the
operator Helm release — to the last good state captured after a successful
deploy or upgrade. Use this to recover from a partially failed upgrade.`,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if kubeContext == "" {
				return errors.New("--context is required")
			}
			kubectl.SetContext(kubeContext)
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			// Safety: only roll back installs wsm deployed.
			hasMarker, err := kubectl.HasDeploymentMarker(ctx, wandbNamespace, "wandb-cr")
			if err != nil {
				return err
			}
			if !hasMarker {
				return fmt.Errorf("no wsm deployment marker found in namespace %q — refusing to roll back an install wsm did not deploy", wandbNamespace)
			}

			// Load the last known-good checkpoint.
			checkpoint, err := operator.GetLatestGoodCheckpoint(ctx, wandbNamespace, wandbName)
			if err != nil {
				return fmt.Errorf("failed to read checkpoint: %w", err)
			}
			if checkpoint == nil {
				return fmt.Errorf("no good checkpoint found for %s/%s — nothing to roll back to (a successful deploy/upgrade records one)", wandbNamespace, wandbName)
			}
			if checkpoint.CR == nil {
				return fmt.Errorf("checkpoint for %s/%s has no CR snapshot; cannot restore", wandbNamespace, wandbName)
			}

			// Build the plan (current → checkpoint). Reading the current CR is
			// best-effort: a failed upgrade may have left it in a bad state.
			currentVersion := "(unknown)"
			if current, err := operator.GetCR(ctx, wandbName, wandbNamespace); err == nil {
				currentVersion = current.Spec.Wandb.Version
			}

			fmt.Printf("Rollback plan for %s/%s (checkpoint from %s):\n", wandbNamespace, wandbName, checkpoint.CreatedAt.Format(time.RFC3339))
			fmt.Printf("  spec.wandb.version: %s → %s\n", currentVersion, checkpoint.WandbVersion)
			if checkpoint.HelmRevision > 0 {
				fmt.Printf("  operator helm release %q: roll back to revision %d (namespace %s)\n", operatorReleaseName, checkpoint.HelmRevision, operatorNamespace)
			} else {
				fmt.Println("  operator helm release: unchanged (CR-only rollback)")
			}

			if dryRun {
				fmt.Println("(dry-run) no changes applied.")
				return nil
			}
			if !yes {
				fmt.Print("Proceed? [y/N]: ")
				answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.ToLower(strings.TrimSpace(answer)) != "y" {
					fmt.Println("aborted.")
					return nil
				}
			}

			if checkpoint.HelmRevision > 0 {
				start := time.Now()
				fmt.Printf("→ Rolling back operator to revision %d...", checkpoint.HelmRevision)
				if err := operator.RollbackRelease(operatorReleaseName, operatorNamespace, checkpoint.HelmRevision, timeout); err != nil {
					return fmt.Errorf("operator rollback failed: %w", err)
				}
				fmt.Printf(" (%s)\n", time.Since(start).Round(time.Second))

				fmt.Printf("→ Waiting for operator to be ready (timeout %s)...\n", timeout)
				if err := operator.WaitForOperator(ctx, operatorNamespace, timeout); err != nil {
					return fmt.Errorf("operator did not become ready after rollback: %w", err)
				}
			}

			checkpoint.CR.TypeMeta = metav1.TypeMeta{APIVersion: "apps.wandb.com/v2", Kind: "WeightsAndBiases"}
			start := time.Now()
			fmt.Print("→ Restoring CR...")
			if err := operator.ApplyCR(ctx, checkpoint.CR); err != nil {
				return fmt.Errorf("failed to restore CR: %w", err)
			}
			fmt.Printf(" (%s)\n", time.Since(start).Round(time.Second))

			if wait {
				fmt.Printf("→ Waiting for %s/%s to be ready (timeout %s)...\n", wandbNamespace, wandbName, timeout)
				if err := operator.WaitForCRReady(ctx, wandbNamespace, wandbName, timeout); err != nil {
					return fmt.Errorf("instance did not become ready after rollback: %w", err)
				}
				fmt.Println("Rollback complete.")
			} else {
				fmt.Printf("Rollback applied. Check status with: kubectl get wandb -n %s %s\n", wandbNamespace, wandbName)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&kubeContext, "context", "", "name of the kubeconfig context to use (required)")
	cmd.Flags().StringVar(&wandbName, "wandb-name", "wandb", "Name of the W&B instance")
	cmd.Flags().StringVar(&wandbNamespace, "wandb-namespace", "wandb", "Namespace of the W&B instance")
	cmd.Flags().StringVar(&operatorNamespace, "operator-namespace", "wandb-operators", "Namespace of the operator Helm release (used only when the checkpoint recorded an operator revision)")
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait for the instance to be ready after rolling back")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "Timeout when --wait is set")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would change without applying")

	return cmd
}
