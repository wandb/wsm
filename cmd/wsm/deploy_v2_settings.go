package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	flagCRFile                          = "cr-file"
	flagHostname                        = "hostname"
	flagIngressClassName                = "ingress-class-name"
	flagLicense                         = "license"
	flagLicenseFile                     = "license-file"
	flagManagedInfraTelemetryEnabled    = "managed-infra-telemetry-enabled"
	flagMySQLTelemetryEnabled           = "mysql-telemetry-enabled"
	flagRedisTelemetryEnabled           = "redis-telemetry-enabled"
	flagKafkaTelemetryEnabled           = "kafka-telemetry-enabled"
	flagObjectStoreTelemetryEnabled     = "object-store-telemetry-enabled"
	flagClickHouseTelemetryEnabled      = "clickhouse-telemetry-enabled"
	flagNetworkingAnnotations           = "networking-annotations"
	flagNetworkingMode                  = "networking-mode"
	flagNetworkingTLSSecretName         = "networking-tls-secret-name"
	flagNetworkingCertManagerIssuer     = "networking-cert-manager-issuer"
	flagNetworkingCertManagerClusterRef = "networking-cert-manager-cluster-issuer"
	flagOperatorTelemetryForwardURL     = "operator-telemetry-forward-endpoint"
	flagOperatorTelemetryMode           = "operator-telemetry-mode"
	flagOperatorTelemetryNamespace      = "operator-telemetry-namespace"
	flagOperatorTelemetryOTelProtocol   = "operator-telemetry-otel-protocol"
	flagOperatorTelemetryOTelSecretName = "operator-telemetry-otel-secret-name"
	flagOperatorTelemetryOTelService    = "operator-telemetry-otel-service-name"
	flagOperatorTelemetryOTelResources  = "operator-telemetry-otel-resource-attributes"
	flagWandbName                       = "wandb-name"
	flagWandbNamespace                  = "wandb-namespace"
	flagWandbVersion                    = "wandb-version"
)

type wandbCROverrides struct {
	hostname                        *string
	networkingMode                  *string
	ingressClassName                *string
	networkingAnnotations           map[string]string
	networkingTLSSecretName         *string
	networkingCertManagerIssuer     *string
	networkingCertManagerClusterRef *string
	managedInfraTelemetryEnabled    *bool
	mysqlTelemetryEnabled           *bool
	redisTelemetryEnabled           *bool
	kafkaTelemetryEnabled           *bool
	objectStoreTelemetryEnabled     *bool
	clickHouseTelemetryEnabled      *bool
}

type operatorTelemetryOverrides struct {
	mode               *string
	namespace          *string
	otelSecretName     *string
	otelProtocol       *string
	otelServiceName    *string
	resourceAttributes *string
	forwardEndpoint    *string
}

func defaultWandbCR() *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps.wandb.com/v2",
			"kind":       "WeightsAndBiases",
			"metadata": map[string]interface{}{
				"name": "wandb",
			},
			"spec": map[string]interface{}{
				"retentionPolicy": map[string]interface{}{
					"onDelete": "detach",
				},
				"wandb": map[string]interface{}{
					"hostname": "http://localhost:8080",
					"features": map[string]interface{}{
						"proxy": true,
					},
					"internalServiceAuth": map[string]interface{}{
						"enabled": true,
					},
				},
				"mysql": map[string]interface{}{
					"managedMysql": map[string]interface{}{
						"telemetry": map[string]interface{}{
							"enabled": true,
						},
					},
				},
				"redis": map[string]interface{}{
					"managedRedis": map[string]interface{}{
						"telemetry": map[string]interface{}{
							"enabled": true,
						},
					},
				},
				"kafka": map[string]interface{}{
					"managedKafka": map[string]interface{}{
						"telemetry": map[string]interface{}{
							"enabled": true,
						},
					},
				},
				"objectStore": map[string]interface{}{
					"managedObjectStore": map[string]interface{}{
						"telemetry": map[string]interface{}{
							"enabled": true,
						},
					},
				},
				"clickhouse": map[string]interface{}{
					"managedClickhouse": map[string]interface{}{
						"telemetry": map[string]interface{}{
							"enabled": true,
						},
					},
				},
			},
		},
	}
}

