# Weights & Biases Server Manager

WSM (`wsm`) is a command-line tool that installs and upgrades W&B Server on
Kubernetes. It targets local development, on-prem, and air-gapped environments.

The tool carries two command surfaces:

- **v2 (`deploy-v2`, `cluster`, `registry`, …)** — the actively maintained flow.
  Installs the [wandb/operator](https://github.com/wandb/operator) v2 stack
  (cert-manager, nginx-gateway-fabric, wandb-operator) via Helm v4 + OCI charts,
  can create Kind clusters in-process, and applies a typed `apps.wandb.com/v2`
  `WeightsAndBiases` CR.
- **v1 (`deploy`, `list`, `download`, `console`)** — the legacy air-gapped flow
  built around the deployer channel API and the `apps.wandb.com/v1` CR. Frozen;
  maintained only for existing air-gapped users.

New deployments should use **v2**.

## Install

Requires **Go 1.26+**. Build from source:

```bash
git clone https://github.com/wandb/wsm
cd wsm
make build          # produces ./wsm
./wsm --help
```

Install onto your `PATH` (needs sudo for the default `/usr/local/bin`):

```bash
sudo make install
```

`make build` builds with `CGO_ENABLED=0 -tags containers_image_openpgp`, so no
system libraries are required. (Only if you build **without** that tag do you
need `pkg-config` + `gpgme`: `brew install pkg-config gpgme`.)

## Commands

Full flag reference: [`docs/reference/commands.md`](./docs/reference/commands.md).
CR field reference: [`docs/reference/cr-fields.md`](./docs/reference/cr-fields.md).

| Command | Surface | Purpose |
|---|---|---|
| [`wsm deploy-v2`](#wsm-deploy-v2) | v2 | Install the v2 operator stack and the W&B CR. |
| [`wsm cluster`](#wsm-cluster) | v2 | Kind cluster lifecycle for local/testing. |
| [`wsm registry`](#wsm-registry) | v2 | Mirror install artifacts to a private registry (on-prem / air-gapped). |
| `wsm convert-v2` | v2 | Diff the live v1 CR against the v2 CR the conversion webhook would produce. |
| `wsm telemetry` | v2 | Open the in-cluster telemetry UIs (Grafana, VictoriaMetrics). |
| `wsm set-version` | — | Set the version of a wsm-managed W&B instance. |
| `wsm console` | v1 | Port-forward and open the W&B console in a browser. |
| [`wsm version`](#wsm-version) | — | Print the wsm version, commit, and build date. |
| [`wsm deploy`](#wsm-deploy) | v1 | Legacy three-phase install (operator → chart ConfigMap → v1 CR). |
| [`wsm list`](#wsm-list) | v1 | List the container images a fresh install would pull. |
| [`wsm download`](#wsm-download) | v1 | Build an air-gapped `./bundle/` (charts + images + spec). |

Every `deploy-v2`, `cluster cleanup`, and `convert-v2` invocation requires
`--context <kubeconfig-context>`.

---

### `wsm deploy-v2`

Install the v2 operator, then create a W&B instance. By default the `operator`
command installs only the operator stack; create the instance separately with
`wsm deploy-v2 wandb deploy`, or pass `--include-cr` to do both in one run.

```bash
wsm deploy-v2 operator --context <ctx> [flags]
wsm deploy-v2 wandb deploy|destroy|get-ca-cert --context <ctx> [flags]
```

**`operator` — key flags:**
- `--operator-chart-version string`: operator chart version, which selects the
  operator image tag (default `2.0.0-beta.1`).
- `--operator-namespace string`: operator namespace (default `wandb-operators`).
- `--install-cert-manager string`: `auto` (detect & reuse), `true` (force
  install), `false` (skip) (default `auto`).
- `--install-nginx-gateway string`: same modes as above (default `auto`).
- `--setup-k8s-cluster`: create a Kind cluster before deploying;
  `--cluster-name` (default `kind`) and `--workers` tune it.
- `--include-cr`: also deploy the W&B CR in this run (default `false`).
- `--mirror-registry string` / `--insecure-registry`: install from a private
  mirror — see [`wsm registry`](#wsm-registry) and the
  [on-prem guide](./docs/deployment/on-prem.md).

**`wandb deploy` — key flags:**
- `--wandb-name string` / `--wandb-namespace string`: instance name / namespace
  (both default `wandb`).
- `--wandb-version string`: server manifest version (defaults to `0.82.2`; must
  be ≥ `0.80.0`).
- `--cr-file string`: path to a `WeightsAndBiases` CR YAML (built-in default
  otherwise).
- `--cr-set path=value`: set any CR field, e.g. `--cr-set spec.wandb.version=0.82.2`;
  repeatable, YAML-typed, overrides the template, `--cr-file`, and typed flags.
- `--license string` / `--license-file string`: inject `spec.wandb.license`.
- `--manifest-repository string`, `--bucket-proxy`, `--mirror-registry` — see the
  command reference.

**`wandb get-ca-cert`** reads the `<wandb-name>-root-cert` secret and writes
`ca.crt` / `tls.crt` locally. **`wandb destroy`** deletes the CR.

---

### `wsm cluster`

Manage Kind clusters for local development and testing.

```bash
wsm cluster create|destroy|list|cleanup [flags]
```

- `create`: `--cluster-name` (default `kind`), `--workers`, `--http-port`
  (default 8080), `--https-port` (default 8443), `--kind-node-image` (offline
  bootstrap), `--insecure-registry-host` (trust a plain-HTTP registry).
- `destroy`: `--cluster-name` (default `kind`).
- `list`: list Kind clusters wsm created (via its deployment marker).
- `cleanup`: delete all resources wsm deployed — requires `--context`.

---

### `wsm registry`

Mirror W&B's install artifacts to a customer-controlled registry (Harbor,
Artifactory, ECR, …) for on-prem / air-gapped installs. Pair with
`wsm deploy-v2 operator --mirror-registry <host>`. Full walkthrough:
[on-prem guide](./docs/deployment/on-prem.md).

```bash
wsm registry mirror|check|values|push [flags]
```

- `mirror`: pull every chart/image `deploy-v2 operator` needs and re-push to your
  mirror. `--to <host>` (required), `--insecure`, `--dry-run`,
  `--operator-chart-version` (default `2.0.0-beta.1`).
- `check`: verify all required images exist in your mirror. `--registry <host>`
  (required), `--fail-on-missing`, `--insecure`.
- `values`: emit a `values.yaml` fragment that re-points images at your registry.
- `push`: push images from a bundle directory into your mirror.

---

### `wsm deploy` (v1, legacy)

Legacy three-phase install to a cluster.

```bash
wsm deploy [operator|chart-cm|wandb-cr] [flags]
```

- Subcommands: `operator`, `chart-cm` (air-gapped chart ConfigMap), `wandb-cr`
  (the `apps.wandb.com/v1` CR).
- Flags: `-a/--airgapped`, `-b/--bundle`, `-c/--chart`, `-n/--namespace`
  (default `wandb`), `-v/--values`.

---

### `wsm list` (v1, legacy)

List the container images a fresh install would pull — useful for pre-pulling.

```bash
wsm list [-p linux/amd64]
```

---

### `wsm download` (v1, legacy)

Download images and charts into a `./bundle/` for offline transfer. Requires
`docker` on `PATH`.

```bash
wsm download [-p linux/amd64]
```

---

### `wsm version`

Print the `wsm` version, git commit, and build date. Needs no cluster.

```bash
wsm version           # wsm v2.0.0 (commit 738c0b9, built 2026-07-17T…)
wsm --version         # same value, cobra's built-in flag
```

Values are stamped at build time (GoReleaser on tagged releases; `make build`
stamps a local `dev` build).

---

## Examples

**Local development (v2, Kind):**
```bash
wsm cluster create --cluster-name wandb-cluster
wsm deploy-v2 operator --context kind-wandb-cluster
wsm deploy-v2 wandb deploy --context kind-wandb-cluster
```
The wandb-operator image is published amd64-only. Pass `--allow-unsupported-arch` if you are running Kind cluster on non-amd64 nodes.  

**On-prem / mirror registry (v2):** install from your own registry. The full
walkthrough (including a laptop test against a local `registry:2`) is in
[`docs/deployment/on-prem.md`](./docs/deployment/on-prem.md). TL;DR:
```bash
# 1. Mirror artifacts upstream -> your registry
wsm registry mirror --to harbor.corp.internal --operator-chart-version 2.0.0-beta.1

# 2. Install pulling only from the mirror
wsm deploy-v2 operator \
  --context <cluster> \
  --mirror-registry harbor.corp.internal \
  --operator-chart-version 2.0.0-beta.1
```

**Air-gapped bundle prep (v1, legacy):**
```bash
wsm list
wsm download --platform linux/amd64
```

## Development

```bash
make build   # build ./wsm
make lint    # go vet + golangci-lint
make fmt     # gofmt (CI fails if this leaves a diff)
```

There is no unit-test suite; CI (`.github/workflows/checks.yaml`) runs build,
lint, format, and `go mod tidy` checks. `.github/workflows/pr-checks.yml`
additionally gates every PR on a Conventional-Commit title/commits, a filled-in
PR body (see [`.github/pull_request_template.md`](./.github/pull_request_template.md)),
and exactly one `release:*` label. The `/ship` Claude Code skill drives
branch → commit → PR so the result satisfies that gate.

## Releasing

Releases are cut automatically when a PR is merged into `main` — no manual
tagging needed. The bump is chosen by a label on the merged PR:

- `release:major` → `vX+1.0.0`
- `release:minor` → `vX.Y+1.0`
- `release:patch` (or no label) → `vX.Y.Z+1`
- `release:skip` → no release

The new tag is pushed and [GoReleaser](https://goreleaser.com) builds the
binaries and publishes the GitHub release. Check the built-in version with
`wsm version`. To cut one by hand, push a tag
(`git tag vX.Y.Z && git push origin vX.Y.Z`) and the same build runs.

## Requirements

- Linux, macOS, or Windows
- `kubectl` (for deployment)
- `docker` / a container runtime (for Kind clusters and v1 image downloads)

## Support

File issues at [github.com/wandb/wsm/issues](https://github.com/wandb/wsm/issues).
See also the [W&B self-managed docs](https://docs.wandb.ai/guides/hosting/self-managed/).
