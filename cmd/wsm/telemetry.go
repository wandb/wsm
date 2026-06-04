package main

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(TelemetryCmd())
}

// TelemetryCmd groups the helpers for viewing the in-cluster telemetry UIs
// deployed by the v2 operator when installed with --observability-mode=full
// (or forward). Those services are ClusterIP-only, so each subcommand tunnels
// to one via kubectl port-forward and opens it in a browser.
func TelemetryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "View the in-cluster telemetry UIs (Grafana, VictoriaMetrics) of a v2 W&B install",
		Long: `View the in-cluster telemetry UIs deployed by the v2 operator.

The telemetry stack is deployed when the operator is installed with
--observability-mode=full (Grafana + Victoria stack) or forward (Victoria
stack only). Its services are ClusterIP-only, so these subcommands port-forward
to a service and open it in a browser; press Ctrl+C to stop.`,
		// Fail fast if kubectl isn't available.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if _, err := exec.LookPath("kubectl"); err != nil {
				return fmt.Errorf("kubectl not found on PATH: `wsm telemetry` requires kubectl for port-forwarding: %w", err)
			}
			return nil
		},
	}

	// Shared across every UI subcommand.
	cmd.PersistentFlags().String("context", "", "name of the kubeconfig context to use")
	cmd.PersistentFlags().String("wandb-namespace", "wandb", "Namespace where the telemetry stack is deployed")

	cmd.AddCommand(telemetryUICmd(telemetryUI{
		use:         "grafana",
		short:       "Port-forward to the Grafana dashboards",
		service:     "grafana-service",
		defaultPort: 3000,
		urlPath:     "",
		modeHint:    "full",
	}))
	cmd.AddCommand(telemetryUICmd(telemetryUI{
		use:         "victoria",
		short:       "Port-forward to the VictoriaMetrics UI (VMUI)",
		service:     "vmsingle-victoria-instance",
		defaultPort: 8428,
		urlPath:     "/vmui/",
		modeHint:    "full or forward",
	}))

	return cmd
}

// telemetryUI describes one viewable telemetry service.
type telemetryUI struct {
	use         string
	short       string
	service     string
	defaultPort int
	urlPath     string
	modeHint    string
}

func telemetryUICmd(ui telemetryUI) *cobra.Command {
	var service string
	var localPort int
	var remotePort int
	var noBrowser bool

	cmd := &cobra.Command{
		Use:   ui.use,
		Short: ui.short,
		RunE: func(cmd *cobra.Command, args []string) error {
			kubeContext, _ := cmd.Flags().GetString("context")
			namespace, _ := cmd.Flags().GetString("wandb-namespace")
			if err := portForwardAndOpen(kubeContext, namespace, service, localPort, remotePort, ui.urlPath, !noBrowser); err != nil {
				return fmt.Errorf("port-forward failed (is the operator installed with --observability-mode=%s?): %w", ui.modeHint, err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&service, "service", ui.service, "Service name to forward")
	cmd.Flags().IntVar(&localPort, "local-port", ui.defaultPort, "Local port to bind")
	cmd.Flags().IntVar(&remotePort, "remote-port", ui.defaultPort, "Service port")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Do not open a browser automatically")

	return cmd
}
