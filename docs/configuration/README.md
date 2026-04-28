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
3. **Operator chart values** — Advanced operator-level configuration (passed via `--operator-chart-version`)

## Precedence

When multiple layers configure the same field:

```
CLI flags > Custom CR values > WSM defaults
```
