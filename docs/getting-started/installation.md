# Installation

WSM is currently distributed as source code. A pre-built binary release pipeline is planned.

## Requirements

- **Operating System**: Linux, macOS, or Windows (with WSL)
- **git**: to clone this repository
- **Shell**: Bash or compatible shell
- **Go**: Version 1.26 or later
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
sudo apt install -y golang-go pkg-config libgpgme-dev
```

On RHEL/CentOS 9:
```bash
# Enable CodeReady Builder repo (required for gpgme-devel)
sudo dnf config-manager --enable codeready-builder-for-rhel-9-rhui-rpms  # AWS RHUI
# Or for non-cloud RHEL: sudo dnf config-manager --enable crb

sudo dnf install -y pkgconfig gpgme-devel

# go-toolset from RHEL repos is too old; install Go from the official release
curl -LO https://go.dev/dl/go1.26.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.0.linux-amd64.tar.gz
echo 'export PATH=/usr/local/go/bin:$PATH' >> ~/.bashrc
export PATH=/usr/local/go/bin:$PATH
```

On RHEL/CentOS 8:
```bash
# Enable CodeReady Builder repo (required for gpgme-devel)
sudo dnf config-manager --enable codeready-builder-for-rhel-8-rhui-rpms  # AWS RHUI
# Or for non-cloud RHEL: sudo dnf config-manager --enable powertools

sudo dnf install -y pkgconfig gpgme-devel

# go-toolset from RHEL repos is too old; install Go from the official release
curl -LO https://go.dev/dl/go1.26.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.0.linux-amd64.tar.gz
echo 'export PATH=/usr/local/go/bin:$PATH' >> ~/.bashrc
export PATH=/usr/local/go/bin:$PATH

# RHEL 8 ships podman aliased as docker; Kind requires Docker CE
sudo dnf config-manager --add-repo https://download.docker.com/linux/rhel/docker-ce.repo
sudo dnf install -y docker-ce docker-ce-cli containerd.io --allowerasing
sudo systemctl enable --now docker
sudo usermod -aG docker $USER

# RHEL 8 defaults to cgroup v1; Kind requires cgroup v2
sudo grubby --update-kernel=ALL --args="systemd.unified_cgroup_hierarchy=1"
sudo reboot
# After reboot, verify: stat -fc %T /sys/fs/cgroup/  (should print "cgroup2fs")
```

On Fedora:
```bash
sudo dnf install -y golang pkgconfig gpgme-devel
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
  deploy-v2   Deploy v2 operator and resources
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
