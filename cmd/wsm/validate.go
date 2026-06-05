package main

import (
	"fmt"

	"github.com/wandb/wsm/pkg/operator"
)

// Rejects any --observability-mode that isn't a telemetry mode the operator chart understands. The valid set is owned by
// pkg/operator (its DeployOperator switch acts on it); this wrapper only adds the CLI-flag-specific error message.
func validateObservabilityMode(mode string) error {
	if !operator.ValidTelemetryMode(mode) {
		return fmt.Errorf("invalid --observability-mode %q (must be %q, %q, or %q)",
			mode, operator.TelemetryModeOff, operator.TelemetryModeFull, operator.TelemetryModeForward)
	}
	return nil
}

// Rejects specifying both an explicit gateway class and an ingress class. --gateway-class has a non-empty default
// ("nginx"), so a bare --ingress-class is allowed and takes precedence.
func validateNetworkingFlags(gatewayClassChanged bool, gatewayClass, ingressClass string) error {
	if gatewayClassChanged && gatewayClass != "" && ingressClass != "" {
		return fmt.Errorf("--gateway-class and --ingress-class are mutually exclusive")
	}
	return nil
}
