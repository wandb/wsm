package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/types"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1remote "github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	wmanifest "github.com/wandb/operator/pkg/wandb/manifest"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"
	"sigs.k8s.io/yaml"
)

// serverManifestUpstream is the public OCI repository the operator pulls the
// server manifest from by default. wsm mirrors it (with rewritten image refs)
// to <mirror>/wandb/server-manifest so an air-gapped install can pull it and
// every W&B application image (weave-trace, weave-python, local, …) from the
// mirror.
const serverManifestUpstream = "us-docker.pkg.dev/wandb-production/public/wandb/server-manifest"

// wandbPublicPrefix is stripped from W&B image references so they land under
// <mirror>/wandb/* — matching how `--mirror-registry` retargets the operator
// image and how buildMirrorPlan maps the operator chart.
const wandbPublicPrefix = "us-docker.pkg.dev/wandb-production/public/"

// mirrorServerManifest pulls the server-manifest OCI artifact for version,
// mirrors every application/migration image it references, rewrites the image
// references inside the manifest to point at target, and pushes the rewritten
// manifest to <target>/wandb/server-manifest:<version>. After this runs,
// `wsm deploy-v2 operator --mirror-registry <target> --wandb-version <version>`
// brings the whole app up with no public-registry access.
func mirrorServerManifest(
	ctx context.Context,
	target, version, manifestSource string,
	insecure, dryRun bool,
	srcCtx, dstCtx *types.SystemContext,
	policyCtx *signature.PolicyContext,
) error {
	// manifestSource is a hidden dev/testing override (--manifest-source): pull
	// the manifest from a non-upstream OCI repo (e.g. a local Tilt registry
	// serving unreleased wandb/core changes) instead of serverManifestUpstream.
	// Not a supported customer workflow.
	source := serverManifestUpstream
	if manifestSource != "" {
		source = manifestSource
	}
	fmt.Printf("\nServer manifest %s:%s\n", source, version)

	var files map[string][]byte
	var err error
	if manifestSource != "" {
		files, err = pullManifestYAMLFrom(ctx, manifestSource, version, insecure)
	} else {
		files, err = pullManifestYAML(ctx, version)
	}
	if err != nil {
		return fmt.Errorf("pull server manifest: %w", err)
	}

	refs, err := collectManifestImages(files)
	if err != nil {
		return fmt.Errorf("enumerate manifest images: %w", err)
	}
	if len(refs) == 0 {
		return fmt.Errorf("server manifest %s:%s referenced no images", source, version)
	}

	// Map each unique source repository to its mirror location once; the same
	// mapping drives both image copying and the in-manifest rewrite.
	repoRewrite := map[string]string{}
	for _, ref := range refs {
		repoRewrite[ref.Repository] = rewriteRepoForMirror(target, ref.Repository)
	}

	fmt.Printf("  %d application image(s) referenced:\n", len(refs))
	for _, ref := range refs {
		src := ref.GetImage("")
		dst := mirrorImageRef(target, ref)
		fmt.Printf("    %s\n      → %s\n", src, dst)
	}
	manifestDst := target + "/wandb/server-manifest:" + version
	fmt.Printf("  manifest → %s (image refs rewritten)\n", manifestDst)

	if dryRun {
		return nil
	}

	// Copy the application images. A failure here must NOT prevent pushing the
	// rewritten manifest — the two are independent, and leaving the manifest in
	// place lets the user retry just the failed images. Collect failures and
	// report them after the manifest is pushed.
	var failedImages []string
	for _, ref := range refs {
		src := ref.GetImage("")
		dst := mirrorImageRef(target, ref)
		fmt.Printf("→ %s\n  → %s ... ", src, dst)
		if err := copyImage(ctx, src, dst, insecure, srcCtx, dstCtx, policyCtx); err != nil {
			fmt.Printf("✗ %v\n", err)
			failedImages = append(failedImages, src)
			continue
		}
		fmt.Println("✓")
	}

	// Rewrite the manifest YAML files and re-push as a fresh OCI artifact.
	rewritten := map[string][]byte{}
	for name, data := range files {
		out := data
		for oldRepo, newRepo := range repoRewrite {
			out = replaceRepo(out, oldRepo, newRepo)
		}
		rewritten[name] = out
	}

	fmt.Printf("→ pushing rewritten manifest to %s ... ", manifestDst)
	if err := pushManifestArtifact(ctx, target, version, rewritten, insecure); err != nil {
		fmt.Println("✗")
		return fmt.Errorf("push rewritten manifest: %w", err)
	}
	fmt.Println("✓")

	if len(failedImages) > 0 {
		return fmt.Errorf("manifest pushed, but %d application image(s) failed to mirror: %s",
			len(failedImages), strings.Join(failedImages, ", "))
	}
	return nil
}

