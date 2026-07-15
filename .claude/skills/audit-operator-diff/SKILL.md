---
name: audit-operator-diff
description: Audit changes between two github.com/wandb/operator refs and produce a categorized impact report on wsm. Use independently for code review / release planning, or as Step 1 of /sync-operator. Triggers on phrases like "audit operator diff", "what changed in operator", "show me operator impact on wsm", "operator compatibility audit". Does NOT make code changes — produces a fix list at .claude/audit-report.md that the user (or /sync-operator) consumes.
allowed-tools:
  # Operator clone reads — matches any path containing "operator"
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
  # Read-only text processing (coverage-ledger classifier, struct dumps)
  - Bash(python3:*)
  - Bash(awk:*)
  - Bash(sed:*)
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
---

# Audit operator diff for wsm impact

Goal: enumerate every change between two operator refs and classify each by its impact on wsm. Output: a structured Markdown report at `.claude/audit-report.md` with user-approved decisions, ready for `/sync-operator` to consume. **Never edits wsm source files** — audit-only.

## Precision mandate (read first)

The report is a forensic record, not a summary. Another agent or engineer must be able to reconstruct *what changed and to what extent* from the report alone, without re-running the diff. Three non-negotiable rules:

1. **Total file coverage.** Every file in the Step 2 inventory must be accounted for in the report — assigned to a bucket and represented by a finding ID *or* listed in the coverage ledger. Silent omission is a defect. "≈260 files, mostly internal" is not coverage; the internal files must still be enumerated by logical change-group (see Step 6).
2. **Concrete before→after.** Every finding cites `file:line` (or `file` + symbol) **and** the actual values that changed — old key → new key, old type → new type, old default → new default, added enum value, removed `required` entry. Never write "telemetry changed" or "values reworked"; write `scrape.kubeStateMetrics` added (bool, default `true`) — `deploy/telemetry/values.yaml:17`, schema `values.schema.json:69`.
3. **Confirm the negative.** When you check a wsm coupling site (a `releaseValues` key, `telemetry.mode`, the webhook name, a CR field) and find it *unaffected*, record that explicitly in a **✅ Verified-safe couplings** section with the evidence that proves it safe. An unmentioned coupling reads as "not checked," which is indistinguishable from a miss. Asserting the negative is how the reader knows the audit was exhaustive.

These rules apply to *every* bucket — including Telemetry and Internal-only, which are the two most often under-reported.

## Operator source convention (`$OP`)

This skill is shared across the team, so it assumes **nothing** about any particular machine — not that an operator clone exists, and not where it lives. Throughout, **`$OP`** denotes the operator repo location resolved in Step 0. Run every operator `git` command against it (`git -C "$OP" …`); never hardcode `../operator`. Default to the clone-free GitHub path so the audit reproduces identically for any teammate regardless of their working copy.

## Sandbox & read actions (read first)

The `allowed-tools` frontmatter only suppresses **permission prompts** — it does **not** lift the **command sandbox** (network/filesystem isolation). These read-only commands routinely fail under the sandbox and must run unsandboxed (`dangerouslyDisableSandbox: true`); the failure is the sandbox, not the operation:

| Command | Failure under sandbox | Action |
|---|---|---|
| `git -C "$OP" fetch …` | `.git/FETCH_HEAD` write denied (`Operation not permitted`) | retry unsandboxed (or allowlist `$OP/.git` writes) |
| `git clone --filter=blob:none …` | network + `.git` writes denied | retry unsandboxed |
| `helm show chart oci://us-docker.pkg.dev/…` | host not allowlisted (`tls: failed to verify certificate`) | retry unsandboxed; **not** a real OCI outage |

Don't treat the first denial as a real failure (a stale clone, a down registry). Retry unsandboxed once, then continue. To remove the friction persistently, offer the `update-config` skill to add `us-docker.pkg.dev` to the network allowlist and `$OP/.git` to the filesystem-write allowlist in `.claude/settings.local.json` (note: the go-proxy hosts are already allowlisted but still break on the sandbox's TLS interception, so `go` commands — used by `/sync-operator`, not here — must run unsandboxed regardless).

## Step 0: Prerequisites & operator source

