# On-Prem / Air-Gapped Deployment

This guide covers deploying W&B with WSM when the target Kubernetes cluster cannot pull from public container registries — either because the network is fully air-gapped, or because the customer requires every workload image to come from a corporate mirror (Harbor, Artifactory, ECR, etc.).

## What "on-prem" means in WSM

A vanilla `wsm deploy-v2 operator` install pulls from several public sources:

- `us-docker.pkg.dev/wandb-production/public/wandb/charts/operator` (operator chart + image)
- `quay.io/jetstack/charts/cert-manager` (chart + 5 component images)
- `ghcr.io/nginx/charts/nginx-gateway-fabric` (chart + 2 images)
- W&B server manifest + application images (pulled at runtime by the operator)
- Subchart controller images (moco, redis-operator, strimzi, seaweedfs-operator, altinity-clickhouse-operator)

On-prem mode replaces those sources with **one customer-controlled mirror**. WSM provides two pieces:

1. **`wsm registry mirror`** — pulls every artifact from its upstream source and re-pushes to your mirror.
2. **`wsm deploy-v2 operator --mirror-registry <host>`** — rewrites every chart URL and image repository in the install to use your mirror instead of the upstream defaults.

> **Scope note.** The current PR covers the operator + cert-manager + nginx-gateway chart graph (3 charts + 8 images). The W&B server manifest, app images, and subchart controller images are upcoming iterations. Today's flow gets you a working operator pod pulling from the mirror; subchart controller pods (mysql/redis/kafka/clickhouse/seaweedfs operators) still pull from public registries until those iterations land. For a true air-gap install, you'll need narrow outbound egress to `ghcr.io`, `quay.io`, and `docker.io` until then, or wait for the follow-up PR.

## Production scenario

In production, a real on-prem customer typically has:

- A mirror at e.g. `harbor.corp.internal:443` with a TLS cert signed by the corporate CA
- Workstations and Kubernetes nodes that already trust the corporate CA (admin policy)
- Docker credentials in `~/.docker/config.json` for the mirror

The flow is two commands, with **no `--insecure*` flags**:

```bash
# On a connected jump host (has internet + reachable from cluster)
docker login harbor.corp.internal

wsm registry mirror \
  --to harbor.corp.internal \
  --operator-chart-version 2.0.0-alpha.2

wsm deploy-v2 operator \
  --context <air-gapped-cluster> \
  --mirror-registry harbor.corp.internal \
  --operator-chart-version 2.0.0-alpha.2
```

Auth is read from `~/.docker/config.json` on the host running `wsm`. The cluster nodes pull from `harbor.corp.internal` directly using their pre-configured CA trust.

For **true sneakernet air-gap** (no network path between connected machine and cluster), the same `wsm registry mirror` command runs on the jump host; the populated mirror is then physically transported into the air-gapped network. Direct OCI push is the only mode supported today.

## Local testing — laptop walkthrough

This is the recipe that proves WSM's on-prem code path end-to-end before you commit to a real customer setup. Everything runs on one laptop against a plain-HTTP `registry:2` container.

### Prerequisites

- Docker / OrbStack running
- WSM built from source (`make build`)
- ~5 GB free disk for mirrored images

### 1. Bring up a local mirror

```bash
docker run -d --restart=always \
  -p 5000:5000 \
  --name local-registry \
  registry:2
```

Plain HTTP on `localhost:5000`. Stand-in for the customer's Harbor.

### 2. Create a Kind cluster that trusts the mirror

```bash
./wsm cluster create \
  --cluster-name onprem-test \
  --workers 0 \
  --http-port 18080 --https-port 18443 \
  --insecure-registry-host host.docker.internal:5000
```

The `--insecure-registry-host` flag drops a containerd `hosts.toml` into the Kind node so kubelet pulls from `host.docker.internal:5000` over plain HTTP without TLS verification. Two notes on hostnames:

- `localhost:5000` on your laptop ≠ `localhost:5000` inside a Kind container. `host.docker.internal:5000` resolves to the same registry from both sides on OrbStack and Docker Desktop.
- `--http-port 18080 --https-port 18443` avoids common conflicts on `:8080` / `:8443`.

### 3. Populate the mirror

```bash
./wsm registry mirror \
  --to host.docker.internal:5000 \
  --insecure \
  --operator-chart-version 2.0.0-alpha.2
```

Pulls 11 artifacts (~1 GB) from upstream and pushes to your local registry:

| Artifact | Source |
|---|---|
| Operator chart | `us-docker.pkg.dev/wandb-production/public/wandb/charts/operator` |
| Operator binary image | `us-docker.pkg.dev/wandb-production/public/wandb/operator` |
| Cert-manager chart | `quay.io/jetstack/charts/cert-manager` |
| Cert-manager controller / webhook / cainjector / acmesolver / startupapicheck | `quay.io/jetstack/cert-manager-*` |
| Nginx-gateway-fabric chart | `ghcr.io/nginx/charts/nginx-gateway-fabric` |
| Nginx-gateway control plane + data plane | `ghcr.io/nginx/nginx-gateway-fabric{,/nginx}` |

