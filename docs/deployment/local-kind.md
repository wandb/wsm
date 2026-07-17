# Deploying to a Kind Cluster

This guide covers deploying W&B to both WSM-provisioned and user-managed [Kind](https://kind.sigs.k8s.io/) clusters.

## Option 1: WSM-Provisioned Kind Cluster

The fastest path. WSM creates the cluster, installs dependencies, deploys the operator, and (optionally) the W&B instance.

### Create and Deploy

```bash
# Phase 1 — create the cluster and install the operator stack
wsm deploy-v2 operator \
  --setup-k8s-cluster \
  --cluster-name wandb-local \
  --workers 2 \
  --context kind-wandb-local

# Phase 2 — deploy the W&B instance
wsm deploy-v2 wandb deploy \
  --context kind-wandb-local
```

### Custom Ports

If ports `8080` and `8443` are in use, specify alternatives:

```bash
wsm cluster create \
  --cluster-name wandb-local \
  --http-port 9090 \
  --https-port 9443

# Then install the operator (no --setup-k8s-cluster) ...
wsm deploy-v2 operator --context kind-wandb-local

# ... and deploy the instance
wsm deploy-v2 wandb deploy \
  --context kind-wandb-local \
  --wandb-hostname http://localhost:9090
```

## Option 2: User-Managed Kind Cluster

If you already have a Kind cluster, ensure it meets the requirements below.

### Kind Cluster Requirements

WSM expects the following configuration in your Kind cluster:

1. **Ingress-ready label** on the control-plane node:
   ```yaml
   nodeRegistration:
     kubeletExtraArgs:
       node-labels: "ingress-ready=true"
   ```

2. **Extra port mappings** for HTTP and HTTPS traffic:
   ```yaml
   extraPortMappings:
     - containerPort: 31437
       hostPort: 8080
       protocol: TCP
     - containerPort: 30478
       hostPort: 8443
       protocol: TCP
   ```

### Example Kind Configuration

Save as `kind-config.yaml`:

```yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    kubeadmConfigPatches:
      - |
        kind: InitConfiguration
        nodeRegistration:
          kubeletExtraArgs:
            node-labels: "ingress-ready=true"
    extraPortMappings:
      - containerPort: 31437
        hostPort: 8080
        protocol: TCP
      - containerPort: 30478
        hostPort: 8443
        protocol: TCP
  - role: worker
  - role: worker
```

Create the cluster:

```bash
kind create cluster --name wandb-local --config kind-config.yaml
```

### Deploy W&B

```bash
# Set kubectl context
kubectl config use-context kind-wandb-local

# Deploy operator and infrastructure
wsm deploy-v2 operator --context kind-wandb-local

# Deploy W&B instance
wsm deploy-v2 wandb deploy \
  --context kind-wandb-local \
  --wandb-hostname http://localhost:8080
```

## Verifying the Deployment

```bash
# Check all system namespaces
kubectl get pods -n cert-manager
kubectl get pods -n nginx-gateway
kubectl get pods -n wandb-operators
kubectl get pods -n wandb

# Check W&B CR status
kubectl get wandb -n wandb

# Access the service
open http://localhost:8080
```

## Cleanup

To delete the entire Kind cluster and all resources:

```bash
wsm cluster destroy --cluster-name wandb-local
```

To delete only WSM-managed resources and keep the cluster:

```bash
wsm cluster cleanup --context kind-wandb-local
```

## See Also

- [Installation Guide](../getting-started/installation.md)
- [SSL / TLS Configuration](../configuration/ssl-tls.md)
- [Managing the Deployment](../operations/managing.md)
