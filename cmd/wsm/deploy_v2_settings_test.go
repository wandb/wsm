package main

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestApplyWandbCROverridesCurrentSchema(t *testing.T) {
	cr := defaultWandbCR()

	host := "https://wandb.example.com"
	mode := "ingress"
	ingressClass := "nginx"
	tlsSecret := "wandb-tls"
	clusterIssuer := "letsencrypt"
	disableTelemetry := false
	enableMySQLTelemetry := true

	err := applyWandbCROverrides(cr, wandbCROverrides{
		hostname:                        &host,
		networkingMode:                  &mode,
		ingressClassName:                &ingressClass,
		networkingAnnotations:           map[string]string{" nginx.ingress.kubernetes.io/proxy-body-size ": " 0 "},
		networkingTLSSecretName:         &tlsSecret,
		networkingCertManagerClusterRef: &clusterIssuer,
		managedInfraTelemetryEnabled:    &disableTelemetry,
		mysqlTelemetryEnabled:           &enableMySQLTelemetry,
	})
	if err != nil {
		t.Fatalf("applyWandbCROverrides returned error: %v", err)
	}

	if got, _, _ := unstructured.NestedString(cr.Object, "spec", "wandb", "hostname"); got != host {
		t.Fatalf("expected hostname %q, got %q", host, got)
	}
	if got, _, _ := unstructured.NestedString(cr.Object, "spec", "networking", "mode"); got != "ingress" {
		t.Fatalf("expected networking mode ingress, got %q", got)
	}
	if got, _, _ := unstructured.NestedString(cr.Object, "spec", "networking", "ingress", "ingressClassName"); got != ingressClass {
		t.Fatalf("expected ingressClassName %q, got %q", ingressClass, got)
	}
	if got, _, _ := unstructured.NestedString(cr.Object, "spec", "networking", "tls", "secretName"); got != tlsSecret {
		t.Fatalf("expected tls secret %q, got %q", tlsSecret, got)
	}
	if got, _, _ := unstructured.NestedString(cr.Object, "spec", "networking", "tls", "certManager", "clusterIssuer"); got != clusterIssuer {
		t.Fatalf("expected cluster issuer %q, got %q", clusterIssuer, got)
	}
	if got, _, _ := unstructured.NestedStringMap(cr.Object, "spec", "networking", "annotations"); got["nginx.ingress.kubernetes.io/proxy-body-size"] != "0" {
		t.Fatalf("expected trimmed annotation value, got %#v", got)
	}
	if got, _, _ := unstructured.NestedBool(cr.Object, "spec", "mysql", "managedMysql", "telemetry", "enabled"); !got {
		t.Fatalf("expected mysql telemetry to remain enabled")
	}
	if got, _, _ := unstructured.NestedBool(cr.Object, "spec", "redis", "managedRedis", "telemetry", "enabled"); got {
		t.Fatalf("expected redis telemetry to be disabled")
	}
	if got, _, _ := unstructured.NestedBool(cr.Object, "spec", "objectStore", "managedObjectStore", "telemetry", "enabled"); got {
		t.Fatalf("expected objectStore telemetry to be disabled")
	}
}

func TestApplyWandbCROverridesFallsBackToOldTelemetryPaths(t *testing.T) {
	cr := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps.wandb.com/v2",
			"kind":       "WeightsAndBiases",
			"spec": map[string]interface{}{
				"mysql":      map[string]interface{}{},
				"redis":      map[string]interface{}{},
				"kafka":      map[string]interface{}{},
				"minio":      map[string]interface{}{},
				"clickhouse": map[string]interface{}{},
			},
		},
	}

	disableTelemetry := false
	err := applyWandbCROverrides(cr, wandbCROverrides{
		managedInfraTelemetryEnabled: &disableTelemetry,
	})
	if err != nil {
		t.Fatalf("applyWandbCROverrides returned error: %v", err)
	}

	if got, _, _ := unstructured.NestedBool(cr.Object, "spec", "mysql", "telemetry", "enabled"); got {
		t.Fatalf("expected old mysql telemetry path to be disabled")
	}
	if got, _, _ := unstructured.NestedBool(cr.Object, "spec", "minio", "telemetry", "enabled"); got {
		t.Fatalf("expected old minio telemetry path to be disabled")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(cr.Object, "spec", "mysql", "managedMysql"); found {
		t.Fatalf("did not expect managedMysql path to be created for old-style CR")
	}
}

func TestApplyWandbCROverridesRejectsIngressClassWithGatewayMode(t *testing.T) {
	cr := defaultWandbCR()
	mode := "gateway"
	ingressClass := "nginx"

	err := applyWandbCROverrides(cr, wandbCROverrides{
		networkingMode:   &mode,
		ingressClassName: &ingressClass,
	})
	if err == nil {
		t.Fatalf("expected error when ingress class is combined with gateway mode")
	}
}

