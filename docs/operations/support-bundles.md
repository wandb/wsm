# Support Bundles

A support bundle is a redacted `.tar.gz` of diagnostics (cluster resources, pod logs, and telemetry when it's enabled). [Lumen](https://github.com/wandb/lumen) collects it from a v2 W&B instance and uploads it to that **instance's own bucket**, under `agent/service-bundles/`. The bundle stays in the customer's storage unless someone downloads it.

The operator installs a suspended CronJob, `<name>-support-bundle-collector`, for each `WeightsAndBiases`. It never runs on a schedule; wsm uses it as a template. Support bundles are on by default (`spec.supportBundle.enabled: true`).

```bash
wsm support-bundle create --wait     # collect; the Job name is the bundle ID
wsm support-bundle status <id> -w    # follow a collection
wsm support-bundle list              # stored bundles and running collections
```

See [`wsm support-bundle`](../reference/commands.md#wsm-support-bundle) for every flag.

## How it works

`create` starts a collection Job from the template.

Commands that read the bucket (`list`) run one short-lived **management Job**, also copied from the template:

- It keeps the template's image, ServiceAccount, bucket credentials, pull secrets, scheduling, and security settings. Its command becomes a bounded `sleep`, and wsm runs Lumen's `bundle-manager` in it over `kubectl exec`.
- It's named `support-bundle-<operation>-<hex>` and labelled `app.kubernetes.io/component=support-bundle-manager`, so it never counts as a collection.
- wsm deletes it when the command ends, including on Ctrl+C. If wsm is killed, the `sleep`, a 16-minute Job deadline, and a 5-minute TTL clean it up.

The management Job runs as the collector's ServiceAccount, but wsm creates it and execs into it with your kubeconfig credentials. Those need, in the instance namespace, `get` and `list` on `weightsandbiases`, `get` on CronJobs, `get`, `list`, `create` and `delete` on Jobs, `get` and `list` on pods, `create` on `pods/exec`, and `list` on events.

## Limitations

- **S3-compatible buckets only** (managed SeaweedFS, AWS S3, MinIO). `bundle-manager` doesn't support GCS or Azure yet.
- **Network policies.** The management pod reaches the bucket from the instance namespace, just like the collector.
- **Bundles don't expire on their own.** Add a lifecycle rule on the bucket's `agent/service-bundles/` prefix.
