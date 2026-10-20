package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/spf13/cobra"
	v2 "github.com/wandb/operator/api/v2"
	"github.com/wandb/wsm/pkg/kubectl"
	"github.com/wandb/wsm/pkg/observability/supportbundle"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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
	cmd.AddCommand(supportBundleCancelCmd())
	cmd.AddCommand(supportBundleStatusCmd())

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
			fmt.Println("Streaming collector logs (Ctrl+C stops following; the collection keeps running)...")
			if err := followCollection(ctxSig, client, job); err != nil {
				if errors.Is(err, context.Canceled) {
					fmt.Printf("Stopped following; %s is still running. Check it with: kubectl -n %s get job %s\n", job.Name, job.Namespace, job.Name)
					return nil
				}
				return err
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&waitForBundle, "wait", false, "Stream the collector logs and print the result when it finishes")

	return cmd
}

func supportBundleCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <bundle-id>",
		Short: "Stop a running support-bundle collection",
		Args:  cobra.ExactArgs(1),
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
			cancelled, err := supportbundle.Cancel(ctx, client, cr, args[0])
			if err != nil {
				return err
			}
			if !cancelled {
				fmt.Printf("Support bundle %s already finished; nothing to cancel\n", args[0])
				return nil
			}
			fmt.Printf("Support bundle %s cancelled\n", args[0])
			return nil
		},
	}
}

func supportBundleStatusCmd() *cobra.Command {
	var watch, asJSON bool

	cmd := &cobra.Command{
		Use:   "status <bundle-id>",
		Short: "Show a support bundle's collection status",
		Args:  bundleIDArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			cr, err := discoverInstallation(ctx, cmd)
			if err != nil {
				return err
			}
			_, client, err := kubectl.GetClientset()
			if err != nil {
				return err
			}

			id := args[0]
			status, err := supportbundle.DescribeCollection(ctx, client, cr, id)
			if err != nil {
				return err
			}
			if status == nil {
				return fmt.Errorf("support bundle %s not found in namespace: %s", id, cr.Namespace)
			}

			if asJSON {
				out, err := json.MarshalIndent(status, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(out))
				return nil
			}
			if !watch || !status.Running() {
				printCollectionStatus(status)
				return nil
			}

			job, err := client.BatchV1().Jobs(cr.Namespace).Get(ctx, id, metav1.GetOptions{})
			if err != nil {
				return err
			}
			printField("Bundle ID:", id)
			printField("Collection Status:", status.Phase)
			fmt.Println()
			if err := followCollection(ctx, client, job); err != nil {
				if errors.Is(err, context.Canceled) {
					fmt.Printf("Stopped watching - %s is still running\n", id)
					return nil
				}
				return err
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Stream the collector logs until the collection finishes")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the status as JSON")
	cmd.MarkFlagsMutuallyExclusive("watch", "json")

	return cmd
}

// bundleIDArg requires one bundle ID and points at `list` when it's missing.
func bundleIDArg(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return errors.New("bundle ID is required - please run `wsm support-bundle list` to retrieve list of bundles")
	}
	return cobra.ExactArgs(1)(cmd, args)
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

// followCollection streams the collector's logs, waits for the Job, and prints Lumen's result.
func followCollection(ctx context.Context, client kubernetes.Interface, job *batchv1.Job) error {
	pod, err := supportbundle.WaitForCollectorPod(ctx, client, job, func(reason string) {
		fmt.Println("  waiting: " + reason)
	})
	if err != nil {
		return err
	}
	container := supportbundle.CollectorContainer(job)
	if pod != nil {
		err = kubectl.FollowPodLogs(ctx, client, job.Namespace, pod.Name, container, func(line string) {
			fmt.Println("  " + supportbundle.FormatLogLine(line))
		})
		if err != nil && ctx.Err() == nil {
			fmt.Printf("Lost the log stream (%v); waiting for the collection to finish\n", err)
		}
	}

	_, jobErr := supportbundle.Wait(ctx, client, job.Namespace, job.Name)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if pod, err = supportbundle.CollectorPod(ctx, client, job); err == nil && pod != nil {
		result, err := supportbundle.PodResult(pod, container)
		if err != nil {
			fmt.Printf("Could not read the collection result: %v\n", err)
		} else if result != nil {
			fmt.Println()
			printBundleResult(job.Name, result)
		}
	}
	if jobErr != nil {
		return jobErr
	}
	fmt.Println("\nSupport bundle has been completed")
	return nil
}

func printCollectionStatus(status *supportbundle.CollectionStatus) {
	printField("Bundle ID:", status.ID)
	phase := status.Phase
	if status.Message != "" {
		phase += " (" + status.Message + ")"
	}
	printField("Collection Status:", phase)
	if status.Started != nil {
		printField("Started:", status.Started.Local().Format(time.DateTime))
	}
	if status.Finished != nil {
		printField("Finished:", status.Finished.Local().Format(time.DateTime))
	}
	if status.ResultError != "" {
		printField("Result:", status.ResultError)
	}
	if status.Result != nil {
		printResultDetails(status.Result)
	}
}

func printField(label, value string) { fmt.Printf("%-23s%s\n", label, value) }

func printBundleResult(id string, result *supportbundle.Result) {
	printField("Bundle ID:", id)
	printResultDetails(result)
}

func printResultDetails(result *supportbundle.Result) {
	status := "Complete"
	if result.Failure != nil {
		status = "Failed"
	} else if result.Partial {
		status = "Partial"
	}

	if result.Artifact != nil {
		printField("Bundle Location:", result.Artifact.Location)
		printField("Size:", fmt.Sprintf("%d bytes (%s)", result.Artifact.SizeBytes, humanBytes(result.Artifact.SizeBytes)))
		printField("SHA256:", result.Artifact.SHA256)
	}
	printField("Bundle Overall Status:", status)
	if result.Failure != nil {
		printField("Failure:", fmt.Sprintf("%s: %s %s", result.Failure.Stage, result.Failure.Reason, result.Failure.Message))
	}

	if len(result.Collectors) == 0 {
		return
	}
	rows := [][]string{}
	for _, collector := range result.Collectors {
		rows = append(rows, []string{collector.Name, collector.Status})
		for _, export := range collector.Exports {
			rows = append(rows, []string{collector.Name + "/" + export.Name, export.Status})
		}
	}
	fmt.Println("\nCollector Status:")
	cell := lipgloss.NewStyle().Padding(0, 1)
	fmt.Println(table.New().
		Border(lipgloss.NormalBorder()).
		StyleFunc(func(row, col int) lipgloss.Style { return cell }).
		Headers("NAME", "STATUS").
		Rows(rows...))
	if result.CollectorsTruncated {
		fmt.Println("(collector list truncated)")
	}
}

// humanBytes formats a size with decimal units, e.g. 10.82 MB.
func humanBytes(n int64) string {
	size := float64(n)
	for _, unit := range []string{"B", "KB", "MB", "GB"} {
		if size < 1000 {
			return fmt.Sprintf("%.2f %s", size, unit)
		}
		size /= 1000
	}
	return fmt.Sprintf("%.2f TB", size)
}