func readCRFile(crPath string) (*unstructured.Unstructured, error) {
	crData, err := os.ReadFile(crPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read CR file: %w", err)
	}

	var obj map[string]interface{}
	if err := yaml.Unmarshal(crData, &obj); err != nil {
		return nil, fmt.Errorf("failed to parse CR YAML: %w", err)
	}
	if len(obj) == 0 {
		return nil, fmt.Errorf("CR YAML %q is empty", crPath)
	}

	return &unstructured.Unstructured{Object: obj}, nil
}

func extractWandbCROverrides(cmd *cobra.Command) (wandbCROverrides, error) {
	var overrides wandbCROverrides
	var err error

	if overrides.hostname, err = stringFlagIfChanged(cmd, flagHostname); err != nil {
		return overrides, err
	}
	if overrides.networkingMode, err = stringFlagIfChanged(cmd, flagNetworkingMode); err != nil {
		return overrides, err
	}
	if overrides.ingressClassName, err = stringFlagIfChanged(cmd, flagIngressClassName); err != nil {
		return overrides, err
	}
	if overrides.networkingTLSSecretName, err = stringFlagIfChanged(cmd, flagNetworkingTLSSecretName); err != nil {
		return overrides, err
	}
	if overrides.networkingCertManagerIssuer, err = stringFlagIfChanged(cmd, flagNetworkingCertManagerIssuer); err != nil {
		return overrides, err
	}
	if overrides.networkingCertManagerClusterRef, err = stringFlagIfChanged(cmd, flagNetworkingCertManagerClusterRef); err != nil {
		return overrides, err
	}
	if overrides.managedInfraTelemetryEnabled, err = boolFlagIfChanged(cmd, flagManagedInfraTelemetryEnabled); err != nil {
		return overrides, err
	}
	if overrides.mysqlTelemetryEnabled, err = boolFlagIfChanged(cmd, flagMySQLTelemetryEnabled); err != nil {
		return overrides, err
	}
	if overrides.redisTelemetryEnabled, err = boolFlagIfChanged(cmd, flagRedisTelemetryEnabled); err != nil {
		return overrides, err
	}
	if overrides.kafkaTelemetryEnabled, err = boolFlagIfChanged(cmd, flagKafkaTelemetryEnabled); err != nil {
		return overrides, err
	}
	if overrides.objectStoreTelemetryEnabled, err = boolFlagIfChanged(cmd, flagObjectStoreTelemetryEnabled); err != nil {
		return overrides, err
	}
	if overrides.clickHouseTelemetryEnabled, err = boolFlagIfChanged(cmd, flagClickHouseTelemetryEnabled); err != nil {
		return overrides, err
	}
	if cmd.Flags().Changed(flagNetworkingAnnotations) {
		annotations, err := cmd.Flags().GetStringToString(flagNetworkingAnnotations)
		if err != nil {
			return overrides, err
		}
		overrides.networkingAnnotations = annotations
	}

	return overrides, nil
}

func extractOperatorTelemetryOverrides(cmd *cobra.Command) (operatorTelemetryOverrides, error) {
	var overrides operatorTelemetryOverrides
	var err error

	if overrides.mode, err = stringFlagIfChanged(cmd, flagOperatorTelemetryMode); err != nil {
		return overrides, err
	}
	if overrides.namespace, err = stringFlagIfChanged(cmd, flagOperatorTelemetryNamespace); err != nil {
		return overrides, err
	}
	if overrides.otelSecretName, err = stringFlagIfChanged(cmd, flagOperatorTelemetryOTelSecretName); err != nil {
		return overrides, err
	}
	if overrides.otelProtocol, err = stringFlagIfChanged(cmd, flagOperatorTelemetryOTelProtocol); err != nil {
		return overrides, err
	}
	if overrides.otelServiceName, err = stringFlagIfChanged(cmd, flagOperatorTelemetryOTelService); err != nil {
		return overrides, err
	}
	if overrides.resourceAttributes, err = stringFlagIfChanged(cmd, flagOperatorTelemetryOTelResources); err != nil {
		return overrides, err
	}
	if overrides.forwardEndpoint, err = stringFlagIfChanged(cmd, flagOperatorTelemetryForwardURL); err != nil {
		return overrides, err
	}

	return overrides, nil
}

