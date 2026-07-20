package operator

import (
	"bytes"
	"testing"

	"helm.sh/helm/v4/pkg/chart/common"
	v2chart "helm.sh/helm/v4/pkg/chart/v2"
)

const brokenGrafanaNotificationPolicyCRD = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: grafananotificationpolicies.grafana.integreatly.org
spec:
  versions:
    - name: v1beta1
      schema:
        openAPIV3Schema:
          properties:
            spec:
              properties:
                route:
                  properties:
                    active_time_intervals:
                      type: array
                    continue:
                      type: boolean
                    match_re:
                      type: object
                    matchers:
                      type: array
                    mute_time_intervals:
                      type: array
                    object_matchers:
                      type: array
                    receiver:
                      type: string
                  type: object
                  x-kubernetes-validations:
                    - message: continue is invalid on the top level route node
                      rule: '!has(self.continue)'
                    - message: match_re is invalid on the top level route node
                      rule: '!has(self.match_re)'
                    - message: matchers is invalid on the top level route node
                      rule: '!has(self.matchers)'
                    - message: object_matchers is invalid on the top level route node
                      rule: '!has(self.object_matchers)'
                    - message: mute_time_intervals is invalid on the top level route node
                      rule: '!has(self.mute_time_intervals)'
                    - message: active_time_intervals is invalid on the top level route node
                      rule: '!has(self.active_time_intervals)'
                    - message: receiver must be present
                      rule: 'has(self.receiver)'
              type: object
          type: object
`

func TestPatchGrafanaNotificationPolicyCRDs(t *testing.T) {
	immutable := &common.File{
		Name: "crds/" + grafanaNotificationPolicyCRDFile,
		Data: []byte(brokenGrafanaNotificationPolicyCRD),
	}
	mutable := &common.File{
		Name: "files/crds/" + grafanaNotificationPolicyCRDFile,
		Data: []byte(brokenGrafanaNotificationPolicyCRD),
	}

	root := testChart("operator")
	grafana := testChart("grafana-operator")
	crds := testChart("crds")
	grafana.Files = []*common.File{mutable}
	crds.Files = []*common.File{immutable}
	grafana.AddDependency(crds)
	root.AddDependency(grafana)

	if err := patchGrafanaNotificationPolicyCRDs(root); err != nil {
		t.Fatalf("patchGrafanaNotificationPolicyCRDs() error = %v", err)
	}

	for _, file := range []*common.File{immutable, mutable} {
		if bytes.Contains(file.Data, []byte(invalidGrafanaContinueRule)) {
			t.Errorf("%s still contains invalid rule %q", file.Name, invalidGrafanaContinueRule)
		}
		if !bytes.Contains(file.Data, []byte(escapedGrafanaContinueRule)) {
			t.Errorf("%s does not contain escaped rule %q", file.Name, escapedGrafanaContinueRule)
		}
		if !bytes.Contains(file.Data, []byte("has(self.receiver)")) {
			t.Errorf("%s lost an unrelated valid rule", file.Name)
		}
		for _, rule := range []string{
			"!has(self.match_re)",
			"!has(self.matchers)",
			"!has(self.object_matchers)",
			"!has(self.mute_time_intervals)",
			"!has(self.active_time_intervals)",
		} {
			if !bytes.Contains(file.Data, []byte(rule)) {
				t.Errorf("%s lost valid rule %q", file.Name, rule)
			}
		}
	}
}

func TestPatchGrafanaNotificationPolicyCRDLeavesFixedRuleUnchanged(t *testing.T) {
	fixed := bytes.ReplaceAll(
		[]byte(brokenGrafanaNotificationPolicyCRD),
		[]byte(invalidGrafanaContinueRule),
		[]byte(escapedGrafanaContinueRule),
	)
	file := &common.File{
		Name: "crds/" + grafanaNotificationPolicyCRDFile,
		Data: fixed,
	}
	root := testChart("operator")
	root.Files = []*common.File{file}

	if err := patchGrafanaNotificationPolicyCRDs(root); err != nil {
		t.Fatalf("patchGrafanaNotificationPolicyCRDs() error = %v", err)
	}
	if !bytes.Equal(file.Data, fixed) {
		t.Fatal("patchGrafanaNotificationPolicyCRDs() changed an already-fixed CRD")
	}
}

func TestPatchGrafanaNotificationPolicyCRDsIgnoresOtherFiles(t *testing.T) {
	root := testChart("operator")
	original := []byte("rule: '!has(self.continue)'")
	file := &common.File{
		Name: "crds/another.yaml",
		Data: append([]byte(nil), original...),
	}
	root.Files = []*common.File{file}

	if err := patchGrafanaNotificationPolicyCRDs(root); err != nil {
		t.Fatalf("patchGrafanaNotificationPolicyCRDs() error = %v", err)
	}
	if !bytes.Equal(file.Data, original) {
		t.Fatal("patchGrafanaNotificationPolicyCRDs() changed an unrelated file")
	}
}

func testChart(name string) *v2chart.Chart {
	return &v2chart.Chart{Metadata: &v2chart.Metadata{Name: name}}
}
