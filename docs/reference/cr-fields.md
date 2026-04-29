# Custom Resource Field Reference

The `WeightsAndBiases` CR (v2) defines the desired state of a W&B deployment. The operator reconciles this CR into actual Kubernetes resources.

## API Version & Kind

```yaml
apiVersion: apps.wandb.com/v2
kind: WeightsAndBiases
```

## Top-Level Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `metadata.name` | string | Yes | Name of the W&B instance |
| `metadata.namespace` | string | Yes | Kubernetes namespace |
| `spec.size` | string | No | Deployment size: `dev`, `small`, `medium`, `large`, `xlarge`, `2xlarge`, `4xlarge` |
| `spec.retentionPolicy.onDelete` | string | No | `detach` (leave infrastructure running) or `purge` (delete all managed resources) |
| `spec.wandb` | object | Yes | Core W&B application configuration |
| `spec.networking` | object | Yes | Networking and TLS configuration |
| `spec.mysql` | object | No | MySQL configuration |
| `spec.redis` | object | No | Redis configuration |
| `spec.kafka` | object | No | Kafka configuration |
| `spec.objectStore` | object | No | Object store configuration |
| `spec.clickHouse` | object | No | ClickHouse configuration |

---

## `spec.wandb`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `hostname` | string | `http://localhost:8080` | External URL for accessing W&B |
| `version` | string | Built-in stable | Server manifest version (e.g., `0.79.2`) |
| `license` | string | — | W&B license string |
| `features` | map[string]bool | `{}` | Feature flags |
| `internalServiceAuth.enabled` | bool | `false` | Enable internal service authentication |

---

## `spec.networking`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `mode` | string | `gateway` | Networking mode: `gateway` or `ingress` |
| `gatewayAPI` | object | — | Gateway API configuration (when `mode: gateway`) |
| `ingress` | object | — | Ingress configuration (when `mode: ingress`) |
| `tls` | object | — | TLS/SSL configuration |

### `spec.networking.gatewayAPI`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `gateway.managed` | bool | `true` | Operator manages the Gateway resource |
| `gateway.gatewayClassName` | string | `nginx` | Gateway class name |
| `gateway.infrastructureAnnotations` | map[string]string | — | Provider-specific annotations for the Gateway service |

### `spec.networking.ingress`

| Field | Type | Description |
|-------|------|-------------|
| `ingressClassName` | string | Name of the IngressClass to use |

### `spec.networking.tls`

| Field | Type | Description |
|-------|------|-------------|
| `secretName` | string | Name of the Kubernetes Secret containing the TLS certificate |
| `certManager.issuer` | string | Name of the cert-manager Issuer |
| `certManager.kind` | string | Type of issuer: `Issuer` only (default: `Issuer`) |

---

## `spec.mysql`

| Field | Type | Description |
|-------|------|-------------|
| `managedMysql` | object | Configuration for operator-managed MySQL |
| `managedMysql.telemetry.enabled` | bool | Enable MySQL telemetry |
| `external` | object | (Alternative) External MySQL connection details |

---

## `spec.redis`

| Field | Type | Description |
|-------|------|-------------|
| `managedRedis` | object | Configuration for operator-managed Redis |
| `managedRedis.telemetry.enabled` | bool | Enable Redis telemetry |
| `external` | object | (Alternative) External Redis connection details |

---

## `spec.kafka`

| Field | Type | Description |
|-------|------|-------------|
| `managedKafka` | object | Configuration for operator-managed Kafka |
| `managedKafka.telemetry.enabled` | bool | Enable Kafka telemetry |
| `external` | object | (Alternative) External Kafka connection details |

---

## `spec.objectStore`

| Field | Type | Description |
|-------|------|-------------|
| `managedObjectStore` | object | Configuration for operator-managed object storage |
| `managedObjectStore.telemetry.enabled` | bool | Enable object store telemetry |
| `external` | object | (Alternative) External S3/GCS/Azure Blob connection details |

---

## `spec.clickHouse`

| Field | Type | Description |
|-------|------|-------------|
| `managedClickHouse` | object | Configuration for operator-managed ClickHouse |
| `managedClickHouse.telemetry.enabled` | bool | Enable ClickHouse telemetry |
| `external` | object | (Alternative) External ClickHouse connection details |

---

## Complete Example

```yaml
apiVersion: apps.wandb.com/v2
kind: WeightsAndBiases
metadata:
  name: wandb
  namespace: wandb
spec:
  size: medium
  retentionPolicy:
    onDelete: detach
  wandb:
    hostname: https://wandb.example.com
    version: "0.79.2"
    license: "YOUR_LICENSE_HERE"
    features:
      artifacts: true
    internalServiceAuth:
      enabled: false
  networking:
    mode: gateway
    gatewayAPI:
      gateway:
        managed: true
        gatewayClassName: nginx
        infrastructureAnnotations:
          service.beta.kubernetes.io/aws-load-balancer-scheme: internet-facing
    tls:
      secretName: wandb-tls-secret
      certManager:
        issuer: letsencrypt-prod
        kind: Issuer
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
