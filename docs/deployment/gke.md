# Deploying to Google Kubernetes Engine (GKE)

This guide covers deploying W&B on GKE using WSM, with Gateway API and SSL termination at the gateway.

## Prerequisites

- A running GKE cluster
- `kubectl` configured with a context for your GKE cluster
- [WSM installed](../getting-started/installation.md)
- A DNS hostname pointing to your cluster (e.g., `wandb.example.com`)

### GKE Cluster Requirements

- **Kubernetes version**: 1.28 or later recommended
- **Node pools**: At least 3 nodes with sufficient resources (see [Sizing Reference](../reference/sizing.md))
- **Gateway API**: Not required to be pre-installed; WSM installs `nginx-gateway-fabric` by default. You may use GKE Gateway if preferred.

## Step 1: Verify kubectl Context

```bash
kubectl config current-context
# Should show your GKE context, e.g., gke_PROJECT_REGION_CLUSTER
```

## Step 2: Deploy the Operator

WSM deploys the operator and all required dependencies (cert-manager, nginx-gateway-fabric):

```bash
wsm deploy-v2 operator \
  --context gke-my-cluster \
  --operator-namespace wandb-operators
```

This will:
- Install cert-manager with Gateway API support
- Install nginx-gateway-fabric
- Install the W&B operator

## Step 3: Deploy W&B

Deploy with a real hostname. Choose your SSL approach:

### Option A: Self-Signed Internal CA (Default, Good for Testing)

```bash
wsm deploy-v2 wandb deploy \
  --context gke-my-cluster \
  --wandb-hostname https://wandb.example.com \
  --wandb-namespace wandb \
  --size small \
  --license "YOUR_WANDB_LICENSE"
```

### Option B: Let's Encrypt (Recommended for Production)

First, create a cert-manager Issuer for Let's Encrypt (see [SSL / TLS Configuration](../configuration/ssl-tls.md) for the full manifest).

Then deploy W&B referencing the issuer:

```bash
wsm deploy-v2 wandb deploy \
  --context gke-my-cluster \
  --wandb-hostname https://wandb.example.com \
  --wandb-namespace wandb \
  --size small \
  --license "YOUR_WANDB_LICENSE" \
  --create-ca=false \
  --issuer-name letsencrypt-prod
```

## Step 4: Retrieve the Public IP

Find the external IP of the gateway:

```bash
kubectl get gateway -n wandb
kubectl get service -n nginx-gateway
```

Point your DNS `A` record for `wandb.example.com` to this IP.

## Step 5: Trust the Internal CA (If Using Self-Signed)

If you used the default internal CA, clients will see certificate warnings. To fix this, retrieve and trust the CA certificate:

```bash
wsm deploy-v2 wandb get-ca-cert \
  --wandb-name wandb \
  --wandb-namespace wandb \
  --output-dir ./certs
```

Then trust the certificate on client machines ([see platform-specific instructions](../configuration/ssl-tls.md#trusting-the-internal-ca-certificate)).

## Verifying the Deployment

```bash
# Check W&B CR status
kubectl get wandb -n wandb
kubectl describe wandb wandb -n wandb

# Check pod status
kubectl get pods -n wandb

# Check gateway status
kubectl get gateway -n wandb
kubectl get httproute -n wandb
```

## GCP-Specific Notes

- **Load Balancer**: By default, nginx-gateway-fabric creates a TCP LoadBalancer service. If you need additional annotations (e.g., for Google Cloud Load Balancer), you can provide a custom Gateway resource or modify the service post-deployment.
- **Cloud NAT / Egress**: Ensure your nodes can reach the internet (for image pulls, Let’s Encrypt validation, etc.).
- **Private Cluster**: If using a private cluster, ensure the control plane can reach the webhook service endpoints in the `wandb-operators` namespace.

## Cleanup

```bash
# Destroy the W&B instance
wsm deploy-v2 wandb destroy \
  --context gke-my-cluster \
  --wandb-name wandb \
  --wandb-namespace wandb

# Full cleanup (operator, cert-manager, nginx-gateway)
wsm cluster cleanup --context gke-my-cluster
```

## See Also

- [SSL / TLS Configuration](../configuration/ssl-tls.md)
- [Customizing the Deployment](../configuration/customizing.md)
- [EKS Deployment](eks.md)
- [AKS Deployment](aks.md)
