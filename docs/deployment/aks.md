# Deploying to Azure Kubernetes Service (AKS)

This guide covers deploying W&B on Azure AKS using WSM, with Gateway API and SSL termination at the gateway.

## Prerequisites

- A running AKS cluster
- `kubectl` configured with a context for your AKS cluster
- [WSM installed](../getting-started/installation.md)
- A DNS hostname pointing to your cluster (e.g., `wandb.example.com`)

### AKS Cluster Requirements

- **Kubernetes version**: 1.28 or later recommended
- **Node pools**: At least 3 nodes with sufficient resources (see [Sizing Reference](../reference/sizing.md))
- **Network plugin**: Any CNI (Azure CNI, Kubenet) is supported
- **Gateway API**: Not required to be pre-installed; WSM installs `nginx-gateway-fabric` by default. You may use Azure Application Gateway or another Gateway implementation if preferred.

## Step 1: Verify kubectl Context

```bash
kubectl config current-context
# Should show your AKS context, e.g., aks-my-cluster
```

## Step 2: Deploy the Operator

```bash
wsm deploy-v2 operator \
  --context aks-my-cluster \
  --operator-namespace wandb-operators
```

This installs:
- cert-manager (with Gateway API support)
- nginx-gateway-fabric
- W&B operator

## Step 3: Deploy W&B

### Option A: Self-Signed Internal CA (Default)

```bash
wsm deploy-v2 wandb deploy \
  --context aks-my-cluster \
  --wandb-hostname https://wandb.example.com \
  --wandb-namespace wandb \
  --size small \
  --license "YOUR_WANDB_LICENSE"
```

### Option B: Let's Encrypt (Recommended for Production)

First, configure a cert-manager Issuer using Azure DNS for DNS-01 validation or HTTP-01 for direct validation (see [SSL / TLS Configuration](../configuration/ssl-tls.md)).

Then deploy with your custom CR or Gateway annotations.

## Step 4: Retrieve the Public IP

```bash
# For nginx-gateway-fabric (default)
kubectl get service -n nginx-gateway

# For Azure-specific gateway
kubectl get gateway -n wandb
```

Point your DNS `A` record for `wandb.example.com` to the external IP.

## Step 5: Trust the Internal CA (If Using Self-Signed)

```bash
wsm deploy-v2 wandb get-ca-cert \
  --wandb-name wandb \
  --wandb-namespace wandb \
  --output-dir ./certs
```

See [trusting the CA](../configuration/ssl-tls.md#trusting-the-internal-ca-certificate) for platform-specific steps.

## Azure-Specific Notes

- **Application Gateway**: If you prefer Azure Application Gateway over nginx-gateway-fabric, set `--install-nginx-gateway=false` and manually configure the Application Gateway for Gateway API.
- **Load Balancer SKU**: Standard Load Balancer is recommended. Ensure your AKS cluster is configured with the Standard SKU for production workloads.
- **Private Endpoints**: For private AKS clusters, ensure the control plane can reach the operator webhook endpoints.

## Verifying the Deployment

```bash
kubectl get wandb -n wandb
kubectl get pods -n wandb
kubectl get pods -n wandb-operators
kubectl get gateway -n wandb
kubectl get httproute -n wandb
```

## Cleanup

```bash
# Destroy the W&B instance
wsm deploy-v2 wandb destroy \
  --context aks-my-cluster \
  --wandb-name wandb \
  --wandb-namespace wandb

# Full cleanup
wsm cluster cleanup --context aks-my-cluster
```

## See Also

- [SSL / TLS Configuration](../configuration/ssl-tls.md)
- [Customizing the Deployment](../configuration/customizing.md)
- [GKE Deployment](gke.md)
- [EKS Deployment](eks.md)