// copyImage mirrors one image, trying containers/image first and falling back to
// go-containerregistry. containers/image rejects config blobs larger than 4 MiB
// (W&B's megabinary exceeds this); go-containerregistry has no such limit.
func copyImage(
	ctx context.Context,
	src, dst string,
	dstInsecure bool,
	srcCtx, dstCtx *types.SystemContext,
	policyCtx *signature.PolicyContext,
) error {
	err := mirrorOne(ctx, src, dst, srcCtx, dstCtx, policyCtx)
	if err == nil {
		return nil
	}
	if cerr := craneCopyImage(ctx, src, dst, dstInsecure); cerr != nil {
		return fmt.Errorf("%w (crane fallback also failed: %v)", err, cerr)
	}
	return nil
}

// craneCopyImage copies an image (or multi-arch index) from src to dst using
// go-containerregistry. The source is always pulled over verified TLS; the
// destination honours dstInsecure, which (matching the containers/image path)
// means "don't be strict about the mirror's TLS": it tolerates a plain-HTTP
// registry AND an HTTPS registry with a self-signed / untrusted cert.
func craneCopyImage(ctx context.Context, src, dst string, dstInsecure bool) error {
	srcRef, err := name.ParseReference(src)
	if err != nil {
		return fmt.Errorf("parse source %q: %w", src, err)
	}
	var dstOpts []name.Option
	if dstInsecure {
		// Allows a plain-HTTP registry; HTTPS-with-untrusted-cert is covered by
		// the skip-verify transport on pushOpts below.
		dstOpts = append(dstOpts, name.Insecure)
	}
	dstRef, err := name.ParseReference(dst, dstOpts...)
	if err != nil {
		return fmt.Errorf("parse target %q: %w", dst, err)
	}

	pullOpts := []v1remote.Option{
		v1remote.WithAuthFromKeychain(authn.DefaultKeychain),
		v1remote.WithContext(ctx),
	}
	pushOpts := []v1remote.Option{
		v1remote.WithAuthFromKeychain(authn.DefaultKeychain),
		v1remote.WithContext(ctx),
	}
	if dstInsecure {
		pushOpts = append(pushOpts, v1remote.WithTransport(insecureHTTPTransport()))
	}

	desc, err := v1remote.Get(srcRef, pullOpts...)
	if err != nil {
		return fmt.Errorf("get source: %w", err)
	}
	if desc.MediaType.IsIndex() {
		idx, err := desc.ImageIndex()
		if err != nil {
			return fmt.Errorf("read index: %w", err)
		}
		return v1remote.WriteIndex(dstRef, idx, pushOpts...)
	}
	img, err := desc.Image()
	if err != nil {
		return fmt.Errorf("read image: %w", err)
	}
	return v1remote.Write(dstRef, img, pushOpts...)
}

// insecureHTTPTransport clones the default transport and disables TLS
// verification — for pushing to a mirror with a self-signed / untrusted cert
// under --insecure.
func insecureHTTPTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // user explicitly opted into --insecure
	return t
}

// pullManifestYAML pulls the server-manifest artifact for version into an
// in-memory store and returns the raw YAML files it carries, keyed by their
// path inside the artifact's layers. It mirrors the operator's own extraction
// logic (pkg/wandb/manifest) so the bytes we rewrite are exactly what the
// operator would have read.
func pullManifestYAML(ctx context.Context, version string) (map[string][]byte, error) {
	// Upstream is real TLS — always verify.
	return pullManifestYAMLFrom(ctx, serverManifestUpstream, version, false)
}

// pullManifestYAMLFrom is pullManifestYAML against an arbitrary server-manifest
// repository (e.g. a customer's mirror at <mirror>/wandb/server-manifest), so
// `wsm registry check` can read the rewritten manifest back out of the mirror
// and enumerate the application images it expects to be present. insecure skips
// TLS verification for a self-signed mirror; like the push side it does not fall
// back to plain HTTP, since the operator could never consume a plain-HTTP
// manifest anyway.
func pullManifestYAMLFrom(ctx context.Context, repoRef, version string, insecure bool) (map[string][]byte, error) {
	src, err := remote.NewRepository(repoRef)
	if err != nil {
		return nil, fmt.Errorf("init source repo: %w", err)
	}
	src.Client = dockerAuthClient(insecure)

	store := memory.New()
	desc, err := oras.Copy(ctx, src, version, store, version, oras.DefaultCopyOptions)
	if err != nil {
		return nil, fmt.Errorf("copy %s:%s: %w", repoRef, version, err)
	}
	return extractManifestYAML(ctx, store, desc)
}

