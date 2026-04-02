# Weights & Biases Server Manager

WSM is a command-line tool designed to manage W&B server deployments.
It simplifies the process of deploying, upgrading, and
maintaining W&B server instances for airgapped environments and local development.

## Install

Operator v2 compatible builds are only available from source at the moment  
`pkg-config` is required for installation, run the following command to install it
```bash
brew install pkg-config gpgme
```
Install WSM from source 
```bash
git clone https://github.com/wandb/wsm
cd wsm
git checkout operator-v2
go build -o wsm ./cmd/wsm
./wsm --help
[optional] sudo mv wsm /usr/local/bin/wsm
```
or if you have sudo permissions
```aiignore
sudo make install
```

## Usage

WSM provides several commands for managing W&B deployments:

### Commands

- [`wsm list`](#wsm-list): List images required for deployment.
- [`wsm download`](#wsm-download): Download images and dependencies for airgapped environments.
- [`wsm deploy`](#wsm-deploy): Deploy W&B operator and resources.
- [`wsm deploy-v2`](#wsm-deploy-v2): Deploy v2 W&B operator and resources.
- [`wsm cluster`](#wsm-cluster): Manage Kubernetes clusters for local testing.
- [`wsm console`](#wsm-console): Open the W&B console.

---

### `wsm list`

List the container images required for a Weights & Biases deployment. This is useful for pre-pulling images or preparing for an airgapped installation.

**Usage:**
```bash
wsm list [flags]
```

**Flags:**
- `-p, --platform string`: Platform to list images for (default "linux/amd64").

---

### `wsm download`

Download images and dependencies required for airgapped environments. This command pulls the necessary container images and saves them as a bundle.

**Usage:**
```bash
wsm download [flags]
```

**Flags:**
- `-p, --platform string`: Platform to download images for (default "linux/amd64").

---

### `wsm deploy`

Deploy the Weights & Biases operator and resources to a Kubernetes cluster.

**Usage:**
```bash
wsm deploy [command] [flags]
```

**Subcommands:**
- `operator`: Deploy the W&B operator.
- `chart-cm`: Deploy the helm chart as a ConfigMap.
- `wandb-cr`: Deploy the WeightsAndBiases Custom Resource.

**Flags:**
- `-a, --airgapped`: Deploy in airgapped mode.
- `-b, --bundle string`: Path to the bundle to deploy with.
- `-c, --chart string`: Path to W&B helm chart. If provided along with bundle, this will take precedence.
- `-n, --namespace string`: Namespace to deploy into (default "wandb").
- `-v, --values string`: Values file to apply to the helm chart yaml.

---

### `wsm deploy-v2`

Deploy the v2 W&B operator, server manifest, and custom resources.

**Usage:**
```bash
wsm deploy-v2 [command] [flags]
```

**Subcommands:**
- `operator`: Deploy the v2 operator with specified versions and configuration.
  - `--operator-chart-version string`: Operator Chart version (e.g., v2.0.0) (default "1.5.2").
    - Note: The Chart version specified will determine the image tag of operator deployed based on the [values file](https://github.com/wandb/operator/blob/v2/deploy/operator/values.yaml#L11)
  - `--operator-namespace string`: Namespace for operator (default "wandb-operators").
  - `--install-cert-manager`: Cert-manager install mode: `auto` (detect and reuse existing), `true` (force install flow), `false` (skip installation) (default "auto").
  - `--include-cr`: Include the WeightsAndBiases Custom Resource in the operator deployment.
  - `--setup-k8s-cluster`: Setup a Kind cluster before deploying.
  - `--cluster-name string`: Name of the Kind cluster (only used with `--setup-k8s-cluster`) (default "kind").
  - `--workers int`: Number of worker nodes (only used with `--setup-k8s-cluster`).
  - *Accepts all flags listed under `wandb deploy` below (Used with `--include-cr`).*
- `wandb`: Manage Weights & Biases instances.
  - `deploy`: Deploy a W&B instance.
    - `--cr-file string`: Path to WeightsAndBiases CR YAML (uses built-in default if not provided).
    - `--license string`: W&B license string (optional, injected into spec.wandb.license).
    - `--license-file string`: Path to W&B license file (optional, injected into spec.wandb.license).
    - `--wandb-name string`: Name of the W&B instance (default "wandb").
    - `--wandb-namespace string`: Namespace for CR (default "wandb").
    - `--wandb-version string`: Server manifest version (e.g., 0.76.1).
  - `destroy`: Destroy an instance of W&B.
    - `--wandb-name string`: Name of the W&B instance (default "wandb").
    - `--wandb-namespace string`: Namespace for CR (default "wandb").

**Flags:**
- `--context string`: Name of the kubeconfig context to use (Required).

---

### `wsm cluster`

Manage Kubernetes clusters (Kind) for local development and testing.

**Usage:**
```bash
wsm cluster [command] [flags]
```

**Subcommands:**
- `create`: Create a new Kind cluster.
  - `--cluster-name string`: Name of the Kind cluster (default "kind").
  - `--workers int`: Number of worker nodes.
- `destroy`: Destroy the Kind cluster and cleanup resources.
  - `--cluster-name string`: Name of the Kind cluster to delete (default "kind").
- `cleanup`: Delete all resources deployed by wsm.
  - `--context string`: Name of the kubeconfig context to use (Required).

---

### `wsm console`

Open the W&B console in your default browser.

**Usage:**
```bash
wsm console
```

---

### Examples

**Local Development Setup:**
```bash
# Create a local Kind cluster
wsm cluster create --cluster-name wandb-cluster

# Deploy v2 operator
wsm deploy-v2 operator --context kind-wandb-cluster

# Deploy v2 W&B CR
wsm deploy-v2 wandb deploy --context kind-wandb-cluster
```

**Airgapped Deployment Preparation:**
```bash
# List all required images
wsm list

# Download images for a specific platform
wsm download --platform linux/amd64
```

## Requirements

- Linux, macOS or Windows
- Bash shell
- curl
- tar
- kubectl (for deployment)
- docker (for cluster management and image downloading)
- pkg-config

## Support

For issues and questions, please visit create an issue [here](https://github.com/wandb/wsm/issues).

For more information on how to use WSM, see the [WSM documentation](https://docs.wandb.ai/guides/hosting/self-managed/operator-airgapped/#install-wsm).
