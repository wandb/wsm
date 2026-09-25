package operator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	yamlv3 "gopkg.in/yaml.v3"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/cli"
	"helm.sh/helm/v4/pkg/release"
)

// renderKubeVersion satisfies the charts' kubeVersion floors (cert-manager >=1.22,
// nginx-gateway-fabric >=1.31); the default client capabilities report too old a version.
const renderKubeVersion = "v1.33.0"

// OperatorChartRepo is the public OCI repo base for the operator chart, matching
// DeployOperator. The mirror renders from here; check renders the mirror's copy.
const OperatorChartRepo = "oci://us-docker.pkg.dev/wandb-production/public/wandb/charts"

// RenderChartImages renders chartRef@version client-side (no cluster) and returns the
// sorted, de-duplicated container images it would create. Beyond plain container images
// it captures init containers, images passed via --…-image args (moco sidecars,
// cert-manager acmesolver), image refs on custom resources (e.g. NginxProxy), and hook
// images. values should match the install (minus mirror retargeting); insecure allows a
// self-signed / plain-HTTP chart source.
func RenderChartImages(ctx context.Context, chartRef, version string, values map[string]any, insecure bool) ([]string, error) {
	settings := cli.New()

	actionConfig, err := initActionConfig(settings)
	if err != nil {
		return nil, fmt.Errorf("init helm action config: %w", err)
	}
	registryClient, err := newRegistryClient(settings, "", "", "", insecure, insecure)
	if err != nil {
		return nil, fmt.Errorf("create registry client: %w", err)
	}
	actionConfig.RegistryClient = registryClient

	kubeVersion, err := common.ParseKubeVersion(renderKubeVersion)
	if err != nil {
		return nil, fmt.Errorf("parse kube version: %w", err)
	}

	inst := action.NewInstall(actionConfig)
	inst.DryRunStrategy = action.DryRunClient // render client-side, no cluster
	inst.KubeVersion = kubeVersion
	inst.Replace = true
	inst.ReleaseName = "wsm-render"
	inst.Namespace = "wsm-render"
	inst.Version = version
	// Skip CRDs: their schema-default image strings aren't necessarily pulled.
	inst.IncludeCRDs = false

	cp, err := inst.LocateChart(chartRef, settings)
	if err != nil {
		return nil, fmt.Errorf("locate chart %q: %w", chartRef, err)
	}
	ch, err := loader.Load(cp)
	if err != nil {
		return nil, fmt.Errorf("load chart %q: %w", chartRef, err)
	}
	rel, err := inst.RunWithContext(ctx, ch, values)
	if err != nil {
		return nil, fmt.Errorf("render chart %q: %w", chartRef, err)
	}

	acc, err := release.NewAccessor(rel)
	if err != nil {
		return nil, fmt.Errorf("access rendered release: %w", err)
	}
	var docs strings.Builder
	docs.WriteString(acc.Manifest())
	for _, h := range acc.Hooks() {
		ha, err := release.NewHookAccessor(h)
		if err != nil {
			continue
		}
		docs.WriteString("\n---\n")
		docs.WriteString(ha.Manifest())
	}
	return extractImages(docs.String()), nil
}

// OperatorChartImages renders the operator chart from chartRepo at chartVersion and
// returns the managed-service images it deploys (operator binary, the moco/redis/
// seaweedfs/altinity operators, and the moco sidecars). disabledSubcharts turns off
// subcharts (<name>.enabled=false) for excluded managed types so their images aren't
// derived. Values match DeployOperator's non-mirror install.
func OperatorChartImages(ctx context.Context, chartRepo, chartVersion string, disabledSubcharts []string, insecure bool) ([]string, error) {
	values := map[string]any{
		"wandb":          map[string]any{"install": false},
		"wandb-operator": map[string]any{"image": map[string]any{"pullPolicy": "Always"}},
	}
	for _, sc := range disabledSubcharts {
		values[sc] = map[string]any{"enabled": false}
	}
	return RenderChartImages(ctx, chartRepo+"/operator", chartVersion, values, insecure)
}

