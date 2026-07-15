# Deployment Guides

Platform-specific instructions for deploying W&B with WSM.

## Platforms

| Guide | Environment | Notes |
|-------|-------------|-------|
| [Local Kind Cluster](local-kind.md) | Docker / local | Quick local development and testing |
| [On-Prem / Air-Gapped](on-prem.md) | Customer-controlled mirror | Mirror artifacts to your own registry and install with `--mirror-registry`. Includes laptop walkthrough against a local `registry:2`. |
| [Google GKE](gke.md) | Cloud | Google Kubernetes Engine |
| [Amazon EKS](eks.md) | Cloud | Amazon Elastic Kubernetes Service |
| [Azure AKS](aks.md) | Cloud | Azure Kubernetes Service |

## Common Patterns

All platform guides assume:

- **Gateway API** as the primary networking model
- **SSL termination at the gateway**
- **cert-manager** for certificate lifecycle management
- **WSM** for operator and dependency installation

If you prefer to use traditional **Ingress** or your own **Gateway implementation**, see [Customizing the Deployment](../configuration/customizing.md).

## Quick Decision Table

| You have... | Start here |
|-------------|-----------|
| Docker on your laptop, no cluster | [Quick Start — Local Kind](../getting-started/quickstart-kind.md) |
| An existing Kind cluster | [Local Kind Cluster](local-kind.md) |
| An on-prem cluster + private registry mirror | [On-Prem / Air-Gapped](on-prem.md) |
| A running GKE cluster | [GKE](gke.md) |
| A running EKS cluster | [EKS](eks.md) |
| A running AKS cluster | [AKS](aks.md) |

## SSL / TLS Everywhere

Before deploying to any cloud platform, decide on your SSL strategy:

- **[Let's Encrypt](../configuration/ssl-tls.md#mode-1-lets-encrypt-recommended)** — Best for production with public DNS
- **[Internal CA](../configuration/ssl-tls.md#mode-2-internal-ca-default)** — Best for development and testing
