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
wsm --help
```

## Requirements

- Linux or macOS operating system
- Bash shell
- curl
- tar

## Support

For issues and questions, please visit create an issue [here](https://github.com/wandb/wsm/issues).

For more information on how to use WSM, see the [WSM documentation](https://docs.wandb.ai/guides/hosting/self-managed/operator-airgapped/#install-wsm).
