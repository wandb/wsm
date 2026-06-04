package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/docker"
	dockerarchive "github.com/containers/image/v5/docker/archive"
	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/types"
	"github.com/spf13/cobra"
)

// registryPushCmd walks a bundle produced by `wsm download` and pushes every
// saved image tarball into the user's mirror registry.
// for the airgapped workflow: customers no longer have to hand-roll a
// docker load / docker tag / docker push loop.
func registryPushCmd() *cobra.Command {
	var (
		registry  string
		bundleDir string
		insecure  bool
		dryRun    bool
	)

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push images from a bundle directory into your mirrored registry",
		Long: `Walk a bundle produced by 'wsm download' and push every saved image into your
mirrored registry. Each source image is re-tagged using the same translation
rule as 'wsm registry check' / 'wsm registry values'.

The --registry flag is YOUR private container registry — the destination
you're mirroring W&B's images into. It's the same hostname you'd pass to
'wsm registry check' to verify the mirror afterwards. wsm does not discover
or provision it; you supply it.

Auth is read from your Docker config (~/.docker/config.json) by default.
Use --insecure for plain-HTTP or self-signed registries.`,
		Example: `  # Push everything from ./bundle to a private Harbor.
  wsm registry push --registry harbor.mycorp.internal

  # Custom bundle path (e.g. one transferred via USB into /mnt/airgap-bundle).
  wsm registry push --bundle /mnt/airgap-bundle --registry harbor.mycorp.internal

  # Dry-run: print the source → target translation without pushing.
  wsm registry push --registry harbor.mycorp.internal --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if registry == "" {
				return fmt.Errorf("--registry is required (the hostname of your mirror, e.g. harbor.example.com)")
			}

			imagesDir := filepath.Join(bundleDir, "images")
			info, err := os.Stat(imagesDir)
			if err != nil {
				return fmt.Errorf("bundle not found at %q — run 'wsm download' first, or pass --bundle <path>", imagesDir)
			}
			if !info.IsDir() {
				return fmt.Errorf("%q is not a directory", imagesDir)
			}

			tarballs, err := findImageTarballs(imagesDir)
			if err != nil {
				return err
			}
			if len(tarballs) == 0 {
				return fmt.Errorf("no image.tgz files found under %q", imagesDir)
			}

			fmt.Printf("Found %d images in %s\n", len(tarballs), imagesDir)
			if dryRun {
				fmt.Println("(dry-run) source → target")
			} else {
				fmt.Printf("Pushing to %s\n\n", registry)
			}

			policyCtx, err := newAcceptAllPolicy()
			if err != nil {
				return fmt.Errorf("failed to init signature policy: %w", err)
			}
			defer func() { _ = policyCtx.Destroy() }()

			sysCtx := &types.SystemContext{}
			if insecure {
				sysCtx.DockerInsecureSkipTLSVerify = types.OptionalBoolTrue
			}

			ctx := context.Background()
			var pushed, failed int
			var tooLarge []tarball // images that need a manual docker push
			for _, t := range tarballs {
				target := translate(t.source, registry)
				if dryRun {
					fmt.Printf("  %s → %s\n", t.source, target)
					continue
				}

				fmt.Printf("→ %s\n  → %s ... ", t.source, target)
				err := pushTarball(ctx, t.path, target, sysCtx, policyCtx)
				if err == nil {
					fmt.Println("✓")
					pushed++
					continue
				}

				failed++
				if isManifestTooLargeErr(err) {
					fmt.Println("✗ image manifest exceeds containers/image 4 MiB cap (deferred to manual push)")
					tooLarge = append(tooLarge, t)
					continue
				}
				fmt.Printf("✗ %v\n", err)
			}

			if dryRun {
				return nil
			}

			fmt.Printf("\n%d total — %d pushed, %d failed\n", len(tarballs), pushed, failed)

			if len(tooLarge) > 0 {
				printManualPushInstructions(tooLarge, registry)
			}

			if failed > 0 {
				return fmt.Errorf("%d image(s) failed to push", failed)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&registry, "registry", "", "Target registry to push into, e.g. harbor.mycorp.internal (required)")
	cmd.Flags().StringVar(&bundleDir, "bundle", "./bundle", "Path to the bundle directory produced by 'wsm download'")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "Skip TLS verification when contacting the registry")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print source → target translations without pushing")
	return cmd
}

type tarball struct {
	path   string // absolute path to image.tgz
	source string // original image reference, e.g. quay.io/prometheus/prometheus:v2.47.0
}

// findImageTarballs walks <bundle>/images/ and reconstructs the original image
// reference from the directory layout. 'wsm download' writes each image to
// bundle/images/<full-image-ref>/image.tgz, so we strip the prefix and suffix
// to recover the ref.
func findImageTarballs(imagesDir string) ([]tarball, error) {
	var out []tarball
	err := filepath.WalkDir(imagesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "image.tgz" {
			return nil
		}
		rel, err := filepath.Rel(imagesDir, filepath.Dir(path))
		if err != nil {
			return err
		}
		// On Windows the separator is '\'; image refs always use '/'.
		ref := strings.ReplaceAll(rel, string(os.PathSeparator), "/")
		out = append(out, tarball{path: path, source: ref})
		return nil
	})
	return out, err
}

// isManifestTooLargeErr matches the hardcoded 4 MiB cap that containers/image
// applies when sniffing manifest MIME type. The cap isn't configurable via
// SystemContext; we have to fall back to a manual docker push for these images.
func isManifestTooLargeErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "exceeded maximum allowed size") ||
		strings.Contains(msg, "MaxManifestBodySize")
}

// printManualPushInstructions emits ready-to-paste commands that let the user
// finish the push for each oversized image using the docker CLI. We can't do
// this automatically without taking a hard dependency on docker (or swapping
// the entire push transport for go-containerregistry), so we make the manual
// step as cheap as possible.
func printManualPushInstructions(images []tarball, registry string) {
	plural := "image"
	if len(images) > 1 {
		plural = "images"
	}

	fmt.Println()
	fmt.Printf("⚠  %d %s could not be pushed via wsm because their manifest exceeds\n", len(images), plural)
	fmt.Println("   the 4 MiB sniff-cap baked into the containers/image library.")
	fmt.Println("   Push them by hand using docker — same registry, same tag:")
	fmt.Println()
	for _, t := range images {
		target := translate(t.source, registry)
		fmt.Printf("     # %s\n", t.source)
		fmt.Printf("     docker load -i %s\n", t.path)
		fmt.Printf("     docker tag %s %s\n", t.source, target)
		fmt.Printf("     docker push %s\n", target)
		fmt.Println()
	}
	fmt.Println("   Then re-run 'wsm registry check' to confirm everything landed.")
	fmt.Println()
}

// colonFreeAlias creates a symlink to tarballPath in a fresh temp directory
// whose path contains no ':'. Returns the symlink path and a cleanup func that
// removes the temp dir. The library's path validator (newReference in
// containers/image/v5/docker/archive) rejects colons unconditionally.
func colonFreeAlias(tarballPath string) (string, func(), error) {
	absSrc, err := filepath.Abs(tarballPath)
	if err != nil {
		return "", func() {}, err
	}
	tmpDir, err := os.MkdirTemp("", "wsm-push-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	if strings.Contains(tmpDir, ":") {
		cleanup()
		return "", func() {}, fmt.Errorf("temp dir %q contains ':' which the docker-archive transport rejects", tmpDir)
	}

	linkPath := filepath.Join(tmpDir, "image.tgz")
	if err := os.Symlink(absSrc, linkPath); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("create symlink: %w", err)
	}
	return linkPath, cleanup, nil
}

func newAcceptAllPolicy() (*signature.PolicyContext, error) {
	policy := &signature.Policy{
		Default: []signature.PolicyRequirement{signature.NewPRInsecureAcceptAnything()},
	}
	return signature.NewPolicyContext(policy)
}

// pushTarball copies a docker-archive tarball into the destination registry
// reference using containers/image — no shell-out to docker required.
func pushTarball(
	ctx context.Context,
	tarballPath string,
	targetImage string,
	sysCtx *types.SystemContext,
	policyCtx *signature.PolicyContext,
) error {
	// containers/image rejects any docker-archive path containing ':', even
	// via NewReader → List() (the internal newReference validates the path
	// regardless of caller). Our bundle layout embeds image tags in the path
	// (wandb/controller:1.22.0/image.tgz), so we symlink to a colon-free
	// temp path and hand the library that instead.
	linkPath, cleanup, err := colonFreeAlias(tarballPath)
	if err != nil {
		return fmt.Errorf("alias tarball: %w", err)
	}
	defer cleanup()

	srcRef, err := dockerarchive.NewReference(linkPath, nil)
	if err != nil {
		return fmt.Errorf("build archive reference: %w", err)
	}

	dstRef, err := docker.ParseReference("//" + targetImage)
	if err != nil {
		return fmt.Errorf("parse target reference %q: %w", targetImage, err)
	}

	if _, err := copy.Image(ctx, policyCtx, dstRef, srcRef, &copy.Options{
		SourceCtx:      sysCtx,
		DestinationCtx: sysCtx,
	}); err != nil {
		return err
	}
	return nil
}
