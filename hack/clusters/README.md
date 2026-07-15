# Cloud Cluster Provisioning

Terraform modules for provisioning Kubernetes clusters on AWS (EKS), Azure (AKS), and
Google Cloud (GKE) for W&B Operator v2 deployments.

## Common Prerequisites

- Terraform >= 1.5
- `kubectl`
- Cloud provider CLI (authenticated)

## Common Input Variables

Use the example `terraform.tfvars.example` as a base for `terraform.tfvars`. Documentation
on the most commonly adjusted input variables:

### `append_timestamp` and `deployment_name`

`deployment_name` is used to name things like k8s clusters, buckets, etc. If `append_timestamp`
is `true`, the current date will be appended in `_YYYYMMDDHHmm` format to help create unique names.

### `create_bucket`

If `true`, create an object storage bucket for use as externally supplied infra.

### `node_count` (nodes per k8s cluster)

- `node_count = 1`: Single node for `dev` size deployments
- `node_count = 3`: Cross-AZ/zone for `small` and above

See each cloud module's README for default instance types.

## Terraform Usage

Each cloud module follows the same workflow:

```bash
cd hack/clusters/<cloud>-tf

cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars for your environment

terraform init
terraform apply
```

After apply completes, configure kubectl:

```bash
eval "$(terraform output -raw kubeconfig_command)"
kubectl get nodes  # verify all nodes are Ready
```

Then deploy the operator and W&B instance:

```bash
wsm deploy-v2 operator --context "$(terraform output -raw kube_context_name)"
wsm deploy-v2 wandb deploy --context "$(terraform output -raw kube_context_name)"
```

Or use the helper scripts:

```bash
hack/clusters/cluster_kubeconfig.sh hack/clusters/<cloud>-tf
wsm deploy-v2 operator --context <cluster_name>
wsm deploy-v2 wandb deploy --context <cluster_name>
```

## Common Output Variables

### External Object Store

Each module can optionally create cloud object storage (`create_bucket = true`). When
enabled, the outputs provide the raw materials for creating the operator's storage
connection secret:

| Output | Description |
|--------|-------------|
| `objectstore_endpoint` | Storage service hostname |
| `objectstore_port` | HTTPS port |
| `objectstore_bucket` | Bucket / container name |
| `objectstore_region` | Cloud region |
| `objectstore_access_key` | Access key / account name (sensitive) |
| `objectstore_secret_key` | Secret key (sensitive) |
| `objectstore_url` | Composed S3-scheme URL (sensitive) |

Retrieve sensitive values:

```bash
terraform output -raw objectstore_access_key
terraform output -raw objectstore_secret_key
terraform output -raw objectstore_url
```

## Helper Scripts

### `cluster_kubeconfig.sh`

```
Usage: ./cluster_kubeconfig.sh <cloud-tf-dir>

Fetch kubeconfig for a Terraform-managed cluster and rename the context
to match the cluster name.
```

## Teardown

```bash
terraform destroy
```

## Cloud Modules

- [aws-tf/](aws-tf/) — EKS on AWS
- [azure-tf/](azure-tf/) — AKS on Azure
- [google-tf/](google-tf/) — GKE on Google Cloud
