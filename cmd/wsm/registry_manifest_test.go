package main

import (
	"sort"
	"strings"
	"testing"

	wmanifest "github.com/wandb/operator/pkg/wandb/manifest"
	"sigs.k8s.io/yaml"
)

// A manifest fixture exercising both image-ref encodings: legacy embedded
// (applications/migrations, registry baked into repository) and the newer
// registry/repository split (infra sections), plus the inert mysql exporter.
const testManifest = `
applications:
  anaconda2:
    image:
      repository: us-docker.pkg.dev/wandb-production/public/wandb/anaconda2
      tag: "0.84.0"
    containers:
      - name: anaconda2
  frontend:
    image:
      repository: us-docker.pkg.dev/wandb-production/public/wandb/frontend-nginx
      tag: "0.84.0"
migrations:
  default:
    image:
      repository: us-docker.pkg.dev/wandb-production/public/wandb/migrations
      tag: "0.84.0"
bucket:
  default:
    images:
      seaweedfs:
        registry: docker.io
        repository: chrislusf/seaweedfs
        tag: "4.35"
clickhouse:
  default:
    images:
      server:
        registry: docker.io
        repository: altinity/clickhouse-server
        tag: "25.8"
clickhouseKeeper:
  default:
    images:
      keeper:
        registry: docker.io
        repository: altinity/clickhouse-keeper
        tag: "25.8"
kafka:
  images:
    bufstream:
      registry: us-docker.pkg.dev
      repository: buf-images-1/buf/images/bufstream
      tag: "0.4.15"
    etcd:
      registry: quay.io
      repository: coreos/etcd
      tag: "v3.5.31"
    bucketEnsure:
      repository: amazon/aws-cli
      tag: "2.35.10"
mysql:
  default:
    images:
      mysql:
        registry: ghcr.io
        repository: cybozu-go/moco/mysql
        tag: "8.4.8"
      exporter:
        registry: docker.io
        repository: prom/mysqld-exporter
        tag: "v0.15.1"
redis:
  default:
    images:
      standalone:
        registry: quay.io
        repository: opstree/redis
        tag: "v7.0.15"
`

func mustExclusions(t *testing.T, ops, mgd []string, skip bool) managedExclusions {
	t.Helper()
	exclusions, err := parseManagedExclusions(ops, mgd, skip)
	if err != nil {
		t.Fatalf("parseManagedExclusions(%v,%v,%v): %v", ops, mgd, skip, err)
	}
	return exclusions
}

func imageSet(refs []wmanifest.ImageRef) map[string]struct{} {
	out := map[string]struct{}{}
	for _, r := range refs {
		out[r.GetImage("")] = struct{}{}
	}
	return out
}

