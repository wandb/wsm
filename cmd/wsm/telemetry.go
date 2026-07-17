package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/kubectl"
	"github.com/wandb/wsm/pkg/operator"
	"github.com/wandb/wsm/pkg/telemetry"
)

func init() {
	rootCmd.AddCommand(TelemetryCmd())
}

// TelemetryCmd groups the helpers for viewing the in-cluster telemetry UIs deployed by the v2 operator
// when installed with --observability-mode=full (Grafana + Victoria) or forward (Victoria only). Those
// services are ClusterIP-only, so each subcommand port-forwards to one (natively, no kubectl binary) and
// opens it in a browser; press Ctrl+C to stop.
func TelemetryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "View the in-cluster telemetry UIs (Grafana, VictoriaMetrics) of a v2 W&B install",
		Long: `View the in-cluster telemetry UIs deployed by the v2 operator.

The telemetry stack is deployed when the operator is installed with
--observability-mode=full (Grafana + Victoria stack) or forward (Victoria
stack only). Its services are ClusterIP-only, so these subcommands port-forward
to a service and open it in a browser; press Ctrl+C to stop.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			kubeContext, _ := cmd.Flags().GetString("context")
			kubectl.SetContext(kubeContext)
			return nil
		},
	}

	cmd.PersistentFlags().String("context", "", "name of the kubeconfig context to use")
	cmd.PersistentFlags().String("wandb-namespace", "wandb", "Namespace where the telemetry stack is deployed")
	cmd.PersistentFlags().String("operator-namespace", "wandb-operators", "Namespace of the operator Helm release (used to read the installed telemetry mode)")

	for _, ui := range telemetry.Catalog {
		cmd.AddCommand(telemetryUICmd(ui))
	}

	return cmd
}

func telemetryUICmd(ui telemetry.UI) *cobra.Command {
	var service string
	var localPort int
	var remotePort int
	var noBrowser bool

	cmd := &cobra.Command{
		Use:   ui.Name,
		Short: fmt.Sprintf("Port-forward to the %s UI", ui.Name),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			namespace, _ := cmd.Flags().GetString("wandb-namespace")
			operatorNamespace, _ := cmd.Flags().GetString("operator-namespace")

			cfg, cs, err := kubectl.GetClientset()
			if err != nil {
				return err
			}

			// Report the installed mode and guard obvious mismatches before forwarding.
			tc, err := operator.GetOperatorTelemetryConfig(ctx, operatorNamespace)
			if err != nil {
				return fmt.Errorf("failed to read telemetry config from operator namespace %q (set --operator-namespace if the operator is installed elsewhere): %w", operatorNamespace, err)
			}
			fmt.Printf("Telemetry mode: %s\n", tc.Mode)
			if tc.Mode == operator.TelemetryModeOff {
				return fmt.Errorf("telemetry is not enabled on this install (mode=off); redeploy the operator with --observability-mode=full")
			}
			if ui.Name == "grafana" && tc.Mode != operator.TelemetryModeFull {
				return fmt.Errorf("grafana is only deployed with --observability-mode=full (current mode: %s); try `wsm telemetry victoria`", tc.Mode)
			}

			if !cmd.Flags().Changed("service") {
				resolved, err := telemetry.ResolveService(ctx, cs, namespace, ui)
				if err != nil {
					return err
				}
				service = resolved
			}

			session, err := kubectl.PortForward(ctx, cfg, cs, namespace, service, remotePort, localPort)
			if err != nil {
				return err
			}
			defer func() { _ = session.Close() }()

			url := fmt.Sprintf("http://localhost:%d%s", session.LocalPort, ui.URLPath)
			fmt.Printf("→ Forwarding %s/%s to %s (Ctrl+C to stop)\n", namespace, service, url)
			if !noBrowser {
				_ = openBrowser(url)
			}

			ctxSig, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			select {
			case <-ctxSig.Done():
				return nil
			case err := <-session.Done():
				if err != nil {
					return fmt.Errorf("port-forward to %s/%s ended unexpectedly: %w", namespace, service, err)
				}
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&service, "service", ui.Service, "Service name to forward")
	cmd.Flags().IntVar(&localPort, "local-port", ui.Port, "Local port to bind (0 for an OS-assigned port)")
	cmd.Flags().IntVar(&remotePort, "remote-port", ui.Port, "Service port")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Do not open a browser automatically")

	return cmd
}
