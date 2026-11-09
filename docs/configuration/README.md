# Configuration Guides

Reference documentation for customizing your W&B deployment.

## Guides

| Guide | Description |
|-------|-------------|
| [SSL / TLS](ssl-tls.md) | Configure Let's Encrypt or WSM's internal CA for HTTPS |
| [Customizing the Deployment](customizing.md) | Size profiles, flags, custom CRs, networking modes |

## Configuration Scope

Configuration can be applied at three layers:

1. **WSM CLI flags** — Quick overrides for common settings (hostname, size, license, etc.)
2. **Custom CR file** — Full control over the `WeightsAndBiases` resource spec
3. **Operator install flags** — Operator-level settings on `wsm deploy-v2 operator`: chart version (`--operator-chart-version`), telemetry, OpenShift, Watchtower permissions. WSM has no flag for arbitrary operator chart values

## Precedence

When multiple layers configure the same field:

```
--cr-set > CLI flags > Custom CR values > WSM defaults
```
