package operator

import (
	"bytes"
	"fmt"
	"path/filepath"

	helmchart "helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/common"
)

const (
	grafanaNotificationPolicyCRDFile = "grafana.integreatly.org_grafananotificationpolicies.yaml"
	invalidGrafanaContinueRule       = "!has(self.continue)"
	escapedGrafanaContinueRule       = "!has(self.__continue__)"
)

// patchGrafanaNotificationPolicyCRDs fixes a CEL rule emitted by the Grafana
// Operator 5.21.4 CRD generator. "continue" is a CEL reserved keyword, so the
// field must use Kubernetes' __{keyword}__ escaping in validation expressions.
// Replacing the exact expression in the exact CRD keeps the workaround narrow;
// a future chart containing the escaped rule passes through unchanged.
// Upstream fix: https://github.com/grafana/grafana-operator/pull/2583
func patchGrafanaNotificationPolicyCRDs(chrt helmchart.Charter) error {
	accessor, err := helmchart.NewAccessor(chrt)
	if err != nil {
		return fmt.Errorf("access Helm chart: %w", err)
	}

	patchFiles := func(files []*common.File) {
		for _, file := range files {
			if filepath.Base(file.Name) != grafanaNotificationPolicyCRDFile {
				continue
			}
			file.Data = bytes.ReplaceAll(
				file.Data,
				[]byte(invalidGrafanaContinueRule),
				[]byte(escapedGrafanaContinueRule),
			)
		}
	}

	patchFiles(accessor.Files())
	patchFiles(accessor.Templates())

	for _, dependency := range accessor.Dependencies() {
		if err := patchGrafanaNotificationPolicyCRDs(dependency); err != nil {
			return err
		}
	}

	return nil
}