// CertManagerImages renders the cert-manager chart from chartRef with the same values
// InstallCertManager uses (startupapicheck disabled), yielding controller/webhook/
// cainjector plus the acmesolver passed via a controller arg.
func CertManagerImages(ctx context.Context, chartRef string, insecure bool) ([]string, error) {
	values := map[string]any{
		"crds":            map[string]any{"enabled": true},
		"startupapicheck": map[string]any{"enabled": false},
	}
	return RenderChartImages(ctx, chartRef, CertManagerVersion, values, insecure)
}

// NginxGatewayImages renders the nginx-gateway-fabric chart (at NginxGatewayVersion)
// from chartRef and returns its images: the control-plane image and the data-plane
// nginx image carried on the NginxProxy custom resource.
func NginxGatewayImages(ctx context.Context, chartRef string, insecure bool) ([]string, error) {
	return RenderChartImages(ctx, chartRef, NginxGatewayVersion, map[string]any{}, insecure)
}

// KubeStateMetricsImages renders the kube-state-metrics chart from chartRef. The tag
// is pinned to match InstallKubeStateMetrics rather than the chart default.
func KubeStateMetricsImages(ctx context.Context, chartRef string, insecure bool) ([]string, error) {
	values := map[string]any{
		"image": map[string]any{"tag": KubeStateMetricsImageTag},
	}
	return RenderChartImages(ctx, chartRef, KubeStateMetricsVersion, values, insecure)
}

// extractImages parses a multi-document manifest string and returns the sorted,
// de-duplicated set of container images it references. See RenderChartImages for
// the forms it recognises.
func extractImages(manifests string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(img string) {
		img = strings.TrimSpace(img)
		// Drop blanks, un-rendered template fragments, and anything that isn't a
		// single token (a real image ref has no whitespace).
		if img == "" || strings.Contains(img, "{{") || strings.ContainsAny(img, " \t\n\"'") {
			return
		}
		if _, ok := seen[img]; ok {
			return
		}
		seen[img] = struct{}{}
		out = append(out, img)
	}

	for _, doc := range strings.Split(manifests, "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var node yamlv3.Node
		if err := yamlv3.Unmarshal([]byte(doc), &node); err != nil {
			continue
		}
		walkImages(&node, add)
	}
	sort.Strings(out)
	return out
}

func walkImages(n *yamlv3.Node, add func(string)) {
	switch n.Kind {
	case yamlv3.DocumentNode, yamlv3.SequenceNode:
		for _, c := range n.Content {
			walkImages(c, add)
		}
	case yamlv3.MappingNode:
		// An image-ref map (e.g. a CR's image: {repository, tag}).
		if repo := scalarChild(n, "repository"); repo != "" {
			add(joinImageRef(scalarChild(n, "registry"), repo, scalarChild(n, "tag"), scalarChild(n, "digest")))
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			val := n.Content[i+1]
			switch {
			case key == "image" && val.Kind == yamlv3.ScalarNode:
				add(val.Value)
			case (key == "args" || key == "command") && val.Kind == yamlv3.SequenceNode:
				addArgImages(val, add)
			}
			walkImages(val, add)
		}
	}
}

// addArgImages scans a container args/command list for images passed via a
// --…-image flag, in either "--foo-image=ref" or "--foo-image", "ref" form.
func addArgImages(seq *yamlv3.Node, add func(string)) {
	items := seq.Content
	for i := 0; i < len(items); i++ {
		if items[i].Kind != yamlv3.ScalarNode {
			continue
		}
		s := items[i].Value
		if !strings.HasPrefix(s, "--") {
			continue
		}
		flag, val, hasEq := strings.Cut(strings.TrimPrefix(s, "--"), "=")
		if !strings.HasSuffix(flag, "image") {
			continue
		}
		if hasEq {
			add(val)
		} else if i+1 < len(items) && items[i+1].Kind == yamlv3.ScalarNode {
			add(items[i+1].Value)
			i++
		}
	}
}

func scalarChild(n *yamlv3.Node, key string) string {
	if n == nil || n.Kind != yamlv3.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key && n.Content[i+1].Kind == yamlv3.ScalarNode {
			return n.Content[i+1].Value
		}
	}
	return ""
}

func joinImageRef(registry, repository, tag, digest string) string {
	img := repository
	if registry != "" {
		img = registry + "/" + repository
	}
	switch {
	case digest != "":
		return img + "@" + digest
	case tag != "":
		return img + ":" + tag
	default:
		return img
	}
}
