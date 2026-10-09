# Support Bundles (preview)

> **Status: proof of concept.** Command names, flags and the record format may change.

`wsm support-bundle` collects diagnostics from a v2 W&B installation with [Lumen](https://github.com/wandb/lumen). wsm creates a Kubernetes Job in the installation namespace. Lumen collects the selected sources, redacts sensitive values, and uploads a `.tar.gz` to the **installation's own bucket**. That is managed SeaweedFS or your external bucket, as reported in the CR's `status.objectStoreStatus`. wsm never downloads the archive and re-uploads it.

```bash
wsm support-bundle create   --context <ctx> --wait          # start, then watch (Ctrl-C stops watching only)
wsm support-bundle list     --context <ctx>                 # newest first
wsm support-bundle status   --context <ctx> sb-7f92ac --watch
wsm support-bundle retrieve --context <ctx> sb-7f92ac -o ./bundle.tgz --delete-after-download
wsm support-bundle cancel   --context <ctx> sb-7f92ac       # stop a running collection, keep the record
wsm support-bundle delete   --context <ctx> sb-7f92ac       # remove bundle + record (prompts if still running)
```

Every subcommand takes `--context`, `--wandb-name` and `--wandb-namespace`. Both installation flags are optional: wsm lists the v2 `WeightsAndBiases` resources and uses the only one (or the only one in `--wandb-namespace`). If several match, it lists them and asks you to choose. No operator namespace is needed, because everything is read from the CR. Image, bucket, credentials and telemetry endpoints are all read from the installation. You never re-enter them.

## What runs

| Object | Name | Lifetime |
|--------|------|----------|
| ServiceAccount | `wsm-lumen` (or `--service-account`) | Kept; reused by later runs |
| Role + RoleBinding | `wsm-lumen-<name>` in the installation namespace | Kept |
| ClusterRole + ClusterRoleBinding | `wsm-lumen-<namespace>-<name>` | Kept |
| Role + RoleBinding (Victoria only) | `wsm-lumen-<name>-telemetry` in the telemetry namespace | Kept |
| Record ConfigMap | `wsm-sb-<id>` | Until pruned or deleted |
| Spec ConfigMap | `support-bundle-<hex>-spec` (collection YAML, no credentials) | Owned by the record |
| Job | `support-bundle-<hex>` (pod `support-bundle-<hex>-<suffix>`) | Owned by the record. TTL = `--retention`. `backoffLimit: 0`. Deadline = `--timeout` |

Every object carries the labels `app.kubernetes.io/managed-by=wsm`, `app.kubernetes.io/component=support-bundle` and `support-bundle.wsm.wandb.ai/installation=<namespace>.<name>`. wsm only updates or deletes objects that carry these labels. It refuses to adopt an existing object with the same name, so repeated or concurrent runs are safe.

The Job runs the collector binary directly, as `/app/bin/collector agent --lumen-collection=false --support-bundle ...`. Its bucket credentials are `secretKeyRef`s copied from the CR status. Secret values never appear in a ConfigMap, the pod spec, or wsm output.

Collected sources:

- `cluster-resources`: resources in the installation namespace, plus cluster-scoped metadata.
- `cluster-info`
- `pod-logs`: the installation namespace, limited by `--since`.
- `victoria-stack`: metrics, logs and traces for `--since`. This is only collected when telemetry mode is `forward` or `full`, the telemetry stack is ready, and `--skip-telemetry` is not set. If the telemetry stack is unavailable, the Kubernetes diagnostics are still collected.

## Interrupts, reconnecting and cancelling

- `create` prints the bundle ID and Job name as soon as the Job exists.
- **Ctrl-C only stops watching.** The Job keeps running in the cluster. Reconnect with `status <id> --watch`.
- Cancelling is explicit. `cancel <id>` stops the Job and its pod, and keeps the record as `Cancelled` so `status` and `list` still show it. Kubernetes removes the pod when it stops it, so collector logs from a cancelled run are not kept.
- `delete <id>` on a bundle that is still collecting asks "Cancel it and delete? [y/N]". Pass `--yes` to skip the prompt. Without a terminal, wsm refuses unless `--yes` is given.
- If wsm exits while `create` is still running, nothing is orphaned. The record is the owner-reference root of the spec ConfigMap and the Job, so deleting or pruning the record lets Kubernetes garbage-collect them.

## Results and retrieval

Lumen reports its outcome as v1 JSON in the container's termination message. The env var `LUMEN_SUPPORT_BUNDLE_RESULT_PATH=/dev/termination-log` tells it where to write. This is the same contract the operator's `SupportBundleRequest` work uses:

```json
{"version":"v1","artifact":{"location":"s3://bucket/.../support-bundle-<ts>.tar.gz","sizeBytes":88080384,"sha256":"<hex>"}}
```

wsm validates the reported location before storing it. Locations with credentials, query strings or a scheme other than `s3`, `gs` or `az` are rejected. wsm never guesses a filename or parses one out of the logs.

> **Current limitation.** The pinned Lumen image does not write this result yet. A successful Job therefore shows as **Completed** with "artifact location reporting unavailable", and `retrieve` refuses to run. The bundle still exists in the bucket under `agent/service-bundles/`.

`retrieve` reaches the bucket from your workstation. For managed SeaweedFS it uses an in-process port-forward. For other buckets it uses the connection secret's keys if present, otherwise your workstation's default AWS credentials.

- An interrupted download resumes on re-run. Progress is kept in `<output>.partial` and `<output>.partial.json`.
- The file only appears at `--output` once it is complete.
- `--delete-after-download` deletes the bucket copy only after the size **and** SHA-256 both match what Lumen reported.
- S3-compatible stores (SeaweedFS, MinIO, AWS S3) are supported. GCS and Azure can create, list and status bundles, but cannot yet retrieve or delete them.

## Retention

Each bundle records an expiry, `--retention` after creation (default 24h). `list` shows expired bundles as `Expired`. Each `create` prunes the installation's expired wsm bundles: it deletes the bucket object, then the record. If the object cannot be deleted, the record is kept. Until Lumen reports artifact locations, wsm does not know which object a bundle produced. Pruning and `delete` then remove only the record, and the archive stays under `agent/service-bundles/` until it is removed by a bucket lifecycle rule or by hand. Pod logs and status stay available for the Job's TTL, which equals `--retention`.

## Permissions

### Person running wsm

`create` checks these permissions up front with SelfSubjectAccessReviews and lists every missing one. wsm never falls back to other credentials.

| Scope | Resources | Verbs |
|-------|-----------|-------|
| Installation namespace | `weightsandbiases.apps.wandb.com` | get (plus list, cluster-wide or in `--wandb-namespace`, when discovering) |
| Installation namespace | `serviceaccounts` | create, get, update (only get with `--service-account`) |
| Installation namespace (+ telemetry namespace) | `roles`, `rolebindings` | create, get, update |
| Cluster | `clusterroles`, `clusterrolebindings` | create, get, update |
| Installation namespace | `configmaps` | create, get, list, update, delete |
| Installation namespace | `jobs.batch` | create, get, delete |
| Installation namespace | `pods` list, `pods/log` get | — |
| Installation namespace (retrieve/delete only) | `secrets` | get (object-store connection secret) |

Kubernetes RBAC escalation rules also apply. To grant the collector's rules, you must either hold them yourself or have `escalate` and `bind` on roles and clusterroles.

### Lumen pod

Everything is read-only, except the Victoria helper pod. **No access to Secrets and no wildcard rules.**

- **Installation namespace:** get/list on pods, events, services, endpoints, configmaps, serviceaccounts, PVCs, limit ranges, resource quotas, replication controllers, deployments, replicasets, statefulsets, daemonsets, jobs and cronjobs. list on HPAs, ingresses, network policies, endpoint slices, PDBs, roles, role bindings and leases. get/list on `weightsandbiases`. `pods/log` get.
- **Cluster:** get/list on namespaces, nodes and PVs. list on storage classes, volume attachments, CRDs, ingress classes, priority classes, cluster roles/bindings, webhook configurations and CSRs. create on `selfsubjectaccessreviews` and `selfsubjectrulesreviews`.
- **Telemetry namespace (Victoria only):** pods create/get/list/delete, plus `pods/exec` create, for Lumen's `curlimages/curl` export pod.

Because Lumen has no access to Secrets or cluster-wide custom resources, a bundle always contains `cluster-resources/image-pull-secrets-errors.json` and `cluster-resources/custom-resources/custom-resources-errors.json` with Forbidden entries. That is expected and does not fail the collection.

### Workload identity

The collector does **not** run as the W&B application ServiceAccount. wsm copies only these annotations from `spec.wandb.serviceAccount.annotations` onto `wsm-lumen`:

- `eks.amazonaws.com/role-arn`
- `iam.gke.io/gcp-service-account`
- `azure.workload.identity/client-id`

wsm never changes cloud IAM. You authorize the new account yourself:

- **AWS IRSA / EKS Pod Identity:** add `system:serviceaccount:<namespace>:wsm-lumen` to the role's trust policy (`sub` condition) or create a Pod Identity association.
- **GKE Workload Identity:** grant `roles/iam.workloadIdentityUser` on the GSA to `<project>.svc.id.goog[<namespace>/wsm-lumen]`.
- **Azure Workload Identity:** add a federated credential for subject `system:serviceaccount:<namespace>:wsm-lumen`.

To use an identity that is already authorized, pass `--service-account <name>`. wsm binds the collector RBAC to it and leaves the ServiceAccount itself untouched.

## Air-gapped installs

`wsm registry mirror` also mirrors the pinned Lumen image (to `<registry>/wandb/lumen:<version>`) and `curlimages/curl:8.21.0`. `create` pulls Lumen from `--image-registry`, else `spec.global.imageRegistry`, else the W&B public registry. It uses the CR's `spec.global.imagePullSecrets` plus any `--image-pull-secret`. The curl helper image is hardcoded in Lumen, so it reaches your mirror only through the node's container-runtime registry mirror.

## Lumen version

wsm pins `supportbundle.DefaultLumenVersion`, currently a Lumen `main` commit SHA because no Lumen release includes support-bundle mode yet. Override it with `--lumen-version <tag>`. `latest` is rejected.
