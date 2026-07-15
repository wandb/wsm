# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`wsm` (Weights & Biases Server Manager) is a Cobra-based CLI that installs and upgrades W&B Server on Kubernetes. The repo currently carries **two parallel command surfaces** that share `cmd/wsm/root.go` but otherwise barely overlap:

- **v1 (`wsm deploy`, `wsm list`, `wsm download`, `wsm console`)** — legacy airgapped-friendly flow built around the deployer channel API and an untyped `WeightsAndBiases` CR (`apps.wandb.com/v1`). Helm v3, hand-rolled CR struct in `pkg/crd/weightsandbiases.go`.
- **v2 (`wsm deploy-v2`, `wsm cluster`)** — the actively maintained surface. Imports the operator's typed Go API (`github.com/wandb/operator/api/v2`) and constructs `v2.WeightsAndBiases` directly. Helm v4 + OCI charts, installs cert-manager and nginx-gateway-fabric itself, can create Kind clusters in-process via `sigs.k8s.io/kind`.

Recent PRs (#51, #58, #57) all extend the v2 surface; the v1 files have not been meaningfully edited in months. New work belongs on v2 unless explicitly maintaining airgapped v1 users.

There is **no test suite** — CI runs lint, fmt, `go mod tidy`, and `go mod verify` only. There are no `*_test.go` files; do not assume a test layer exists.

User-facing docs live under `docs/` (getting-started, deployment per cloud, configuration, operations, reference) and are the canonical source for flag semantics and CR field meanings.

## Common commands

```bash
make build          # builds ./wsm from ./cmd/wsm
make fmt            # gofmt; CI fails if `git diff --exit-code` is dirty after
make lint           # installs golangci-lint v2.11.4 then runs `go vet` + golangci-lint
make lint-fix       # golangci-lint --fix
make safe-update-deps  # go get -u ./... && go mod tidy
```

Go **1.26** is required (matches `go.mod` and CI). On macOS the Makefile sets `CGO_LDFLAGS=-Wl,-w` to suppress linker warnings from the `containers/image` dependency.

Two Helm SDKs are imported simultaneously by design:
- `helm.sh/helm/v3` — used only by the legacy `pkg/helm` package serving v1 commands.
- `helm.sh/helm/v4` — used by `pkg/operator` for everything v2 (OCI registry support, `WaitStrategy = "hookOnly"`, etc.).
Don't try to consolidate them without a migration plan.

## v2 CLI surface (the active one)

All `deploy-v2` commands require `--context <kubeconfig-context>` and call `kubectl.SetContext` in `PersistentPreRunE`. The `pkg/kubectl` package keeps singleton clientsets behind `sync.Once`; `kubectl.ResetClients()` is required after switching context mid-process (used by `cluster list` to probe each kind cluster).

- `wsm deploy-v2 operator` — installs cert-manager (OCI: `oci://quay.io/jetstack/charts/cert-manager`), nginx-gateway-fabric (OCI: `oci://ghcr.io/nginx/charts/nginx-gateway-fabric`), the wandb-operator (OCI: `oci://us-docker.pkg.dev/wandb-production/public/wandb/charts/operator`), and optionally a Kind cluster + the CR. Each install/upgrade is a `helm.sh/helm/v4` action with `WaitStrategy = "hookOnly"`, followed by an explicit `operator.WaitForX` poll.
- `wsm deploy-v2 wandb deploy` — applies the `v2.WeightsAndBiases` CR via the dynamic client with SSA (`FieldManager: "wsm"`). Builds the CR from flags via `processWandbCR` in `cmd/wsm/deploy_v2.go:897`.
- `wsm deploy-v2 wandb destroy` — deletes the CR.
- `wsm deploy-v2 wandb get-ca-cert` — reads `<wandbName>-root-cert` secret and writes `ca.crt`/`tls.crt` data to a local file.
- `wsm cluster create|destroy|list|cleanup` — Kind cluster lifecycle. `list` and `cleanup` rely on `kubectl.HasDeploymentMarker` (a ConfigMap-based marker written into namespaces by `wsm` so it knows what it owns).

### How v2 builds the CR

The CR is a **typed struct from the operator repo**, not a `map[string]interface{}` or a hand-rolled mirror:

```go
import v2 "github.com/wandb/operator/api/v2"

var wandbCR = &v2.WeightsAndBiases{
    TypeMeta: metav1.TypeMeta{APIVersion: "apps.wandb.com/v2", Kind: "WeightsAndBiases"},
    Spec: v2.WeightsAndBiasesSpec{
        Wandb:       v2.WandbAppSpec{Hostname: "http://localhost:8080", ...},
        MySQL:       v2.MySQLSpec{ManagedMysql: &v2.ManagedMysqlSpec{...}},
        Redis:       v2.RedisSpec{ManagedRedis: &v2.ManagedRedisSpec{...}},
        Kafka:       v2.KafkaSpec{ManagedKafka: &v2.ManagedKafkaSpec{...}},
        ObjectStore: v2.ObjectStoreSpec{ManagedObjectStore: &v2.ManagedObjectStoreSpec{...}},
        ClickHouse:  v2.ClickHouseSpec{ManagedClickHouse: &v2.ManagedClickHouseSpec{...}},
    },
}
```

This is the single most important architectural fact about v2. The implication is the maintenance workflow: when the operator changes its CR shape upstream, `go get github.com/wandb/operator@<new>` + `go build` reports every stale construction in `deploy_v2.go` as a compile error. PR #58's diff is essentially a mechanical rename across these typed fields (e.g. `WBMySQLSpec` → `MySQLSpec`, plus a new `ManagedMysqlSpec` nesting).

### Hand-bumped per release

Two constants in `cmd/wsm/deploy_v2.go` need updating each W&B/operator release:
- `defaultWandbVersion` (currently `"0.82.2"`, line 34) — used when `--wandb-version` is unset.
- The default of `--operator-chart-version` flag (currently `"2.0.0-beta.1"`).

cert-manager (`v1.20.2`) and nginx-gateway-fabric (`2.5.1`) versions are pinned as constants in `pkg/operator/operator.go:53-64`. Gateway API CRDs come from a hardcoded URL at line 66.

## v1 CLI surface (legacy, still present)

- `wsm list` — prints the operator + W&B images that a fresh install would pull. Uses a Bubble Tea spinner UI.
- `wsm download` — builds a `./bundle/` directory containing `charts/`, `images/<image>/image.tgz`, and `spec.yaml` for offline transfer. Requires `docker` on PATH (image save uses `docker pull`/`docker save`).
- `wsm deploy [operator|chart-cm|wandb-cr]` — three-phase v1 install (operator chart → `wandb-charts` ConfigMap for airgapped → `apps.wandb.com/v1` CR). `chart-cm` only runs when `--airgapped`.
- `wsm console` — `kubectl port-forward service/wandb-console 8082:8082` and opens a browser with the auto-login URL built from the `wandb-password` secret.

v1 architecture is unchanged from before the v2 work — the orchestration triangle is still: **deployer channel API** (`https://deploy.wandb.ai/api/v1/operator/channel`, overridable via `DEPLOYER_CHANNEL_URL`) → **`charts.wandb.ai` Helm repo** for the `operator` and `operator-wandb` charts → **`WeightsAndBiases` CR** that the in-cluster operator reconciles. Chart constants in `pkg/helm/consts.go`; CR shape in `pkg/crd/weightsandbiases.go` (`Kind=WeightsAndBiases`, `Group=apps.wandb.com`, `GroupVersion=v1`, `Namespace=default` hardcoded).

## Package layout (non-obvious bits)

- `cmd/wsm/deploy_v2.go` (~1.3k lines) — all v2 commands, the typed CR template, flag plumbing, and Kind cluster helpers in one file. Look here first for v2 questions.
- `pkg/operator/operator.go` (~900 lines) — Helm v4 wrappers for installing/uninstalling/waiting on cert-manager, nginx-gateway-fabric, wandb-operator; SSA `ApplyCR` / `DeleteCR` / `ListCRs` against the `apps.wandb.com/v2` GVR; readiness polls. `WaitForOperator` waits for `wandb-operator-mutating-webhook-configuration` to get a non-empty CA bundle injected before considering the operator ready.
- `pkg/kind/kind.go` — wraps `sigs.k8s.io/kind` to create/delete clusters in-process. Used by `wsm cluster` and by `--setup-k8s-cluster` in `deploy-v2 operator`.
- `pkg/kubectl/kubectl.go` — context-aware singleton client. `SetContext` + `ResetClients` are the API for switching kubeconfig context mid-process. Also hosts SSA helpers `ApplyUnstructured`, `ApplyCertificate`, `ApplyIssuer`, `ApplyClusterIssuer`, `ApplyIngressClass`, `ApplyStorageClass`, and the deployment-marker ConfigMap helpers (`CreateDeploymentMarker`, `HasDeploymentMarker`, `FindNamespacesWithMarker`, `DeleteDeploymentMarker`) that track what `wsm` owns.
- `pkg/helm` (v1 only) — wraps Helm SDK v3. `GetRuntimeObjects` does a client-only dry-run install to render manifests; `ExtractImages` walks the rendered Deployment/StatefulSet/DaemonSet/Job/CronJob/etc. to enumerate container images. This is what `list`/`download` depend on — only those workload kinds are inspected.
- `pkg/helm/values` (v1 only) — `Values` is `map[string]interface{}` with dot-path `GetValue`/`SetValue`/`Merge`/`Coalesce`/`AddHelmValue` helpers. User-supplied values from `--values` files are merged under `wandb:` (chart values) or `operator:` (operator-chart values).
- `pkg/crd/weightsandbiases.go` (v1 only) — hand-rolled v1 CR struct + dynamic-client SSA with `FieldManager: "wandb-controller-manager"`. v2 does not use this; v2's CR comes from the imported operator types and is applied via `pkg/operator.ApplyCR` with `FieldManager: "wsm"`.
- `pkg/deployer` (v1 only) — calls the channel API. v2 does not use this; chart versions are flags/constants.
- `pkg/images`, `pkg/term/pkgm`, `pkg/term/task`, `pkg/spec` — all v1-only.

## Updating wsm when the operator changes

Two skills, used together:

- **`audit-operator-diff`** (`.claude/skills/audit-operator-diff/SKILL.md`) — read-only audit between two operator refs. Produces `.claude/audit-report.md` with categorized findings (🔍 Pre-flight, 🚫 Breaks, ⚠️ Mismatches, ➕ Missing capabilities, ℹ️ Internal-only) and a user-approved fix list. Use independently for code review / release planning.
- **`sync-operator`** (`.claude/skills/sync-operator/SKILL.md`) — invokes `audit-operator-diff` first, then bumps `go.mod`, applies the approved fix list, updates docs, and smoke-tests on Kind.

Typical flow: `/sync-operator` → audit runs → user approves fixes → sync applies them → verify. Both skills require a local clone of `github.com/wandb/operator` (typically at `../operator`) and OrbStack running for the smoke-test stage.

For the rare case where v1 needs an edit, the skill defers to the v1 update playbook below — v1 is otherwise frozen.

### v1 update playbook (legacy)

| Change category | File:line | What to edit |
|---|---|---|
| `image.tag` values key renamed | `cmd/wsm/list.go:99-103`, `cmd/wsm/download.go:65-71` | replace dotted path passed to chart values map |
| Sub-chart toggle renamed (e.g. `weave-trace.install`) | `cmd/wsm/list.go:117-120`, `cmd/wsm/download.go:84-86` | replace the literal map key |
| v1 CR API group/version/kind/plural | `pkg/crd/weightsandbiases.go:16-24` | update constants |
| v1 CR spec field added/renamed | `pkg/spec/spec.go` + `pkg/crd/weightsandbiases.go:26-58` | add field + plumb through `NewWeightsAndBiases` and its caller at `cmd/wsm/deploy.go:135` |
| Chart name / repo URL change | `pkg/helm/consts.go:4-6` | edit constants |
| Controller image moved off Docker Hub | `cmd/wsm/list.go:87, 143-184`, `cmd/wsm/download.go:56` | rewrite `getMostRecentTag` or replace call sites |
| Deployer channel API moved | `pkg/deployer/deployer.go:12` | edit constant (also overridable via `DEPLOYER_CHANNEL_URL`) |
| Chart adds new workload kind that holds images | `pkg/helm/helm.go:118-155` `ExtractImage` | extend the type switch |
| Chart starts shipping a webhook | `pkg/helm/chart.go:47-58, 61-71` | set `client.Wait = true; client.Timeout = 5*time.Minute` |

Verify v1 edits with `./wsm list` (no cluster needed).

## Gotchas

- **`apps.wandb.com/v1` vs `apps.wandb.com/v2`** — these are different CRDs reconciled by different operators. Don't mix v1 and v2 in the same cluster. `pkg/crd` is v1, `pkg/operator.ApplyCR` is v2.
- **v2 webhook readiness is load-bearing.** `pkg/operator/operator.go:WaitForOperator` waits for the mutating webhook's CA bundle to be non-empty before returning. Skipping this wait will race the webhook and surface as `failed calling webhook ... connection refused` on the next `ApplyCR`.
- **Singleton kubectl client requires `ResetClients` after `SetContext`.** Any code switching kubeconfig context mid-process (the only example today is `cluster list`) must call `kubectl.ResetClients()` or it will keep talking to the previous context.
- **`weave-trace.install` is force-enabled in v1's `list.go` and `download.go`** before computing images so the trace component's images are included in bundles, even though it ships disabled by default. Only relevant if maintaining v1; v2 doesn't enumerate images this way.
- **v1 CR is always applied to namespace `default`** (`pkg/crd/weightsandbiases.go:81`); only the operator Helm release respects `--namespace`. v2 puts the CR in `--wandb-namespace` (default `wandb`).
- **`getMostRecentTag` (v1, `cmd/wsm/list.go:143`) parses Docker Hub tags as semver and discards anything that doesn't parse** — non-semver tags (branch builds, `latest`) never become the "latest" operator tag.
- **Helm chart download for v1 `operator` chart passes `""` as version** (`cmd/wsm/download.go:62-71`), so `wsm download` always grabs the latest chart from `charts.wandb.ai`. There is no v1 pinning mechanism.

## docs/ tree

Documentation users see lives under `docs/` and includes deployment guides per cloud (Kind / GKE / EKS / AKS), CR field reference, sizing reference, and SSL/TLS configuration. When changing flag semantics or CR field handling in v2, check `docs/reference/commands.md` and `docs/reference/cr-fields.md` and keep them in sync.