func TestApplyWandbCROverridesNetworkingModeNoneRemovesEntireNetworkingTree(t *testing.T) {
	cr := defaultWandbCR()
	if err := unstructured.SetNestedField(cr.Object, "gateway", "spec", "networking", "mode"); err != nil {
		t.Fatalf("failed to seed networking mode: %v", err)
	}
	if err := unstructured.SetNestedField(cr.Object, "existing", "spec", "networking", "ingress", "ingressClassName"); err != nil {
		t.Fatalf("failed to seed ingress class: %v", err)
	}
	if err := unstructured.SetNestedField(cr.Object, "listener", "spec", "networking", "gatewayAPI", "listenerName"); err != nil {
		t.Fatalf("failed to seed gateway listener: %v", err)
	}
	if err := unstructured.SetNestedField(cr.Object, "tls-secret", "spec", "networking", "tls", "secretName"); err != nil {
		t.Fatalf("failed to seed tls secret: %v", err)
	}
	if err := unstructured.SetNestedStringMap(cr.Object, map[string]string{"foo": "bar"}, "spec", "networking", "annotations"); err != nil {
		t.Fatalf("failed to seed annotations: %v", err)
	}

	mode := "none"
	if err := applyWandbCROverrides(cr, wandbCROverrides{networkingMode: &mode}); err != nil {
		t.Fatalf("applyWandbCROverrides returned error: %v", err)
	}

	if _, found, _ := unstructured.NestedFieldNoCopy(cr.Object, "spec", "networking"); found {
		t.Fatalf("expected spec.networking to be removed entirely")
	}
}

func TestApplyWandbCROverridesRejectsNetworkingModeNoneWithOtherNetworkingFlags(t *testing.T) {
	cr := defaultWandbCR()
	mode := "none"
	ingressClass := "nginx"

	err := applyWandbCROverrides(cr, wandbCROverrides{
		networkingMode:   &mode,
		ingressClassName: &ingressClass,
	})
	if err == nil {
		t.Fatalf("expected error when networking mode none is combined with other networking settings")
	}
}

func TestBuildOperatorReleaseValuesForwardMode(t *testing.T) {
	mode := "forward"
	secretName := "wandb-otel-connection"
	forwardEndpoint := "https://otel.example.com"

	values, err := buildOperatorReleaseValues("wandb", operatorTelemetryOverrides{
		mode:            &mode,
		otelSecretName:  &secretName,
		forwardEndpoint: &forwardEndpoint,
	})
	if err != nil {
		t.Fatalf("buildOperatorReleaseValues returned error: %v", err)
	}

	if got, _, _ := unstructured.NestedString(values, "telemetry", "mode"); got != "forward" {
		t.Fatalf("expected telemetry.mode=forward, got %q", got)
	}
	if got, _, _ := unstructured.NestedString(values, "telemetry", "namespace"); got != "wandb" {
		t.Fatalf("expected telemetry namespace wandb, got %q", got)
	}
	if got, _, _ := unstructured.NestedString(values, "telemetry", "otel", "secretName"); got != secretName {
		t.Fatalf("expected telemetry.otel.secretName %q, got %q", secretName, got)
	}
	if got, _, _ := unstructured.NestedString(values, "telemetry", "forwarding", "otlp", "endpoint"); got != forwardEndpoint {
		t.Fatalf("expected telemetry forwarding endpoint %q, got %q", forwardEndpoint, got)
	}
	if got, _, _ := unstructured.NestedBool(values, "victoria-metrics-operator", "enabled"); !got {
		t.Fatalf("expected victoria-metrics-operator to be enabled")
	}
	if got, _, _ := unstructured.NestedBool(values, "grafana-operator", "enabled"); got {
		t.Fatalf("expected grafana-operator to remain disabled in forward mode")
	}
}

func TestBuildOperatorReleaseValuesRequiresModeForOtherFlags(t *testing.T) {
	secretName := "wandb-otel-connection"

	_, err := buildOperatorReleaseValues("wandb", operatorTelemetryOverrides{
		otelSecretName: &secretName,
	})
	if err == nil {
		t.Fatalf("expected error when operator telemetry flags are set without a mode")
	}
}

func TestBuildOperatorReleaseValuesTreatsEmptyOTelStringsAsUnset(t *testing.T) {
	mode := "full"
	secretName := "wandb-otel-connection"
	empty := "   "

	values, err := buildOperatorReleaseValues("wandb", operatorTelemetryOverrides{
		mode:            &mode,
		otelSecretName:  &secretName,
		otelProtocol:    &empty,
		otelServiceName: &empty,
	})
	if err != nil {
		t.Fatalf("buildOperatorReleaseValues returned error: %v", err)
	}

	if got, _, _ := unstructured.NestedString(values, "telemetry", "otel", "protocol"); got != "http/protobuf" {
		t.Fatalf("expected telemetry.otel.protocol to fall back to default, got %q", got)
	}
	if got, _, _ := unstructured.NestedString(values, "telemetry", "otel", "serviceName"); got != "wandb-service" {
		t.Fatalf("expected telemetry.otel.serviceName to fall back to default, got %q", got)
	}
}