// extractManifestYAML walks an image index or image manifest and returns every
// .yaml file found in its (optionally gzipped) tar layers.
func extractManifestYAML(ctx context.Context, fetcher content.Fetcher, desc ocispec.Descriptor) (map[string][]byte, error) {
	switch desc.MediaType {
	case ocispec.MediaTypeImageIndex, "application/vnd.docker.distribution.manifest.list.v2+json":
		data, err := content.FetchAll(ctx, fetcher, desc)
		if err != nil {
			return nil, fmt.Errorf("fetch index: %w", err)
		}
		var index ocispec.Index
		if err := json.Unmarshal(data, &index); err != nil {
			return nil, fmt.Errorf("unmarshal index: %w", err)
		}
		if len(index.Manifests) == 0 {
			return nil, errors.New("index has no manifests")
		}
		return extractManifestYAML(ctx, fetcher, index.Manifests[0])

	case ocispec.MediaTypeImageManifest, "application/vnd.docker.distribution.manifest.v2+json":
		data, err := content.FetchAll(ctx, fetcher, desc)
		if err != nil {
			return nil, fmt.Errorf("fetch manifest: %w", err)
		}
		var m ocispec.Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("unmarshal manifest: %w", err)
		}

		files := map[string][]byte{}
		for _, layer := range m.Layers {
			rc, err := fetcher.Fetch(ctx, layer)
			if err != nil {
				return nil, fmt.Errorf("fetch layer %s: %w", layer.Digest, err)
			}
			var reader io.Reader = rc
			if layer.MediaType == ocispec.MediaTypeImageLayerGzip ||
				layer.MediaType == "application/vnd.docker.image.rootfs.diff.tar.gzip" {
				gzr, err := gzip.NewReader(rc)
				if err != nil {
					rc.Close()
					return nil, fmt.Errorf("gzip layer %s: %w", layer.Digest, err)
				}
				reader = gzr
			}
			tr := tar.NewReader(reader)
			for {
				header, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					rc.Close()
					return nil, fmt.Errorf("read layer %s: %w", layer.Digest, err)
				}
				if filepath.Ext(header.Name) != ".yaml" {
					continue
				}
				contents, err := io.ReadAll(tr)
				if err != nil {
					rc.Close()
					return nil, fmt.Errorf("read %s: %w", header.Name, err)
				}
				files[header.Name] = contents
			}
			rc.Close()
		}
		if len(files) == 0 {
			return nil, errors.New("no .yaml files found in manifest layers")
		}
		return files, nil

	default:
		return nil, fmt.Errorf("unsupported media type: %s", desc.MediaType)
	}
}

// collectManifestImages parses each manifest YAML file and returns the unique
// image references across applications (including init/sidecar containers) and
// migration jobs.
func collectManifestImages(files map[string][]byte) ([]wmanifest.ImageRef, error) {
	seen := map[string]struct{}{}
	var refs []wmanifest.ImageRef
	add := func(ref wmanifest.ImageRef) {
		if ref.Repository == "" {
			return
		}
		key := ref.GetImage("")
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		refs = append(refs, ref)
	}

	names := sortedKeys(files)
	for _, name := range names {
		var m wmanifest.Manifest
		if err := yaml.Unmarshal(files[name], &m); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		for _, app := range m.Applications {
			add(app.Image)
			for _, c := range app.InitContainers {
				add(c.Image)
			}
			for _, c := range app.Containers {
				add(c.Image)
			}
		}
		for _, mig := range m.Migrations {
			add(mig.Image)
		}
	}
	return refs, nil
}

// rewriteRepoForMirror maps an upstream image repository to its location in the
// mirror. W&B images drop the wandb-production/public project path so they land
// under <mirror>/wandb/*; everything else just loses its registry host. This
// matches the mapping buildMirrorPlan uses for the charts and operator image.
func rewriteRepoForMirror(target, repo string) string {
	target = strings.TrimRight(target, "/")
	if strings.HasPrefix(repo, wandbPublicPrefix) {
		return target + "/" + strings.TrimPrefix(repo, wandbPublicPrefix)
	}
	if i := strings.IndexByte(repo, '/'); i > 0 {
		host := repo[:i]
		if strings.ContainsAny(host, ".:") || host == "localhost" {
			return target + "/" + repo[i+1:]
		}
	}
	return target + "/" + repo
}

