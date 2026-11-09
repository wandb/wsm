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
  --operator-chart-version 2.0.0-beta.10
```

### Upgrade W&B Version

Use `wsm set-version`. It changes only `spec.wandb.version` on the live CR, refuses downgrades, shows the change and asks for confirmation:

```bash
wsm set-version \
  --context <ctx> \
  --wandb-version 0.86.0 \
  --wandb-name wandb \
  --wandb-namespace wandb
```

Add `--dry-run` to preview without applying, or `--wait` to block until the instance reports ready. The operator performs a rolling update of the W&B instance.

Re-running `wsm deploy-v2 wandb deploy --wandb-version <ver>` also upgrades, but it rebuilds the whole CR from the flags you pass, so any field you set at install time and leave out now changes. Use it when you also mean to change other settings.

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

It asks for confirmation first; pass `--yes` in scripts. This deletes:
- W&B CRs and their namespaces (where markers exist)
- W&B operator Helm releases
- cert-manager, nginx-gateway-fabric and kube-state-metrics, each only if a WSM marker claims it
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

WSM does not roll back automatically.

To return the W&B server to an earlier version, set it explicitly with `--force`, which `set-version` requires for a downgrade:

```bash
wsm set-version \
  --context <ctx> \
  --wandb-version <previous-version> \
  --force
```

> A downgrade runs the older server against a database the newer one may already have migrated. Check the W&B Server release notes before rolling back across a release with schema changes.

To roll back the operator, re-run `wsm deploy-v2 operator --context <ctx> --operator-chart-version <previous-chart-version>`.

## See Also

- [Troubleshooting](troubleshooting.md)
- [Customizing the Deployment](../configuration/customizing.md)
