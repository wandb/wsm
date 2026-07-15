# Google Cloud (GKE)

See [../README.md](../README.md) for common usage, object store outputs, and teardown.

## Prerequisites

- `gcloud` CLI installed and authenticated (`gcloud auth login` and `gcloud auth application-default login`)
- GCP project with the Kubernetes Engine API enabled

## tfvars Notes

- `project_id` is required

## Default Instance Type

`e2-standard-8` (8 vCPU, 32 GB RAM)

## Networking Scenarios

| Scenario | `install_cloud_lb_controller` | W&B CR `networking.mode` | Class name |
|----------|------------------------------|--------------------------|------------|
| W&B nginx ingress | `false` | `ingress` | `nginx` |
| W&B nginx-gateway-fabric | `false` | `gateway` | `nginx` |
| GCE ingress | `true` | `ingress` | `gce` |
| GKE Gateway API | `true` | `gateway` | `gke-l7-global-external-managed` |

GKE has the most mature cloud-native Gateway API support. When `install_cloud_lb_controller = true`, both the GCE ingress controller and the GKE Gateway controller are enabled as built-in cluster add-ons (no Helm install needed).

## External Object Store

Set `create_bucket = true` to create a GCS bucket with HMAC credentials (S3-compatible).
