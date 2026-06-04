# Managing the Deployment

Once W&B is deployed, here are the common day-to-day operations.

## Check Status

### W&B Custom Resource

```bash
kubectl get wandb -n wandb
```

The `READY` column indicates whether the instance is fully reconciled. For detailed status:

```bash
kubectl describe wandb wandb -n wandb
```

### Pods

```bash
# W&B application pods
kubectl get pods -n wandb

# Operator pods
kubectl get pods -n wandb-operators

# Infrastructure pods
kubectl get pods -n cert-manager
kubectl get pods -n nginx-gateway
```

### Gateway and Routes

```bash
kubectl get gateway -n wandb
kubectl get httproute -n wandb
```

## Upgrading

### Upgrade the Operator

```bash
wsm deploy-v2 operator \
  --context <ctx> \
  --operator-chart-version 2.0.0-alpha.2
```

### Upgrade W&B Version

```bash
wsm deploy-v2 wandb deploy \
  --context <ctx> \
  --wandb-version 0.81.0 \
  --wandb-name wandb \
  --wandb-namespace wandb
```

The operator will perform a rolling update of the W&B instance.

## Destroy the W&B Instance

This removes the W&B application but preserves the operator and infrastructure:

```bash
wsm deploy-v2 wandb destroy \
  --context <ctx> \
  --wandb-name wandb \
  --wandb-namespace wandb
```

> The `--retention-policy` (configured at deploy time) controls whether managed infrastructure is left running (`detach`) or deleted (`purge`) when the CR is destroyed.

## Full Cleanup

To remove **all** WSM-managed resources from the cluster:

```bash
wsm cluster cleanup --context <ctx>
```

This deletes:
- W&B CRs and their namespaces (where markers exist)
- W&B operator Helm releases
- cert-manager
- nginx-gateway-fabric
- Deployment markers

> Note: `cluster cleanup` only removes resources that have WSM deployment markers. It will not delete user-created resources.

## Destroy a Kind Cluster

For WSM-provisioned local clusters:

```bash
wsm cluster destroy --cluster-name wandb-local
```

This deletes the Kind cluster entirely, including all resources.

## Check Deployment Markers

WSM uses ConfigMap-based markers to track which resources it manages. These markers prevent accidental deletion of user-managed resources.

If you need to see which namespaces and resources WSM considers managed, check for ConfigMaps with the label `app.kubernetes.io/managed-by=wsm`.

## Rolling Back

WSM does not currently support automatic rollback. To revert to a previous version:

1. Destroy the W&B instance (preserving data with `--retention-policy detach`)
2. Re-deploy with the desired `--wandb-version` or `--operator-chart-version`

## See Also

- [Troubleshooting](troubleshooting.md)
- [Customizing the Deployment](../configuration/customizing.md)
