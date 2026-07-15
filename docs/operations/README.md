# Operations Guides

Day-to-day management, monitoring, and troubleshooting of W&B deployments.

## Guides

| Guide | Description |
|-------|-------------|
| [Managing the Deployment](managing.md) | Check status, upgrade, destroy, cleanup |
| [Troubleshooting](troubleshooting.md) | Common issues, log collection, fixes |

## Operational Tasks at a Glance

| Task | Command |
|------|---------|
| Check W&B status | `kubectl get wandb -n wandb` |
| Check all pods | `kubectl get pods -n wandb` |
| Upgrade operator | `wsm deploy-v2 operator --context <ctx>` |
| Upgrade W&B | `wsm deploy-v2 wandb deploy --wandb-version <ver>` |
| Destroy W&B only | `wsm deploy-v2 wandb destroy` |
| Full cleanup | `wsm cluster cleanup --context <ctx>` |

## Health Checks

The W&B operator continuously reconciles the CR. Key resources to monitor:

- **`WeightsAndBiases` CR** — `status.ready` should be `true`
- **Gateway** — Should have assigned addresses and programmed routes
- **HTTPRoutes** — Should be accepted and have parent references resolved
- **Certificates** — Should be in `Ready` state

## Support

For issues not covered in the troubleshooting guide, [create an issue](https://github.com/wandb/wsm/issues) with logs and reproduction steps.
