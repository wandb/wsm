---
name: sync-operator
description: Sync wsm to be e2e compatible with a target github.com/wandb/operator ref — always invokes /audit-operator-diff first, then applies the user-approved fix list, updates docs, and smoke-tests on Kind. Triggers on phrases like "sync wsm with operator", "bump operator dep", "update for new operator release", "match operator changes". v2 surface only by default; v1 only on explicit user request. Requires a local clone of the operator repo and OrbStack running.
allowed-tools:
  # Operator clone reads
  - Bash(git -C *operator *)
  # Same-repo read-only git
  - Bash(git log:*)
  - Bash(git diff:*)
  - Bash(git show:*)
  - Bash(git status:*)
  - Bash(git branch:*)
  - Bash(git rev-parse:*)
  - Bash(git describe:*)
  # File search / read
  - Bash(grep:*)
  - Bash(rg:*)
  - Bash(find:*)
  - Bash(ls:*)
  - Bash(cat:*)
  - Bash(head:*)
  - Bash(tail:*)
  - Bash(wc:*)
  # OCI chart inspection
  - Bash(helm show chart:*)
  - Bash(helm pull oci://*)
  # GitHub reads (no writes)
  - Bash(gh pr view:*)
  - Bash(gh pr list:*)
  - Bash(gh repo view:*)
  - Bash(gh api -X GET *)
  - WebFetch(domain:github.com)
  - WebFetch(domain:api.github.com)
  - WebFetch(domain:raw.githubusercontent.com)
  # Go module / build (used in Step 2 of the sync)
  - Bash(go get github.com/wandb/operator*)
  - Bash(go mod tidy:*)
  - Bash(go mod verify:*)
  - Bash(go build:*)
  - Bash(go vet:*)
  # Lint / fmt
  - Bash(make fmt)
  - Bash(make lint)
  - Bash(make build)
  - Bash(golangci-lint run:*)
  - Bash(gofmt:*)
  # Smoke test orchestration
  - Bash(./wsm cluster create:*)
  - Bash(./wsm cluster cleanup:*)
  - Bash(./wsm cluster destroy:*)
  - Bash(./wsm cluster list:*)
  - Bash(./wsm deploy-v2:*)
  - Bash(helm --kube-context kind-* uninstall:*)
  - Bash(kubectl --context kind-* get:*)
  - Bash(kubectl --context kind-* describe:*)
  - Bash(kubectl --context kind-* logs:*)
  - Bash(kubectl --context kind-* apply --dry-run:*)
  - Bash(docker info:*)
  - Bash(docker ps:*)
  - Bash(orbctl status:*)
  - Bash(lsof -nP -iTCP:*)
---

# Sync wsm with current operator

Goal: bring wsm into compile + runtime compatibility with a current operator release, with the smallest possible diff. Do not introduce new packages, abstractions, build-time flags, or refactors. Defer all "what changed" analysis to `/audit-operator-diff`; this skill only applies the audit's approved fixes.

## Sandbox & read actions (read first)

`allowed-tools` only suppresses **permission prompts** — it does **not** lift the command **sandbox**. Every command below is read-only yet fails under the sandbox and must run unsandboxed (`dangerouslyDisableSandbox: true`); the failure is the sandbox, not the operation:

| Command | Failure under sandbox |
|---|---|
| `docker info` | docker unix socket blocked — **a denial here is NOT evidence the daemon is down; retry unsandboxed before telling the user to start it** |
| `git -C "$OP" fetch …` | `.git/FETCH_HEAD` write denied (`Operation not permitted`) |
| `go get …` / `go mod tidy` / `go mod verify` | `proxy.golang.org` **TLS interception** (`OSStatus -26276`) — the host is already allowlisted; allowlisting does not fix it, so `go` runs unsandboxed |
| `go build` / `go vet` / `golangci-lint run` | module cache / proxy under sandbox |
| `helm show chart oci://us-docker.pkg.dev/…` | host not allowlisted (`tls: failed to verify certificate`) |

Retry unsandboxed once on any of these; don't misread the first denial as a real failure. To persist the friction-removers, offer the `update-config` skill to add `us-docker.pkg.dev` to the network allowlist and `$OP/.git` to the filesystem-write allowlist in `.claude/settings.local.json`. (The `go`/docker cases can't be allowlisted away — they need unsandboxed execution.)

