# Try W&B Server on-prem — locally, from your own registry

New to `wsm`? This is the fastest way to see it in action. In about 10 minutes you'll spin
up **W&B Server on a local Kubernetes cluster where every image comes from a throwaway
registry on your laptop** — a miniature air-gapped install that shows off what `wsm` is for:
deploying W&B entirely from a registry *you* control, with zero public pulls at deploy time.

> Just want to see W&B running with the least fuss? Jump to
> [The 3-command version](#even-faster-the-3-command-version) — it pulls from the public
> registries and skips the mirror.

## Before you start

You'll need, on your PATH:

- **Docker**
- **[kind](https://kind.sigs.k8s.io/)** (local Kubernetes in Docker)
- **`wsm`** ([install it here](installation.md))

> **Run this on an amd64 (x86_64) machine.** The W&B operator image is amd64-only today, so
> it won't run on an Apple Silicon Mac's local cluster (a multi-arch image is on the roadmap).
> On Apple Silicon, point `wsm` at a remote amd64 cluster instead.

## The walkthrough

### 1. Stand up a private registry on your laptop

```bash
docker run -d -p 5000:5000 --name wandb-registry registry:2
export REG=host.docker.internal:5000
export VER=0.82.2                       # the W&B server version to install
```

`registry:2` is a throwaway stand-in for the private registry you'd use in production
(Harbor, Amazon ECR, Artifactory, …).

### 2. Mirror every W&B chart + image into it

```bash
wsm registry mirror --to $REG --insecure --wandb-version $VER
```

This copies the operator, cert-manager, NGINX Gateway, the managed databases, and every W&B
application image into your registry.

> The `manifest push failed` line at the very end is **expected** on a plain-HTTP registry —
> the server manifest is served over HTTPS only, so you'll hand it to the operator directly in
> step 4. (On a real HTTPS registry this step just works and step 4 disappears.)

### 3. Create a local cluster and install the operator — from your registry

```bash
wsm cluster create --cluster-name wandb --insecure-registry-host $REG

kubectl --context kind-wandb apply -f \
  https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.0/standard-install.yaml

wsm deploy-v2 operator --context kind-wandb \
  --mirror-registry $REG --insecure-registry --skip-gateway-api-crds
```

`--insecure-registry-host` wires the cluster's nodes to pull from your plain-HTTP registry.

### 4. Hand the operator the server manifest, then launch W&B

```bash
# grab the published server manifest, point its image refs at your registry
docker pull --platform linux/amd64 us-docker.pkg.dev/wandb-production/public/wandb/server-manifest:$VER
cid=$(docker create --platform linux/amd64 us-docker.pkg.dev/wandb-production/public/wandb/server-manifest:$VER x)
docker cp "$cid:/manifest.yaml" . && docker cp "$cid:/sizing.yaml" . && docker rm "$cid"
sed -i '' "s#us-docker.pkg.dev/wandb-production/public/#$REG/#g" manifest.yaml   # Linux: sed -i (no '')

# mount it onto the operator so it reads the manifest from disk (file://)
kubectl create configmap wandb-manifest -n wandb-operators --from-file=manifest.yaml --from-file=sizing.yaml
CTR=$(kubectl get deploy wandb-operator -n wandb-operators -o jsonpath='{.spec.template.spec.containers[0].name}')
kubectl patch deployment wandb-operator -n wandb-operators --type strategic -p \
  "{\"spec\":{\"template\":{\"spec\":{\"volumes\":[{\"name\":\"m\",\"configMap\":{\"name\":\"wandb-manifest\"}}],\"containers\":[{\"name\":\"$CTR\",\"volumeMounts\":[{\"name\":\"m\",\"mountPath\":\"/manifests/$VER\"}]}]}}}}"
kubectl rollout status deploy/wandb-operator -n wandb-operators

# deploy the W&B instance, reading the manifest from disk
wsm deploy-v2 wandb deploy --context kind-wandb \
  --manifest-repository file:///manifests --wandb-version $VER
```

### 5. Watch it come up

```bash
kubectl get pods -n wandb -w
```

The W&B app, Weave, and the managed databases (ClickHouse, Kafka, MySQL, Redis, object store)
should all reach `Running` — **every one pulled from your local registry.**

## Tear down

```bash
kind delete cluster --name wandb
docker rm -f wandb-registry
```

## Even faster: the 3-command version

If you just want to see W&B running and don't need the air-gap demo, skip the registry and
pull from the public registries directly:

```bash
wsm cluster create --cluster-name wandb
wsm deploy-v2 operator     --context kind-wandb
wsm deploy-v2 wandb deploy --context kind-wandb --wandb-version 0.82.2
```

## What this is (and isn't)

- The **manifest handoff in step 4 exists only because this demo uses a plain-HTTP registry.**
  Any HTTPS registry (Harbor, ECR, Artifactory, or a self-signed local one) skips it entirely —
  the operator pulls the manifest directly, and the flow is simply **mirror → deploy**. That's
  the real production experience; the local demo pays a small tax for using a throwaway registry.
- This is a **local test**, not a production install. For real deployments — HTTPS registries,
  Amazon ECR, external databases, TLS, sizing, and air-gapped clusters — see the full
  [On-Prem / Air-Gapped Deployment guide](../deployment/on-prem.md).
