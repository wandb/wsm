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

			items := buildMirrorPlan(targetRegistry, operatorChartVersion)
			if !skipManaged {
				// Managed MySQL/Redis/Kafka/ClickHouse/object-store services. These
				// pull from docker.io/quay.io/ghcr.io and are pushed to provided registry mirror
				// At install they're retargeted to the mirror by:
				// Helm image values set from --mirror-registry using
				// spec.global.imageRegistry on the CR, which the operator host-replaces
				// (requires an operator version that declares the field). On a plain-HTTP local
				// install without that field, the node's containerd registry mirrors
				// (wsm cluster create --insecure-registry-host) redirect them instead.
				items = append(items, buildManagedImagePlan(targetRegistry)...)
			}

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

			ctx := context.Background()
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
				if err := mirrorServerManifest(ctx, targetRegistry, wandbVersion, manifestSource, insecure, dryRun, srcCtx, dstCtx, policyCtx); err != nil {
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
	cmd.Flags().StringVar(&operatorChartVersion, "operator-chart-version", "2.0.0-beta.1", "Operator chart version; also used as the tag for the operator binary image")
	cmd.Flags().StringVar(&wandbVersion, "wandb-version", "", "W&B server version (e.g. 0.81.0); when set, also mirror the server manifest and every application image it references, rewriting them to point at the mirror")
	cmd.Flags().BoolVar(&skipManaged, "skip-managed-images", false, "Don't mirror the managed-service operator + data-plane images (ClickHouse/Kafka/MySQL/Redis/object-store). Use when you run W&B against external databases.")
	// TESTING ONLY, hidden from --help: pull the server manifest from a non-upstream
	// OCI repo (e.g. a local Tilt registry serving unreleased wandb/core manifest
	// changes) instead of us-docker.pkg.dev. Not a supported customer workflow.
	cmd.Flags().StringVar(&manifestSource, "manifest-source", "", "TESTING ONLY: pull the server manifest from this OCI repo (host/path, no tag) instead of the public upstream; --wandb-version supplies the tag. Reuses --insecure for TLS skip.")
	_ = cmd.Flags().MarkHidden("manifest-source")
	return cmd
}

type mirrorItem struct {
	src string // full upstream OCI reference, e.g. quay.io/jetstack/cert-manager-controller:v1.20.2
	dst string // full target reference,  e.g. localhost:5000/jetstack/cert-manager-controller:v1.20.2
}

// buildMirrorPlan returns the static set of artifacts Iteration 1 mirrors.
// cert-manager and nginx-gateway versions come from pkg/operator so the
// install side and mirror side stay in lockstep automatically.
func buildMirrorPlan(target, operatorChartVersion string) []mirrorItem {
	certManagerVersion := operator.CertManagerVersion
	nginxGatewayVersion := operator.NginxGatewayVersion

	plan := []mirrorItem{
		// Operator OCI chart + binary image
		{
			src: "us-docker.pkg.dev/wandb-production/public/wandb/charts/operator:" + operatorChartVersion,
			dst: target + "/wandb/charts/operator:" + operatorChartVersion,
		},
		{
			src: "us-docker.pkg.dev/wandb-production/public/wandb/operator:" + operatorChartVersion,
			dst: target + "/wandb/operator:" + operatorChartVersion,
		},

		// cert-manager OCI chart
		{
			src: "quay.io/jetstack/charts/cert-manager:" + certManagerVersion,
			dst: target + "/jetstack/charts/cert-manager:" + certManagerVersion,
		},
	}

	for _, comp := range []string{"controller", "webhook", "cainjector", "acmesolver", "startupapicheck"} {
		plan = append(plan, mirrorItem{
			src: "quay.io/jetstack/cert-manager-" + comp + ":" + certManagerVersion,
			dst: target + "/jetstack/cert-manager-" + comp + ":" + certManagerVersion,
		})
	}

	// nginx-gateway-fabric chart + 2 images (control plane + data plane)
	plan = append(plan,
		mirrorItem{
			src: "ghcr.io/nginx/charts/nginx-gateway-fabric:" + nginxGatewayVersion,
			dst: target + "/nginx/charts/nginx-gateway-fabric:" + nginxGatewayVersion,
		},
		mirrorItem{
			src: "ghcr.io/nginx/nginx-gateway-fabric:" + nginxGatewayVersion,
			dst: target + "/nginx/nginx-gateway-fabric:" + nginxGatewayVersion,
		},
		mirrorItem{
			src: "ghcr.io/nginx/nginx-gateway-fabric/nginx:" + nginxGatewayVersion,
			dst: target + "/nginx/nginx-gateway-fabric/nginx:" + nginxGatewayVersion,
		},
	)

	return plan
}

func buildManagedImagePlan(target string) []mirrorItem {
	images := []string{
		// Tier 2 — subchart operator images (default-enabled subcharts).
		"alpine/k8s:1.35.4", // altinity-clickhouse-operator.crdHook
		"altinity/clickhouse-operator:0.26.3",
		"altinity/metrics-exporter:0.26.3",
		"chrislusf/seaweedfs-operator:1.0.21",
		"ghcr.io/cybozu-go/moco:0.36.0",
		"quay.io/opstree/redis-operator:v0.22.2",

		// Tier 3 — data-plane server images.
		"altinity/clickhouse-server:25.8.16.10002.altinitystable",
		"altinity/clickhouse-keeper:25.8.16.10002.altinitystable",
		// Kafka (Bufstream): broker + etcd + aws-cli bucket-ensure init image.
		"us-docker.pkg.dev/buf-images-1/buf/images/bufstream:0.4.15",
		"quay.io/coreos/etcd:v3.5.31",
		"amazon/aws-cli:2.35.10",
		// moco injects agent/fluent-bit/mysqld_exporter sidecars; all must be mirrored.
		"ghcr.io/cybozu-go/moco/mysql:8.4.8",
		"ghcr.io/cybozu-go/moco-agent:0.16.0",
		"ghcr.io/cybozu-go/moco/fluent-bit:5.0.2.1",
		"ghcr.io/cybozu-go/moco/mysqld_exporter:0.19.0.1",
		"quay.io/opstree/redis:v7.0.15",
		"quay.io/opstree/redis-sentinel:v7.0.12",
		"quay.io/opstree/redis-exporter:v1.44.0",
		"chrislusf/seaweedfs:4.35",
	}

	plan := make([]mirrorItem, 0, len(images))
	for _, img := range images {
		plan = append(plan, mirrorItem{src: img, dst: translate(img, target)})
	}
	return plan
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
