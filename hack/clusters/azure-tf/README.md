# Azure (AKS)

See [../README.md](../README.md) for common usage, object store outputs, and teardown.

## Prerequisites

- Azure CLI installed and authenticated (`az login`)

## Default Instance Type

`Standard_D8s_v5` (8 vCPU, 32 GB RAM)

## Networking Scenarios

| Scenario | `install_cloud_lb_controller` | W&B CR `networking.mode` | Class name |
|----------|------------------------------|--------------------------|------------|
| W&B nginx ingress | `false` | `ingress` | `nginx` |
| W&B nginx-gateway-fabric | `false` | `gateway` | `nginx` |
| Azure AppGW ingress | `true` | `ingress` | `azure-application-gateway` |

Azure does not yet have a mature native Gateway API controller comparable to GKE's.

## External Object Store

Set `create_bucket = true` to create an Azure Blob Storage account and container.
