# Weights & Biases Server Manager

WSM is a command-line tool designed to manage W&B server deployments.
It simplifies the process of deploying, upgrading, and
maintaining W&B server instances for airgapped environments.

## Install

Download and install WSM:

```bash
curl -sSL https://raw.githubusercontent.com/wandb/wsm/main/install.sh | bash /usr/local/bin
```

## Usage

Basic commands to get started:

```bash
wsm list
wsm download
wsm deploy
wsm version   # print the build's version, commit, and date
wsm --help
```

## Releasing

Releases are cut automatically when a PR is merged into `main` — no manual tagging needed.

- The version bump is chosen by a label on the merged PR:
  - `release:major` → `vX+1.0.0`
  - `release:minor` → `vX.Y+1.0`
  - `release:patch` (or no label) → `vX.Y.Z+1`
  - `release:skip` → no release
- The new tag is pushed and [GoReleaser](https://goreleaser.com) builds the binaries and
  publishes the GitHub release. The version is baked into the binary — check it with
  `wsm version`.
- Need to cut one by hand? Push a tag (`git tag vX.Y.Z && git push origin vX.Y.Z`) and the
  same build runs.

## Requirements

- Linux, macOS or Windows
- Bash shell
- curl
- tar

## Support

For issues and questions, please visit create an issue [here](https://github.com/wandb/wsm/issues).

For more information on how to use WSM, see the [WSM documentation](https://docs.wandb.ai/guides/hosting/self-managed/operator-airgapped/#install-wsm).
