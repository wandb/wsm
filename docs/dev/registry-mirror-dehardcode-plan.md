# Plan: remove hard-coded image lists from `wsm registry mirror`

**Goal:** stop hand-maintaining image lists in `cmd/wsm/registry_mirror.go`. Instead
download the pinned **operator chart** and **server manifest**, parse them, and
derive every image to mirror — and rewrite the manifest so an air-gapped install
pulls **only** from the customer's private registry. Verified against operator
chart `2.0.0-beta.3`, server manifests `0.83.0` / `0.84.0-daily.20`, operator
source `../operator` (`v2.0.0-beta.3-9-geb182a5`), and moco `v0.34.0`.

**Feasibility: mostly-derivable.** Two residuals need a decision (clickhouse-keeper
on pre-0.84 manifests; the mysqld_exporter image, see §7).

---

## 1. What is hard-coded today

Two lists in [`cmd/wsm/registry_mirror.go`](../../cmd/wsm/registry_mirror.go):
`buildMirrorPlan` (operator/cert-manager/nginx/ksm charts + their images) and
`buildManagedImagePlan` (~18 tier-2 operator + tier-3 data-plane images). A third,
implicit copy of the mapping lives in `pkg/operator/operator.go` `InstallOperator`,
which retargets each subchart image to the mirror via Helm values. Mirror, `check`,
and install must all agree — today, by hand.

`collectManifestImages` in `cmd/wsm/registry_manifest.go` already derives the W&B
**application + migration** images and `mirrorServerManifest` rewrites those refs to
the mirror. The plan extends both to the managed/infra images.

## 2. The hand-maintained lists are already wrong (why derive)

| Hard-coded | Reality | |
|---|---|---|
| `seaweedfs-operator:1.0.21` | chart renders **1.0.32** | stale |
| `alpine/k8s:1.35.4` (altinity crdHook) | crdHook disabled by default; real image is `bitnami/kubectl`, never `alpine/k8s` | **phantom** |
| keeper always mirrored | absent in 0.83.0 manifest, present in 0.84.0-daily.20 | version-dependent (§7) |
| `moco/mysqld_exporter:0.19.0.1` vs manifest `prom/mysqld-exporter:v0.15.1` | operator deploys the moco-chart image; manifest field is **not honorable** by moco's CR API (§7) | needs a decision |

## 3. Where each image comes from (verified)

The operator reads **manifest** image refs for every managed data-plane component
and resolves them through `ImageRef.GetImage(wandb.Spec.Global.ImageRegistry)`:

