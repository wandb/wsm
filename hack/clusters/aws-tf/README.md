# AWS (EKS)

See [../README.md](../README.md) for common usage, object store outputs, and teardown.

## Prerequisites

- AWS CLI installed and authenticated (`aws sso login --profile <profile>` or `aws configure`)

### Required IAM Permissions

The caller needs permissions to create: VPC, subnets, internet gateway, route tables, IAM roles/policies, OIDC provider, EKS cluster, EKS node groups, EKS add-ons, S3 buckets (if `create_bucket = true`), and Helm releases (if `install_cloud_lb_controller = true`).

## tfvars Notes

- Set `aws_profile` if using SSO or named profiles

## Default Instance Type

`m5.2xlarge` (8 vCPU, 32 GB RAM)

## Networking Scenarios

| Scenario | `install_cloud_lb_controller` | W&B CR `networking.mode` | Class name |
|----------|------------------------------|--------------------------|------------|
| W&B nginx ingress | `false` | `ingress` | `nginx` |
| W&B nginx-gateway-fabric | `false` | `gateway` | `nginx` |
| AWS ALB ingress | `true` | `ingress` | `alb` |
| AWS Gateway API | `true` | `gateway` | `amazon-vpc-lattice` |

The AWS Load Balancer Controller requires IAM integration and is installed by Terraform when `install_cloud_lb_controller = true`.

## External Object Store

Set `create_bucket = true` to create an S3 bucket with a dedicated IAM user and access key.
