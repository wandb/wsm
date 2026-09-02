package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/types"
	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/operator"
)

// registryMirrorCmd pulls every artifact wsm needs for a v2 install from its
// upstream registry and pushes a copy to the customer's mirror. Pair this with
// `wsm deploy-v2 operator --mirror-registry <same-host>` to install entirely
// from the mirror.
func registryMirrorCmd() *cobra.Command {
	var (
		targetRegistry       string
		insecure             bool
		dryRun               bool
		operatorChartVersion string
		wandbVersion         string
		skipManaged          bool
		excludeOperators     []string
		excludeManaged       []string
		manifestSource       string
	)

	cmd := &cobra.Command{
		Use:   "mirror",
		Short: "Mirror v2 install artifacts to a private registry",
		Long: `Pull every chart and image wsm needs for ` + "`wsm deploy-v2 operator`" + ` from
its upstream source and push a copy to your mirror. After this runs, the install
side can fetch everything from --mirror-registry instead of the public sources.

Auth is read from your Docker config (~/.docker/config.json) by default. Use
--insecure for a plain-HTTP / self-signed mirror (e.g. a local registry:2).

Mirrors, across three tiers: (1) the operator OCI chart + binary image,
cert-manager and its 5 component images, and nginx-gateway-fabric with its 2
images; (2) the managed-service operator images (moco/altinity/opstree/
seaweedfs); (3) the managed data-plane images (ClickHouse/Kafka/MySQL/Redis/
SeaweedFS servers). With --wandb-version it also mirrors the server manifest and
every W&B application image it references (weave, megabinary, frontend, …),
rewriting the manifest's image refs to point at the mirror. Pass
--skip-managed-images to omit tiers 2 and 3 (e.g. when running W&B against
external databases).`,
		Example: `  # Mirror everything to a local registry:2 on localhost:5000.
  wsm registry mirror --to localhost:5000 --insecure

  # Mirror to a private Harbor.
  wsm registry mirror --to harbor.mycorp.internal

  # Preview without pushing.
  wsm registry mirror --to harbor.mycorp.internal --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if targetRegistry == "" {
				return fmt.Errorf("--to is required (the hostname of your mirror, e.g. harbor.example.com)")
			}
			targetRegistry = strings.TrimRight(targetRegistry, "/")

			exclusions, err := parseManagedExclusions(excludeOperators, excludeManaged, skipManaged)
			if err != nil {
				return err
			}

			ctx := context.Background()

			// Charts render from upstream (verified TLS); false = not from the mirror.
			items, err := buildMirrorPlan(ctx, targetRegistry, operatorChartVersion, false, false)
			if err != nil {
				return err
			}
			// Managed-service operator + sidecar images (operator chart render) plus the
			// data-plane server images (manifest below). Excluded types are dropped.
			managed, err := buildManagedImagePlan(ctx, targetRegistry, operator.OperatorChartRepo, operatorChartVersion, exclusions, false)
			if err != nil {
				return err
			}
			items = append(items, managed...)

			fmt.Printf("Mirroring %d artifacts to %s\n\n", len(items), targetRegistry)

			policyCtx, err := newAcceptAllPolicy()
			if err != nil {
				return fmt.Errorf("failed to init signature policy: %w", err)
			}
			defer func() { _ = policyCtx.Destroy() }()

			srcCtx := &types.SystemContext{}
			dstCtx := &types.SystemContext{}
			if insecure {
				dstCtx.DockerInsecureSkipTLSVerify = types.OptionalBoolTrue
				dstCtx.OCIInsecureSkipTLSVerify = true
			}

			var pushed, failed int
			for _, item := range items {
				if dryRun {
					fmt.Printf("  %s\n  → %s\n\n", item.src, item.dst)
					continue
				}
				fmt.Printf("→ %s\n  → %s ... ", item.src, item.dst)
				if err := copyImage(ctx, item.src, item.dst, insecure, srcCtx, dstCtx, policyCtx); err != nil {
					fmt.Printf("✗ %v\n", err)
					failed++
					continue
				}
				fmt.Println("✓")
				pushed++
			}

			if !dryRun {
				fmt.Printf("\n%d total — %d pushed, %d failed\n", len(items), pushed, failed)
				if failed > 0 {
					return fmt.Errorf("%d artifact(s) failed to mirror", failed)
				}
			}

			// The server manifest + every W&B application image it references
			// (weave-trace, weave-python, local, console, migrations, …) are only
			// mirrored when a version is given, since they're version-specific.
			if wandbVersion != "" {
				if err := mirrorServerManifest(ctx, targetRegistry, wandbVersion, manifestSource, exclusions, insecure, dryRun, srcCtx, dstCtx, policyCtx); err != nil {
					return err
				}
			} else {
				fmt.Println("\nNote: pass --wandb-version to also mirror the server manifest and W&B app images (weave, etc.).")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&targetRegistry, "to", "", "Hostname of your mirror, e.g. harbor.example.com (required)")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "Skip TLS verification when pushing to the mirror (use for plain-HTTP registries like local registry:2)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the source → target mirroring plan without pushing")
	cmd.Flags().StringVar(&operatorChartVersion, "operator-chart-version", defaultOperatorChartVersion, "Operator chart version; also used as the tag for the operator binary image")
	cmd.Flags().StringVar(&wandbVersion, "wandb-version", "", "W&B server version (e.g. 0.81.0); when set, also mirror the server manifest and every application image it references, rewriting them to point at the mirror")
	cmd.Flags().BoolVar(&skipManaged, "skip-managed-images", false, "Alias for --exclude-managed clickhouse,mysql,redis,object-store. Kafka is always mirrored.")
	cmd.Flags().StringSliceVar(&excludeOperators, "exclude-operators", nil, "Managed types whose OPERATOR images to skip (you run your own cluster-wide operator), comma-separated: clickhouse,mysql,redis,object-store. The managed data-plane service is still mirrored.")
	cmd.Flags().StringSliceVar(&excludeManaged, "exclude-managed", nil, "Managed types to skip ENTIRELY — operator AND data-plane images (you use an external service), comma-separated: clickhouse,mysql,redis,object-store. Kafka cannot be excluded.")
	// TESTING ONLY, hidden from --help: pull the server manifest from a non-upstream
	// OCI repo (e.g. a local Tilt registry serving unreleased wandb/core manifest
	// changes) instead of us-docker.pkg.dev. Not a supported customer workflow.
	cmd.Flags().StringVar(&manifestSource, "manifest-source", "", "TESTING ONLY: pull the server manifest from this OCI repo (host/path, no tag) instead of the public upstream; --wandb-version supplies the tag. Reuses --insecure for TLS skip.")
	_ = cmd.Flags().MarkHidden("manifest-source")
	return cmd
}

type mirrorItem struct {
	src string // full upstream OCI reference, e.g. quay.io/jetstack/cert-manager-controller:v1.20.2
	dst string // full target reference, e.g. localhost:5000/jetstack/cert-manager-controller:v1.20.2
}

// buildMirrorPlan returns the OCI chart artifacts wsm mirrors plus the component
// images each chart (operator/cert-manager/nginx-gateway/kube-state-metrics) deploys.
// Chart artifacts and the operator binary are fixed version-pinned refs; component
// images are derived by rendering each chart, so the set always matches a real install
// with no hand-maintained list.
//
// fromMirror renders the tool charts from their pushed copies under target (for check,
// which needs registry access only) instead of upstream (for mirror). insecure skips
// TLS verification for a self-signed mirror source.
func buildMirrorPlan(ctx context.Context, target, operatorChartVersion string, fromMirror, insecure bool) ([]mirrorItem, error) {
	certManagerVersion := operator.CertManagerVersion
	nginxGatewayVersion := operator.NginxGatewayVersion
	ksmChartVersion := operator.KubeStateMetricsVersion

	// OCI chart artifacts + the operator binary image (fixed refs, not rendered).
	plan := []mirrorItem{
		{
			src: "us-docker.pkg.dev/wandb-production/public/wandb/charts/operator:" + operatorChartVersion,
			dst: target + "/wandb/charts/operator:" + operatorChartVersion,
		},
		{
			src: "us-docker.pkg.dev/wandb-production/public/wandb/operator:" + operatorChartVersion,
			dst: target + "/wandb/operator:" + operatorChartVersion,
		},
		{
			src: "quay.io/jetstack/charts/cert-manager:" + certManagerVersion,
			dst: target + "/jetstack/charts/cert-manager:" + certManagerVersion,
		},
		{
			src: "ghcr.io/nginx/charts/nginx-gateway-fabric:" + nginxGatewayVersion,
			dst: target + "/nginx/charts/nginx-gateway-fabric:" + nginxGatewayVersion,
		},
		{
			src: "ghcr.io/prometheus-community/charts/kube-state-metrics:" + ksmChartVersion,
			dst: target + "/prometheus-community/charts/kube-state-metrics:" + ksmChartVersion,
		},
	}

	// pick selects the upstream chart ref (mirror) or its pushed copy under target
	// (check); rendered image refs are upstream either way, so they translate() to
	// the same mirror destinations.
	pick := func(upstream, mirrored string) string {
		if fromMirror {
			return mirrored
		}
		return upstream
	}
	toolCharts := []struct {
		images   func(context.Context, string, bool) ([]string, error)
		upstream string
		mirrored string
	}{
		{operator.CertManagerImages, "oci://quay.io/jetstack/charts/cert-manager", "oci://" + target + "/jetstack/charts/cert-manager"},
		{operator.NginxGatewayImages, "oci://ghcr.io/nginx/charts/nginx-gateway-fabric", "oci://" + target + "/nginx/charts/nginx-gateway-fabric"},
		{operator.KubeStateMetricsImages, "oci://ghcr.io/prometheus-community/charts/kube-state-metrics", "oci://" + target + "/prometheus-community/charts/kube-state-metrics"},
	}
	for _, tc := range toolCharts {
		imgs, err := tc.images(ctx, pick(tc.upstream, tc.mirrored), insecure)
		if err != nil {
			return nil, err
		}
		for _, img := range imgs {
			plan = append(plan, mirrorItem{src: img, dst: translate(img, target)})
		}
	}

	return plan, nil
}

// buildManagedImagePlan returns the managed-service operator + moco-sidecar images,
// derived by rendering the operator chart so the set matches a real install with no
// hand-maintained list. (The data-plane server images come from the manifest.)
// chartRepo is the render source — upstream when mirroring, the mirror's own copy
// when checking an air-gapped registry. exclusions drops excluded types' operator
// images (their subcharts are disabled in the render). The W&B operator-binary image
// is skipped: buildMirrorPlan mirrors it under W&B's path convention, which
// translate() wouldn't reproduce. insecure skips TLS for a self-signed source.
func buildManagedImagePlan(ctx context.Context, target, chartRepo, operatorChartVersion string, exclusions managedExclusions, insecure bool) ([]mirrorItem, error) {
	if exclusions.allOperatorsExcluded() {
		return nil, nil
	}
	imgs, err := operator.OperatorChartImages(ctx, chartRepo, operatorChartVersion, exclusions.disabledSubcharts(), insecure)
	if err != nil {
		return nil, fmt.Errorf("derive operator chart images: %w", err)
	}

	plan := make([]mirrorItem, 0, len(imgs))
	for _, img := range imgs {
		if strings.HasPrefix(img, wandbPublicPrefix) {
			continue
		}
		plan = append(plan, mirrorItem{src: img, dst: translate(img, target)})
	}
	return plan, nil
}

func mirrorOne(
	ctx context.Context,
	source, target string,
	srcCtx, dstCtx *types.SystemContext,
	policyCtx *signature.PolicyContext,
) error {
	srcRef, err := docker.ParseReference("//" + source)
	if err != nil {
		return fmt.Errorf("parse source %q: %w", source, err)
	}
	dstRef, err := docker.ParseReference("//" + target)
	if err != nil {
		return fmt.Errorf("parse target %q: %w", target, err)
	}
	if _, err := copy.Image(ctx, policyCtx, dstRef, srcRef, &copy.Options{
		SourceCtx:          srcCtx,
		DestinationCtx:     dstCtx,
		ImageListSelection: copy.CopyAllImages,
	}); err != nil {
		return err
	}
	return nil
}
