# EKS Test Cluster

Terraform configuration for provisioning an EKS cluster for Operator v2 deployments.

## Prerequisites

- AWS CLI installed and authenticated (`aws sso login --profile <profile>` or `aws configure`)
- Terraform >= 1.5
- `kubectl`

### Required IAM Permissions

The caller needs permissions to create: VPC, subnets, internet gateway, route tables, IAM roles/policies, OIDC provider, EKS cluster, EKS node groups, EKS add-ons, S3 buckets (if `create_bucket = true`), and Helm releases (if `install_cloud_lb_controller = true`).

## Usage

```bash
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars:
#   - set aws_profile if using SSO or named profiles

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

## Networking Scenarios

| Scenario | `install_cloud_lb_controller` | W&B CR `networking.mode` | Class name |
|----------|------------------------------|--------------------------|------------|
| W&B nginx ingress | `false` | `ingress` | `nginx` |
| W&B nginx-gateway-fabric | `false` | `gateway` | `nginx` |
| AWS ALB ingress | `true` | `ingress` | `alb` |
| AWS Gateway API | `true` | `gateway` | `amazon-vpc-lattice` |

The AWS Load Balancer Controller requires IAM integration and is installed by Terraform when `install_cloud_lb_controller = true`.

## Node Sizing

- `node_count = 1`: Single node for `dev` size deployments
- `node_count = 3`: Cross-AZ for `small` and above

Default instance type `m5.2xlarge` (8 vCPU, 32 GB RAM) provides sufficient resources per node.

## External Object Store

Set `create_bucket = true` to create an S3 bucket with a dedicated IAM user and access key. The outputs map directly to the `wandb-objectstore-connection` / `external-objectstore-connection` secret keys:

| Output | Secret Key |
|--------|------------|
| `objectstore_endpoint` | `Host` |
| `objectstore_port` | `Port` |
| `objectstore_bucket` | `Bucket` |
| `objectstore_region` | `Region` |
| `objectstore_access_key` | `AccessKey` |
| `objectstore_secret_key` | `SecretKey` |
| `objectstore_url` | `url` |

Retrieve sensitive values:

```bash
terraform output -raw objectstore_access_key
terraform output -raw objectstore_secret_key
terraform output -raw objectstore_url
```

## Teardown

```bash
terraform destroy
```
