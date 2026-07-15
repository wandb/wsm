# Sizing Reference

The `--size` flag selects a resource profile that is applied across all components of a W&B deployment (app, MySQL, Redis, Kafka, ClickHouse, etc.). Actual resource allocations, storage volumes, replicas, and autoscaling behavior are defined in the server manifest and may vary between W&B versions.

> **Before changing sizes, read the following important caveats:**
>
> ### Dev Size
> Changing **to or from** the `dev` size is **not supported** after initial deployment. The `dev` profile uses a fundamentally different topology that cannot be migrated non-destructively. If you need to switch to or from `dev`, you must destroy and redeploy the W&B instance, which may result in data loss.
>
> ### MySQL
> The MySQL operator used by W&B does **not** support automatic PVC resizing. If a size change requires larger MySQL volumes, the MySQL PVCs may need to be manually resized or recreated.
>
> ### Volume Expansion
> Resizing may require increasing PersistentVolumeClaim capacity. This will only succeed if the underlying StorageClass has **`allowVolumeExpansion: true`**. If volume expansion is not enabled, PVC resize operations will fail.

---

## Available Sizes

The following size profiles are typically available. The exact set depends on the W&B server manifest version you deploy.

| Profile | Recommended For | Notes |
|---------|----------------|-------|
| `dev` | Local development, single user | All services may be consolidated; minimal resource footprint. Not for production. |
| `micro` | Lightweight testing / CI | Smaller than `small`; reduced storage and resource footprint for short-lived or evaluation deployments. |
| `small` | Small team, proof of concept | Default profile for new deployments. Sufficient for light usage and evaluation. |
| `medium` | Growing team | Increased resources and storage for more concurrent users and larger workloads. |
| `large` | Multi-team organization | Dedicated resources per component; higher throughput and reliability. |
| `xlarge` | Large organization | High availability with replicas; horizontal scaling for stateless services. |
| `2xlarge` | Enterprise scale | Maximum throughput and replication across all data stores. |

> Some server manifests may include additional profiles beyond those listed above. Check the manifest for your specific `--wandb-version` to see all available sizes.

## Selecting a Size

Using the `wsm` CLI:

```bash
wsm deploy-v2 wandb deploy \
  --context <ctx> \
  --size medium
```

Using a custom CR:

```yaml
spec:
  size: medium
```

## How Sizing Works

Each managed component in the W&B stack (MySQL, Redis, Kafka, ClickHouse, Object Store, and the applications) has its own sizing configuration within the server manifest keyed by profile name. When you specify `--size medium`, the operator applies the `medium` configuration for every component uniformly.

This typically affects:
- **CPU / memory requests and limits**
- **Replica counts** (for stateless services and some data stores)
- **Persistent volume sizes**
- **Autoscaling thresholds** (min/max replicas)
- **Sharding configuration** (for ClickHouse)

Because these values come from the server manifest, upgrading to a newer `--wandb-version` may change the resource allocations even if you keep the same `--size`.

## Node Requirements

| Size | Minimum Nodes | Recommended Node Size |
|------|--------------|----------------------|
| dev | 1 (Kind) | 4 vCPU, 8 GB RAM |
| micro | 1–2 | 4 vCPU, 8 GB RAM |
| small | 3 | 4 vCPU, 16 GB RAM |
| medium | 3 | 8 vCPU, 32 GB RAM |
| large | 3–5 | 16 vCPU, 64 GB RAM |
| xlarge | 5–7 | 16–32 vCPU, 64–128 GB RAM |
| 2xlarge | 7–10 | 32 vCPU, 128 GB RAM |

> These are rough estimates for typical configurations. Actual requirements depend on the specific server manifest and workload characteristics.

## Storage

All managed data stores use PersistentVolumeClaims. The volume sizes specified in the manifest for each size profile determine the initial PVC capacity.

- Ensure your cluster has a default StorageClass capable of provisioning the required capacity.
- Storage capacity varies significantly by component and size profile (e.g., ClickHouse volumes are typically larger than Redis volumes).

## Changing Size After Deployment

You can change the size by re-deploying with a different `--size`:

```bash
wsm deploy-v2 wandb deploy \
  --context <ctx> \
  --wandb-name wandb \
  --wandb-namespace wandb \
  --size large
```

The operator will perform a rolling update where possible. Be sure to review the important caveats at the top of this page before resizing.

## See Also

- [Customizing the Deployment](../configuration/customizing.md)
- [Managing the Deployment](../operations/managing.md)
