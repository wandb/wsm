#!/bin/bash

# Define GitHub repo
GITHUB_REPO="wandb/wsm"

# Fetch the latest release tag from GitHub
API_URL="https://api.github.com/repos/${GITHUB_REPO}/releases/latest"
RELEASE_TAG=$(curl -s $API_URL | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

if [ -z "$RELEASE_TAG" ]; then
    echo "Failed to fetch the latest release tag. Exiting."
    exit 1
fi

# Detect OS and architecture
OS=$(uname | tr '[:upper:]' '[:lower:]')
OS="${OS^}" # Capitalize the first letter of OS
ARCH=$(uname -m)
case $ARCH in
    x86_64) ARCH="x86_64";;
    i386) ARCH="i386";;
    i686) ARCH="i386";;
    arm*) ARCH="arm64";;
    aarch64) ARCH="arm64";;
    *) echo "Unsupported architecture: $ARCH"; exit 1;;
esac

# Construct download URL
FILENAME="wsm_${OS}_${ARCH}.tar.gz"
DOWNLOAD_URL="https://github.com/${GITHUB_REPO}/releases/download/${RELEASE_TAG}/${FILENAME}"
echo "Download URL: ${DOWNLOAD_URL}"

# Download tarzip file
echo "Downloading ${FILENAME}..."
curl -L -o "${FILENAME}" "${DOWNLOAD_URL}"

# Verify download success
if [ $? -ne 0 ]; then
    echo "Download failed."
    exit 1
fi

# Extract the tarzip file
echo "Extracting ${FILENAME}..."
tar -xzf "${FILENAME}" || { echo "Failed to extract ${FILENAME}. Exiting."; exit 1; }

# Optionally, move to specific location
# sudo mv yourbinary /usr/local/bin

echo "Installation completed."

https://github.com/wandb/wsm/releases/download/v0.1.0/wsm_Linux_arm64.tar.gz
https://github.com/wandb/wsm/releases/download/v0.1.0/wsm_Linux_amd64.tar.gz