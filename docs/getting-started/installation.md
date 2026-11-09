# Installation

WSM ships as a pre-built binary for Linux, macOS and Windows, attached to each [GitHub release](https://github.com/wandb/wsm/releases). You can also build it from source.

## Option 1: Install a release binary (recommended)

On Linux and macOS, `install.sh` downloads the latest release for your OS and architecture and installs it into `/usr/local/bin` (it uses `sudo` if that directory isn't writable):

```bash
curl -fsSL https://raw.githubusercontent.com/wandb/wsm/main/install.sh | bash
```

To install somewhere else, pass the directory as the first argument:

```bash
curl -fsSL https://raw.githubusercontent.com/wandb/wsm/main/install.sh | bash -s -- "$HOME/.local/bin"
```

Or download the archive for your platform by hand from the [releases page](https://github.com/wandb/wsm/releases) (`wsm_<OS>_<arch>.tar.gz`, e.g. `wsm_Darwin_arm64.tar.gz`; on Windows `wsm_Windows_<arch>.zip`), extract it, and put `wsm` on your `PATH`. On Windows, download the `.zip` by hand: `install.sh` only fetches `.tar.gz` archives.

## Option 2: Build from source

### Requirements

- **git**, to clone this repository
- **Go 1.27.3 or later** (matches `go.mod`; older toolchains will not build)
- **make**

`make build` builds with `CGO_ENABLED=0 -tags containers_image_openpgp`, so no C compiler or system libraries are needed. Only a plain `go build` without that tag needs `gcc`, `pkg-config` and `gpgme`.

> **Go version**: distro packages (for example RHEL's `go-toolset`) are often too old to build WSM.
> Install an upstream Go 1.27.3+ from <https://go.dev/dl/> instead, e.g.:
> ```bash
> curl -fsSLO https://go.dev/dl/go1.27.3.linux-amd64.tar.gz
> sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.27.3.linux-amd64.tar.gz
> export PATH=$PATH:/usr/local/go/bin
> ```
> On macOS: `brew install go`.

### Build

```bash
git clone https://github.com/wandb/wsm.git
cd wsm

# Build ./wsm (stamps the version from git describe)
make build
./wsm version

# (Optional) Build and install to /usr/local/bin
sudo make install
```

## Verify Installation

```bash
wsm version
wsm --help
```

`wsm version` prints the version, commit and build date. `wsm --help` lists every command; see the [Command Reference](../reference/commands.md) for details.

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