- clickhouse server — `altinity/spec.go:247`; keeper — `keeper/spec.go:71` (+ unexported fallback const)
- redis standalone/replication/sentinel/**exporter** — `redis/opstree/spec.go:188/353/274/229`
- object store (seaweedfs) — `objectstore/seaweedfs/spec.go:136`
- kafka bufstream + etcd — `kafka/bufstream/spec.go:553/345`; `bucketEnsure` (aws-cli) is the bucket-ensure init image
- mysql server (moco/mysql) — `mysql/moco/spec.go:83/109`

**Key mechanism — `GetImage(registry)` PREPENDS the global registry to the *full*
upstream ref** (`manifest.go:112`): `{registry:"docker.io", repository:"altinity/clickhouse-server"}`
→ `GetImage("mirror.io")` = `mirror.io/docker.io/altinity/clickhouse-server`. So
`spec.global.imageRegistry` yields a **pull-through** layout (`<mirror>/<host>/<path>`),
whereas wsm's push layout is **flattened** (`<mirror>/<path>`, host stripped by
`translate`). That mismatch is exactly why wsm already rewrites app-image refs itself
instead of relying on `global.imageRegistry`.

The operator chart (`.../charts/operator:<ver>`) **bundles all subcharts** as
`charts/*.tgz`, so a Helm v4 ClientOnly/DryRun render needs no cluster and no network
beyond pulling the one chart. It yields the tier-2 **operator** images; the moco
**sidecars** (`moco-agent`/`fluent-bit`/`mysqld_exporter`) appear only as moco-controller
container **args** (`--agent-image` …) — invisible to a `container.Image` walk, but
readable from args or from the bundled `charts/moco/values.yaml`.

## 4. Decisions taken (from review) — the shape we're building

1. **Rewrite ALL images in the manifest to the mirror** (not just app images), so an
   air-gapped install pulls only from the private registry — no dependence on node
   containerd registry-mirrors or on `spec.global.imageRegistry` being wired for every
   code path. Flattened layout (`<mirror>/<path>`), consistent with today's app-image
   rewrite. (Reverses the earlier "don't rewrite infra" note.)
2. **Granular opt-out flags** so customers supplying their own managed services don't
   mirror images they won't use (see §6).
3. **mysqld_exporter should be manifest-driven** — but moco's CR API blocks the clean
   version of this; see §7 for the constraint and options.

## 5. Manifest rewrite — must handle BOTH `ImageRef` encodings

The manifest mixes two encodings today and will until the migration to
registry/repository completes — **the rewrite must support both**:

- **Embedded** (legacy; today's app/migration images): `registry: ""`, host baked into
  `repository` (e.g. `us-docker.pkg.dev/wandb-production/public/wandb/local`). This is
  what today's byte-level `replaceRepo` / `rewriteRepoForMirror` handles.
- **Split** (new; today's infra images): `registry: "docker.io"`,
  `repository: "altinity/clickhouse-server"`. The current byte rewrite touches only the
  `repository` substring, leaving `registry: "docker.io"` in place → broken
  `docker.io/<mirror>/…` ref. **This is the gap to close.**

One helper must map either encoding to the **same flattened mirror ref**
(`<mirror>/<path>`, matching where wsm pushes via `translate`/`rewriteRepoForMirror` —
which strips the host for generic images and the `wandb-production/public` prefix for
W&B images). Recommended output: **normalize to** `registry: ""`,
`repository: "<mirror>/<path>"`, so `GetImage("")` returns the mirror ref uniformly for
both inputs and both current + future manifests.

Recommended mechanism: a **typed round-trip** — wsm already imports the operator's
`wmanifest.Manifest`, so unmarshal → rewrite every `ImageRef` (apps, initContainers,
containers, migrations, and the infra `Images` maps) → marshal. The operator parses the
same struct via `sigs.k8s.io/yaml`, so field order/comment loss is cosmetic and cannot
change what it reads. **Leave `spec.global.imageRegistry` unset** for these so there's no
double-prepend (§3).

## 6. Scoping flags (Stage 3) — two independent axes

Two orthogonal customer concerns, NOT one:
- **Infra operators** (altinity-clickhouse, moco, redis-operator, seaweedfs) — a customer
  may run their own cluster-wide installs of these, so wsm must be able to skip mirroring
  the **operator** images.
- **Managed infra servers** (clickhouse, mysql, redis, object-store) — a customer may
  bring external DBs and never use W&B's managed infra, so skip those **server** images.

So each type has **three states**: (1) full managed — operator + server (default);
(2) BYO operator — server only; (3) not used — neither. Operator-exclusion is a *subset*
of managed-exclusion (excluding the server implies excluding its now-purposeless operator;
the reverse combo "server off / operator on" is meaningless).

**Kafka CANNOT be excluded.** It is not a member of the excludable set at all — its
images (bufstream/etcd/aws-cli, run directly by the wandb-operator; no subchart, no
operator image) are **always** mirrored. `kafka` is not a valid value for either flag;
passing it is a hard error, not a silent no-op. There is no code path that drops kafka.

**Chosen design: Option B — two list flags** (over Option A's 8 booleans):
- `--exclude-operators <list>` — skip only the operator images (BYO operator).
- `--exclude-managed <list>` — skip operator **and** server images (external DB).

Values: `clickhouse`, `mysql`, `redis`, `object-store`. `kafka`/unknown → error.
`--exclude-managed X` implies operator X excluded (listing X in both is redundant-but-OK).
`--skip-managed-images` becomes an alias for
`--exclude-managed clickhouse,mysql,redis,object-store`.

Why B over 8 booleans: the three states compose with no nonsensical combination, the
operator-vs-managed distinction is explicit, and new infra types are just new allowed
values — no new flags. Same flags on `registry check` (must compute the same set); and
for install coherence, matching `--set <subchart>.enabled=false` toggles on
`deploy-v2 operator` so a customer's real install doesn't fight their own operator.

Value → images (operator images dropped by `--exclude-operators`; both by `--exclude-managed`):

| Value | Operator images | Server images (manifest) |
|---|---|---|
| clickhouse | altinity-clickhouse-operator, altinity/metrics-exporter | clickhouse-server, clickhouse-keeper |
| mysql | moco + agent/fluent-bit/mysqld_exporter sidecars¹ | moco/mysql |
| redis | opstree redis-operator | opstree redis / sentinel / exporter |
| object-store | seaweedfs-operator | seaweedfs |
| kafka *(never excludable)* | — | bufstream, etcd, aws-cli (always mirrored) |

¹ The moco sidecars are operator-injected and operator-versioned, so they ride with the
mysql **operator** bucket — with BYO operator, the customer's own moco supplies them.

## 7. The two residuals

**clickhouse-keeper.** `keeper/spec.go` reads `ClickhouseKeeper[..].Images["keeper"]`
and falls back to an **unexported** const `defaultKeeperImage` (`keeper/values.go:10`)
when absent — which is the case for the plain 0.83.0 manifest, present in
0.84.0-daily.20. Options: (a) require/recommend a manifest that populates keeper (0.84+);
(b) guarded fallback = keeper repo at the `clickhouse-server` tag, with a warning;
(c) upstream: export the const. The operator has a TODO to delete the const once all
supported manifests supply keeper — so (a) is the direction.

**mysqld_exporter — moco API constraint (new finding).** The intent "have the operator
set the exporter image from the manifest" is **not achievable via moco's CR API in
v0.34.0**:
- Enabling telemetry sets `cluster.Spec.Collectors` (`mysql/moco/spec.go:96`), which makes
  moco inject the exporter sidecar.
- moco resolves that sidecar's image **only** from the controller-global
  `--mysqld-exporter-image` flag (`moco/controllers/mysql_container.go:242`
  `WithImage(r.ExporterImage)`), set once at controller start from the chart value.
- The per-cluster override moco exposes, `OverwriteContainer`
  (`moco/api/v1beta2/mysqlcluster_types.go:473`), carries only `Name`/`Resources`/
  `SecurityContext` — **no image**. So the operator cannot honor
  `mysql.<inst>.images.exporter` per-CR.

**Decision:** the image the operator actually deploys is the moco-chart
`ghcr.io/cybozu-go/moco/mysqld_exporter:0.19.0.1`, read from the moco-controller's
`--mysqld-exporter-image` arg at chart-render time (§Stage 2). So:
- **Source the exporter image from the chart render.** wsm parses the
  `--mysqld-exporter-image` arg; if it is ever absent, fall back to the exported moco
  const `moco.ExporterImage` (and warn) — the chart template has no `default`, so an
  empty tag would render a malformed `repo:` rather than fall back.
- **Explicitly ignore `mysql.<inst>.images.exporter` in the manifest** — it is inert
  (moco has no per-CR exporter-image override, §above) and must not be mirrored. In the
  §5 rewrite, **drop the `exporter` key** from the mysql `images` map so no dead upstream
  ref lingers in the air-gapped manifest. This one small, *documented* skip is the only
  manifest-specific special-case; tag it with a TODO to remove once the manifest stops
  emitting the field.
- **Going forward (operator/manifest side, separate):** the manifest will stop including
  the mysql exporter image; at that point the skip above becomes a no-op and can be
  deleted. (Optionally also raise an upstream moco request to allow a per-CR exporter
  image, but that is not needed for this work.)

## 8. Staged plan

- **Stage 0 — centralize version inputs.** Hoist the duplicated
  `operator-chart-version` default (`deploy_v2.go:530` + `registry_mirror.go:134`) to one
  const; keep reusing the `pkg/operator` cert-manager/nginx/ksm consts.
- **Stage 1 — derive tier-3 from the manifest + rewrite it. ✅ DONE.** `collectManifestImages`
  now walks the infra `Images` maps (gated by `includeManaged`/`--skip-managed-images`) and
  skips the `mysql.images.exporter` trap; the manifest rewrite is now yaml.v3 node-based,
  registry-aware (both encodings, §5), drops the exporter key, and only rewrites pushed
  refs (unknowns warned). `checkClickhouseKeeper` enforces §7(a). The manifest-derived
  data-plane images were removed from `buildManagedImagePlan` (operator + moco-sidecar
  images kept there pending Stage 2). `check` passes `!skipManaged` too. Covered by
  `cmd/wsm/registry_manifest_test.go` and validated against the real 0.84.0-daily.20
  manifest. Behavior change: data-plane images now require `--wandb-version` (they're
  version-specific, read from that version's manifest).
- **Stage 2a — derive operator + subchart + moco-sidecar images. ✅ DONE.**
  `pkg/operator/render.go` renders the operator chart client-side (helm v4
  `DryRunClient`, no cluster) and extracts images from workload containers,
  initContainers, `--…-image` container args (the moco sidecars), CR image maps, and
  hooks. `buildManagedImagePlan` now calls `operator.OperatorChartImages` instead of a
  hard-coded list — fixing the `seaweedfs-operator` drift (→1.0.32) and dropping the
  `alpine/k8s` phantom automatically. The operator binary (a W&B image) is filtered out
  (mirrored by tier-1). `check` renders the chart **from the mirror**
  (`oci://<host>/wandb/charts`, bundled subcharts travel with it) so it stays
  air-gapped. Tested by `pkg/operator/render_test.go` (pure-extractor unit + real-chart
  render).
- **Stage 2b — derive cert-manager / nginx-gateway / kube-state-metrics images. ✅ DONE.**
  `operator.CertManagerImages` / `NginxGatewayImages` / `KubeStateMetricsImages` render
  each tool chart (with the same values install uses; a modern `KubeVersion` satisfies
  the charts' `kubeVersion` floors). `buildMirrorPlan` now derives their component images
  instead of hard-coding them — dropping the unused cert-manager `startupapicheck` (5→4),
  and picking up the nginx NginxProxy data-plane image + cert-manager `acmesolver` arg.
  `buildMirrorPlan` is now `ctx`-taking/fallible with a `fromMirror` switch so `check`
  renders the tool charts from the mirror (air-gapped). Only the OCI **chart artifacts**
  and the operator binary remain fixed refs (version-pinned, not image lists). Covered by
  `pkg/operator/render_test.go`.
- **Stage 3 — scoping flags. ✅ DONE.** `cmd/wsm/registry_managed.go` adds the
  `managedExclusions` model (two axes: `--exclude-operators` = operator images only;
  `--exclude-managed` = operator + server images) with `parseManagedExclusions`
  validating values and rejecting `kafka`/unknowns. `--skip-managed-images` is now an
  alias for `--exclude-managed clickhouse,mysql,redis,object-store`. Exclusions thread
  through the derivation: excluded operators disable their subchart in the operator-chart
  render (dropping the operator image and, for moco, its sidecars); excluded managed
  types skip their manifest infra sections; the keeper check is skipped when ClickHouse
  is excluded. Flags added to both `mirror` and `check`. Verified end-to-end via dry-run
  (`--exclude-managed mysql` drops moco+sidecars; `--exclude-operators clickhouse` drops
  the altinity operator but keeps the manifest's clickhouse-server/keeper; `--exclude-*
  kafka` errors). Tested in `registry_managed_test.go` + `registry_manifest_test.go`.
  Kafka is always mirrored. Follow-up (not done): mirror matching `--set <subchart>.enabled=false`
  toggles on `deploy-v2 operator` so a customer's install doesn't fight their own operator.
- **Operator repo (separate PR):** raise the mysqld_exporter constraint (§7) and, if
  agreed, the keeper-const export.

## 9. Risks / landmines

1. **Extractor must exceed `container.Image`** — else it drops moco sidecars (args),
   cert-manager acmesolver (`--acme-http01-solver-image` arg), startupapicheck / altinity
   crdHook (`release.Hooks`), nginx data-plane (NginxProxy CR), seaweedfs (CRD defaults).
2. **Value/toggle coupling** — render with the same values wsm installs with
   (telemetry.mode, crdHook off, KSM only when telemetry=full, cert-manager
   startupapicheck off, victoria/grafana gated). The §6 `--managed-*` flags drive this.
3. **Registry-aware rewrite** (§5) — split-registry infra refs must have both fields
   rewritten, and `spec.global.imageRegistry` left unset, or refs double-prepend/break.
4. **Silent failures** — keeper fallback and any future operator-Go-only image produce no
   compile/runtime signal; golden tests + explicit warnings are the mitigations.
