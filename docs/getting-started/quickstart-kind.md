# Quick Start — Local Kind Cluster

Get a local W&B deployment running in under 5 minutes using a WSM-provisioned [Kind](https://kind.sigs.k8s.io/) cluster.

## What Gets Installed

Running the quick-start command will automatically create and configure:

1. A **Kind cluster** with ingress-ready labels and host port mappings
2. **cert-manager** — for TLS certificate management
3. **nginx-gateway-fabric** — Gateway API controller
4. **W&B Operator** (v2) — manages the W&B custom resource
5. **W&B Instance** — the full application stack (MySQL, Redis, Kafka, Object Store, ClickHouse)

## Prerequisites

- [WSM installed](./installation.md)
- [Docker](https://docs.docker.com/get-docker/) running — Kind uses Docker as its container runtime. **Podman is not sufficient.** On RHEL/CentOS, Docker is not installed by default; see [Installation → Linux Dependencies](./installation.md#linux-dependencies) for setup.
- [`kubectl`](https://kubernetes.io/docs/tasks/tools/) — used by the [Monitor the Deployment](#monitor-the-deployment) steps below.
- Port `8080` and `8443` available on your machine

## Deploy

Run the following single command:

```bash
wsm deploy-v2 operator \
  --setup-k8s-cluster \
  --cluster-name wandb \
  --include-cr \
  --size dev \
  --context kind-wandb
```

### What the flags do

| Flag | Description |
|------|-------------|
| `--setup-k8s-cluster` | Creates a new Kind cluster named `kind` before deploying |
| `--include-cr` | Also deploys the `WeightsAndBiases` custom resource (the actual W&B instance) |
| `--size dev` | Sets the size of the `WeightsAndBiases` deployment to dev |
| `--context kind-wandb` | Uses the `kind-wandb` kubectl context (auto-created by Kind) |

## Monitor the Deployment

WSM will print progress as it installs each component. The full process typically takes 3–5 minutes.

After WSM reports `Deployment complete!`, check that everything is running:

```bash
# Check the W&B CR status
kubectl get wandb -n wandb

# Check all pods in the W&B namespace
kubectl get pods -n wandb

# Check operator pods
kubectl get pods -n wandb-operators
```

## Access W&B

By default, the W&B instance is available at:

```
http://localhost:8080
```

> Note: By default, the quick-start deployment uses HTTP (not HTTPS). This is suitable for local testing. For production or shared environments, see [SSL / TLS Configuration](../configuration/ssl-tls.md) to configure HTTPS.

## Default Configuration

The quick-start deployment uses these defaults:

| Setting | Default Value |
|---------|--------------|
| **Hostname** | `http://localhost:8080` |
| **Instance Name** | `wandb` |
| **Namespace** | `wandb` |
| **Operator Namespace** | `wandb-operators` |
| **Size** | `small` |
| **Version** | `0.79.2` (latest stable) |
| **Gateway Class** | `nginx` |

## Troubleshooting

### Deploy fails: `stale GroupVersion discovery: metrics.k8s.io/v1beta1`

If the deploy aborts at the gateway-API CRD check with:

```
failed to check if gateway api crds exist: unable to retrieve the complete list of
server APIs: metrics.k8s.io/v1beta1: stale GroupVersion discovery: metrics.k8s.io/v1beta1
```

the cluster's metrics-server `APIService` is registered but not yet `Available`, so API discovery returns a partial result. Remove the unavailable aggregated API and re-run against the existing cluster (omit `--setup-k8s-cluster` so it isn't recreated):

```bash
kubectl --context kind-wandb delete apiservice v1beta1.metrics.k8s.io
wsm deploy-v2 operator --include-cr --size dev --context kind-wandb
```

## Next Steps

- Customize the deployment: [SSL / TLS Configuration](../configuration/ssl-tls.md)
- Deploy to a cloud provider:
  - [Google GKE](../deployment/gke.md)
  - [Amazon EKS](../deployment/eks.md)
  - [Azure AKS](../deployment/aks.md)
- [Manage or destroy the deployment](../operations/managing.md)
