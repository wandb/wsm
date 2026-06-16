# WSM Deployment Documentation

This guide covers deploying [Weights & Biases](https://wandb.ai) (W&B) Server using the **Weights & Biases Server Manager** (WSM) CLI tool.

## What is WSM?

WSM is a command-line tool that automates the deployment, upgrade, and management of W&B server instances on Kubernetes. It is designed to simplify:

- Deploying the W&B operator and its dependencies
- Configuring networking (Gateway API) and SSL/TLS
- Day-to-day operations (status, upgrades, cleanup)

## Documentation Structure

| Section | Description |
|---------|-------------|
| [Getting Started](./getting-started/) | Installation and your first deployment |
| [Deployment Guides](./deployment/) | Platform-specific instructions for Kind, on-prem/air-gapped, GKE, EKS, and AKS |
| [Configuration](./configuration/) | SSL/TLS setup, sizing, and customization options |
| [Operations](./operations/) | Managing, monitoring, and troubleshooting deployments |
| [Reference](./reference/) | Command flags, CR fields, and sizing details |

## Recommended Reading Order

1. [Install WSM](./getting-started/installation.md)
2. [Quick Start — Local Kind Cluster](./getting-started/quickstart-kind.md)
3. [SSL / TLS Configuration](./configuration/ssl-tls.md)
4. [Your Target Platform](./deployment/) (GKE, EKS, AKS, on-prem mirror, or existing Kind cluster)
5. [Managing the Deployment](./operations/managing.md)
6. [Troubleshooting](./operations/troubleshooting.md)

## Prerequisites

- [kubectl](https://kubernetes.io/docs/tasks/tools/) — configured to access your target cluster
- [Docker](https://docs.docker.com/get-docker/) — required for Kind cluster creation and image handling
- [Go](https://go.dev/doc/install) + [pkg-config](https://www.freedesktop.org/wiki/Software/pkg-config/) — required for building WSM from source

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                        Kubernetes Cluster                    │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐   │
│  │ cert-manager │  │ nginx-gateway│  │  W&B Operator    │   │
│  │              │  │   -fabric    │  │  (Helm release)  │   │
│  └──────────────┘  └──────────────┘  └──────────────────┘   │
│                                               │             │
│                              ┌────────────────┴──────────┐  │
│                              │ WeightsAndBiases CR (v2)  │  │
│                              │  - wandb app              │  │
│                              │  - mysql (managed)        │  │
│                              │  - redis (managed)        │  │
│                              │  - kafka (managed)        │  │
│                              │  - object store (managed) │  │
│                              │  - clickhouse (managed)   │  │
│                              └───────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
                              │
                          Gateway API
                              │
                     HTTPS termination
                              │
                      End Users / SDK
```

WSM deploys and manages three layers of infrastructure:

1. **Cluster dependencies** — `cert-manager` (for TLS) and `nginx-gateway-fabric` (for Gateway API routing)
2. **W&B Operator** — a Kubernetes operator that reconciles the `WeightsAndBiases` Custom Resource
3. **W&B Instance** — the actual application stack defined by the CR, including managed data stores

## Default Networking Model

This documentation assumes deployments use **Gateway API** with **SSL termination at the gateway**. This is the default and recommended pattern. WSM installs `nginx-gateway-fabric` automatically, but you may provide your own Gateway API implementation (e.g., GCP GKE Gateway, AWS ALB, Azure Application Gateway).

For environments where Ingress is preferred, WSM also supports specifying an `--ingress-class` as an alternative.

## Support

For issues and questions, please [create an issue](https://github.com/wandb/wsm/issues) in the WSM repository.