## Step 0: Prerequisites — block on these

1. **Local operator clone.** Resolve the path the same way `/audit-operator-diff` does — `$WANDB_OPERATOR_DIR` if set, else ask the user (do **not** hardcode `../operator`; see `feedback_shared_skill_portability` in user memory). Verify the resolved `$OP/go.mod` contains `module github.com/wandb/operator`. If absent, ask for the path or abort. The audit (Step 1) does the authoritative resolution and may also use the portable GitHub-clone path; this is just the blocking prerequisite check.
2. **Container runtime running.** Prefer `docker info` (works for OrbStack — reports `Name: orbstack` — and Docker Desktop). `orbctl status` is OrbStack-specific and not always on PATH. **Run it unsandboxed** (the socket is sandbox-blocked); a sandboxed failure is not a down daemon. If genuinely down, ask the user to start it.
3. **Kind ports free.** OrbStack binds `:8080` and `:8443` for its proxy, colliding with wsm's default Kind ports. Check `lsof -nP -iTCP:8080 -sTCP:LISTEN` and `:8443`; if bound, use higher ports later (`--http-port 18080 --https-port 18443`).
4. **Working tree state.** `git status --porcelain`. Three cases:
   - **Clean** — proceed normally.
   - **In-progress sync from a previous run** — *do not* ask to stash. Recognize this state when `git diff HEAD -- go.mod` shows an operator pin bump AND `cmd/wsm/deploy_v2.go` / `pkg/operator/operator.go` are modified AND/OR `.claude/audit-report.md` already exists. Treat the in-progress edits as the baseline and continue: `/audit-operator-diff` will pick up the in-progress pin as the target (see its Step 0/1), and Step 2 below applies the remaining fix list on top of what's already there.
   - **Unrelated dirty edits** — ask the user whether to stash / continue / abort, as before.

## Step 1: Audit the diff

**Invoke `/audit-operator-diff`.** Hand off control; the audit will:

1. Ask the user for the target operator ref.
2. Enumerate all changes between the current pin (read from `go.mod`) and the target.
3. Cross-reference each change with wsm's coupling sites.
4. Classify findings into 🔍 Pre-flight, 🚫 Breaks, ⚠️ Mismatches, ➕ Missing capabilities, ℹ️ Internal-only.
5. Probe OCI chart freshness vs the operator-repo Chart.yaml (run early when chart deps / RBAC changed).
6. Run the systematic `SecretKeySelector` zero-value-trap sweep and emit the mandatory **full-surface capability ledger** (every CR field → exposed / new / gap), so operator↔wsm parity is reconciled *in the report* — not left for the user to push back on.
7. Present the report inline and via `.claude/audit-report.md`.
8. Collect the user's batched decisions (`all` / `none` / `abort` / comma-separated IDs like `B1=C, M1, M2, C1`).
9. Update the report's `## Decisions` and `## Recommended action` sections.

When the audit hands back control, **read `.claude/audit-report.md`**. The Decisions section is the source of truth for what to apply in Steps 2–3.

### Audit verdicts that change the flow

- **Recommended action: `ABORT-AND-ROLLBACK`** — typically when a pre-flight is fatal (chart-Go drift) or a Break has no acceptable resolution. Default behavior: do NOT proceed to Step 2. Roll back if a previous run already bumped `go.mod`: `go get github.com/wandb/operator@<previous-pin> && go mod tidy`. Tell the user the report path and stop.
- **Recommended action: `WAIT-FOR-UPSTREAM`** — same default: do not proceed. Surface the upstream dependencies (e.g. "operator team needs to re-push chart at tag X") and stop.
- **Recommended action: `INFORMATIONAL-ONLY`** — no fixes selected. Stop.
- **Recommended action: `PROCEED-TO-SYNC`** — continue to Step 2.

#### Override path on ABORT / WAIT-FOR-UPSTREAM