func TestCollectManifestImages_Managed(t *testing.T) {
	files := map[string][]byte{"manifest.yaml": []byte(testManifest)}

	refs, err := collectManifestImages(files, mustExclusions(t, nil, nil, false))
	if err != nil {
		t.Fatalf("collectManifestImages: %v", err)
	}
	got := imageSet(refs)

	want := []string{
		"us-docker.pkg.dev/wandb-production/public/wandb/anaconda2:0.84.0",
		"us-docker.pkg.dev/wandb-production/public/wandb/frontend-nginx:0.84.0",
		"us-docker.pkg.dev/wandb-production/public/wandb/migrations:0.84.0",
		"docker.io/chrislusf/seaweedfs:4.35",
		"docker.io/altinity/clickhouse-server:25.8",
		"docker.io/altinity/clickhouse-keeper:25.8",
		"us-docker.pkg.dev/buf-images-1/buf/images/bufstream:0.4.15",
		"quay.io/coreos/etcd:v3.5.31",
		"amazon/aws-cli:2.35.10",
		"ghcr.io/cybozu-go/moco/mysql:8.4.8",
		"quay.io/opstree/redis:v7.0.15",
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("expected image %q not enumerated", w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("image count = %d, want %d; got=%v", len(got), len(want), sortedSet(got))
	}
	// The inert mysql exporter must never be enumerated.
	if _, ok := got["docker.io/prom/mysqld-exporter:v0.15.1"]; ok {
		t.Errorf("mysql exporter image should be skipped, but was enumerated")
	}
}

func TestCollectManifestImages_SkipManaged(t *testing.T) {
	files := map[string][]byte{"manifest.yaml": []byte(testManifest)}

	// --skip-managed-images excludes clickhouse/mysql/redis/object-store — but NOT
	// kafka, which is always mirrored. So the set is app+migration images plus the
	// kafka data-plane images.
	refs, err := collectManifestImages(files, mustExclusions(t, nil, nil, true))
	if err != nil {
		t.Fatalf("collectManifestImages: %v", err)
	}
	got := imageSet(refs)

	wantPresent := []string{
		"us-docker.pkg.dev/wandb-production/public/wandb/anaconda2:0.84.0",
		"us-docker.pkg.dev/wandb-production/public/wandb/frontend-nginx:0.84.0",
		"us-docker.pkg.dev/wandb-production/public/wandb/migrations:0.84.0",
		"us-docker.pkg.dev/buf-images-1/buf/images/bufstream:0.4.15",
		"quay.io/coreos/etcd:v3.5.31",
		"amazon/aws-cli:2.35.10",
	}
	for _, w := range wantPresent {
		if _, ok := got[w]; !ok {
			t.Errorf("expected %q present under skip-managed; got=%v", w, sortedSet(got))
		}
	}
	// The excluded managed servers must be gone.
	for _, bad := range []string{"clickhouse-server", "clickhouse-keeper", "opstree/redis", "moco/mysql", "seaweedfs"} {
		for img := range got {
			if strings.Contains(img, bad) {
				t.Errorf("excluded managed image %q should be skipped", img)
			}
		}
	}
	if len(got) != len(wantPresent) {
		t.Errorf("skip-managed image count = %d, want %d; got=%v", len(got), len(wantPresent), sortedSet(got))
	}
}

func TestCollectManifestImages_ExcludeOneType(t *testing.T) {
	files := map[string][]byte{"manifest.yaml": []byte(testManifest)}

	// --exclude-managed mysql drops the mysql server image but keeps everything else.
	refs, err := collectManifestImages(files, mustExclusions(t, nil, []string{"mysql"}, false))
	if err != nil {
		t.Fatalf("collectManifestImages: %v", err)
	}
	got := imageSet(refs)
	if _, ok := got["ghcr.io/cybozu-go/moco/mysql:8.4.8"]; ok {
		t.Errorf("mysql server image should be excluded")
	}
	for _, keep := range []string{"docker.io/altinity/clickhouse-server:25.8", "quay.io/opstree/redis:v7.0.15", "quay.io/coreos/etcd:v3.5.31"} {
		if _, ok := got[keep]; !ok {
			t.Errorf("non-excluded image %q should remain; got=%v", keep, sortedSet(got))
		}
	}

	// --exclude-operators mysql is an operator-axis exclusion: it must NOT drop the
	// mysql server image (still a managed service).
	refs2, err := collectManifestImages(files, mustExclusions(t, []string{"mysql"}, nil, false))
	if err != nil {
		t.Fatalf("collectManifestImages: %v", err)
	}
	if _, ok := imageSet(refs2)["ghcr.io/cybozu-go/moco/mysql:8.4.8"]; !ok {
		t.Errorf("--exclude-operators mysql must not drop the mysql server image")
	}
}

func TestRewriteManifestImages(t *testing.T) {
	const target = "mirror.test"
	files := map[string][]byte{"manifest.yaml": []byte(testManifest)}

	refs, err := collectManifestImages(files, mustExclusions(t, nil, nil, false))
	if err != nil {
		t.Fatalf("collectManifestImages: %v", err)
	}
	mirrored := map[string]string{}
	for _, ref := range refs {
		mirrored[ref.GetImage("")] = rewriteRepoForMirror(target, upstreamRepo(ref))
	}

	out, unknown, err := rewriteManifestImages([]byte(testManifest), mirrored)
	if err != nil {
		t.Fatalf("rewriteManifestImages: %v", err)
	}
	if len(unknown) != 0 {
		t.Errorf("unexpected unknown refs left unrewritten: %v", unknown)
	}

	// Every upstream registry host must be gone — an air-gapped install must pull
	// only from the mirror.
	for _, host := range []string{"docker.io", "quay.io", "ghcr.io", "us-docker.pkg.dev"} {
		if strings.Contains(string(out), host) {
			t.Errorf("rewritten manifest still references upstream host %q:\n%s", host, out)
		}
	}
	// The inert mysql exporter must be dropped entirely.
	if strings.Contains(string(out), "prom/mysqld-exporter") {
		t.Errorf("mysql exporter should be dropped from rewritten manifest:\n%s", out)
	}

	// Re-parse via the operator's own decoder and confirm every image now resolves
	// to a mirror ref.
	refs2, err := collectManifestImages(map[string][]byte{"manifest.yaml": out}, mustExclusions(t, nil, nil, false))
	if err != nil {
		t.Fatalf("re-parse rewritten manifest: %v", err)
	}
	for _, r := range refs2 {
		img := r.GetImage("")
		if !strings.HasPrefix(img, target+"/") {
			t.Errorf("rewritten image %q does not point at the mirror", img)
		}
	}
	want := []string{
		target + "/wandb/anaconda2:0.84.0",
		target + "/altinity/clickhouse-server:25.8",
		target + "/altinity/clickhouse-keeper:25.8",
		target + "/buf-images-1/buf/images/bufstream:0.4.15",
		target + "/amazon/aws-cli:2.35.10",
		target + "/cybozu-go/moco/mysql:8.4.8",
		target + "/opstree/redis:v7.0.15",
	}
	got := imageSet(refs2)
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("expected mirrored image %q missing; got=%v", w, sortedSet(got))
		}
	}
}

func TestCheckClickhouseKeeper(t *testing.T) {
	// Fixture has clickhouse server + keeper → OK.
	files := map[string][]byte{"manifest.yaml": []byte(testManifest)}
	if err := checkClickhouseKeeper(files); err != nil {
		t.Errorf("keeper present should pass, got: %v", err)
	}

	// Drop the keeper section → must error (0.83-style manifest).
	var m map[string]interface{}
	if err := yaml.Unmarshal([]byte(testManifest), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	delete(m, "clickhouseKeeper")
	noKeeper, err := yaml.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := checkClickhouseKeeper(map[string][]byte{"manifest.yaml": noKeeper}); err == nil {
		t.Errorf("expected error when clickhouse present without keeper image")
	}

	// No managed clickhouse at all → OK (nothing to require).
	if err := checkClickhouseKeeper(map[string][]byte{"m.yaml": []byte("applications: {}\n")}); err != nil {
		t.Errorf("no clickhouse should pass, got: %v", err)
	}
}

func sortedSet(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