1. **Resolve the operator source — ask first.** The diff needs git history of `github.com/wandb/operator` at two refs; where that history comes from is a per-machine detail, so decide it explicitly with `AskUserQuestion`:
   - **GitHub upstream (no local clone needed)** — the portable default. Make a fresh blobless clone to a temp dir so every branch/tag resolves and the result is independent of anyone's working copy:
     ```bash
     git clone --filter=blob:none https://github.com/wandb/operator "$TMPDIR/operator"
     ```
     Then `OP="$TMPDIR/operator"`. (Network + `.git` writes are sandboxed; if the clone fails on a sandbox denial, retry with `dangerouslyDisableSandbox: true`. The first time the command runs it may also prompt for permission — that's expected.)
   - **Existing local clone** — faster and offline, but path and freshness vary per machine, so it can be stale or parked on a feature branch. Resolve the path in priority order — `$WANDB_OPERATOR_DIR` if set, otherwise ask the user — then set `OP` to it and verify `test -f "$OP/api/v2/weightsandbiases_types.go"`. Report its `branch`/`HEAD` and offer to `git -C "$OP" fetch origin --tags --prune` so the target refs are current before diffing.

   Record the resolved choice in the report header `- **Diff source:**` line (e.g. `GitHub upstream, cloned <sha>` or `local clone <path> @ <sha>, fetched <date>`).

2. **Working tree state** — `git -C . status --porcelain` (wsm) and `git -C "$OP" status --porcelain` (operator).
   - Dirty `$OP` (only possible for a local clone): warn the user; the named refs are still authoritative for the diff.
   - Dirty wsm tree: check whether the dirty state is **an in-progress sync from a previous run**. Heuristic: `git diff HEAD -- go.mod | grep -E '^[-+].*github.com/wandb/operator'` shows a pin change AND any of `cmd/wsm/deploy_v2.go`, `pkg/operator/operator.go` are modified AND/OR `.claude/audit-report.md` exists from a prior run. In that case, **treat the dirty pin as the implicit target** (see Step 1.2) and do not ask the user to stash. Otherwise warn that uncommitted wsm edits could confuse the cross-reference in Step 4.

## Step 1: Resolve refs

1. Read **committed** pin from wsm's `go.mod` (always `git show HEAD:go.mod`, never the working-tree file — the working tree may already be bumped by a prior incomplete sync):
   ```bash
   git show HEAD:go.mod | grep 'github.com/wandb/operator '
   ```
   Parse the version string. Decode pseudo-versions (`v1.21.3-0.20260424225346-b25b977174fd`) to commit hashes (here `b25b977174fd`); stable tags (`v1.2.3`) map directly. This is the audit's "from" ref.

2. **Pick the target ref.** Two paths:
   - **In-progress sync detected in Step 0** — read the bumped pin from the working-tree `go.mod` and default the target to that SHA. Confirm with the user in one line ("Auditing from committed pin `<old>` to in-progress bump `<new>`?") rather than asking open-ended.
   - **Clean tree, no in-progress sync** — ask the user. Typical answers: a branch HEAD (`v2`, `main`), a tag (`v1.22.0`), or a specific commit hash. List candidate refs (works for both source types — remote-tracking refs and tags in the temp clone):
     ```bash
     git -C "$OP" for-each-ref --sort=-creatordate \
       --format='%(refname:short)  %(creatordate:short)' refs/remotes refs/tags | head -15
     ```

3. Resolve both refs to commit SHAs and confirm they're reachable: `git -C "$OP" rev-parse <ref>` for each.

## Step 2: File-level diff inventory

```bash
git -C "$OP" diff <current>..<target> --name-status | sort
git -C "$OP" diff <current>..<target> --stat | tail -1   # total file/insertion/deletion count for the ledger
```

Assign **every** file to exactly one bucket. Build a **coverage ledger** as you go (bucket → file count → which files / finding IDs) — this is reproduced verbatim in the report (Step 7) and is the proof of total coverage. Do not let any file fall through; if a path doesn't obviously fit a bucket, default it to Operator internals and note it. Renames (`R###`) count as one file and must record both old and new path.

**Canonical classifier (test-first, then path-prefix — avoids double-counting).** Tests live in many dirs, so classify `*_test.go` *first*, then by prefix; this guarantees mutually-exclusive buckets that sum to the total on the first pass:
```bash
git -C "$OP" diff <from>..<to> --name-status | awk -F'\t' '{print $NF}' > /tmp/op_paths.txt
python3 - /tmp/op_paths.txt <<'PY'
import sys, re
def bucket(p):
    if p.endswith("_test.go"): return "tests"
    if p.startswith("api/v1/"): return "api_v1"
    if re.match(r"api/v2/.*\.go$", p): return "go_api"
    if p.startswith("config/crd/bases/"): return "crd"
    if p.startswith("config/rbac/"): return "rbac"
    if p.startswith("deploy/operator/"): return "op_chart"
    if p.startswith("deploy/telemetry/"): return "telemetry"
    if p.startswith("deploy/"): return "deploy_other"
    if p.startswith("hack/"): return "hack"
    if p.startswith("docs/"): return "docs"
    return "internals"   # internal/, pkg/, cmd/, config/* non-crd/rbac
b = {}
for line in open(sys.argv[1]):
    p = line.strip()
    if p: b.setdefault(bucket(p), []).append(p)
tot = 0
for k in sorted(b, key=lambda k: -len(b[k])):
    print(f"{k}: {len(b[k])}"); tot += len(b[k])
print("TOTAL:", tot)   # must equal `git diff --name-only | wc -l`
PY
```
For renames, `--name-status` emits the new path in `$NF` (one file). Internals (`internal/`, `pkg/vendored`, `cmd/`, `config/` non-crd/rbac) still get logical change-group enumeration per the Precision mandate.

Categorize each changed file into one of:

| Bucket | Path prefix | Treatment |
|---|---|---|
| **Go API** (typed CR) | `api/v2/*.go` | Diff per-type; flag non-pointer struct fields with `omitempty` (zero-value traps) |
| **CRD schema** | `config/crd/bases/*.yaml` | Diff for added/removed required fields, type changes, new CEL/enum rules |
| **Operator chart structure** | `deploy/operator/Chart.yaml`, `Chart.lock`, `values.yaml`, `values.schema.json` | Diff subchart deps, top-level keys wsm sets in `releaseValues`, new toggles |
| **Operator chart templates** | `deploy/operator/templates/*.yaml` | grep for `eq $mode "X"` / validation rules — enum-value drift |
| **Operator chart profiles** | `deploy/operator/profiles/*.yaml` | Reference configs that enumerate valid value combinations |
| **RBAC** | `config/rbac/role.yaml` | RBAC drift indicates chart-Go split (Moco-style break) |
| **Telemetry subchart** | `deploy/telemetry/*` **and** the `telemetry:` block of `deploy/operator/values.yaml` | Diff EVERY file: `values.yaml`, `values.schema.json`, each `templates/*.yaml`, each `dashboards/*.json`. Enumerate every added/removed/renamed key with its default; cross-ref `telemetry.mode` enum against wsm. wsm sets `telemetry.mode`, so this bucket is wsm-coupled — never treat as background. |
| **Operator internals** | `internal/**`, `pkg/vendored/**`, `cmd/main.go`, `cmd/manager/**` | Do not analyze per-file wsm-impact, but DO enumerate by logical change-group (e.g. "MySQL reconciler mysql-operator→moco: N files under `internal/.../mysql/moco/`"). Each group = one `I<n>` with a concrete one-line description and representative file path. Vendored CRD/type swaps (minio→seaweedfs, mysql-operator→moco) are the usual drivers of a P-tier chart-Go break — always tie them back to the relevant P finding. |
| **Hack / docs / tests** | `hack/**`, `docs/**`, `*_test.go`, `Dockerfile`, `.gitignore` | Skip (informational only) |
| **v1 conversion** | `api/v1/*.go` | Skip — wsm always sends v2 |

## Step 3: Run the targeted checks per bucket

The Precision mandate applies to **every** bucket equally — telemetry was only the example that exposed a miss; it is not special. For each bucket: diff every file in it, and enumerate *every* change with concrete before→after, not a representative sample. The per-bucket notes below say *what kind* of change matters most in each, but they never license skimming. Run `git -C "$OP" diff <from>..<to> -- <bucket-paths>` for the whole bucket and account for each hunk — an unmentioned change is a coverage gap, the same way an unlisted file is. The Telemetry subsection shows the depth expected; replicate that depth here for Go API, CRD, chart, RBAC, and internals.

### Go API
- Diff the whole file: `git -C "$OP" diff <from>..<to> -- api/v2/*.go`.
- For **every** modified `type X struct`: enumerate each field change (added / removed / type change / JSON-tag change) with concrete before→after (e.g. `OidcSpec.ClientId: string → corev1.SecretKeySelector`; `*Connection.URL` json tag `URL` → `url,omitempty`). Do not stop at the first interesting type.

- **Systematic zero-value / `SecretKeySelector` trap sweep (mandatory — do not check just the one flagged field).** The OIDC trap is a *pattern*, not a one-off: any non-pointer struct field declared `<Field> <StructType> `json:"<name>,omitempty"`` is a candidate, because Go's `omitempty` does NOT drop a zero-value struct (only nil pointers, empty slices/maps/strings — confirmed: the k8s `DefaultUnstructuredConverter.isZero` returns `false` for `reflect.Struct`). A zero `corev1.SecretKeySelector` serializes as `{"key":""}` (its `Key` field has no `omitempty`), so a by-value wrapper emits a non-empty object the operator may reject or mis-resolve. Run the sweep every time:
  1. Enumerate **every** struct in the target API that contains a `SecretKeySelector` (or any nested no-`omitempty` field):
     ```bash
     git -C "$OP" show <to>:api/v2/weightsandbiases_types.go | grep -nE 'corev1\.SecretKeySelector|^type .* struct'
     ```
  2. For **each** such struct, trace how wsm's CR reaches it and classify — record the verdict for all three classes:
     - **By-value `omitempty` on a path wsm builds** (e.g. `WandbAppSpec.OIDC OidcSpec`) → **TRAP**. wsm emits `{"key":""}` whether or not it sets the field. Candidate 🚫 B (strip in `ApplyCR`, or per-leaf flags — see sync skill).
     - **Reached only via a `*pointer` field** (e.g. `MySQLSpec.ExternalMysql *MysqlConnection`) → **nil-safe**, `omitempty` drops it on wsm's managed-only path. Record in ✅ Verified-safe with the pointer evidence (NOT "operator-generated" — the real reason is pointer-gating).
     - **`.status` / operator-output only** → not sent on apply.
  3. Also sweep the managed `*Config` / `ManagedInfraSpec` / `Telemetry` structs wsm builds **by value** for any nested no-`omitempty` field, not just `SecretKeySelector`.
  - Cross-reference each candidate against wsm's `cmd/wsm/deploy_v2.go:wandbCR` template and `processWandbCR`. A `SecretKeySelector`-bearing struct that is neither a B finding nor a ✅ Verified-safe line is a coverage gap — every one must be accounted for.

### CRD schema
- Diff the full file: `git -C "$OP" diff <from>..<to> -- config/crd/bases/*.yaml`. Account for every hunk.
- Enumerate **every** added/removed `required:` entry (each can reject or relax a CR that previously validated — note which direction).
- Enumerate **every** `+kubebuilder:validation:Enum=...` / `enum:` change (tightening *or* widening) with the old and new value sets.
- Enumerate **every** `type:` change (e.g. `string` → `object`) and new property block, with the path.
- For each, state whether wsm's CR is affected, and put the unaffected ones in ✅ Verified-safe couplings (e.g. "removed `required: -URL` relaxes the schema; wsm never sent URL → safe").

### Chart structure
- Diff `dependencies:` in `Chart.yaml` and `Chart.lock`. List **every** dep added / removed / renamed / version-bumped with old→new (name + version + repository). If a dep was renamed (e.g. `mysql-operator 2.1.10` → `moco 0.24.0`), check the chart `version:` field too — **if the chart version string was NOT bumped, the OCI tag is mutable and runtime is unpredictable** (P-tier; see Step 5).
- **Early-gate trigger:** the moment this bucket shows a dep rename/add/remove **or** `config/rbac/role.yaml` (RBAC) changed, run **Step 5 (OCI freshness) immediately** — before the deep per-bucket dive. If the published OCI chart is frozen, most downstream findings become "staged-only" and the user should learn that up front, not at the end. The OCI drift check is the single most decision-determining probe in the audit; do not leave it for last when these signals are present.
- Diff **all** top-level keys in `values.yaml` (added/removed/renamed, each with old→new) and `values.schema.json`. Cross-reference with `pkg/operator/operator.go:DeployOperator` `releaseValues` (currently sets `wandb.install`, `wandb-operator.image.pullPolicy`, `telemetry.mode`). Flag any key wsm sets that no longer exists (🚫 B); record the keys wsm sets that still exist in ✅ Verified-safe.
- **Operator image tag, not just chart version.** The deployed *binary* is `wandb-operator.image.{repository,tag}` in `deploy/operator/values.yaml`, separate from the chart `version:`. Record both at `<to>` (`git -C "$OP" show <to>:deploy/operator/values.yaml | grep -A4 '^wandb-operator:'`). If the image tag is unchanged while the binary (Go API / RBAC / reconcilers) changed, the published image is frozen too — reinforces the P-tier chart-Go drift. A chart-version bump with a stale image tag (or vice-versa) is itself a drift signal.

### Chart templates
- Diff every modified template: `for f in $(git -C "$OP" diff <from>..<to> --name-only -- deploy/operator/templates/); do echo "=== $f ==="; git -C "$OP" diff <from>..<to> -- "$f"; done`.
- Enumerate every Helm validation rule change — `eq $.. "..."`, `case`, `fail`, `required` blocks — with old→new accepted values; cross-reference enum values with wsm's flag-accepted values.
- Always inspect by name and report what changed (even if "no semantic change"):
  - `deploy/operator/templates/telemetry-validation.yaml` — telemetry mode enum
  - `deploy/operator/templates/*-role.yaml` — RBAC scope changes (renames like `*-minio-role` → `*-seaweedfs-role` tie to the P-tier chart-Go break)
  - `deploy/operator/templates/webhooks.yaml` — the `MutatingWebhookConfiguration` name `WaitForOperator` depends on
  - `deploy/operator/templates/hooks/*` — crd-installer / helm-hook changes (interact with wsm's `WaitStrategy="hookOnly"`)

### RBAC
- Diff `config/rbac/role.yaml` fully and enumerate **every** apiGroup/resource/verb added or removed with old→new (e.g. `mysql.oracle.com` removed, `moco.cybozu.com`/`mysqlclusters` added; `minio.min.io` removed, `seaweed.seaweedfs.com`/`seaweeds` added).
- Any resource-group change is a **chart-Go mismatch indicator**: the operator binary needs the new RBAC, but the chart at the current OCI tag may still grant the old. Cross-check against Step 5 — if `role.yaml` changed but the chart `version:` didn't bump, this is a fatal P-tier finding regardless of any wsm code fix.

### Telemetry subchart

This is the worked example of the per-bucket depth every bucket above must match — not a uniquely-detailed bucket. wsm sets `telemetry.mode` in `releaseValues`, so it is also directly wsm-coupled. Run all of:

```bash
# operator chart's top-level telemetry block (the mode enum wsm depends on)
git -C "$OP" diff <from>..<to> -- deploy/operator/values.yaml | grep -A30 'telemetry:'
git -C "$OP" show <to>:deploy/operator/values.yaml | grep -A20 '^telemetry:'   # confirm mode comment block: off/full/forward
# telemetry subchart — diff EVERY file, not just values
git -C "$OP" diff <from>..<to> -- deploy/telemetry/values.yaml
git -C "$OP" diff <from>..<to> -- deploy/telemetry/values.schema.json
git -C "$OP" diff <from>..<to> --stat -- deploy/telemetry/templates/ deploy/telemetry/dashboards/
for f in $(git -C "$OP" diff <from>..<to> --name-only -- deploy/telemetry/templates/); do echo "=== $f ==="; git -C "$OP" diff <from>..<to> -- "$f"; done
```

For each result, record concretely:
- **Every added/removed/renamed key** in `values.yaml` + `values.schema.json` with its type and default (e.g. `scrape.kubeStateMetrics: true` added). New optional keys are candidate ➕ C findings; removed keys wsm sets are 🚫 B.
- **The `telemetry.mode` enum** (`off` / `full` / `forward`). Cross-reference with wsm's `releaseValues["telemetry"]["mode"]` value and any wsm flag that feeds it. If the enum is unchanged, say so in **✅ Verified-safe couplings**; if a mode was added/removed/renamed, it's an M or B.
- **Template reworks** — scrape-target selector/namespace changes, new `VMServiceScrape`/`VMPodScrape` objects (e.g. kube-state-metrics, seaweedfs replacing minio), honorLabels changes. These are usually I-tier (internal scrape wiring), but enumerate each as a one-liner; a scrape renamed minio→seaweedfs ties back to the P-tier chart-Go break.
- **Dashboard JSON** add/modify — I-tier, but list each file by name and whether added or modified.

## Step 4: Cross-reference findings with wsm

For each operator change identified in Step 3, find the wsm coupling site:

| Operator change | wsm file/line to check |
|---|---|
| New CR field on `WandbAppSpec`/`*Spec` | `cmd/wsm/deploy_v2.go:47-96` (template), `:897-1021` (`processWandbCR`) |
| Renamed CR field | Same as above + grep `wandbCR.Spec.<...>` across `cmd/wsm/` |
| New CLI flag needed | `cmd/wsm/deploy_v2.go:115-134` (`DeployV2Cmd().PersistentFlags()`) |
| Chart values key wsm sets | `pkg/operator/operator.go:571-583` (`releaseValues` in `DeployOperator`) |
| Operator chart OCI repo / name | `pkg/operator/operator.go:542-545` |
| Webhook config name | `pkg/operator/operator.go:649` |
| Telemetry mode value | `pkg/operator/operator.go` `releaseValues["telemetry"]` + any feeding flag |
| Default version constants | `cmd/wsm/deploy_v2.go:34` (`defaultWandbVersion`), `:419` (`--operator-chart-version` default) |

**Reconcile `defaultWandbVersion`.** CLAUDE.md calls `cmd/wsm/deploy_v2.go:34 defaultWandbVersion` "hand-bumped per release" but neither audit nor sync verifies it. Compare wsm's `defaultWandbVersion` against the chart's `wandb.version` default at `<to>` (`git -C "$OP" show <to>:deploy/operator/values.yaml | grep -A3 '^wandb:'`). If they diverge, flag it (⚠️ M) with both values — wsm may be defaulting to a server manifest version the matched operator/chart doesn't expect. If they agree (or wsm intentionally pins newer), record it in ✅ Verified-safe with both values.

**Confirm the negative.** Walk this whole table every run, even when nothing changed. For each coupling site that you checked and found *unaffected*, add a line to the report's **✅ Verified-safe couplings** section stating what you verified and the evidence (e.g. "wsm `releaseValues` keys `wandb.*`, `wandb-operator.image.pullPolicy`, `telemetry.mode` all still present in `<to>:deploy/operator/values.yaml` — no break"; "all 22 `v2.*` type names wsm references survive at `<to>` — `grep -rohE 'v2\.[A-Z][A-Za-z0-9]+' cmd/ pkg/` cross-checked against the `api/v2` diff"). A coupling that is neither a finding nor a verified-safe line is a coverage gap.

## Step 4.5: Parity validation gate (operator ↔ wsm completeness — mandatory)

The audit must **prove** it reconciled the operator surface against wsm on its own — the reader should never have to push back with "are you sure you got everything." Two reconciliations, both required, both written into the report:

1. **Diff-range completeness (the coverage gate, Step 6)** — every changed file is bucketed and every hunk is a finding or verified-safe. This proves nothing was *missed in the diff*.

2. **Full-surface capability ledger (new, mandatory)** — enumerate the **entire** `WeightsAndBiasesSpec` tree at `<to>` (not just the diff range) and mark each field's wsm exposure. This proves wsm parity is *understood*, and surfaces pre-existing gaps proactively instead of reactively.
   ```bash
   # Dump the full spec tree to enumerate every field
   git -C "$OP" show <to>:api/v2/weightsandbiases_types.go | sed -n '/type WeightsAndBiasesSpec struct/,/^}/p'
   # …then recurse into WandbAppSpec, NetworkingSpec, each *Spec / Managed*Spec / *Config
   ```
   For every leaf field, assign one status:
   - **✅ exposed** — wsm sets it (flag or template). Cite the flag / template line.
   - **➕ new-this-diff** — added in `<from>..<to>`; this is a C finding (in scope).
   - **⬜ gap → `--cr-file`** — operator supports it, wsm has no flag, pre-existing. Out of *fix* scope, but **must still be listed** so the user sees the full picture without asking.

   Write this as a `## Full-surface capability ledger` section in the report (table: CR field → status → wsm site / note). The C-bucket fix list stays diff-range-scoped; this ledger is the *visibility* mechanism, not a fix list. If the ledger is large, group by spec (Wandb / Networking / each infra) — but enumerate every field; "mostly covered" is not a ledger.

This gate is the structural answer to "did you reconcile *everything*": the SKS sweep (Step 3) covers secret-bearing correctness, the coverage gate covers the diff, and this ledger covers full-surface capability. All three are mandatory.

## Step 5: Check chart vs Go-API drift (OCI freshness)

> Run this **as soon as** Step 3's Chart-structure or RBAC bucket shows a dep rename/add/remove or `role.yaml` change (see the early-gate trigger there) — not only at this position. Its verdict reorders the whole audit.

```bash
helm show chart oci://us-docker.pkg.dev/wandb-production/public/wandb/charts/operator \
  --version <wsm-default-chart-version>
```

**Sandbox note:** `us-docker.pkg.dev` is typically outside the default network allowlist. Expect the first attempt to fail with `tls: failed to verify certificate` or similar; retry with `dangerouslyDisableSandbox: true`. Don't treat the first failure as a real OCI outage.

Compare its `dependencies:` against `$OP/deploy/operator/Chart.yaml` at `<target>` ref (`git -C "$OP" show <target>:deploy/operator/Chart.yaml`). If they differ AND the chart `version:` string didn't bump → **fatal pre-flight condition**. Note the OCI manifest digest in the report for traceability.

Also: if `config/rbac/role.yaml` changed but the chart `version:` didn't, surface this prominently — runtime will fail regardless of any wsm code fix.

## Step 5.5: Check for removable wsm-side workarounds

When prior syncs added wsm-side strips/workarounds (e.g. `pkg/operator/operator.go:ApplyCR` removes zero-value OIDC or `.status`), the audit should look for whether the **upstream condition that justified each workaround has resolved**:

- **OIDC strip** is removable when either:
  - `WandbAppSpec.OIDC` in the new operator types is `*OidcSpec` (pointer), so Go's `omitempty` correctly drops the nil; the strip's `== v2.OidcSpec{}` comparison won't even compile.
  - The OCI chart at the deployed tag has been re-published with the new `SecretKeySelector`-typed OIDC CRD (so wsm sending zero-value `{"key":""}` validates).
- **Status strip** is permanent best practice; flag only if a reviewer wants to revisit.

Surface removable workarounds as ⚠️ Mismatches with a "remove cleanup" recipe.

## Step 6: Classify findings into 5 buckets

Assign each finding a stable ID (`P<n>`, `B<n>`, `M<n>`, `C<n>`, `I<n>`):

1. **🔍 Pre-flight (P)** — independent gates. If any P is fatal, the audit's recommended action is ABORT regardless of other findings.
2. **🚫 Breaks (B)** — wsm fails to compile or deploy without resolution. Per finding: resolution options (e.g. operator-side fix vs wsm workaround vs rollback).
3. **⚠️ Mismatches (M)** — existing wsm drift caught by the audit (flag descriptions wrong, missing enum validation, etc.).
4. **➕ Missing capabilities (C)** — operator added something new in this diff range that wsm doesn't expose. **Scope: only changes within `<current>..<target>`.** Pre-existing gaps are out of the *fix* scope here — but they are NOT invisible: they must all appear in the mandatory `## Full-surface capability ledger` (Step 4.5) so the user sees full parity without asking.
5. **ℹ️ Internal-only (I)** — operator-internal changes, listed for context, no action. Still enumerated by logical change-group per the Precision mandate — never collapsed to "internal refactor."

### Coverage gate (run before writing the report)

Reconcile the finding set against the Step 2 inventory:

```bash
# total files changed
git -C "$OP" diff <from>..<to> --name-only | wc -l
```

Sum the file counts across your coverage-ledger buckets and confirm it equals the total. If they don't match, you dropped files — find them and assign them before proceeding. Every changed file must be either (a) cited by a P/B/M/C/I finding, or (b) present in the coverage-ledger row for a bucket (e.g. "Hack/docs/tests — 41 files, skipped"). The ledger's "skipped" rows are legitimate coverage; an *unlisted* file is not. Do not write the report until the gate balances.

## Step 7: Write the audit report to .claude/audit-report.md

Template:

```markdown
# Operator sync audit

- **Generated:** <ISO timestamp>
- **From:** <current-pin-ref> (<short SHA>)
- **To:** <target-ref> (<short SHA>)
- **Diff source:** <GitHub upstream, cloned <sha> | local clone <path> @ <sha>, fetched <date>>
- **OCI chart probed:** <chart URL>@<version> (digest <sha256>)
- **Diff size:** <N files changed, +X/-Y lines> (`git diff --stat` total)

## Change coverage ledger

Every changed file is accounted for here (the Coverage gate must balance: Σ counts == total files changed).

| Bucket | Files | Finding IDs / disposition |
|---|---|---|
| Go API (`api/v2`) | <n> | B1, C1, I8 |
| CRD schema | <n> | (covered by B1/relaxations) |
| Operator chart structure | <n> | P1 |
| Operator chart templates | <n> | P1, I-roles |
| RBAC | <n> | P1 |
| Telemetry | <n> | C-ksm, I-scrapes |
| Operator internals (`internal/`, `pkg/vendored/`, `cmd/`) | <n> | I1–I6 |
| Hack / docs / tests | <n> | skipped (informational) |
| v1 conversion (`api/v1`) | <n> | skipped (wsm always v2) |
| **Total** | **<N>** | == `git diff --name-only \| wc -l` |

## ✅ Verified-safe couplings

Coupling sites checked and confirmed unaffected (proves the negative — see Precision mandate):

- <coupling> — <evidence, e.g. "wsm `releaseValues` keys all present in `<to>:deploy/operator/values.yaml`">
- <coupling> — <evidence>
- <SecretKeySelector sweep result — every SKS-bearing struct classified trap / pointer-safe / status, per Step 3>

## Full-surface capability ledger

Scope note: the ➕ Missing-capabilities fix list below is **diff-range only**. This ledger is the full-surface view (Step 4.5) — every `WeightsAndBiasesSpec` field at `<to>`, so pre-existing gaps are visible without the user asking.

| CR field | Status | wsm site / note |
|---|---|---|
| `spec.size` | ✅ exposed | `--size` |
| `spec.wandb.oidc.*` | ⬜ gap → `--cr-file` | no flag; pre-existing |
| `spec.<field>` | ✅ exposed / ➕ new-this-diff / ⬜ gap | <flag or note> |
| … (enumerate every leaf field) | | |

## 🔍 Pre-flight conditions

[P1] **Chart-Go drift** — <description>. Severity: <fatal | warning>. Resolution: <ABORT | WAIT-FOR-UPSTREAM | acknowledge>.

[continue for each P...]

## 🚫 Breaks
[B1] **<title>**
- Operator-side: `<file>:<line>` (commit `<sha>`) — **before→after:** `<old>` → `<new>`
- wsm-side: `<file>:<line>`
- Why: <one-line root cause>
- Resolution options: A) <option> · B) <option> · C) <option>
- Default if unselected: <option>
- **Verification:** `<how this fix is confirmed>` — tag **`UNVERIFIED`** if runtime proof (Step 4b dry-run / 4c smoke) was blocked (e.g. by a P-tier gate). An `UNVERIFIED` fix is staged on faith; say so explicitly here and in the PR description.

[continue...]

## ⚠️ Mismatches
[M1] **<title>**
- wsm-side: `<file>:<line>` (current state) — **before→after:** `<old>` → `<new>`
- Operator-side expectation: `<file>:<line>` or schema rule
- Fix recipe: <1-2 lines>

[continue...]

## ➕ Missing capabilities (new in this diff range)
[C1] **<title>**
- Operator added: `<file>:<line>` (commit `<sha>`) — **what:** `<concrete field/key/type + default>`
- wsm would need: <flag / CR field / chart value plumbing>
- Edit recipe if approved: <1-2 lines>

[continue...]

## ℹ️ Internal-only

One entry per logical change-group, each with a concrete description and a representative path — never a bare "internal refactor".

- I1: <change-group> — <what changed, concretely> (`<representative file>`, <N files>)
- I2: ...

## Decisions

(Filled in by user via batched approval. Format: comma-separated IDs with optional resolution choice. Examples:
- `B1=C, M1, M2, C1` — abort on B1 via option C, fix M1+M2, add C1
- `all-mismatches, C1` — fix every M, add C1, leave B/P unresolved
- `none` — informational audit only, no fixes
- `abort` — pre-flight fatal, stop here)

Decisions: <FILLED IN>

## Recommended action

<Based on Pre-flight + Breaks + user decisions: PROCEED-TO-SYNC | ABORT-AND-ROLLBACK | WAIT-FOR-UPSTREAM | INFORMATIONAL-ONLY>
```

## Step 8: Present batched approval to user

Print the full report inline (or its summary if long), then collect batched decisions. **Prefer `AskUserQuestion` when the decision space collapses to 2–4 distinct paths** — typical when there's one P-tier fatal whose resolution determines everything else (e.g. "abort", "override and apply M+C fix list", "override and add capabilities"). The structured UI gets clearer responses than free-text. Fall back to free-text only when the report has 5+ independent findings the user might want to mix-and-match.

Accept formats (whether via AskUserQuestion options or free text):

- `all` — fix every M, add every C, resolve every B with its default
- `none` — informational audit, no fix list
- `abort` — pre-flight fatal, stop
- Comma-separated IDs (`B1=C, M1, M2, C1`) — explicit per-item, with `=<choice>` for B items that have options

Update the `## Decisions` section in `.claude/audit-report.md` with the user's response, then set the `## Recommended action` based on the combined state.

## Step 9: Hand off

Print the report path: `.claude/audit-report.md`. Caller (user or `/sync-operator`) reads from here.

If invoked standalone (not via `/sync-operator`), exit. If invoked from `/sync-operator`, control returns to that skill's next step.

## What this skill does NOT do

- No edits to wsm source files. Audit-only.
- No bumping of `go.mod`. That's `/sync-operator`'s job.
- No verification (lint, smoke test). Also `/sync-operator`'s job.
- No fix for the v1 surface. v1 is frozen — see CLAUDE.md's v1 update playbook.
- No *fixing* of pre-existing gaps wsm doesn't expose — the C fix list stays diff-range-scoped (`<current>..<target>`). It DOES, however, **list** every pre-existing gap in the mandatory `## Full-surface capability ledger` (Step 4.5) for visibility; listing ≠ fixing.

## Failure modes

- **Operator clone missing or wrong module:** Step 0 catches; ask user for path.
- **Refs unreachable:** `git rev-parse` will error; surface and ask user to fetch.
- **OCI chart probe times out / 401:** continue audit without the OCI drift check, flag P-tier "OCI freshness unverified" with severity warning.
- **No changes at all between refs:** acceptable — report with all empty buckets, recommended action INFORMATIONAL-ONLY.
- **`.claude/audit-report.md` already exists:** overwrite without prompt; previous report is single-run scratch.
