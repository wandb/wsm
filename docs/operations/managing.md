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

### Upgrade nginx-gateway-fabric

By default (`--install-nginx-gateway auto`) `wsm deploy-v2 operator` reuses an existing nginx-gateway-fabric install and does **not** upgrade it; it prints a warning when the running version is older than the one wsm pins (2.9.0). Merging or installing a newer wsm therefore changes nothing on a running cluster until you rerun with `--install-nginx-gateway true`.

Before upgrading, check:

```bash
kubectl version                                  # server must be 1.32 or later
helm list -A | grep nginx-gateway-fabric         # wsm's release is `nginx-gateway` in namespace `nginx-gateway`
```

If nginx-gateway-fabric runs under any other release name or namespace, wsm didn't install it: `auto` reuses it (warning if it's outdated, and without recording it as wsm-owned), and `true` stops without changing anything. Uninstall it and rerun wsm, or upgrade it yourself (use `oci://<mirror>/nginx/charts/nginx-gateway-fabric` instead of the public chart if you install from a mirror):

```bash
kubectl apply --server-side -f <gateway-api v1.7.0 standard-install.yaml>
helm pull oci://ghcr.io/nginx/charts/nginx-gateway-fabric --version 2.9.0 --untar
kubectl apply --server-side --force-conflicts -f nginx-gateway-fabric/crds/    # Helm won't upgrade these itself
helm upgrade <release> oci://ghcr.io/nginx/charts/nginx-gateway-fabric --version 2.9.0 -n <namespace>
```

To upgrade a wsm-installed release:

```bash
wsm deploy-v2 operator \
  --context <ctx> \
  --operator-chart-version 2.0.0-beta.10 \
  --install-nginx-gateway true
```

This upgrades, in order:

1. **Gateway API CRDs** to v1.7.0 (standard channel), if older ones are installed. wsm stops instead, and you apply the v1.7.0 CRDs yourself and rerun, when `--skip-gateway-api-crds` is set, when any CRD in the bundle is from the experimental channel, or when something other than wsm or kubectl manages any of them (a platform addon such as GKE's managed Gateway API, or another gateway controller's chart), including when only some of the CRDs are installed. A `--gateway-api-crd-url` that serves anything but the complete v1.7.0-or-later standard bundle is rejected before anything is applied. CRDs without a `gateway.networking.k8s.io/bundle-version` annotation are left as is, with a warning.
2. **nginx-gateway-fabric CRDs** from the new chart (Helm does not upgrade `crds/` on its own).
3. **The `nginx-gateway` Helm release** to 2.9.0. Data-plane pods for each Gateway roll to the new NGINX image; the Gateway's Service and its load balancer are kept.

nginx-gateway-fabric 2.9.0 requires Kubernetes 1.32 or later; on an older cluster wsm stops before changing anything. For air-gapped clusters, run `wsm registry mirror` from the new wsm first so the 2.9.0 chart and images are in your mirror, and pass the same `--mirror-registry` flags you installed with.

Verify the running images. The first command shows the control plane; the second shows the NGINX data-plane pods (the ones that serve traffic) with their image digests:

```bash
kubectl get deploy -A -l app.kubernetes.io/name=nginx-gateway-fabric -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\t"}{.spec.template.spec.containers[0].image}{"\n"}{end}'
kubectl get pods -A -l gateway.networking.k8s.io/gateway-name -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\t"}{.status.containerStatuses[*].imageID}{"\n"}{end}'
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
