# Installation

WSM is currently distributed as source code. A pre-built binary release pipeline is planned.

## Requirements

- **Operating System**: Linux, macOS, or Windows (with WSL)
- **git**: to clone this repository
- **Shell**: Bash or compatible shell
- **Go**: Version 1.23 or later
- **pkg-config** and **gpgme**: Required for Go build dependencies

### macOS Dependencies

```bash
brew install go pkg-config gpgme 
```

### Linux Dependencies

On Debian/Ubuntu:
```bash
sudo add-apt-repository ppa:longsleep/golang-backports
sudo apt update
sudo apt install golang-go
sudo apt-get install -y pkg-config libgpgme-dev
```

On RHEL/CentOS/Fedora:
```bash
sudo dnf install -y go-toolset pkgconfig gpgme-devel
```

## Building from Source

### Option 1: Build manually

```bash
# Clone the repository
git clone https://github.com/wandb/wsm.git
cd wsm

# Checkout the operator-v2 branch
git checkout operator-v2

# Build the binary
go build -o wsm ./cmd/wsm

# Verify
./wsm --help

# (Optional) Install to your PATH
sudo mv wsm /usr/local/bin/wsm
```

### Option 2: Use Makefile

```bash
# Clone and checkout (as above)
git clone https://github.com/wandb/wsm.git
cd wsm
git checkout operator-v2

# Build and install to /usr/local/bin
sudo make install
```

## Verify Installation

After installation, confirm the CLI is available:

```bash
wsm --help
```

Expected output:
```
A utility for managing Weights & Biases Server deployments

Usage:
  wsm [command]

Available Commands:
  cluster     Manage Kubernetes clusters
  completion  Generate the autocompletion script for the specified shell
  console     Open the W&B console
  deploy      Deploy W&B operator and resources (legacy)
  deploy-v2   Deploy v2 operator and resources (recommended)
  help        Help about any command

Flags:
  -h, --help   help for wsm
```

## Shell Completion

WSM supports shell autocompletion via Cobra:

```bash
# Bash
wsm completion bash > /etc/bash_completion.d/wsm

# Zsh
wsm completion zsh > "${fpath[1]}/_wsm"

# Fish
wsm completion fish > ~/.config/fish/completions/wsm.fish
```

## Next Steps

- [Quick Start — Local Kind Cluster](./quickstart-kind.md)
