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
| `--operator-chart-version` | `2.0.0-beta.1` | Operator Helm chart version |
| `--operator-version` | — | Operator image version (defaults to chart value) |
| `--operator-namespace` | `wandb-operators` | Namespace for the operator |
| `--install-cert-manager` | `auto` | Cert-manager install mode: `auto`, `true`, `false` |
| `--install-nginx-gateway` | `auto` | Nginx-gateway-fabric install mode: `auto`, `true`, `false` |
| `--enable-gateway-api` | `true` | Enable Gateway API support in cert-manager |
| `--include-cr` | `false` | Also deploy the W&B CR in the same command |
| `--mirror-registry` | — | Pull every chart and image from this registry (e.g. `harbor.corp:5443`). Populate it first with `wsm registry mirror --to <same-host>`. See [On-Prem Deployment](../deployment/on-prem.md). |
| `--insecure-registry` | `false` | Use plain HTTP / skip TLS verification when fetching from `--mirror-registry`. Required for plain-HTTP `registry:2`; **never** in production. |

#### Inherited Flags (when `--include-cr` is used)

All flags from `wsm deploy-v2 wandb deploy` are also accepted.

#### Examples

```bash
# Install just the operator (+ cert-manager, nginx-gateway) into an existing cluster
wsm deploy-v2 operator --context my-cluster

# One shot: create a local Kind cluster, install the operator, and deploy the CR
wsm deploy-v2 operator --context kind-wandb \
  --setup-k8s-cluster --cluster-name wandb --include-cr --size dev

# Air-gapped: pull every chart and image from a mirror registry
# (populate it first with `wsm registry mirror --to harbor.corp:5443`)
wsm deploy-v2 operator --context prod --mirror-registry harbor.corp:5443
```

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
| `--size` | `small` | Deployment size profile: `dev`, `micro`, `small`, `medium`, `large`, `xlarge`, `xxlarge` |
| `--license` | — | W&B license string |
| `--license-file` | — | Path to a file containing the W&B license |
| `--oidc-client-id` | — | OIDC client ID as `<secret-name>:<key>` (`spec.wandb.oidc.clientId`). Optional; leave unset to disable OIDC. Ignored for any leaf already set via `--cr-file` |
| `--oidc-client-secret` | — | OIDC client secret as `<secret-name>:<key>` (`spec.wandb.oidc.clientSecret`) |
| `--oidc-issuer-url` | — | OIDC issuer URL as `<secret-name>:<key>` (`spec.wandb.oidc.issuerUrl`) |
| `--oidc-auth-method` | — | OIDC auth method as `<secret-name>:<key>` (`spec.wandb.oidc.authMethod`) |
| `--oidc-session-length` | — | OIDC session length, e.g. `720h` (`spec.wandb.oidc.sessionLength`). Optional; `--cr-file` wins if it already set the value |
| `--image-registry` | — | Retarget container images to this registry for air-gapped installs (`spec.global.imageRegistry`) |
| `--custom-ca-cert-file` | — | Path to a PEM CA certificate to trust in W&B workloads; repeatable, each file's contents is appended to `spec.global.customCACerts` |
| `--custom-ca-configmap` | — | Name of a ConfigMap holding CA certificates to trust in W&B workloads (`spec.global.caCertsConfigMap`) |
| `--objectstore-copies` | — | Managed object store replica copies (`spec.objectStore.managedObjectStore.copies`). Operator default applies when unset. Applies to the default managed instance only (see note below) |
| `--cr-set` | — | Set an arbitrary CR field as `<path>=<value>`, e.g. `spec.wandb.version=0.82.2`; repeatable. Values are YAML-typed (`3`→number, `true`→bool, `[a,b]`→list). Overrides the built-in template, `--cr-file`, and the typed flags above (see note below) |
| `--gateway-class` | `nginx` | Gateway class name (selects Gateway API mode; the default). Mutually exclusive with `--ingress-class` |
| `--ingress-class` | — | Ingress class name (selects Ingress mode). Takes precedence over the default `--gateway-class`; setting both explicitly is an error |
| `--ingress-name` | — | Override the generated Ingress resource name (defaults to the CR name) |
| `--create-ca` | `true` | Create a self-signed CA issuer for TLS |
| `--issuer-name` | — | cert-manager Issuer name for TLS (requires `--create-ca=false`) |
| `--create-aws-ingress-class` | `false` | Create an AWS ALB IngressClass (requires `--ingress-class`) |
| `--create-aws-storage-class` | `false` | Create a default AWS `gp3` StorageClass |
| `--add-ingress-annotations` | `false` | Add AWS load-balancer annotations to the managed Gateway (**Gateway API mode only**; ignored in Ingress mode) |
| `--observability-mode` | `off` | Telemetry mode: `off`, `full` (in-cluster Victoria Metrics stack **+ local Grafana**), or `forward` (Victoria stack + forward OTLP externally) |
| `--observability-forward-endpoint` | — | OTLP endpoint to forward telemetry to. **Required** when `--observability-mode=forward` |
| `--observability-otel-secret` | — | Name of the OTEL connection secret (`telemetry.otel.secretName`). Chart default `wandb-otel-connection` if unset. Applied when mode is `full` or `forward` |
| `--observability-otel-protocol` | — | OTEL exporter protocol, e.g. `http/protobuf` or `grpc` (`telemetry.otel.protocol`). Chart default if unset |
| `--observability-otel-service-name` | — | OTEL `service.name` resource attribute (`telemetry.otel.serviceName`). Chart default if unset |
| `--observability-otel-resource-attributes` | — | Additional OTEL resource attributes, comma-separated `key=value` (`telemetry.otel.resourceAttributes`). Chart default if unset |
| `--observability-forward-protocol` | — | OTLP forwarding protocol, e.g. `http/protobuf` or `grpc` (`telemetry.forwarding.otlp.protocol`). Only applied when `--observability-mode=forward` |
| `--observability-forward-headers` | — | OTLP forwarding headers as repeatable `key=value` pairs, e.g. `Authorization=Bearer …` (`telemetry.forwarding.otlp.headers`). Only applied when `--observability-mode=forward` |
| `--retention-policy` | `detach` | Behavior on CR deletion: `detach` (leave infrastructure running) or `purge` (delete all managed resources and PVCs) |
| `--wait` | `false` | Wait for the W&B instance to report Ready |

