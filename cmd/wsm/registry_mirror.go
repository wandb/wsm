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
)

// registryMirrorCmd pulls every artifact wsm needs for a v2 install from its
// upstream registry and pushes a copy to the customer's mirror. Pair this with
// `wsm deploy-v2 operator --mirror-registry <same-host>` to install entirely
// from the mirror.
//
// Iteration 1 scope: just the infrastructure charts and their images — operator
// chart + binary, cert-manager chart + 5 images, nginx-gateway chart + 2 images.
// W&B server manifest, app images, and subchart images are follow-ups.
func registryMirrorCmd() *cobra.Command {
	var (
		targetRegistry       string
		insecure             bool
		dryRun               bool
		operatorChartVersion string
	)

	cmd := &cobra.Command{
		Use:   "mirror",
		Short: "Mirror v2 install artifacts to a private registry",
		Long: `Pull every chart and image wsm needs for ` + "`wsm deploy-v2 operator`" + ` from
its upstream source and push a copy to your mirror. After this runs, the install
side can fetch everything from --mirror-registry instead of the public sources.

Auth is read from your Docker config (~/.docker/config.json) by default. Use
--insecure for a plain-HTTP / self-signed mirror (e.g. a local registry:2).

Currently mirrors: the operator OCI chart and binary image, cert-manager and
its 5 component images, and nginx-gateway-fabric with its 2 images. Server
manifest and subchart images are upcoming.`,
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
				if err := mirrorOne(ctx, item.src, item.dst, srcCtx, dstCtx, policyCtx); err != nil {
					fmt.Printf("✗ %v\n", err)
					failed++
					continue
				}
				fmt.Println("✓")
				pushed++
			}

			if dryRun {
				return nil
			}

			fmt.Printf("\n%d total — %d pushed, %d failed\n", len(items), pushed, failed)
			if failed > 0 {
				return fmt.Errorf("%d artifact(s) failed to mirror", failed)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&targetRegistry, "to", "", "Hostname of your mirror, e.g. harbor.example.com (required)")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "Skip TLS verification when pushing to the mirror (use for plain-HTTP registries like local registry:2)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the source → target mirroring plan without pushing")
	cmd.Flags().StringVar(&operatorChartVersion, "operator-chart-version", "2.0.0-alpha.2", "Operator chart version; also used as the tag for the operator binary image")
	return cmd
}

type mirrorItem struct {
	src string // full upstream OCI reference, e.g. quay.io/jetstack/cert-manager-controller:v1.20.2
	dst string // full target reference,  e.g. localhost:5000/jetstack/cert-manager-controller:v1.20.2
}

// buildMirrorPlan returns the static set of artifacts Iteration 1 mirrors.
// cert-manager and nginx-gateway versions are duplicated from pkg/operator
// (those constants are private to that package); keeping them in sync is a
// one-line edit when pkg/operator bumps them.
func buildMirrorPlan(target, operatorChartVersion string) []mirrorItem {
	const (
		certManagerVersion  = "v1.20.2"
		nginxGatewayVersion = "2.5.1"
	)

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

// mirrorOne copies a single artifact from upstream to mirror using
// containers/image. Works for both container images and OCI helm charts —
// they're indistinguishable at the registry transport layer.
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
		SourceCtx:      srcCtx,
		DestinationCtx: dstCtx,
		// Copy every architecture in the manifest list, not just the one
		// matching the host OS. A customer's k8s nodes are linux/amd64 (or
		// linux/arm64) and must pull from the mirror; the default
		// CopySystemImage on a Mac would land only darwin/arm64 (which
		// often doesn't even exist) and break the install.
		ImageListSelection: copy.CopyAllImages,
	}); err != nil {
		return err
	}
	return nil
}
