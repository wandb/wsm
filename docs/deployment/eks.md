# Deploying to Amazon Elastic Kubernetes Service (EKS)

This guide covers deploying W&B on Amazon EKS using WSM, with Gateway API and SSL termination at the gateway.

## Prerequisites

- A running EKS cluster
- `kubectl` configured with a context for your EKS cluster
- [WSM installed](../getting-started/installation.md)
- A DNS hostname pointing to your cluster (e.g., `wandb.example.com`)

### EKS Cluster Requirements

- **Kubernetes version**: 1.28 or later recommended
- **Node groups**: At least 3 nodes across multiple availability zones
- **IAM roles**: Worker nodes should have appropriate IAM roles for load balancing and storage
- **Gateway API**: Not required to be pre-installed; WSM installs `nginx-gateway-fabric` by default.

### Optional AWS Components

- **AWS Load Balancer Controller**: Only required if you plan to use ALB/NLB Ingress instead of Gateway API
- **EBS CSI Driver**: Required for persistent volume provisioning (gp3 storage class)

## Step 1: Verify kubectl Context

```bash
kubectl config current-context
# Should show your EKS context, e.g., arn:aws:eks:REGION:ACCOUNT:cluster/CLUSTER
```

## Step 2: Deploy the Operator

```bash
wsm deploy-v2 operator \
  --context eks-my-cluster \
  --operator-namespace wandb-operators
```

## Step 3: Deploy W&B

### Option A: Self-Signed Internal CA (Default)

```bash
wsm deploy-v2 wandb deploy \
  --context eks-my-cluster \
  --wandb-hostname https://wandb.example.com \
  --wandb-namespace wandb \
  --size small \
  --license "YOUR_WANDB_LICENSE"
```

### Option B: Let's Encrypt (Recommended for Production)

Set up a cert-manager Issuer for Route 53 DNS-01 or HTTP-01 validation (see [SSL / TLS Configuration](../configuration/ssl-tls.md)).

Then deploy with a custom CR referencing the issuer, or annotate the Gateway for automatic certificate provisioning.

### Using AWS-Specific Flags

WSM provides convenience flags for AWS environments:

```bash
wsm deploy-v2 wandb deploy \
  --context eks-my-cluster \
  --wandb-hostname https://wandb.example.com \
  --wandb-namespace wandb \
  --size small \
  --license "YOUR_WANDB_LICENSE" \
  --create-aws-storage-class \
  --create-aws-ingress-class \
  --add-ingress-annotations
```

| Flag | Effect |
|------|--------|
| `--create-aws-storage-class` | Creates a default `gp3` StorageClass using the EBS CSI driver |
| `--create-aws-ingress-class` | Creates an ALB IngressClass (only if using `--ingress-class`) |
| `--add-ingress-annotations` | Adds `service.beta.kubernetes.io/aws-load-balancer-scheme: internet-facing` for internet-facing load balancers |

> Note: `--add-ingress-annotations` applies AWS load-balancer annotations when using Ingress mode. For Gateway API mode, you may need to add annotations manually to the Gateway service if you switch to AWS Load Balancer Controller.

## Step 4: Retrieve the Load Balancer Endpoint

```bash
kubectl get service -n nginx-gateway
# Look for the EXTERNAL-IP column
```

Or, if using a custom Gateway:

```bash
kubectl get gateway -n wandb -o jsonpath='{.status.addresses[0].value}'
```

Point your DNS `CNAME` or `A` alias record for `wandb.example.com` to this endpoint.

## Step 5: Trust the Internal CA (If Using Self-Signed)

For non-production testing with the default internal CA:

```bash
wsm deploy-v2 wandb get-ca-cert \
  --wandb-name wandb \
  --wandb-namespace wandb \
  --output-dir ./certs
```

See [trusting the CA](../configuration/ssl-tls.md#trusting-the-internal-ca-certificate) for platform-specific steps.

## Verifying the Deployment

```bash
# Check W&B CR status
kubectl get wandb -n wandb

# Check pod status across all namespaces
kubectl get pods -n wandb
kubectl get pods -n wandb-operators
kubectl get pods -n cert-manager
kubectl get pods -n nginx-gateway

# Check gateway
kubectl get gateway -n wandb
```

## EKS-Specific Notes

- **Security Groups**: Ensure the EKS node security groups allow traffic from the load balancer on ports 80/443.
- **Private Subnets**: If your nodes are in private subnets, ensure NAT Gateways are configured for outbound internet access.
- **IRSA**: For production deployments, use IAM Roles for Service Accounts (IRSA) to grant fine-grained permissions to W&B pods.

## Cleanup

```bash
# Destroy the W&B instance
wsm deploy-v2 wandb destroy \
  --context eks-my-cluster \
  --wandb-name wandb \
  --wandb-namespace wandb

# Full cleanup
wsm cluster cleanup --context eks-my-cluster
```

## See Also

- [SSL / TLS Configuration](../configuration/ssl-tls.md)
- [Customizing the Deployment](../configuration/customizing.md)
- [GKE Deployment](gke.md)
- [AKS Deployment](aks.md)