func applyWandbCROverrides(cr *unstructured.Unstructured, overrides wandbCROverrides) error {
	if overrides.hostname != nil {
		setOrRemoveNestedString(cr.Object, overrides.hostname, "spec", "wandb", "hostname")
	}

	mode := overrides.networkingMode
	if mode == nil && overrides.ingressClassName != nil {
		defaultMode := "ingress"
		mode = &defaultMode
	}

	normalizedMode, err := normalizeNetworkingMode(mode)
	if err != nil {
		return err
	}

	switch {
	case normalizedMode != nil && *normalizedMode == "":
		if overrides.hasAdditionalNetworkingConfig() {
			return fmt.Errorf("--%s=none cannot be combined with other networking settings", flagNetworkingMode)
		}
		removeNestedFieldAndEmptyParents(cr.Object, "spec", "networking")
	case normalizedMode != nil:
		if err := unstructured.SetNestedField(cr.Object, *normalizedMode, "spec", "networking", "mode"); err != nil {
			return err
		}
	}

	if normalizedMode != nil {
		switch *normalizedMode {
		case "ingress":
			removeNestedFieldAndEmptyParents(cr.Object, "spec", "networking", "gatewayAPI")
		case "gateway":
			removeNestedFieldAndEmptyParents(cr.Object, "spec", "networking", "ingress")
		}
	}

	if overrides.ingressClassName != nil {
		if normalizedMode != nil && *normalizedMode == "gateway" {
			return fmt.Errorf("%s requires --%s=ingress", flagIngressClassName, flagNetworkingMode)
		}
		setOrRemoveNestedString(cr.Object, overrides.ingressClassName, "spec", "networking", "ingress", "ingressClassName")
	}

	if overrides.networkingAnnotations != nil {
		if len(overrides.networkingAnnotations) == 0 {
			removeNestedFieldAndEmptyParents(cr.Object, "spec", "networking", "annotations")
		} else if err := unstructured.SetNestedStringMap(cr.Object, sanitizeStringMap(overrides.networkingAnnotations), "spec", "networking", "annotations"); err != nil {
			return err
		}
	}

	setOrRemoveNestedString(cr.Object, overrides.networkingTLSSecretName, "spec", "networking", "tls", "secretName")
	setOrRemoveNestedString(cr.Object, overrides.networkingCertManagerClusterRef, "spec", "networking", "tls", "certManager", "clusterIssuer")
	setOrRemoveNestedString(cr.Object, overrides.networkingCertManagerIssuer, "spec", "networking", "tls", "certManager", "issuer")

	removeEmptyMap(cr.Object, "spec", "networking", "tls", "certManager")
	removeEmptyMap(cr.Object, "spec", "networking", "tls")
	removeEmptyMap(cr.Object, "spec", "networking", "ingress")
	removeEmptyMap(cr.Object, "spec", "networking")

	if overrides.managedInfraTelemetryEnabled != nil {
		for _, component := range []string{"mysql", "redis", "kafka", "objectStore", "clickhouse"} {
			if err := setManagedInfraTelemetry(cr, component, *overrides.managedInfraTelemetryEnabled); err != nil {
				return err
			}
		}
	}

	for component, enabled := range map[string]*bool{
		"mysql":       overrides.mysqlTelemetryEnabled,
		"redis":       overrides.redisTelemetryEnabled,
		"kafka":       overrides.kafkaTelemetryEnabled,
		"objectStore": overrides.objectStoreTelemetryEnabled,
		"clickhouse":  overrides.clickHouseTelemetryEnabled,
	} {
		if enabled == nil {
			continue
		}
		if err := setManagedInfraTelemetry(cr, component, *enabled); err != nil {
			return err
		}
	}

	return nil
}