The user can legitimately want to stage the wsm-side bump even when the audit recommends abort — so that when the upstream gap closes (chart republish, missing field added, etc.) no wsm work blocks the release. Recognize this when the user explicitly says "proceed anyway", "override P1", "stage the wsm side", or picks an `AskUserQuestion` option that says so. In that case:

- Apply the approved M+C fix list normally (Step 2).
- **Skip Step 4c smoke test** — it will fail at the known upstream gap, producing no new signal. Run Step 4b server-side dry-run if useful; document the known-broken runtime gap in the PR description instead.
- Do NOT roll back `go.mod`. The whole point of the override is to land the wsm-side bump.
- **Tag every fix whose runtime verification was blocked as `UNVERIFIED`** (matches the audit report's per-Break `Verification:` field). A B-fix like an OIDC strip that depends on the blocked 4b/4c is staged on static reasoning only — the static build proves it compiles, not that the CRD/operator accept it. List the `UNVERIFIED` fixes explicitly in the PR description so a reviewer knows what still needs a real-cluster pass once the upstream gap closes.
- The PR description should call out the upstream dependency and what unblocks the runtime side (e.g. "blocked by operator chart republish at tag X — wsm side ready"), plus the `UNVERIFIED` fix list and the audit's full-surface capability ledger (so deferred gaps are visible, not silently dropped).

## Step 2: Apply the approved fix list

Iterate the Decisions block in `.claude/audit-report.md`. For each approved finding:

### Bump operator dep (always — implied by any non-INFORMATIONAL run)

```bash
go get github.com/wandb/operator@<target-ref>
go mod tidy
```

**Baseline build before any edits (mandatory).** Immediately after `go get`/`go mod tidy`, run `go build -tags=containers_image_openpgp ./cmd/wsm ./pkg/operator` *before touching a line of wsm source*. This isolates dep-bump breaks (operator renamed/removed a type wsm references → compile error now) from fix-induced breaks (errors you introduce later). A clean baseline build also positively confirms the audit's "all `v2.*` types survive" verified-safe claim. If the baseline build fails, the failing symbols are exactly the mechanical-rename Breaks — fix those first.

### Resolution of 🚫 Breaks

Use the resolution the user selected for each Break finding (e.g. `B1=C`). Per-finding edits live in the audit report. Typical patterns:

- **CR field renamed/restructured**: mechanical rename in `cmd/wsm/deploy_v2.go:47-96` (template) and `:897-1021` (`processWandbCR`). Driven by the compiler — `go build ./cmd/wsm` reports every stale construction.
- **Zero-value serialization trap** (OIDC-style): minimal Unstructured strip in `pkg/operator/operator.go:ApplyCR` if the user picked the wsm-side workaround. Document the upstream dependency that allows the workaround to be removed.

### Resolution of ⚠️ Mismatches

Apply the fix recipe from the audit report. Common edit sites:

- Flag descriptions / defaults: `cmd/wsm/deploy_v2.go:115-134` (`DeployV2Cmd().PersistentFlags()`).
- Flag value validation: in `processWandbCR` (`cmd/wsm/deploy_v2.go:897-1021`), `operatorDeployCmd().RunE`, or `wandbCreateCmd().RunE`.
- Chart values wsm sets: `pkg/operator/operator.go:DeployOperator` `releaseValues` map.

### Resolution of ➕ Missing capabilities

When approved (`add`):

1. Register the new flag in `DeployV2Cmd()` (~line 115).
2. Read it in `wandbCreateCmd().RunE` (~line 250) and `operatorDeployCmd().RunE` (~line 331).
3. Plumb through `processWandbCR` signature (~line 897), `performDeploy` signature (~line 429), and `operator.DeployOperator` signature in `pkg/operator/operator.go` (~line 535).
4. Set the corresponding `wandbCR.Spec.<...>` field or `releaseValues["..."]` key.

#### Pattern: new `corev1.SecretKeySelector` field on the CR

When the operator adds a new SecretKeySelector-typed field (or a struct of them, like `OidcSpec`), expose **one flag per leaf field** taking `<secret-name>:<key>` form. Do **not** collapse into a single `--<feature>-secret <name>` flag that assumes conventional keys — pre-existing secrets in real clusters rarely conform to wsm's chosen key names, and forcing rename is operational friction. See `feedback_secret_key_selector_flags.md` in user memory for the why.

- Flag naming: after the leaf field (`--oidc-client-id`, `--smtp-username`), not the parent struct (`--oidc-secret`).
- Value format: `<secret-name>:<key>`, parsed via `strings.Cut(value, ":")`. Empty value = leave the SecretKeySelector zero — the existing `ApplyCR` strip handles serialization.
- No separate `--<feature>-file` flag — `--cr-file` is already the escape hatch for users with complex CR shapes.
- Canonical example: `cmd/wsm/deploy_v2.go` `processWandbCR` `parseSecretRef` helper + `oidcRefs` slice.

### Drive cleanup with the compiler

After each round of edits: `go build -tags=containers_image_openpgp ./cmd/wsm` (the tag avoids the `pkg-config`/`gpgme` system dep that `make build` doesn't gate). Iterate until clean. Operator-side renames cascade — don't declare done after the first clean build mid-edit.

## Step 3: Update user-facing docs

For every flag whose name, default, or description changed (per Decisions), update:

- `docs/reference/commands.md` — flag table for the affected command.
- `docs/reference/cr-fields.md` — if a CR field reference changed.

Diff each affected docs section against the new flag set before editing.

**Cross-check enums/defaults on adjacent flags too, not just the changed ones.** While editing a flag table, verify the *documented* enum/default of neighboring rows still matches the code — pre-existing doc bugs cluster in the same table. (This run found `--observability-mode` documented as `off|on` when the real enum is `off|full|forward` — an unrelated stale doc fixed opportunistically.) Fixing an adjacent doc error you're already looking at is in scope; chasing doc bugs in unrelated files is not.

## Step 4: Verify

### 4a. Static checks (mandatory)

`make fmt` is safe. `make lint` runs `go vet` without the `containers_image_openpgp` tag and will fail locally with `pkg-config: executable file not found` unless `libgpgme-dev` is installed (CI installs it). To verify locally:

```bash
make fmt
go vet -tags=containers_image_openpgp ./...
GOGC=off golangci-lint run --build-tags=containers_image_openpgp --timeout=5m
go build -tags=containers_image_openpgp ./cmd/wsm
```

Pre-existing lint findings unrelated to the operator bump (e.g. unchecked `resp.Body.Close()`, deprecated `certmanagermetav1.ObjectReference`) are out of scope — note them in the PR description, don't fix.

### 4b. Server-side CR dry-run (cheap, catches schema mismatches the typed compile misses)

Before launching the full smoke test, dry-run the CR shape against the cluster's installed CRD. This catches OIDC-style zero-value serialization traps and other CRD-vs-Go mismatches without spinning up the operator pod.

```bash
# After Step 4c's `wsm cluster create` and `wsm deploy-v2 operator` (without --include-cr),
# probe with a representative CR YAML:
kubectl --context kind-wsm-sync-test apply --server-side --dry-run=server -f <(cat <<'EOF'
apiVersion: apps.wandb.com/v2
kind: WeightsAndBiases
metadata: {name: wandb-probe, namespace: wandb}
spec:
  size: small
  retentionPolicy: {onDelete: detach}
  wandb: {hostname: "http://localhost:8080", version: "<defaultWandbVersion>", features: {}, internalServiceAuth: {enabled: false}}
  mysql:       {managedMysql:       {telemetry: {enabled: false}}}
  redis:       {managedRedis:       {telemetry: {enabled: false}}}
  kafka:       {managedKafka:       {telemetry: {enabled: false}}}
  objectStore: {managedObjectStore: {telemetry: {enabled: false}}}
  clickhouse:  {managedClickhouse:  {telemetry: {enabled: false}}}
EOF
)
```

A `serverside-applied (server dry run)` response means the CRD accepts wsm's shape. Failures here = additional findings missed by the audit; loop back to Step 2.

### 4c. Kind smoke test (catches runtime gaps Step 4b can't predict)

```bash
./wsm cluster create --cluster-name wsm-sync-test --workers 0 --http-port 18080 --https-port 18443
./wsm deploy-v2 operator --context kind-wsm-sync-test --include-cr
```

**Do not pass `--wait`** — the flag is declared but commented out in `cmd/wsm/deploy_v2.go` (TODO note), so it errors `unknown flag: --wait`. Poll manually:

```bash
kubectl --context kind-wsm-sync-test -n wandb get weightsandbiases wandb \
  -o jsonpath='{.status.ready}' --watch
```

Watch for `true`. If the install times out on a pre-install hook (`context deadline exceeded` on a cold cluster — chart has many sub-chart hooks), that's an environmental timeout, not a regression. Retry with cached images:

```bash
helm --kube-context kind-wsm-sync-test -n wandb-operators uninstall wandb-operator
./wsm deploy-v2 operator --context kind-wsm-sync-test --include-cr
```

### 4d. Diagnostic decision tree on failure

| Failure | Likely cause |
|---|---|
| `unknown flag: --wait` | The skill's command, not wsm. Drop `--wait`. |
| `failed pre-install: ... context deadline exceeded` | Helm timeout vs slow image pulls. Retry once cached. |
| Operator pod never Ready, `WaitForOperator` hangs | Webhook config rename (audit's Category E) — confirm `wandb-operator-mutating-webhook-configuration` name in `pkg/operator/operator.go:649`. |
| `ApplyCR` rejected with `missing required field` | Audit missed an added required field; loop back to Step 1. |
| `ApplyCR` rejected with `unknown field` or `expected X, got Y` | Audit missed a CRD shape change (often zero-value serialization). Loop back. |
| `failed calling webhook ... connection refused` | Webhook not ready; should be caught by `WaitForOperator`. Check pod logs in `wandb-operators` namespace. |
| Operator pod runs but CR `status.ready` never flips | Reconcile failure. `kubectl -n wandb-operators logs -l app.kubernetes.io/name=wandb-operator` — often chart-Go RBAC drift (the audit should have caught this as a pre-flight). |

### 4e. Cleanup

```bash
./wsm cluster cleanup --context kind-wsm-sync-test
./wsm cluster destroy --cluster-name wsm-sync-test
```

## v1 path (only when user explicitly asks)

v1 (`wsm deploy`, `wsm list`, `wsm download`, `wsm console`) is in maintenance mode. If a v1 change is requested, defer to the "v1 update playbook" table in CLAUDE.md. Edit sites: `cmd/wsm/list.go`, `cmd/wsm/download.go`, `pkg/crd/weightsandbiases.go`, `pkg/spec/spec.go`, `pkg/helm/consts.go`, `pkg/deployer/deployer.go`. Verify with `./wsm list` (no cluster needed). The audit skill does not currently cover v1.

## What this skill does NOT do

- No analysis or classification of operator changes — that's `/audit-operator-diff`'s job.
- No new packages, build-time flags, or abstractions.
- No refactor of the typed CR template.
- No automatic PR creation — leave the working tree clean for review.
- No consolidation of helm v3/v4 imports.
- No edits to v1 surface unless explicitly requested.

## Failure modes

- **Audit recommends ABORT:** stop here. Restore go.mod if needed. Surface the upstream blockers.
- **Build cascades:** rerun after each fix round; operator changes routinely surface more compile errors after the first.
- **`go mod tidy` adds unexpected indirect deps:** define "unexpected" so you know when to stop. A new **top-level / direct** module, or a **major-version** jump of any module → stop, `git diff go.mod go.sum`, and ask the user. **Patch/minor bumps of already-present transitive deps** (e.g. `k8s.io/*`, `go-openapi/*` moving a patch level — common, since the operator bump drags its own dep graph) → expected; note the count in the PR description and proceed without asking. When unsure, show the `go.mod` (not `go.sum`) diff — it's the signal; `go.sum` churn is noise.
- **Smoke test fails with a category not in 4d:** treat as new audit finding; loop back to Step 1 with the new evidence.
- **Docs already in sync:** skip Step 3 edits, report "docs already match".