Use `--dry-run` first if you want to preview the plan without pulling.

Confirm the mirror is populated:

```bash
curl -s http://localhost:5000/v2/_catalog | jq
# expect ~11 repositories: wandb/charts/operator, wandb/operator, jetstack/*, nginx/*
```

### 4. Install W&B operator from the mirror

```bash
./wsm deploy-v2 operator \
  --context kind-onprem-test \
  --mirror-registry host.docker.internal:5000 \
  --insecure-registry \
  --operator-chart-version 2.0.0-alpha.2
```

Expected output (~1 minute):

```
[1/3] Ensuring nginx-gateway-fabric...Pulled: host.docker.internal:5000/nginx/charts/nginx-gateway-fabric:2.5.1 ✓
[2/3] Ensuring cert-manager...Pulled: host.docker.internal:5000/jetstack/charts/cert-manager:v1.20.2 ✓
[3/3] Deploying Required operators...Pulled: host.docker.internal:5000/wandb/charts/operator:2.0.0-alpha.2 ✓
✓ Deployment complete!
```

### 5. Verify every WSM-installed pod is pulling from the mirror

```bash
kubectl --context kind-onprem-test get pods --all-namespaces \
  -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\t"}{range .spec.containers[*]}{.image}{" "}{end}{"\n"}{end}' \
  | grep -v 'kube-system\|local-path-storage'
```

These five pods should reference `host.docker.internal:5000/...`:

- `cert-manager/cert-manager-*` (controller)
- `cert-manager/cert-manager-cainjector-*`
- `cert-manager/cert-manager-webhook-*`
- `nginx-gateway/nginx-gateway-nginx-gateway-fabric-*`
- `wandb-operators/wandb-operator-*`

The five subchart controller pods in `wandb-operators` (`moco-controller`, `redis-operator`, `strimzi-cluster-operator`, `altinity-clickhouse-operator`, `seaweedfs-operator`) still reference public registries — those images are covered in a follow-up iteration.

### 6. Tear down

```bash
./wsm cluster destroy --cluster-name onprem-test
docker rm -f local-registry
```

## Combined one-command path

If you want WSM to create the cluster and install in the same invocation (after the mirror is populated):

```bash
./wsm deploy-v2 operator \
  --setup-k8s-cluster \
  --cluster-name onprem-test \
  --context kind-onprem-test \
  --mirror-registry host.docker.internal:5000 \
  --insecure-registry \
  --operator-chart-version 2.0.0-alpha.2
```

When `--setup-k8s-cluster` is combined with `--mirror-registry` and `--insecure-registry`, the corresponding `--insecure-registry-host` is applied to the new Kind node automatically.

## Going from plain-HTTP testing to a real TLS setup

The flags above (`--insecure`, `--insecure-registry`, `--insecure-registry-host`) only exist because a local `registry:2` serves plain HTTP. A real on-prem customer with a TLS-fronted mirror drops all three:

| Flag | Drop in production when... |
|---|---|
| `wsm registry mirror --insecure` | Mirror has a real cert; your laptop trusts the signing CA via system trust store |
| `wsm deploy-v2 --insecure-registry` | Same — Helm validates the cert via system trust |
| `wsm cluster create --insecure-registry-host` | Cluster nodes already trust the mirror's CA (admin-installed) |

For a productionized **local** test (TLS instead of plain HTTP), the recipe is similar but uses [`mkcert`](https://github.com/FiloSottile/mkcert) to issue a locally-trusted dev cert. The Kind side requires a `--registry-ca-cert` flag that installs the CA into the node's trust store — coming in a follow-up.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `wsm registry mirror` fails with `no image found in manifest list for architecture "darwin"` | Old WSM build — multi-arch copy was added recently. Rebuild. |
| `lookup local-registry: no such host` from wsm process | The wsm process runs on your laptop, where `local-registry` isn't a DNS name. Use `localhost:5000` for the host side; use `host.docker.internal:5000` for both sides on OrbStack/Docker Desktop. |
| Pod `ImagePullBackOff` with `http: server gave HTTP response to HTTPS client` | Cluster was created without `--insecure-registry-host`. Destroy and recreate with the flag. |
| `kubeadm init … exit status 1` and kubelet logs show `unknown service runtime.v1.RuntimeService` | Stale containerd patch shape in an older wsm build (`mirrors.X.endpoint` form). Pull the latest WSM source. |
| Pre-install Helm hook Job times out | Subchart pre-install hook image not yet in mirror, or wrong hostname. Check the failing pod's `kubectl describe` for the exact image reference. |