func buildOperatorReleaseValues(managedNamespace string, overrides operatorTelemetryOverrides) (map[string]interface{}, error) {
	values := map[string]interface{}{
		"wandb": map[string]interface{}{
			"install": false,
		},
	}

	if !overrides.changed() {
		return values, nil
	}

	if overrides.mode == nil {
		return nil, fmt.Errorf("operator telemetry flags require --%s", flagOperatorTelemetryMode)
	}

	mode := strings.ToLower(strings.TrimSpace(*overrides.mode))
	switch mode {
	case "off", "forward", "full":
	default:
		return nil, fmt.Errorf("operator telemetry mode must be one of \"off\", \"forward\", or \"full\"")
	}

	if mode == "off" {
		if overrides.namespace != nil || overrides.otelSecretName != nil || overrides.otelProtocol != nil ||
			overrides.otelServiceName != nil || overrides.resourceAttributes != nil || overrides.forwardEndpoint != nil {
			return nil, fmt.Errorf("operator telemetry mode %q cannot be combined with additional operator telemetry settings", mode)
		}
		values["telemetry"] = map[string]interface{}{
			"mode": "off",
		}
		values["victoria-metrics-operator"] = map[string]interface{}{"enabled": false}
		values["grafana-operator"] = map[string]interface{}{"enabled": false}
		return values, nil
	}

	if mode == "full" && overrides.forwardEndpoint != nil {
		return nil, fmt.Errorf("--%s is only valid with --%s=forward", flagOperatorTelemetryForwardURL, flagOperatorTelemetryMode)
	}

	namespace := strings.TrimSpace(managedNamespace)
	if overrides.namespace != nil && strings.TrimSpace(*overrides.namespace) != "" {
		namespace = strings.TrimSpace(*overrides.namespace)
	}
	if namespace == "" {
		return nil, fmt.Errorf("operator telemetry requires a managed namespace")
	}

	if overrides.otelSecretName == nil || strings.TrimSpace(*overrides.otelSecretName) == "" {
		return nil, fmt.Errorf("--%s is required when --%s is %q or %q", flagOperatorTelemetryOTelSecretName, flagOperatorTelemetryMode, "forward", "full")
	}

	if mode == "forward" && (overrides.forwardEndpoint == nil || strings.TrimSpace(*overrides.forwardEndpoint) == "") {
		return nil, fmt.Errorf("--%s is required when --%s=forward", flagOperatorTelemetryForwardURL, flagOperatorTelemetryMode)
	}

	telemetryValues := map[string]interface{}{
		"mode":      mode,
		"namespace": namespace,
		"otel": map[string]interface{}{
			"secretName":         strings.TrimSpace(*overrides.otelSecretName),
			"protocol":           stringOrDefault(overrides.otelProtocol, "http/protobuf"),
			"serviceName":        stringOrDefault(overrides.otelServiceName, "wandb-service"),
			"resourceAttributes": stringOrDefault(overrides.resourceAttributes, ""),
		},
	}

	if mode == "forward" {
		telemetryValues["forwarding"] = map[string]interface{}{
			"otlp": map[string]interface{}{
				"endpoint": strings.TrimSpace(*overrides.forwardEndpoint),
			},
		}
	}

	values["telemetry"] = telemetryValues
	values["victoria-metrics-operator"] = map[string]interface{}{"enabled": true}
	values["grafana-operator"] = map[string]interface{}{"enabled": mode == "full"}

	return values, nil
}

func (o operatorTelemetryOverrides) changed() bool {
	return o.mode != nil || o.namespace != nil || o.otelSecretName != nil || o.otelProtocol != nil ||
		o.otelServiceName != nil || o.resourceAttributes != nil || o.forwardEndpoint != nil
}

func (o wandbCROverrides) hasAdditionalNetworkingConfig() bool {
	return o.ingressClassName != nil || o.networkingAnnotations != nil || o.networkingTLSSecretName != nil ||
		o.networkingCertManagerIssuer != nil || o.networkingCertManagerClusterRef != nil
}

func normalizeNetworkingMode(mode *string) (*string, error) {
	if mode == nil {
		return nil, nil
	}

	normalized := strings.ToLower(strings.TrimSpace(*mode))
	switch normalized {
	case "", "none":
		normalized = ""
	case "ingress":
	case "gateway", "gatewayapi":
		normalized = "gateway"
	default:
		return nil, fmt.Errorf("networking mode must be one of \"none\", \"ingress\", or \"gateway\"")
	}

	return &normalized, nil
}

