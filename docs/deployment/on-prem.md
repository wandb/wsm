# On-Prem / Air-Gapped Deployment

This guide covers two deployment models:

- **Managed (online)** — your cluster can reach the public registries; WSM pulls charts and images directly.
- **Air-gapped (mirrored)** — your cluster has no internet egress; you mirror every artifact to a private registry first, then install entirely from it.

Both install the same thing (the operator, its dependencies, and a W&B instance). The difference is *where the bytes come from*.

> If you just want a local laptop walkthrough against a throwaway `registry:2`, jump to [Full air-gapped walkthrough](#full-air-gapped-walkthrough).
>
> See also the [Local Kind guide](local-kind.md) for cluster setup details.

---

## The image surface (read this first)

A complete install pulls from **four** upstreams, in three tiers:

| Tier | What | Upstream | How WSM retargets it to your registry |
|------|------|----------|----------------------------------------|
| **1** | operator chart + image, cert-manager, nginx-gateway, the **server manifest** and every **app image** it references (weave, megabinary, frontend, …) | `us-docker.pkg.dev`, `quay.io/jetstack`, `ghcr.io/nginx` | **Explicit** — Helm chart refs + values, and the server manifest is rewritten so app image refs point at `<registry>/wandb/*`. Works on any cluster, no node config. |
| **2** | managed-service **operators** (moco, strimzi, altinity-clickhouse, opstree redis, seaweedfs, alpine/k8s) | `docker.io`, `quay.io`, `ghcr.io` | **Transparent** — a containerd registry mirror redirects the (hardcoded) refs. |
| **3** | managed-service **data-plane** pods (ClickHouse, Kafka, MySQL, Redis, SeaweedFS servers) | `docker.io`, `quay.io`, `ghcr.io` | **Transparent** — same containerd mirror. The operator hardcodes these image refs with no override, so a node-level redirect is the only retarget path. |

The tier-1 refs are rewritten to literally point at your registry, so those pods pull from it directly. Tiers 2/3 keep their original `docker.io`/`quay.io`/`ghcr.io` names, so the **nodes** must be told to mirror those hosts to your registry (WSM does this automatically for WSM-provisioned Kind clusters — see below).

---

## Model 1: Managed (online)

Your cluster reaches the internet; nothing to mirror.

```
            ┌──────────────────────── public registries ────────────────────────┐
            │  us-docker.pkg.dev    quay.io    ghcr.io    docker.io    github.com │
            └─────────────────────────────────┬──────────────────────────────────┘
                                               │  charts + images (tiers 1/2/3)
                                               │  + Gateway API CRDs
   wsm deploy-v2 operator --include-cr ───────►│
                                               ▼
                                   ┌───────────────────────┐
                                   │      your cluster      │
                                   │  operator + deps + W&B │
                                   └───────────────────────┘
```

```bash
wsm deploy-v2 operator \
  --context <kube-context> \
  --wandb-version <version> \
  --include-cr
```

That's the whole story for an online cluster. Everything below is for the air-gapped case.

---

## Model 2: Air-gapped (mirrored)

Two phases. **Phase 1 runs once, online** (on a bastion / your laptop / a CI job that can reach both the internet and your registry). **Phase 2 runs offline**, against the sealed cluster.

> ### Insecure (plain-HTTP) registry — what works fully offline
>
> A local **insecure** `registry:2` (`--insecure-registry`) is the quick way to test the **install layer** offline. From it, fully offline, you get: the **operator**, **cert-manager**, **nginx-gateway**, and the bundled **managed-service operators** (moco/strimzi/altinity/opstree/seaweedfs). These all install and run from the mirror.
>
> What it **cannot** do: bring up the **W&B instance itself** — the databases (ClickHouse/Kafka/MySQL/Redis), the app, and **weave**. The operator fetches the **server manifest over HTTPS from inside the cluster**, and a plain-HTTP registry can't serve it, so reconcile never produces those workloads. `wsm` prints a warning and continues.
>
> **To install the actual W&B instance offline you need an HTTPS registry** — see [TLS requirements](#tls-requirements). A self-signed cert is fine, and `--registry-ca-file` wires up the trust for you.

### Phase 1 — mirror everything (online)

```
   ┌─────────────── public registries ───────────────┐
   │ us-docker.pkg.dev  quay.io  ghcr.io  docker.io   │
   └───────────────────────┬──────────────────────────┘
                            │  pull
                            ▼
                 wsm registry mirror --to <registry> --wandb-version <v>
                            │  push  (tier 1 → <registry>/wandb/*, jetstack/*, nginx/*)
                            │        (tiers 2/3 → <registry>/<host-stripped path>)
                            ▼
                 ┌───────────────────────────┐
                 │   your private registry   │   ◄── now holds the full image surface
                 └───────────────────────────┘
```

```bash
# Charts + operator + cert-manager + nginx-gateway + managed-service images (tiers 1-3)
# and, with --wandb-version, the server manifest + all app images (weave, etc.).
wsm registry mirror \
  --to registry.corp.internal \
  --wandb-version <version>
  # --insecure                for a plain-HTTP / self-signed registry
  # --skip-managed-images     if you run W&B against external databases
```

Preview the exact source→target plan first with `--dry-run`.

### Phase 2 — install from the registry (offline)

```
                      ┌───────────────────────────┐
                      │   your private registry   │
                      └─────────────┬─────────────┘
                                    ▼
        wsm deploy-v2 operator --mirror-registry <registry> --include-cr
                                    │  installs operator + deps (tier 1, explicit refs)
                                    ▼
              ┌──────────────────────────────────────────────────┐
              │                   your cluster                    │
              │                                                   │
              │   operator (reconciles the CR) creates:           │
              │                                                   │
              │     app pods (weave, …) ──pull──► <reg>/wandb/*    │   tier 1: ref already
              │                                                   │   points at the registry
              │     DB pods (clickhouse, ──pull──► docker.io/...   │   tiers 2/3: node containerd
              │       kafka, mysql, …)            └─ redirected ─► │   mirror rewrites the host
              │                                   <reg>/<stripped> │   to the registry
              └──────────────────────────────────────────────────┘
```

```bash
wsm deploy-v2 operator \
  --context <kube-context> \
  --mirror-registry registry.corp.internal \
  --wandb-version <version> \
  --skip-gateway-api-crds \      # see "What still reaches the internet" below
  --include-cr
```

`--mirror-registry` retargets the charts + operator image and **defaults `--manifest-repository` to `oci://<registry>/wandb/server-manifest`**, so the operator pulls the rewritten manifest (and every app image) from your registry.

> **Your registry must be served over HTTPS.** `--insecure-registry` is rejected together with `--mirror-registry`: the W&B operator fetches the server manifest over HTTPS *from inside the cluster* (its own client, with TLS verification — not containerd), so a plain-HTTP or TLS-skipped registry can never complete a deploy. A self-signed cert is fine — see [TLS requirements](#tls-requirements) below. (`wsm registry mirror` itself may still use `--insecure` for the push.)

### Which command installs which piece

| Diagram piece | Command / flag | Source after Phase 1 |
|---------------|----------------|----------------------|
| operator chart + image | `wsm registry mirror` → `wsm deploy-v2 operator --mirror-registry` | `<reg>/wandb/charts/operator`, `<reg>/wandb/operator` |
| cert-manager, nginx-gateway | same | `<reg>/jetstack/*`, `<reg>/nginx/*` |
| server manifest + app images (weave, …) | `wsm registry mirror --wandb-version` → `--mirror-registry` (auto-sets `--manifest-repository`) | `<reg>/wandb/server-manifest`, `<reg>/wandb/*` |
| managed-service operators + data-plane (tiers 2/3) | `wsm registry mirror` (image push) + node containerd mirror (retarget) | `<reg>/<host-stripped>` |
| Kind node image (only if WSM provisions the cluster) | `wsm deploy-v2 operator --setup-k8s-cluster --kind-node-image <reg>/...` | `<reg>/...` |

### Configuring the node redirect (tiers 2/3)

Tiers 2/3 only resolve from your registry if the **nodes** mirror `docker.io`/`quay.io`/`ghcr.io` to it.

- **WSM-provisioned Kind clusters** — automatic. `wsm cluster create --insecure-registry-host <registry>` (and `--setup-k8s-cluster`) writes `/etc/containerd/certs.d/{docker.io,quay.io,ghcr.io,<registry>}/hosts.toml` for you.
- **Your own cluster (EKS/GKE/AKS/bare-metal)** — you configure it once, on every node (including future autoscaled nodes). Drop this on each node and restart containerd:

  ```toml
  # /etc/containerd/certs.d/docker.io/hosts.toml   (repeat for quay.io, ghcr.io)
  server = "https://registry-1.docker.io"          # https://quay.io , https://ghcr.io
  [host."https://registry.corp.internal"]
    capabilities = ["pull", "resolve"]
  ```

  > `us-docker.pkg.dev` is deliberately **not** mirrored this way — its images are retargeted explicitly (tier 1) under a different path convention.

---

## TLS requirements

The registry **must** be served over HTTPS and be reachable by **one address** that resolves the same from three vantage points — they pull different pieces:

| Consumer | Runs on | Pulls |
|----------|---------|-------|
| `wsm registry mirror` (push), `wsm deploy-v2` chart pulls | your **host** | charts |
| container image pulls | **nodes** (containerd) | all images |
| **server manifest fetch** | **operator pod** (its own oras client) | the manifest |

A production registry (Harbor/ECR/Artifactory) with a cert from a CA your cluster already trusts needs nothing extra. The rest of this section is for a **self-signed / internal-CA** registry, where you must make all three trust the CA.

### 1. Generate a CA + server cert

```bash
REG=registry.corp.internal      # or an IP reachable from host, nodes, and pods
openssl req -x509 -newkey rsa:4096 -nodes -days 3650 \
  -keyout ca.key -out ca.crt -subj "/CN=your-org-ca"
openssl req -newkey rsa:4096 -nodes -keyout tls.key -out tls.csr -subj "/CN=$REG"
cat > san.ext <<EOF
subjectAltName       = DNS:$REG          # use IP:<addr> instead for a bare IP
extendedKeyUsage     = serverAuth        # required — clients reject certs without it
keyUsage             = digitalSignature, keyEncipherment
EOF
openssl x509 -req -in tls.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out tls.crt -days 397 -extfile san.ext     # keep <= 398 days; macOS rejects longer
```

> Two gotchas that surface as `x509: certificate is not standards compliant`: the cert **must** have `extendedKeyUsage = serverAuth`, and a validity of **≤ 398 days** (macOS/Apple rule). A bare-IP registry needs the IP in an `IP:` SAN.

### 2. Run the registry with TLS

```bash
docker run -d --name registry -p 5000:5000 \
  -v "$PWD":/certs \
  -e REGISTRY_HTTP_TLS_CERTIFICATE=/certs/tls.crt \
  -e REGISTRY_HTTP_TLS_KEY=/certs/tls.key \
  registry:2
```

### 3. Deploy with `--registry-ca-file` (handles host **and** operator trust)

Pass the CA to `wsm` and it does two things: trusts the CA for its own chart pulls (an explicit cert pool — works on macOS too, no keychain edit), **and** mounts the CA into the operator + sets `SSL_CERT_FILE` so the operator's in-cluster manifest fetch trusts the registry. No manual `kubectl patch` required.

```bash
wsm deploy-v2 operator \
  --context <kube-context> \
  --mirror-registry $REG:5000 \
  --registry-ca-file ./ca.crt \
  --skip-gateway-api-crds \
  --wandb-version <version> \
  --include-cr
```

> Without this, the operator loops on `x509: certificate is not trusted` and never reconciles — the manifest fetch is the step that's easy to miss. `wsm` re-applies the injection on every deploy, so it survives operator upgrades.

### 4. Make the nodes trust the CA (for image pulls)

The nodes pull container images via containerd, which is separate from `wsm` and the operator. In each `/etc/containerd/certs.d/<host>/hosts.toml`, either point at a CA file you place on the node (`ca = "/etc/containerd/certs.d/<host>/ca.crt"`) or, for a lab, `skip_verify = true`. WSM-provisioned Kind clusters get this written for you.

---

## What still reaches the internet — and how to close it

Even with everything mirrored, a few things default to an online source. Each has an offline path:

| Still online | When | What the air-gapped customer does |
|--------------|------|------------------------------------|
| **Gateway API CRDs** (a YAML on `github.com`) | Whenever nginx-gateway installs | Pre-apply the CRDs from a copy you carried in, then pass `--skip-gateway-api-crds`; **or** host the YAML internally and pass `--gateway-api-crd-url <internal-url>`. |
| **Observability / telemetry images** (otel-collector, victoria-metrics-operator, grafana-operator + their data-plane pods) | Only if `--observability-mode=full\|forward` | These are **not yet mirrored by `wsm registry mirror`**. Either keep `--observability-mode=off` (default), or mirror those images into your registry by hand and ensure your node redirect covers them. |
| **Node containerd config** (not an image pull, but a manual step) | Tiers 2/3 on any non-Kind cluster | Apply the `certs.d` config above to every node. On platforms with **no node access** (GKE Autopilot, EKS Fargate) tiers 2/3 cannot be retargeted today — you get the W&B app (tier 1) but not the managed databases. |
| **Kind node image** | Only if `--setup-k8s-cluster` | Mirror `kindest/node` and pass `--kind-node-image <reg>/kindest/node:...`. N/A for an existing cluster. |

> The deepest limitation — tiers 2/3 needing a node-level redirect — exists because the operator hardcodes those image references. A future operator change adding a registry override would let WSM retarget them explicitly (like tier 1) and remove the node-config requirement on all platforms.

---

## Manual steps not handled by `wsm`

`wsm` installs the operator, dependencies, and the CR. These steps are the customer's, done out-of-band (kubectl / your platform tooling) — not by `wsm`:

**Before / during the deploy**
- **Gateway API CRDs** — carry in `standard-install.yaml` and `kubectl apply` it (then `--skip-gateway-api-crds`), or host it internally and use `--gateway-api-crd-url`.
- **Node containerd registry mirror** — on non-Kind clusters, configure `/etc/containerd/certs.d` on every node (incl. autoscaled ones) so `docker.io`/`quay.io`/`ghcr.io` resolve to your registry (tiers 2/3).
- **Registry pull credentials** — for an auth'd registry, `docker login` + `helm registry login` for `wsm`, and create an `imagePullSecret` for the operator + W&B namespaces so in-cluster pulls and the manifest fetch authenticate.

**After the deploy completes**
- **License** — apply your W&B license (`--license` / `--license-file` at deploy, or edit the CR's `spec.wandb.license` after). Without it the instance runs in a limited state.
- **External DNS + TLS for the W&B endpoint** — point your hostname at the gateway/ingress and provide the *serving* cert (this is the user-facing W&B URL — separate from the registry CA). Set it via `--wandb-hostname` and the networking/TLS flags.
- **Verify readiness** — `kubectl get wandb -n <ns>` should report the managed services and app ready; `kubectl get pods -n <ns>` should be all Running.
- **(If observability enabled)** mirror the telemetry stack images by hand — `wsm registry mirror` does not yet include them.

---

## Full air-gapped walkthrough

**Bring up the complete app, offline.** This brings up the **whole** W&B instance (databases + app + **weave**) from your registry with the cluster offline. It uses an **HTTPS** registry, because the operator fetches the server manifest over HTTPS from inside the cluster (a plain-HTTP registry only gets you the install layer — see the insecure callout above).

> ### Using your own registry? Skip the setup, swap two things.
>
> Steps 2–3 (cert + `registry:2` + the kind-network attach + node `certs.d`) only exist to stand up and reach an HTTPS registry locally. If you already run one (Harbor/ECR/Artifactory/…):
> - **Skip steps 2–3.** Use your registry's hostname as `$REG` everywhere; it's already reachable from host, nodes, and pods.
> - **If your registry's cert is signed by a CA your hosts/nodes/cluster already trust** (the normal enterprise case): **drop `--registry-ca-file`** from the deploy, and skip the node `skip_verify` — it all just works. Keep `--registry-ca-file ./ca.crt` only for a self-signed / internal-CA cert that isn't in the system trust store.
> - **If your registry requires a login:** replace `--insecure` on the mirror with `docker login $REG` + `helm registry login $REG`. Note: a registry that requires auth for *pulls* has an operator-side limitation — the operator fetches the server manifest anonymously, so allow anonymous read (at least on the `wandb/server-manifest` repo) or the W&B instance won't reconcile.

> **Local-Kind ordering matters.** The registry needs one address reachable from the **host** (`wsm` push/pulls), the **nodes** (image pulls), and the **pods** (the operator's manifest fetch). For local Kind that's the registry's IP on the `kind` docker network — which only exists once the cluster is created. So: **create the cluster first, attach the registry, then mirror.** A real registry has one DNS name reachable everywhere and skips this entirely.

### 1. (online) Build wsm and create the cluster

```bash
make build
wsm cluster create --cluster-name airgap        # creates the 'kind' docker network
```

### 2. (online) Stand up an HTTPS registry and attach it to the cluster network

Pick an IP inside the kind subnet (`docker network inspect kind -f '{{(index .IPAM.Config 1).Subnet}}'` → e.g. `192.168.117.0/24`, so `192.168.117.200` is free). Generate a CA + cert for it (two gotchas: `serverAuth` EKU and ≤ 398-day validity), run `registry:2` with TLS, and attach it to the kind network.

```bash
REGIP=192.168.117.200
REG=$REGIP:5000

openssl req -x509 -newkey rsa:4096 -nodes -days 3650 -keyout ca.key -out ca.crt -subj "/CN=wsm-airgap-ca"
openssl req -newkey rsa:4096 -nodes -keyout tls.key -out tls.csr -subj "/CN=wandb-registry"
cat > san.ext <<EOF
subjectAltName   = IP:$REGIP, DNS:localhost, IP:127.0.0.1
extendedKeyUsage = serverAuth
keyUsage         = digitalSignature, keyEncipherment
EOF
openssl x509 -req -in tls.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out tls.crt -days 397 -extfile san.ext

docker run -d --name local-registry -p 5000:5000 -v "$PWD":/certs \
  -e REGISTRY_HTTP_TLS_CERTIFICATE=/certs/tls.crt -e REGISTRY_HTTP_TLS_KEY=/certs/tls.key registry:2
docker network connect --ip $REGIP kind local-registry         # now reachable from host, nodes, pods
curl -sk https://$REG/v2/ -o /dev/null -w "registry -> %{http_code}\n"   # expect 200
```

### 3. (online) Point the nodes' containerd at the registry (HTTPS)

`wsm cluster create --insecure-registry-host` writes **HTTP** config, which won't work against an HTTPS registry — so write the `certs.d` redirect yourself (HTTPS, skip-verify for the self-signed cert). This redirects the tier-2/3 hosts to your registry:

```bash
docker exec -i airgap-control-plane sh -s <<EOF
for h in $REG docker.io quay.io ghcr.io; do
  mkdir -p /etc/containerd/certs.d/\$h
  printf '[host."https://$REG"]\n  capabilities = ["pull", "resolve"]\n  skip_verify = true\n' \
    > /etc/containerd/certs.d/\$h/hosts.toml
done
EOF
```

> With your own registry whose CA the nodes already trust, drop `skip_verify` (or point `ca = ...` at the CA), and there's no `--insecure-registry-host` HTTP gotcha.

### 4. (online) Mirror the full image surface

```bash
docker login                                 # avoids Docker Hub's anonymous pull rate limit (tiers 2/3)
wsm registry mirror --to $REG --wandb-version <version> --insecure
#   --insecure  → skip TLS verification of the self-signed cert on push
#                 (drop it if your registry's CA is already trusted)
```

This pushes tiers 1–3 (charts, operator, cert-manager, nginx-gateway, the managed-service operators + data-plane images) and the **server manifest + every app image** (weave, megabinary, …) rewritten to point at `$REG`.

### 5. (online, once) Install the Gateway API CRDs

While still online, apply the CRDs `wsm` would otherwise fetch (v1.4.0) directly from the source:

```bash
kubectl --context kind-airgap apply -f \
  https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.0/standard-install.yaml
```

> For a node that's *already* sealed, download that YAML on a connected machine and `kubectl apply -f ./standard-install.yaml` from one with cluster access — or host it internally and pass `--gateway-api-crd-url` at deploy instead of `--skip-gateway-api-crds`.

### 6. GO OFFLINE

Disconnect the cluster from the internet.

### 7. Install everything from the registry

```bash
wsm deploy-v2 operator \
  --context kind-airgap \
  --mirror-registry $REG \
  --registry-ca-file ./ca.crt \
  --skip-gateway-api-crds \
  --wandb-version <version> \
  --include-cr \
  --size dev \                       # single-node Kind: 'small' (default) won't fit (see below)
  --object-store-storage-size 20Gi   # required workaround for the SeaweedFS limit (see below)
```

`--registry-ca-file` makes `wsm`'s chart pulls trust the CA **and** mounts it into the operator (`SSL_CERT_FILE`) so its manifest fetch over HTTPS succeeds — no manual patch. **Drop `--registry-ca-file` if your registry's CA is already trusted by the host and cluster.** Do **not** pass `--insecure-registry` here — the manifest fetch needs real HTTPS.

**Two flags that matter on a small / single-node cluster (and why):**

- **`--size dev`** — the default `small` runs **3 replicas** of ClickHouse/Kafka/Redis/MySQL for quorum, which won't schedule on one Kind node (you'll see pods `Pending` with `Insufficient cpu/memory`). `dev` runs single replicas. For a production-like multi-replica install, use a multi-node cluster with adequate resources instead.
- **`--object-store-storage-size 20Gi`** — **required workaround for a SeaweedFS issue.** The operator derives SeaweedFS's `volumeSizeLimitMB` from the object-store storage size (`storage / 1MiB`), and the SeaweedFS master **refuses to start with a limit ≥ 30000** (it crash-loops with `volumeSizeLimitMB should be smaller than 30000`, leaving `OBJECTSTORE` `Unavailable`/`Degraded`). The size presets default to `100Gi` → `102400`, over the cap. Set this **under 30Gi** so the derived limit stays valid. This applies on **any** cluster, not just single-node.

### 8. Verify (no internet)

```bash
kubectl get pods -n wandb | grep -iE "weave|clickhouse|kafka|mysql|redis|seaweed"
kubectl get wandb -n wandb
```

The weave + database pods should reach `Running`, all pulled from `$REG`. Then complete the [manual steps not handled by `wsm`](#manual-steps-not-handled-by-wsm) (license, external DNS/TLS for the W&B endpoint).
