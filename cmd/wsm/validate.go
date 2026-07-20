package main

import (
	"fmt"
	"strings"

	"github.com/wandb/operator/api/v2"
	"github.com/wandb/wsm/pkg/operator"
)

// The valid sets come from the operator API so a new value upstream lands here on
// `go get`. Size is also enforced by the CRD; retentionPolicy has no CRD enum, so
// this is its only backstop.
var validSizes = []v2.Size{
	v2.SizeDev, v2.SizeMicro, v2.SizeSmall, v2.SizeMedium,
	v2.SizeLarge, v2.SizeXLarge, v2.SizeXXLarge,
}

var validRetentionPolicies = []v2.OnDeletePolicy{v2.DetachOnDelete, v2.PurgeOnDelete}

func validateSize(size string) error {
	for _, s := range validSizes {
		if v2.Size(size) == s {
			return nil
		}
	}
	sizes := make([]string, len(validSizes))
	for i, s := range validSizes {
		sizes[i] = string(s)
	}
	return fmt.Errorf("invalid --size %q (must be one of: %s)", size, strings.Join(sizes, ", "))
}

func validateRetentionPolicy(policy string) error {
	for _, p := range validRetentionPolicies {
		if v2.OnDeletePolicy(policy) == p {
			return nil
		}
	}
	return fmt.Errorf("invalid --retention-policy %q (must be %q or %q)",
		policy, v2.DetachOnDelete, v2.PurgeOnDelete)
}

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
