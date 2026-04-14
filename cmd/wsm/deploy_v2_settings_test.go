package main

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestApplyWandbCROverridesCurrentSchema(t *testing.T) {
	cr := defaultWandbCR()

	disableTelemetry := false
	enableMySQLTelemetry := true

	err := applyWandbCROverrides(cr, wandbCROverrides{
		managedInfraTelemetryEnabled: &disableTelemetry,
		mysqlTelemetryEnabled:        &enableMySQLTelemetry,
	})
	if err != nil {
		t.Fatalf("applyWandbCROverrides returned error: %v", err)
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
