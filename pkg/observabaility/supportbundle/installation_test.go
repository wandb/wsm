package supportbundle

import (
	"strings"
	"testing"
	"time"

	v2 "github.com/wandb/operator/api/v2"
	"sigs.k8s.io/yaml"
)

func TestFromCRResolvesInstallation(t *testing.T) {
	inst, warnings, err := FromCR(testCR("forward"))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if inst.Telemetry == nil || inst.Telemetry.Namespace != "wandb-telemetry" {
		t.Fatalf("telemetry not resolved: %+v", inst.Telemetry)
	}
	if got := inst.IdentityAnnotations; len(got) != 1 || got["eks.amazonaws.com/role-arn"] == "" {
		t.Errorf("only identity annotations should be copied, got %v", got)
	}
	if inst.AppServiceAccount != "wandb" {
		t.Errorf("app service account default = %q", inst.AppServiceAccount)
	}
}

func TestFromCRRejectsUnsafeObjectStore(t *testing.T) {
	cr := testCR("")
	conn := cr.Status.ObjectStoreStatus["default"]
	conn.Connection.URL = v2.LiteralValue("s3://ak:sk@host/bucket")
	cr.Status.ObjectStoreStatus["default"] = conn
	if _, _, err := FromCR(cr); err == nil || !strings.Contains(err.Error(), "not a Secret reference") {
		t.Fatalf("literal url must be rejected, got %v", err)
	}

	cr = testCR("")
	conn = cr.Status.ObjectStoreStatus["default"]
	conn.Connection.SecretKey = v2.LiteralValue("hunter2")
	cr.Status.ObjectStoreStatus["default"] = conn
	if _, _, err := FromCR(cr); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("literal secret key must be rejected without echoing it, got %v", err)
	}

	cr = testCR("")
	cr.Status.ObjectStoreStatus = nil
	if _, _, err := FromCR(cr); err == nil {
		t.Fatal("missing object store status must be rejected")
	}
}

func TestTelemetryGating(t *testing.T) {
	if inst := testInstallation("off"); inst.Telemetry != nil {
		t.Error("mode off must not enable Victoria collection")
	}

	cr := testCR("full")
	cr.Status.TelemetryStatus.Ready = false
	inst, warnings, _ := FromCR(cr)
	if inst.Telemetry != nil || len(warnings) == 0 {
		t.Error("unready telemetry must be skipped with a warning")
	}

	cr = testCR("full")
	cr.Status.TelemetryStatus.Connection.MetricsReadEndpoint = "http://user:pw@vm:8428"
	cr.Status.TelemetryStatus.Connection.LogsReadEndpoint = ""
	inst, warnings, _ = FromCR(cr)
	if inst.Telemetry != nil || len(warnings) < 2 {
		t.Errorf("credentialed endpoint must be dropped, got %+v %v", inst.Telemetry, warnings)
	}
}

func TestResolveImage(t *testing.T) {
	inst := testInstallation("")
	if _, err := ResolveImage(inst, "", "latest"); err == nil {
		t.Error("latest must be rejected")
	}
	img, _ := ResolveImage(inst, "", "abc")
	if img != v2.DefaultImageRegistry+"/wandb/lumen:abc" {
		t.Errorf("default image = %s", img)
	}
	inst.ImageRegistry = "registry.corp/mirror"
	img, _ = ResolveImage(inst, "", "abc")
	if img != "registry.corp/mirror/wandb/lumen:abc" {
		t.Errorf("CR registry image = %s", img)
	}
	img, _ = ResolveImage(inst, "other.corp/", "abc")
	if img != "other.corp/wandb/lumen:abc" {
		t.Errorf("override image = %s", img)
	}
}

func TestBuildCollectionSpec(t *testing.T) {
	for _, tc := range []struct {
		mode, wantSources string
		skip              bool
	}{
		{"", "cluster-resources,cluster-info,pod-logs", false},
		{"forward", "cluster-resources,cluster-info,pod-logs,victoria-stack", false},
		{"forward", "cluster-resources,cluster-info,pod-logs", true},
	} {
		inst := testInstallation(tc.mode)
		out, sources, err := BuildCollectionSpec(inst, time.Hour, !tc.skip)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(sources, ","); got != tc.wantSources {
			t.Errorf("mode=%q skip=%t sources=%s", tc.mode, tc.skip, got)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(out, &doc); err != nil {
			t.Fatal(err)
		}
		text := string(out)
		if strings.Contains(text, "dns") {
			t.Error("dns collector must not be configured")
		}
		if !strings.Contains(text, "maxAge: 1h") || !strings.Contains(text, "- wandb\n") {
			t.Errorf("window/namespace not applied:\n%s", text)
		}
		if strings.Contains(text, "victoriaStack") != strings.Contains(tc.wantSources, "victoria") {
			t.Errorf("victoriaStack presence mismatch:\n%s", text)
		}
	}
}

func TestSelectInstallation(t *testing.T) {
	one := []InstallationRef{{Namespace: "wandb", Name: "wandb"}}
	two := append(one, InstallationRef{Namespace: "team-b", Name: "wandb"})

	if ref, err := SelectInstallation(one, "", ""); err != nil || ref.String() != "wandb/wandb" {
		t.Errorf("single CR must be discovered: %v %v", ref, err)
	}
	if _, err := SelectInstallation(two, "", ""); err == nil || !strings.Contains(err.Error(), "team-b/wandb") {
		t.Errorf("ambiguous cluster must list candidates: %v", err)
	}
	if ref, err := SelectInstallation(two, "team-b", ""); err != nil || ref.Namespace != "team-b" {
		t.Errorf("namespace filter must disambiguate: %v %v", ref, err)
	}
	if _, err := SelectInstallation(two, "", "other"); err == nil {
		t.Error("unknown name must error")
	}
	if _, err := SelectInstallation(nil, "", ""); err == nil {
		t.Error("no CRs must error")
	}
}

func TestTelemetryOffKeepsKubernetesSources(t *testing.T) {
	for _, mode := range []string{"", "off"} {
		inst := testInstallation(mode)
		out, sources, err := BuildCollectionSpec(inst, time.Hour, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(sources, ","); got != "cluster-resources,cluster-info,pod-logs" {
			t.Errorf("mode=%q sources=%s", mode, got)
		}
		if strings.Contains(string(out), "victoriaStack") {
			t.Errorf("mode=%q must not configure victoriaStack", mode)
		}
	}
}