func setManagedInfraTelemetry(cr *unstructured.Unstructured, component string, enabled bool) error {
	path := managedInfraTelemetryPath(cr.Object, component)
	return unstructured.SetNestedField(cr.Object, enabled, append(path, "telemetry", "enabled")...)
}

func managedInfraTelemetryPath(obj map[string]interface{}, component string) []string {
	switch component {
	case "mysql":
		if hasNestedField(obj, "spec", "mysql", "managedMysql") || !hasNestedField(obj, "spec", "mysql") {
			return []string{"spec", "mysql", "managedMysql"}
		}
		return []string{"spec", "mysql"}
	case "redis":
		if hasNestedField(obj, "spec", "redis", "managedRedis") || !hasNestedField(obj, "spec", "redis") {
			return []string{"spec", "redis", "managedRedis"}
		}
		return []string{"spec", "redis"}
	case "kafka":
		if hasNestedField(obj, "spec", "kafka", "managedKafka") || !hasNestedField(obj, "spec", "kafka") {
			return []string{"spec", "kafka", "managedKafka"}
		}
		return []string{"spec", "kafka"}
	case "objectStore":
		if hasNestedField(obj, "spec", "objectStore", "managedObjectStore") || !hasNestedField(obj, "spec", "objectStore") && !hasNestedField(obj, "spec", "minio") {
			return []string{"spec", "objectStore", "managedObjectStore"}
		}
		if hasNestedField(obj, "spec", "objectStore") {
			return []string{"spec", "objectStore"}
		}
		return []string{"spec", "minio"}
	case "clickhouse":
		if hasNestedField(obj, "spec", "clickhouse", "managedClickhouse") || !hasNestedField(obj, "spec", "clickhouse") {
			return []string{"spec", "clickhouse", "managedClickhouse"}
		}
		return []string{"spec", "clickhouse"}
	default:
		return []string{"spec", component}
	}
}

func stringFlagIfChanged(cmd *cobra.Command, name string) (*string, error) {
	if !cmd.Flags().Changed(name) {
		return nil, nil
	}
	value, err := cmd.Flags().GetString(name)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func boolFlagIfChanged(cmd *cobra.Command, name string) (*bool, error) {
	if !cmd.Flags().Changed(name) {
		return nil, nil
	}
	value, err := cmd.Flags().GetBool(name)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func setOrRemoveNestedString(obj map[string]interface{}, value *string, fields ...string) {
	if value == nil {
		return
	}
	if strings.TrimSpace(*value) == "" {
		removeNestedFieldAndEmptyParents(obj, fields...)
		return
	}
	_ = unstructured.SetNestedField(obj, strings.TrimSpace(*value), fields...)
}

func removeNestedFieldAndEmptyParents(obj map[string]interface{}, fields ...string) {
	if len(fields) == 0 {
		return
	}
	unstructured.RemoveNestedField(obj, fields...)
	for depth := len(fields) - 1; depth > 0; depth-- {
		removeEmptyMap(obj, fields[:depth]...)
	}
}

func removeEmptyMap(obj map[string]interface{}, fields ...string) {
	if len(fields) == 0 {
		return
	}
	value, found, err := unstructured.NestedMap(obj, fields...)
	if err != nil || !found || len(value) > 0 {
		return
	}
	unstructured.RemoveNestedField(obj, fields...)
}

func hasNestedField(obj map[string]interface{}, fields ...string) bool {
	_, found, err := unstructured.NestedFieldNoCopy(obj, fields...)
	return err == nil && found
}

func sanitizeStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		out[trimmedKey] = strings.TrimSpace(value)
	}
	return out
}

func wandbCRUsesIngress(cr *unstructured.Unstructured) bool {
	mode, found, err := unstructured.NestedString(cr.Object, "spec", "networking", "mode")
	return err == nil && found && strings.EqualFold(mode, "ingress")
}

func stringOrDefault(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}