> **Default managed instance.** Managed `mysql`, `redis`, `objectStore`, and `clickHouse` are keyed by instance name; `wsm` builds a single instance under the reserved key `default`. Flags that tune managed infra — `--observability-mode` (per-service telemetry) and `--objectstore-copies` — only affect that `default` instance. To run multiple instances or tune a differently-keyed one, supply the full shape via `--cr-file`.

> **Setting arbitrary CR fields with `--cr-set`.** Rather than adding a dedicated flag for every CR field, `--cr-set <path>=<value>` sets any field on the CR by its dotted path. It applies last — after the built-in template, `--cr-file`, and the typed flags — so it always wins, and the operator's CRD validates the result on apply. Use it for fields without a dedicated flag; use `--cr-file` for large or deeply-nested shapes. Values are parsed as YAML, so types infer automatically. List **indices** are not addressable (set the whole list, or use `--cr-file`). Example:
>
> ```bash
> wsm deploy-v2 wandb deploy --context <ctx> \
>   --cr-set spec.objectStore.default.seaweedObjectStore.filerStorageSize=50Gi \
>   --cr-set spec.wandb.additionalHostnames='[wandb.corp.example.com]'
> ```

> **Observability.** `--observability-mode` is applied to the operator chart during `wsm deploy-v2 operator` (it enables the `victoria-metrics-operator` and, for `full`, the `grafana-operator` dependencies the chart requires) and also toggles per-service telemetry on the CR. `full` deploys Grafana and the Victoria Metrics/Logs/Traces stack as ClusterIP services in the W&B namespace — view Grafana with [`wsm telemetry grafana`](#wsm-telemetry) and VictoriaMetrics with [`wsm telemetry victoria`](#wsm-telemetry). `forward` ships OTLP data to `--observability-forward-endpoint` and does not run Grafana (VMUI is still available via `wsm telemetry victoria`).

#### Examples

```bash
# Minimal deploy against an already-installed operator
wsm deploy-v2 wandb deploy --context prod

# Pin a specific server version (must be >= the minimum supported version)
wsm deploy-v2 wandb deploy --context prod --wandb-version 0.82.2

# TLS with a self-signed CA (https hostname triggers cert-manager wiring)
wsm deploy-v2 wandb deploy --context prod \
  --wandb-hostname https://wandb.example.com --create-ca

# OIDC, each leaf sourced from a Secret as <secret-name>:<key>
wsm deploy-v2 wandb deploy --context prod \
  --wandb-hostname https://wandb.example.com --create-ca \
  --oidc-client-id wandb-oidc:clientId \
  --oidc-client-secret wandb-oidc:clientSecret \
  --oidc-issuer-url wandb-oidc:issuerUrl \
  --oidc-session-length 720h

# Set a CR field that has no dedicated flag, via --cr-set (repeatable, YAML-typed)
wsm deploy-v2 wandb deploy --context prod \
  --cr-set spec.wandb.additionalHostnames='[wandb.corp.example.com]' \
  --cr-set spec.objectStore.default.seaweedObjectStore.filerStorageSize=50Gi

# Advanced shapes: hand the whole CR in a file
wsm deploy-v2 wandb deploy --context prod --cr-file ./my-wandb.yaml
```

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
| `--kind-node-image` | — | Override the Kind node image (e.g. point at a mirrored `kindest/node` for offline cluster bootstrap) |
| `--insecure-registry-host` | — | Configure containerd to pull from this host over plain HTTP (e.g. `host.docker.internal:5000`). Pairs with `wsm registry mirror --insecure` for local-laptop testing against a plain-HTTP `registry:2`. See [On-Prem Deployment](../deployment/on-prem.md). |

#### Examples

```bash
# Single-node cluster with default ports
wsm cluster create --cluster-name wandb

# Use higher host ports when 8080/8443 are taken (e.g. OrbStack/Docker Desktop proxies)
wsm cluster create --cluster-name wandb --http-port 18080 --https-port 18443
```

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

## `wsm registry`

Tools for mirroring W&B's install artifacts to a customer-controlled container registry. Pair with `wsm deploy-v2 operator --mirror-registry <host>` for on-prem / air-gapped installs.

See [On-Prem Deployment](../deployment/on-prem.md) for the end-to-end walkthrough.

### `wsm registry mirror`

Pulls every chart and image required by `wsm deploy-v2 operator` from its upstream source and pushes a copy to the customer's mirror.

```bash
wsm registry mirror --to <host> [flags]
```

Scope today: the operator OCI chart + binary image, cert-manager OCI chart + 5 component images, nginx-gateway-fabric OCI chart + 2 images (control plane + data plane). W&B server manifest, application images, and subchart controller images are upcoming iterations.

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--to` | — | **Required.** Hostname of your mirror, e.g. `harbor.example.com` or `localhost:5000`. |
| `--insecure` | `false` | Skip TLS verification when pushing to the mirror. Use for plain-HTTP registries like a local `registry:2`. **Never** in production. |
| `--dry-run` | `false` | Print the source → target mirroring plan without pushing. |
| `--operator-chart-version` | `2.0.0-beta.1` | Operator chart version; also used as the tag for the operator binary image. Match this to the version you'll pass to `wsm deploy-v2 operator`. |

Auth is read from your Docker config (`~/.docker/config.json`). Run `docker login <mirror-host>` before this command for any registry that requires credentials.

### `wsm registry check`

Verifies that every required image is present in your mirror.

```bash
wsm registry check --registry <host> [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--registry` | — | **Required.** Hostname of your mirror to check against. |
| `--insecure` | `false` | Skip TLS verification when contacting the registry. |
| `--fail-on-missing` | `false` | Exit non-zero if any image is missing. |

### `wsm registry values`

Emits a `values.yaml` fragment that overrides each image reference to use your mirror instead of the upstream source. Used by the legacy v1 install flow; not required for the v2 `--mirror-registry` path.

```bash
wsm registry values --registry <host> [-o overrides.yaml]
```

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

### `wsm telemetry`

View the in-cluster telemetry UIs deployed by the v2 operator. The telemetry stack is deployed when the operator is installed with `--observability-mode=full` (Grafana + Victoria stack) or `forward` (Victoria stack only). The services are ClusterIP-only, so each subcommand port-forwards to a service and opens it in your browser.

> **Requires `kubectl` on your PATH.** Unlike the other v2 commands (which talk to the cluster via client-go), `wsm telemetry` shells out to `kubectl port-forward` so kubectl handles Service→Pod resolution and stream negotiation. If kubectl isn't installed, the command errors with the equivalent `kubectl port-forward` invocation you can run manually.

```bash
wsm telemetry grafana  --context <kubeconfig-context>   # Grafana dashboards (localhost:3000)
wsm telemetry victoria --context <kubeconfig-context>   # VictoriaMetrics VMUI (localhost:8428/vmui/)
```

#### Persistent flags (both subcommands)

| Flag | Default | Description |
|------|---------|-------------|
| `--context` | — | Name of the kubeconfig context to use |
| `--wandb-namespace` | `wandb` | Namespace where the telemetry stack is deployed |

#### `wsm telemetry grafana`

| Flag | Default | Description |
|------|---------|-------------|
| `--service` | `grafana-service` | Grafana service name to forward |
| `--local-port` / `--remote-port` | `3000` | Local / service port |
| `--no-browser` | `false` | Do not open a browser automatically |

#### `wsm telemetry victoria`

| Flag | Default | Description |
|------|---------|-------------|
| `--service` | `vmsingle-victoria-instance` | VictoriaMetrics single service name to forward |
| `--local-port` / `--remote-port` | `8428` | Local / service port |
| `--no-browser` | `false` | Do not open a browser automatically |

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
