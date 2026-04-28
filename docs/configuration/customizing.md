# Customizing the Deployment

WSM provides flags for common configuration needs. For advanced use cases, you can provide a fully custom `WeightsAndBiases` CR YAML file.

## Sizing Profiles

Use the `--size` flag to select a resource profile for your W&B deployment:

| Profile | Recommended For | Description |
|---------|----------------|-------------|
| `dev` | Local testing, single user | Minimal resources, all services on shared nodes |
| `small` | Small team, early production | Default profile, suitable for ~10–50 users |
| `medium` | Growing team | Increased CPU/memory for core services |
| `large` | Multi-team organization | Dedicated resources per service |
| `xlarge` | Large organization | High availability, horizontal scaling enabled |
| `2xlarge` | Enterprise | Maximum throughput, replicated data stores |
| `4xlarge` | Maximum scale | Full isolation, extensive resource allocation |

```bash
wsm deploy-v2 wandb deploy \
  --context <ctx> \
  --size medium
```

## Common Deployment Flags

### Identity & Licensing

| Flag | Default | Description |
|------|---------|-------------|
| `--wandb-name` | `wandb` | Name of the W&B instance (used in resource names) |
| `--wandb-namespace` | `wandb` | Kubernetes namespace for the W&B CR |
| `--license` | — | W&B license string (quoted) |
| `--license-file` | — | Path to a file containing the W&B license |

> You cannot use both `--license` and `--license-file` simultaneously.

### Version & Hostname

| Flag | Default | Description |
|------|---------|-------------|
| `--wandb-hostname` | `http://localhost:8080` | External URL for accessing W&B |
| `--wandb-version` | `0.79.2` | Server manifest version to deploy |

### Networking

| Flag | Default | Description |
|------|---------|-------------|
| `--gateway-class` | `nginx` | Gateway class name (Gateway API mode) |
| `--ingress-class` | — | Ingress class name (mutually exclusive with `--gateway-class`) |
| `--create-ca` | `true` | Automatically create a self-signed CA issuer for TLS |
| `--issuer-name` | — | cert-manager Issuer name for TLS (requires `--create-ca=false`) |
| `--add-ingress-annotations` | `false` | Add cloud-provider annotations for load balancers |

### Operations & Telemetry

| Flag | Default | Description |
|------|---------|-------------|
| `--observability-mode` | `off` | Enable telemetry for managed MySQL, Redis, Kafka, etc. (`off` or `on`) |
| `--retention-policy` | `detach` | Behavior when deleting the CR: `detach` (leave infrastructure running) or `purge` (delete all managed resources and PVCs) |

## Using a Custom CR File

For advanced configuration, provide your own `WeightsAndBiases` CR:

```bash
wsm deploy-v2 wandb deploy \
  --context <ctx> \
  --cr-file my-custom-cr.yaml
```

When using `--cr-file`, flag-based overrides still apply for overlapping fields. The precedence order is:

1. Command-line flags (highest)
2. Values in `--cr-file`
3. WSM built-in defaults (lowest)

### Example Custom CR

```yaml
apiVersion: apps.wandb.com/v2
kind: WeightsAndBiases
metadata:
  name: wandb
  namespace: wandb
spec:
  size: medium
  wandb:
    hostname: https://wandb.example.com
    version: "0.79.2"
    license: "YOUR_LICENSE_HERE"
  networking:
    mode: gateway
    gatewayAPI:
      gateway:
        managed: true
        gatewayClassName: nginx
    tls:
      secretName: wandb-tls-secret
      certManager:
        issuer: letsencrypt-prod
        kind: Issuer
  retentionPolicy:
    onDelete: detach
  mysql:
    managedMysql:
      telemetry:
        enabled: true
  redis:
    managedRedis:
      telemetry:
        enabled: true
  kafka:
    managedKafka:
      telemetry:
        enabled: true
  objectStore:
    managedObjectStore:
      telemetry:
        enabled: true
  clickHouse:
    managedClickHouse:
      telemetry:
        enabled: true
```

## Networking Modes

### Gateway API (Default & Recommended)

This is the default when using `--gateway-class` (or omitting it, which defaults to `nginx`).

WSM configures the CR to create a managed Gateway resource. The operator reconciles Gateway, HTTPRoute, and other Gateway API resources automatically.

```bash
wsm deploy-v2 wandb deploy \
  --context <ctx> \
  --gateway-class nginx
```

### Ingress (Alternative)

If your environment requires traditional Ingress resources:

```bash
wsm deploy-v2 wandb deploy \
  --context <ctx> \
  --ingress-class nginx
```

> `--gateway-class` and `--ingress-class` are mutually exclusive.

## Observability

Enable telemetry for all managed data stores:

```bash
wsm deploy-v2 wandb deploy \
  --context <ctx> \
  --observability-mode on
```

This sets `telemetry.enabled: true` for MySQL, Redis, Kafka, Object Store, and ClickHouse.

## Retention Policies

| Policy | Behavior on CR Deletion |
|--------|------------------------|
| `detach` (default) | Deletes the W&B CR but leaves all managed infrastructure (MySQL, Kafka, ClickHouse, Redis, Object Store) running with their data intact |
| `purge` | Deletes the W&B CR **and** all managed infrastructure resources (StatefulSets, Deployments, PVCs, Services, Secrets) — data is permanently lost |

```bash
wsm deploy-v2 wandb deploy \
  --context <ctx> \
  --retention-policy purge
```

## Operator Deployment Flags

When running `wsm deploy-v2 operator`, you can control infrastructure installation:

| Flag | Default | Description |
|------|---------|-------------|
| `--install-cert-manager` | `auto` | `auto` (detect/reuse), `true` (force install), `false` (skip) |
| `--install-nginx-gateway` | `auto` | `auto` (detect/reuse), `true` (force install), `false` (skip) |
| `--enable-gateway-api` | `true` | Enable Gateway API support in cert-manager |
| `--operator-chart-version` | `2.0.0-alpha.1` | Operator Helm chart version |
| `--operator-namespace` | `wandb-operators` | Namespace for the operator |

### Example: Use Existing cert-manager

```bash
wsm deploy-v2 operator \
  --context <ctx> \
  --install-cert-manager false \
  --install-nginx-gateway true
```

## See Also

- [SSL / TLS Configuration](ssl-tls.md)
- [Managing the Deployment](../operations/managing.md)
- [CR Field Reference](../reference/cr-fields.md)
- [Sizing Reference](../reference/sizing.md)
