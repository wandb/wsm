# On-Prem / Air-Gapped Deployment

This guide covers two deployment models:

- **Managed (online)** — your cluster can reach the public registries; WSM pulls charts and images directly.
- **Air-gapped (mirrored)** — your cluster has no internet egress; you mirror every artifact to a private registry first, then install entirely from it.

Both install the same thing (the operator, its dependencies, and a W&B instance). The difference is *where the bytes come from*.

> If you just want a local laptop walkthrough against a throwaway `registry:2`, jump to [Column 1 in Airgap Walkthrough](#pick-your-registry-setup-first).
>
> See also the [Local Kind guide](local-kind.md) for cluster setup details.

---

## The image surface (read this first)

A complete install pulls from **four** upstreams, in three tiers:

| Tier | What | Upstream | How WSM retargets it to your registry |
|------|------|----------|----------------------------------------|
| **1** | operator chart + image, cert-manager, nginx-gateway, the **server manifest** and every **app image** it references (weave, megabinary, frontend, …) | `us-docker.pkg.dev`, `quay.io/jetstack`, `ghcr.io/nginx` | **Explicit** — Helm chart refs + values, and the server manifest is rewritten so app image refs point at `<registry>/wandb/*`. Set by `--mirror-registry`. |
| **2** | managed-service **operators** (moco, altinity-clickhouse, opstree redis, seaweedfs, alpine/k8s) | `docker.io`, `quay.io`, `ghcr.io` | **Explicit** — `--mirror-registry` sets per-subchart Helm image values at operator install. |
| **3** | managed-service **data-plane** pods (ClickHouse, MySQL, Redis, SeaweedFS, Kafka/Bufstream) | `docker.io`, `quay.io`, `ghcr.io`, `us-docker.pkg.dev` | **Node/runtime registry mirror** — the operator emits these with their upstream refs, so each node's container runtime must mirror those registries to `<registry>/<host-stripped path>`, exactly where `wsm registry mirror` pushes. wsm configures it for Kind (`--insecure-registry-host`); on a real cluster you configure it per node. |

Tiers 1 and 2 are retargeted explicitly by `--mirror-registry` (Helm values + server-manifest rewrite), landing at `<registry>/<host-stripped path>` where `wsm registry mirror` pushes. Tier 3 (data-plane pods) is retargeted by the **node/runtime registry mirror**, not by `--mirror-registry`: see the Tier-3 note below for why `spec.global.imageRegistry` does not work with a host-stripped mirror.

> **Node CA trust is separate, and still required for a self-signed / internal-CA registry.** `--mirror-registry` and `--registry-ca-file` change *which registry the image refs point at* and make **wsm** + the **operator** trust your CA — but the actual image pulls happen in each node's **containerd**, which has its own trust store. If `$REG` uses a cert your nodes don't already trust, pulls fail with `x509: certificate signed by unknown authority` regardless of the refs. Make the nodes trust the CA — see [Make the nodes trust the CA](#make-the-nodes-trust-the-ca). (A registry with a publicly/enterprise-trusted cert needs nothing here.)

### How retargeting is implemented (under the hood)

Tiers 1 and 2 are driven by `--mirror-registry` and land on `<registry>/<host-stripped path>` (where `wsm registry mirror` pushes). Tier 3 is driven by the node/runtime registry mirror:

- **Tier 1 — explicit Helm values + server-manifest rewrite.** wsm points the operator / cert-manager / nginx-gateway chart image values at the mirror, and `wsm registry mirror --wandb-version` rewrites the **server manifest** so every application image ref reads `<registry>/wandb/*`. The operator pulls that rewritten manifest (phase 2 auto-defaults `--manifest-repository` to `oci://<registry>/wandb/server-manifest`).
- **Tier 2 — per-subchart Helm values, at operator install.** The managed-service operators are bundled Helm subcharts, each with its **own** image-value convention (no single `global.imageRegistry`), so wsm sets each one when `--mirror-registry` is given — `moco.image.repository` (+ its `agent`/`fluentbit`/`mysqldExporter` sidecars), `redis-operator.redisOperator.imageName`, `altinity-clickhouse-operator.operator.image.registry` + `metrics.image.registry`, and `seaweedfs-operator.image.registry`. (Kafka has no such subchart operator — Bufstream is reconciled directly, and its data-plane image is retargeted at tier 3.)
- **Tier 3 — node/runtime registry mirror.** The data-plane image refs (ClickHouse, MySQL, Redis, SeaweedFS, and Kafka/Bufstream — broker, etcd, bucket-ensure) come from the server manifest with their **upstream** registry/repository (`docker.io/…`, `quay.io/…`, `ghcr.io/…`, `us-docker.pkg.dev/buf-images-1/…`). wsm does **not** rewrite these and does **not** set `spec.global.imageRegistry`. They reach the mirror through the container runtime's per-registry mirror: each node maps those registries to `<registry>/<host-stripped path>` — exactly where `wsm registry mirror` pushed them. wsm sets this up for Kind (`wsm cluster create --insecure-registry-host $REG`, or `wsm deploy-v2 operator --setup-k8s-cluster --insecure-registry`); on a real cluster you configure your runtime's registry mirror per node.

> **Why not `spec.global.imageRegistry`?** That operator field is a *prepend*: `GetImage` builds `<imageRegistry>/<full-upstream-path>`, e.g. `<registry>/quay.io/opstree/redis` and `<registry>/us-docker.pkg.dev/buf-images-1/…/bufstream`. But `wsm registry mirror` pushes **host-stripped** (`<registry>/opstree/redis`, `<registry>/buf-images-1/…/bufstream`), so the prepended path doesn't exist in the mirror → `ImagePullBackOff`. It also double-prefixes the mirror-rewritten app refs (`<registry>/<registry>/wandb/*` → `InvalidImageName`). `--mirror-registry` therefore leaves the field unset and relies on the node/runtime mirror for the data plane. `--image-registry` / `--cr-set spec.global.imageRegistry=<host>` remains only for a registry that serves images at their **full upstream paths** (not `wsm registry mirror`'s host-stripped layout).

---

## Model 1: Managed (online)

Your cluster reaches the internet; nothing to mirror.

```
            ┌──────────────────────── public registries ────────────────────────┐
            │  us-docker.pkg.dev    quay.io    ghcr.io    docker.io    github.com │
            └─────────────────────────────────┬──────────────────────────────────┘
                                               │  charts + images (tiers 1/2/3)
                                               │  + Gateway API CRDs
   wsm deploy-v2 operator   (phase 1) ────────►│
   wsm deploy-v2 wandb deploy (phase 2) ──────►│
                                               ▼
                                   ┌───────────────────────┐
                                   │      your cluster      │
                                   │  operator + deps + W&B │
                                   └───────────────────────┘
```

Installing is **two phases**: phase 1 brings up the operator stack, phase 2 creates
the W&B instance.

```bash
# Phase 1 — operator stack
wsm deploy-v2 operator --context <kube-context>

# Phase 2 — W&B instance
wsm deploy-v2 wandb deploy --context <kube-context> --wandb-version <version>
```

That's the whole story for an online cluster. Everything below is for the air-gapped case.

---

## Model 2: Air-gapped (mirrored)

Your cluster has no internet egress. You mirror every artifact to a private registry
**once, online**, then install entirely from that registry **offline**.

- **Phase 1 (online)** — `wsm registry mirror` copies every chart + image (and, with
  `--wandb-version`, the server manifest and all app images) into your registry. Run it
  on a bastion / your laptop / a CI job that can reach both the internet and the registry.
- **Phase 2 (offline)** — `wsm deploy-v2 operator` then `wsm deploy-v2 wandb deploy`
  install from the mirror, with every image ref retargeted to `<registry>/...`.

### Pick your registry setup first

Everything downstream depends on this choice. Each path's **full command sequence is in its
column** — decide, then copy from one place. `<ver>` = `--operator-chart-version`, `<v>` =
`--wandb-version`, `<ctx>` = your kubeconfig context.

The split is really about **how the operator gets the server manifest**: over `oci://` (an
HTTPS registry pull — needs TLS) or from `file://` (mounted onto the operator pod — no TLS at
all). The charts, operator, and images never need TLS; only the `oci://` manifest fetch does.

| | **Local — no TLS** (plain-HTTP registry, `file://` manifest)                                                                                                                                                                                                                                                                                                                                                                        | **TLS registry** (production air-gap, `oci://` manifest) |
|---|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|---|
| Use it for | Local testing / demos on a laptop                                                                                                                                                                                                                                                                                                                                                                                                   | A real air-gapped install |
| Registry | a throwaway `registry:2` (plain HTTP)                                                                                                                                                                                                                                                                                                                                                                                               | your Harbor / ECR / Artifactory / internal registry (HTTPS) |
| Brings up | **everything** — databases, app, weave                                                                                                                                                                                                                                                                                                                                                                                              | **everything** |
| Manifest delivery | `file://`, mounted into the operator pod (no registry TLS)                                                                                                                                                                                                                                                                                                                                                                          | `oci://`, pulled from the registry (needs a valid / trusted cert) |
| **1. Set up + mirror** (online) | `docker run -d -p 5000:5000 --name local-registry registry:2`<br>`REG=host.docker.internal:5000`<br>`wsm registry mirror --to $REG --insecure --operator-chart-version <ver> --wandb-version <v>`<br> pushes charts + operator + managed + **app** images; the final manifest-push step fails on plain HTTP — expected. <br> **see ‡ below for steps to mirror manifest without TLS then continue to step 2**                       | `REG=<registry reachable from host, nodes, and pods>`<br>`wsm registry mirror --to $REG --operator-chart-version <ver> --wandb-version <v>`<br>_add `--insecure` only to skip verifying a self-signed push cert_<br>`wsm registry check --registry $REG --wandb-version <v> --fail-on-missing` |
| **2. Install** (offline) | `wsm cluster create --cluster-name airgap --insecure-registry-host $REG`<br>`wsm deploy-v2 operator --context kind-airgap --mirror-registry $REG --insecure-registry --operator-chart-version <ver> --skip-gateway-api-crds`<br>_mount the manifest onto the operator pod — see ‡ below_<br>`wsm deploy-v2 wandb deploy --context kind-airgap --manifest-repository file:///manifests --wandb-version <v>`<br>_no `--mirror-registry` on this path: the node's containerd mirrors (from `--insecure-registry-host`) retarget the managed DB images, and the mounted `file://` manifest carries the app refs. `--mirror-registry` on `wandb deploy` only defaults `--manifest-repository` to the mirror — it does **not** retarget the DB images_ | _if the registry CA is self-signed, first [make the nodes trust it](#make-the-nodes-trust-the-ca)_<br>`wsm deploy-v2 operator --context <ctx> --mirror-registry $REG --operator-chart-version <ver> --registry-ca-file ./ca.crt --skip-gateway-api-crds`<br>`wsm deploy-v2 wandb deploy --context <ctx> --mirror-registry $REG --wandb-version <v>`<br>_drop `--registry-ca-file` if the CA is already trusted by host + cluster._<br>_the managed DB images reach `$REG` via your nodes' container-runtime registry mirror (configure per node); `--mirror-registry` covers charts/operator/app images only_ |

**‡ Deliver the manifest via `file://` (no registry TLS).** The published server manifest is an
OCI artifact. Pull it, point its **app** image refs at your mirror, and mount the YAML onto the
operator pod — `wsm` plumbs `--manifest-repository file://…` into the CR but does **not** mount
the files.

```bash
# 1. pull the published manifest artifact for the version you want and extract its YAML
MREF=us-docker.pkg.dev/wandb-production/public/wandb/server-manifest:<v>
docker pull --platform linux/amd64 "$MREF"          # artifact is amd64-only; --platform is required on Apple Silicon
cid=$(docker create --platform linux/amd64 "$MREF" x)   # scratch image whose layer holds /manifest.yaml + /sizing.yaml; 'x' is a throwaway cmd (never run — cp only reads the layer)
docker cp "$cid:/manifest.yaml" ./manifest.yaml
docker cp "$cid:/sizing.yaml"  ./sizing.yaml
docker rm "$cid"

# 2. repoint the APP image refs at your mirror (the same prefix wsm strips when it mirrors them).
#    Managed DB refs (docker.io/…, quay.io/…, us-docker.pkg.dev/…) are left alone — they reach
#    the mirror via the node's container-runtime registry mirror (--insecure-registry-host).
sed -i '' "s#us-docker.pkg.dev/wandb-production/public/#$REG/#g" manifest.yaml   # Linux: drop the '' after -i

# 3. mount the YAML onto the operator pod at /manifests/<v>
OPNS=wandb-operators   # wsm default (--operator-namespace)
CTR=$(kubectl get deploy wandb-operator -n $OPNS -o jsonpath='{.spec.template.spec.containers[0].name}')
kubectl create configmap wandb-manifest -n $OPNS --from-file=manifest.yaml --from-file=sizing.yaml
kubectl patch deployment wandb-operator -n $OPNS --type strategic -p \
  "{\"spec\":{\"template\":{\"spec\":{\"volumes\":[{\"name\":\"m\",\"configMap\":{\"name\":\"wandb-manifest\"}}],\"containers\":[{\"name\":\"$CTR\",\"volumeMounts\":[{\"name\":\"m\",\"mountPath\":\"/manifests/<v>\"}]}]}}}}"
kubectl rollout status deploy/wandb-operator -n $OPNS
```

> **Just want to confirm the operator installs?** Use the plain-HTTP registry, skip the app-image
> and manifest steps, and stop after `wsm deploy-v2 operator --insecure-registry`. You get the
> operator stack (operator, cert-manager, nginx-gateway, the managed-service **operators**) but
> **no** databases/app/weave


### Phase 1 — mirror everything (online)

The commands are in the [setup table](#pick-your-registry-setup-first) above. This is what
they do:

```
        ┌──────────────── public registries ────────────────┐
        │  us-docker.pkg.dev   quay.io   ghcr.io   docker.io │
        └────────────────────────┬───────────────────────────┘
                                 │ pull
                                 ▼
      wsm registry mirror --to $REG [--insecure] --wandb-version <v>
                                 │ push: charts, operator, cert-manager, nginx-gateway,
                                 │       managed-service images, app images (weave, …)
                                 ▼
                     ┌───────────────────────────┐
                     │    your private registry  │  ◄── now holds the full IMAGE surface
                     └───────────────────────────┘

   the server MANIFEST is handled differently per setup:
   • TLS registry     → wsm re-pushes it (image refs rewritten) over HTTPS to
                        oci://$REG/wandb/server-manifest   ← operator pulls it here in Phase 2
   • insecure / local → that push can't reach a plain-HTTP registry (it's HTTPS-only, so the
                        mirror run ends with an "expected" manifest-push error). Instead extract
                        it locally and rewrite its app refs to $REG:
                          docker pull …/server-manifest:<v>  →  manifest.yaml (+ sizing.yaml)
                        ← mounted onto the operator pod in Phase 2 and read via file://
```

### Phase 2 — install from the registry (offline)

The commands are in the [setup table](#pick-your-registry-setup-first) above. This is what
they do:

Two setups, differing in **where the operator reads the manifest** and **how the managed
DB images get retargeted**:

```
═══ TLS registry — oci:// manifest ═════════════════════════════════════════════
  
  wsm deploy-v2 operator     --context <ctx> --mirror-registry $REG --registry-ca-file ca.crt
  
  wsm deploy-v2 wandb deploy --context <ctx> --mirror-registry $REG
       │
       ▼  operator reconciles the CR and:
  • reads the manifest ◄─ oci://$REG/wandb/server-manifest   (HTTPS pull; trusts the CA)
  • app pods    pull  ◄─ $REG/wandb/*                        (refs from the mirrored manifest)
  • DB pods     pull  ◄─ docker.io / quay.io / ghcr.io / us-docker.pkg.dev refs, redirected
     (clickhouse, mysql, redis, seaweedfs, kafka)             to $REG/<host-stripped> by each
                                                              node's container-runtime registry
                                                              mirror (configure it per node)

═══ insecure / local — file:// manifest (plain-HTTP) ═══════════════════════════
  
  cluster: wsm cluster create --insecure-registry-host $REG
  
  wsm deploy-v2 operator     --context <ctx> --mirror-registry $REG --insecure-registry
  (mount manifest.yaml onto the operator pod as a ConfigMap — see ‡)
  
  wsm deploy-v2 wandb deploy --context <ctx> --manifest-repository file:///manifests   (no --mirror-registry)
       │
       ▼  operator reconciles the CR and:
  • reads the manifest ◄─ file:///manifests/<v>/*.yaml       (mounted ConfigMap; no registry, no TLS)
  • app pods    pull  ◄─ $REG/wandb/*                        (manifest refs you rewrote to $REG)
  • DB pods     pull  ◄─ docker.io / quay.io / ghcr.io refs, transparently redirected to $REG
     (clickhouse, mysql, redis, seaweedfs)                    by the node's containerd mirrors
                                                              (from --insecure-registry-host) —
                                                              no spec.global.imageRegistry needed
```

### Which command installs which piece

| Diagram piece | Command / flag | Source after Phase 1 |
|---------------|----------------|----------------------|
| operator chart + image | `wsm registry mirror` → `wsm deploy-v2 operator --mirror-registry` | `$REG/wandb/charts/operator`, `$REG/wandb/operator` |
| cert-manager, nginx-gateway | same | `$REG/jetstack/*`, `$REG/nginx/*` |
| server manifest + app images (weave, …) | `wsm registry mirror --wandb-version` → `--mirror-registry` (auto-sets `--manifest-repository`) | `$REG/wandb/server-manifest`, `$REG/wandb/*` |
| managed-service operators + Kafka (tier 2) | `wsm registry mirror` (push) → `wsm deploy-v2 operator --mirror-registry` (per-subchart Helm values) | `$REG/<host-stripped>` |
| managed data-plane: ClickHouse/MySQL/Redis/SeaweedFS/Kafka (tier 3) | `wsm registry mirror` (push) → node/runtime registry mirror (`--insecure-registry-host` on Kind; per-node config on a real cluster) | `$REG/<host-stripped>` |
| Kind node image (only if WSM provisions the cluster) | `wsm deploy-v2 operator --setup-k8s-cluster --kind-node-image $REG/...` | `$REG/...` |

### Make the nodes trust the CA

For a **TLS registry with a self-signed / internal CA**, the nodes must trust that CA or image
pulls fail with `x509: certificate signed by unknown authority`. The nodes pull via containerd,
which is **separate from `wsm` and the operator** — `--registry-ca-file` does not reach it. Two
ways (a registry whose CA the nodes already trust needs neither):

**A. System trust (simplest; works on a stock Kind node).** Drop the CA into the node trust
store and restart containerd:

```bash
for node in $(kind get nodes --name <cluster>); do
  docker cp ca.crt $node:/usr/local/share/ca-certificates/registry-ca.crt
  docker exec $node update-ca-certificates
  docker exec $node systemctl restart containerd
done
```

**B. Per-host `certs.d`** — set `ca`/`skip_verify` per registry host under
`/etc/containerd/certs.d/<host>/hosts.toml`. This requires containerd's
`config_path = "/etc/containerd/certs.d"` (set on a `wsm cluster create --insecure-registry-host`
node; not on a stock Kind node). On a real cluster, set `config_path` once and drop a
`hosts.toml` per node.

> Two cert gotchas that surface as TLS errors regardless of node trust, since `wsm` and the
> operator verify the cert directly: the server cert must include the **exact address you use**
> in its `subjectAltName` (an `IP:` SAN for a bare IP — `x509: ... doesn't contain any IP SANs`
> means it's missing), and it must carry `extendedKeyUsage = serverAuth`. These are the
> registry owner's responsibility on a real registry.

## What still reaches the internet — and how to close it

Even with everything mirrored, a few things default to an online source. Each has an offline path:

| Still online | When | What the air-gapped customer does |
|--------------|------|------------------------------------|
| **Gateway API CRDs** (a YAML on `github.com`) | Whenever nginx-gateway installs | Pre-apply the CRDs from a copy you carried in, then pass `--skip-gateway-api-crds`; **or** host the YAML internally and pass `--gateway-api-crd-url <internal-url>`. |
| **Observability / telemetry images** (otel-collector, victoria-metrics-operator, grafana-operator + their data-plane pods) | Only if `--observability-mode=full\|forward` | These are **not yet mirrored by `wsm registry mirror`**. Either keep `--observability-mode=off` (default), or mirror those images into your registry by hand and ensure your node redirect covers them. |
| **Node container-runtime registry mirror** (required for the data plane) | Always, for the managed DB images (ClickHouse/MySQL/Redis/SeaweedFS/Kafka) | The operator emits these with upstream refs, so each node must mirror `docker.io`/`quay.io`/`ghcr.io`/`us-docker.pkg.dev` → `$REG/<host-stripped>`. `wsm cluster create --insecure-registry-host $REG` sets this up for Kind; on a real cluster you configure `certs.d` per node. Platforms with no node access (GKE Autopilot, EKS Fargate) can't run the managed databases air-gapped. |
| **Kind node image** | Only if `--setup-k8s-cluster` | Mirror `kindest/node` and pass `--kind-node-image <reg>/kindest/node:...`. N/A for an existing cluster. |

---

## Manual steps not handled by `wsm`

`wsm` installs the operator, dependencies, and the CR. These steps are the customer's, done out-of-band (kubectl / your platform tooling) — not by `wsm`:

**Before / during the deploy**
- **Gateway API CRDs** — carry in `standard-install.yaml` and `kubectl apply` it (then `--skip-gateway-api-crds`), or host it internally and use `--gateway-api-crd-url`.
- **Node container-runtime registry mirror** — how the managed DB images (ClickHouse/MySQL/Redis/SeaweedFS/Kafka) reach the mirror on **every** path (see the Phase 2 diagrams): each node mirrors `docker.io`/`quay.io`/`ghcr.io`/`us-docker.pkg.dev` → `$REG/<host-stripped>`. `wsm cluster create --insecure-registry-host $REG` sets this up for Kind; on a real cluster you configure `certs.d` per node. `--mirror-registry` does not retarget these (see the Tier-3 note above).
- **Registry pull credentials** — for an auth'd registry, `docker login` + `helm registry login` for `wsm`, and create an `imagePullSecret` for the operator + W&B namespaces so in-cluster pulls and the manifest fetch authenticate.

**After the deploy completes**
- **License** — apply your W&B license (`--license` / `--license-file` at deploy, or edit the CR's `spec.wandb.license` after). Without it the instance runs in a limited state.
- **External DNS + TLS for the W&B endpoint** — point your hostname at the gateway/ingress and provide the *serving* cert (this is the user-facing W&B URL — separate from the registry CA). Set it via `--wandb-hostname` and the networking/TLS flags.
- **Verify readiness** — `kubectl get wandb -n <ns>` should report the managed services and app ready; `kubectl get pods -n <ns>` should be all Running.
- **(If observability enabled)** mirror the telemetry stack images by hand — `wsm registry mirror` does not yet include them.

---

### Verify (no internet)

```bash
# Registry side — every artifact the install needs is present in the mirror:
wsm registry check --registry $REG --wandb-version <version> --insecure --fail-on-missing

# Cluster side — the workloads came up from it:
kubectl get pods -n wandb | grep -iE "weave|clickhouse|kafka|mysql|redis|seaweed"
kubectl get wandb -n wandb
```

The weave + database pods should reach `Running`, all pulled from `$REG`. If a pod is stuck in `ImagePullBackOff` while `registry check` reports everything present, the image is in the registry but a pod is still asking for a public host — check that you passed `--mirror-registry` on **both** phases (step 7). Confirm the data-plane pods carry the mirror host:

```bash
kubectl get wandb -n wandb -o jsonpath='{.spec.global.imageRegistry}{"\n"}'   # should be $REG
kubectl get pods -n wandb -o jsonpath='{range .items[*]}{.spec.containers[*].image}{"\n"}{end}' | sort -u
```

If a pull fails with `x509: certificate signed by unknown authority`, the refs are right but the **node doesn't trust the registry CA** — see [Make the nodes trust the CA](#make-the-nodes-trust-the-ca). If the data-plane pods `ImagePullBackOff` on **public** hosts (`docker.io/…`, `quay.io/…`, `us-docker.pkg.dev/…`), the node's container-runtime registry mirror isn't configured for that registry — add it to `certs.d` (`--insecure-registry-host` on Kind). Then complete the [manual steps not handled by `wsm`](#manual-steps-not-handled-by-wsm) (license, external DNS/TLS for the W&B endpoint).
