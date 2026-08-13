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

// renderKubeVersion is the Kubernetes version the client-side render advertises.
// It must satisfy the kubeVersion floors the charts declare (cert-manager needs
// >=1.22, nginx-gateway-fabric >=1.31); the default client capabilities report an
// old version and would fail the chart's kubeVersion check.
const renderKubeVersion = "v1.33.0"

// OperatorChartRepo is the public OCI repository base the operator chart is
// published to. It matches DeployOperator's repositoryURL so the mirror and
// install sides pull the same chart. The mirror command renders from here; the
// check command renders the copy under a mirror's "oci://<host>/wandb/charts".
const OperatorChartRepo = "oci://us-docker.pkg.dev/wandb-production/public/wandb/charts"

// RenderChartImages pulls chartRef@version over verified TLS, renders it entirely
// client-side (no cluster, no release), and returns every distinct container image
// it would create, sorted. It looks beyond plain container images so it also
// captures images the chart injects indirectly:
//
//   - workload container and initContainer images (image: <ref> scalars),
//   - images passed to a container as a --…-image argument (moco's
//     --agent-image/--fluent-bit-image/--mysqld-exporter-image, cert-manager's
//     --acme-http01-solver-image),
//   - image refs expressed as {registry?, repository, tag?/digest?} maps on custom
//     resources (e.g. nginx-gateway-fabric's NginxProxy),
//   - images in the chart's hook manifests.
//
// values are the Helm values to render with; pass the same ones the install side
// uses (minus any mirror retargeting) so the derived set matches what a real
// install pulls. insecure skips TLS verification (and allows plain HTTP) when
// pulling the chart — used when rendering a chart copy from a self-signed mirror.
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
	// DryRunClient renders entirely client-side: no cluster contact, default
	// capabilities.
	inst.DryRunStrategy = action.DryRunClient
	inst.KubeVersion = kubeVersion
	inst.Replace = true
	inst.ReleaseName = "wsm-render"
	inst.Namespace = "wsm-render"
	inst.Version = version
	// CRDs carry image strings as openAPIV3Schema defaults that are not
	// necessarily pulled; rendering templates only keeps the set to images the
	// chart actually deploys.
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

// OperatorChartImages renders the wandb operator chart from chartRepo (an OCI repo
// base — OperatorChartRepo for upstream, or a mirror's "oci://<host>/wandb/charts")
// at chartVersion and returns the managed-service images it deploys: the operator
// binary, the bundled managed-service operator images (moco/redis/seaweedfs/
// altinity), and the moco sidecars injected via controller args. Rendered with the
// same non-mirror values DeployOperator installs with, so the set matches a real
// install. insecure is passed through for a self-signed mirror source.
func OperatorChartImages(ctx context.Context, chartRepo, chartVersion string, insecure bool) ([]string, error) {
	values := map[string]any{
		"wandb":          map[string]any{"install": false},
		"wandb-operator": map[string]any{"image": map[string]any{"pullPolicy": "Always"}},
	}
	return RenderChartImages(ctx, chartRepo+"/operator", chartVersion, values, insecure)
}

// CertManagerImages renders the cert-manager chart (at CertManagerVersion) from
// chartRef and returns the component images it deploys. Rendered with the same
// values InstallCertManager uses (startupapicheck disabled), so the derived set —
// controller/webhook/cainjector plus the acmesolver passed via a controller arg —
// matches what a real install pulls, and the unused startupapicheck image is not
// mirrored.
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

// KubeStateMetricsImages renders the kube-state-metrics chart (at
// KubeStateMetricsVersion) from chartRef and returns its image. The image tag is
// pinned via the same value InstallKubeStateMetrics sets, so the derived image
// matches the install even if the chart's default tag differs.
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
			continue // skip anything that isn't valid YAML
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
		// {registry?, repository, tag?/digest?} image-ref map (e.g. a CR's image).
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

// scalarChild returns the scalar value of key in a mapping node, or "".
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
