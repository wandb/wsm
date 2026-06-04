# Command Reference

Complete reference for all `wsm` commands, flags, and options.

---

## Global Flags

WSM commands do not have top-level persistent flags. Each command family defines its own flags.

---

## `wsm deploy-v2`

Deploys the W&B operator (v2) and W&B instances.

### `wsm deploy-v2 operator`

Deploys the W&B operator and its dependencies (cert-manager, nginx-gateway-fabric).

```bash
wsm deploy-v2 operator [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--context` | — | **Required.** Name of the kubeconfig context to use |
| `--setup-k8s-cluster` | `false` | Create a Kind cluster before deploying |
| `--cluster-name` | `kind` | Name of the Kind cluster (used with `--setup-k8s-cluster`) |
| `--workers` | `0` | Number of Kind worker nodes |
| `--operator-chart-version` | `2.0.0-alpha.2` | Operator Helm chart version |
| `--operator-version` | — | Operator image version (defaults to chart value) |
| `--operator-namespace` | `wandb-operators` | Namespace for the operator |
| `--install-cert-manager` | `auto` | Cert-manager install mode: `auto`, `true`, `false` |
| `--install-nginx-gateway` | `auto` | Nginx-gateway-fabric install mode: `auto`, `true`, `false` |
| `--enable-gateway-api` | `true` | Enable Gateway API support in cert-manager |
| `--include-cr` | `false` | Also deploy the W&B CR in the same command |

#### Inherited Flags (when `--include-cr` is used)

All flags from `wsm deploy-v2 wandb deploy` are also accepted.

---

### `wsm deploy-v2 wandb deploy`

Deploys a W&B instance (WeightsAndBiases CR).

```bash
wsm deploy-v2 wandb deploy [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--context` | — | **Required.** Name of the kubeconfig context to use |
| `--cr-file` | — | Path to a custom WeightsAndBiases CR YAML file |
| `--wandb-name` | `wandb` | Name of the W&B instance |
| `--wandb-namespace` | `wandb` | Kubernetes namespace for the CR |
| `--wandb-hostname` | `http://localhost:8080` | External URL for accessing W&B |
| `--wandb-version` | — | Server manifest version (defaults to built-in stable version) |
| `--size` | `small` | Deployment size profile: `dev`, `small`, `medium`, `large`, `xlarge`, `2xlarge`, `4xlarge` |
| `--license` | — | W&B license string |
| `--license-file` | — | Path to a file containing the W&B license |
| `--gateway-class` | `nginx` | Gateway class name for Gateway API mode |
| `--ingress-class` | — | Ingress class name (mutually exclusive with `--gateway-class`) |
| `--create-ca` | `true` | Create a self-signed CA issuer for TLS |
| `--issuer-name` | — | cert-manager Issuer name for TLS (requires `--create-ca=false`) |
| `--create-aws-ingress-class` | `false` | Create an AWS ALB IngressClass (requires `--ingress-class`) |
| `--create-aws-storage-class` | `false` | Create a default AWS `gp3` StorageClass |
| `--add-ingress-annotations` | `false` | Add AWS load-balancer annotations |
| `--observability-mode` | `off` | Telemetry mode for managed services: `off`, `full` (in-cluster Victoria + Grafana), or `forward` (Victoria stack + external OTLP forwarding) |
| `--retention-policy` | `detach` | Behavior on CR deletion: `detach` (leave infrastructure running) or `purge` (delete all managed resources and PVCs) |
| `--wait` | `false` | Wait for the W&B instance to report Ready |

---

### `wsm deploy-v2 wandb destroy`

Destroys a W&B instance.

```bash
wsm deploy-v2 wandb destroy [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--context` | — | **Required.** Name of the kubeconfig context to use |
| `--wandb-name` | `wandb` | Name of the W&B instance to destroy |
| `--wandb-namespace` | `wandb` | Namespace of the W&B instance |

> This command requires a WSM deployment marker for the CR. It will refuse to delete CRs not managed by WSM.

---

### `wsm deploy-v2 wandb get-ca-cert`

Retrieves the W&B internal CA certificate to a local file.

```bash
wsm deploy-v2 wandb get-ca-cert [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--context` | — | **Required.** Name of the kubeconfig context to use |
| `--wandb-name` | `wandb` | Name of the W&B instance |
| `--wandb-namespace` | `wandb` | Namespace of the W&B instance |
| `--output-dir` | `.` | Directory to write the CA certificate file (`<name>.crt`) |

---

## `wsm cluster`

Manages local Kind clusters.

### `wsm cluster create`

Creates a new Kind cluster.

```bash
wsm cluster create [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cluster-name` | `kind` | Name of the Kind cluster |
| `--workers` | `0` | Number of worker nodes |
| `--http-port` | `8080` | Host port mapped to HTTP ingress |
| `--https-port` | `8443` | Host port mapped to HTTPS ingress |

---

### `wsm cluster destroy`

Destroys a WSM-managed Kind cluster.

```bash
wsm cluster destroy [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cluster-name` | `kind` | Name of the Kind cluster to delete |

> Only clusters created by WSM (verified by deployment marker) can be destroyed with this command.

---

### `wsm cluster cleanup`

Deletes **all** WSM-managed resources from a cluster.

```bash
wsm cluster cleanup [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--context` | — | **Required.** Name of the kubeconfig context to use |

This removes:
- W&B CRs and their namespaces
- W&B operator releases
- cert-manager
- nginx-gateway-fabric
- WSM deployment markers

> Resources without WSM markers are not deleted.

---

## Legacy Commands

### `wsm deploy` (Legacy v1 Operator)

For deploying the legacy v1 operator. The subcommands are:

| Subcommand | Description |
|------------|-------------|
| `operator` | Deploy the legacy W&B operator |
| `chart-cm` | Deploy the Helm chart as a ConfigMap |
| `wandb-cr` | Deploy the WeightsAndBiases Custom Resource (v1) |

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-n, --namespace` | `wandb` | Namespace to deploy into |
| `-c, --chart` | — | Path to a custom W&B Helm chart |
| `-v, --values` | — | Path to a values file |

---

### `wsm console`

Opens the W&B admin console in your default browser.

```bash
wsm console
```

This command port-forwards the `wandb-console` service on `localhost:8082` and opens the login URL.

---

## Flag Precedence

When multiple configuration sources specify the same value, the resolution order is:

1. Command-line flags (highest priority)
2. Custom CR file (`--cr-file`)
3. WSM built-in defaults (lowest priority)

---

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | General error (deployment failure, missing prerequisites) |
| Other | Subcommand-specific errors (refer to error message)