// mirrorImageRef returns the full mirror reference (repo + tag/digest) for an
// upstream image reference.
func mirrorImageRef(target string, ref wmanifest.ImageRef) string {
	mirrored := wmanifest.ImageRef{
		Repository: rewriteRepoForMirror(target, ref.Repository),
		Tag:        ref.Tag,
		Digest:     ref.Digest,
	}
	return mirrored.GetImage("")
}

// replaceRepo replaces every standalone occurrence of oldRepo with newRepo in
// data. A match is "standalone" only when neither the preceding nor following
// byte continues a repository path, so replacing "…/weave-trace" never touches
// "…/weave-trace-server".
func replaceRepo(data []byte, oldRepo, newRepo string) []byte {
	if oldRepo == "" || oldRepo == newRepo {
		return data
	}
	old := []byte(oldRepo)
	var out bytes.Buffer
	for i := 0; i < len(data); {
		if bytes.HasPrefix(data[i:], old) {
			beforeOK := i == 0 || !isRepoPathByte(data[i-1])
			afterIdx := i + len(old)
			afterOK := afterIdx == len(data) || !isRepoPathByte(data[afterIdx])
			if beforeOK && afterOK {
				out.WriteString(newRepo)
				i = afterIdx
				continue
			}
		}
		out.WriteByte(data[i])
		i++
	}
	return out.Bytes()
}

// isRepoPathByte reports whether b can appear inside a repository path segment
// (used to detect token boundaries). ':' is deliberately excluded so a tag
// separator counts as a boundary.
func isRepoPathByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '/', b == '.', b == '-', b == '_':
		return true
	default:
		return false
	}
}

// pushManifestArtifact packs the rewritten YAML files into a single gzipped tar
// layer and pushes a fresh OCI image manifest to <target>/wandb/server-manifest
// tagged with version. The layer uses MediaTypeImageLayerGzip so the operator's
// extractor decompresses it.
func pushManifestArtifact(ctx context.Context, target, version string, files map[string][]byte, insecure bool) error {
	layerData, err := buildLayerTarGz(files)
	if err != nil {
		return fmt.Errorf("build layer: %w", err)
	}

	store := memory.New()
	layerDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayerGzip,
		Digest:    digest.FromBytes(layerData),
		Size:      int64(len(layerData)),
	}
	if err := store.Push(ctx, layerDesc, bytes.NewReader(layerData)); err != nil {
		return fmt.Errorf("stage layer: %w", err)
	}

	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1,
		"application/vnd.wandb.server-manifest", oras.PackManifestOptions{
			Layers: []ocispec.Descriptor{layerDesc},
		})
	if err != nil {
		return fmt.Errorf("pack manifest: %w", err)
	}
	if err := store.Tag(ctx, manifestDesc, version); err != nil {
		return fmt.Errorf("tag manifest: %w", err)
	}

	dst, err := remote.NewRepository(strings.TrimRight(target, "/") + "/wandb/server-manifest")
	if err != nil {
		return fmt.Errorf("init target repo: %w", err)
	}
	// The server manifest must be served over HTTPS (the operator fetches it over
	// HTTPS from inside the cluster). --insecure skips TLS verification for a
	// self-signed cert; it does NOT fall back to plain HTTP, since a plain-HTTP
	// manifest could never be consumed anyway.
	dst.Client = dockerAuthClient(insecure)

	if _, err := oras.Copy(ctx, store, version, dst, version, oras.DefaultCopyOptions); err != nil {
		return fmt.Errorf("copy to mirror: %w", err)
	}
	return nil
}

// buildLayerTarGz packs files into a deterministic gzipped tar (entries sorted
// by name).
func buildLayerTarGz(files map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for _, name := range sortedKeys(files) {
		data := files[name]
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(data)),
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gzw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// dockerAuthClient returns an oras auth client backed by the user's Docker
// config (~/.docker/config.json), falling back to anonymous access if no
// config is present. When insecure, TLS verification is skipped (for a mirror
// with a self-signed / untrusted cert).
func dockerAuthClient(insecure bool) *auth.Client {
	httpClient := retry.DefaultClient
	if insecure {
		httpClient = &http.Client{Transport: retry.NewTransport(insecureHTTPTransport())}
	}
	client := &auth.Client{
		Client: httpClient,
		Cache:  auth.NewCache(),
	}
	if store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{}); err == nil {
		client.Credential = credentials.Credential(store)
	}
	return client
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
