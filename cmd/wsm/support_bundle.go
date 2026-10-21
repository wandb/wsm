package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	v2 "github.com/wandb/operator/api/v2"
	"github.com/wandb/wsm/pkg/kubectl"
	"github.com/wandb/wsm/pkg/observability/supportbundle"
)

func init() {
	rootCmd.AddCommand(SupportBundleCmd())
}

func SupportBundleCmd() *cobra.Command {
	var (
		kubeContext string
		enabled     bool
	)

	cmd := &cobra.Command{
		Use:   "support-bundle",
		Short: "Collect support bundles from a W&B instance",
		Long:  `Run the operator's support-bundle collector`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if kubeContext != "" {
				kubectl.SetContext(kubeContext)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("enabled") {
				return cmd.Help()
			}
			return setSupportBundleEnabled(cmd, enabled)
		},
	}

	cmd.PersistentFlags().StringVar(&kubeContext, "context", "", "kubeconfig context to use (default: current context, or in-cluster)")
	cmd.PersistentFlags().String("wandb-name", "", "Name of the W&B instance (default: auto-discover)")
	cmd.PersistentFlags().String("wandb-namespace", "", "Namespace of the W&B instance (default: auto-discover)")

	cmd.Flags().BoolVar(&enabled, "enabled", true, "Turn support bundles on (true) or off (false) for the W&B instance")

	cmd.AddCommand(supportBundleCreateCmd())

	return cmd
}

func supportBundleCreateCmd() *cobra.Command {
	var waitForBundle bool

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Start a support-bundle collection",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cr, err := discoverInstallation(ctx, cmd)
			if err != nil {
				return err
			}
			_, client, err := kubectl.GetClientset()
			if err != nil {
				return err
			}

			job, err := supportbundle.Create(ctx, client, cr, supportbundle.CollectOptions{})
			if err != nil {
				return err
			}
			fmt.Printf("Started support bundle %s in %s/%s\n", job.Name, cr.Namespace, cr.Name)

			if !waitForBundle {
				fmt.Printf("Follow it with:\n  kubectl -n %s get job %s\n  kubectl -n %s logs job/%s\n", job.Namespace, job.Name, job.Namespace, job.Name)
				return nil
			}

			ctxSig, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			fmt.Println("Waiting for the collection to finish (Ctrl+C stops waiting; the collection keeps running)...")
			if _, err := supportbundle.Wait(ctxSig, client, job.Namespace, job.Name); err != nil {
				if errors.Is(err, context.Canceled) {
					fmt.Printf("Stopped waiting; %s is still running. Check it with: kubectl -n %s get job %s\n", job.Name, job.Namespace, job.Name)
					return nil
				}
				return err
			}
			fmt.Printf("Support bundle %s collected\n", job.Name)
			return nil
		},
	}

	cmd.Flags().BoolVar(&waitForBundle, "wait", false, "Wait for the collection to finish")

	return cmd
}

func setSupportBundleEnabled(cmd *cobra.Command, enabled bool) error {
	ctx := context.Background()
	cr, err := discoverInstallation(ctx, cmd)
	if err != nil {
		return err
	}
	if err := supportbundle.SetEnabled(ctx, cr.Name, cr.Namespace, enabled); err != nil {
		return fmt.Errorf("failed to update support bundles: %w", err)
	}
	if enabled {
		fmt.Printf("Support bundles enabled on namespace: %s instance: %s\n", cr.Namespace, cr.Name)
	} else {
		fmt.Printf("Support bundles disabled on namespace: %s instance: %s\n", cr.Namespace, cr.Name)
	}
	return nil
}

func discoverInstallation(ctx context.Context, cmd *cobra.Command) (*v2.WeightsAndBiases, error) {
	wandbName, _ := cmd.Flags().GetString("wandb-name")
	wandbNamespace, _ := cmd.Flags().GetString("wandb-namespace")
	return supportbundle.Discover(ctx, wandbNamespace, wandbName)
}
