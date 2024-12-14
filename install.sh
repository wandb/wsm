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

#https://github.com/wandb/wsm/releases/download/v0.1.0/wsm_Linux_arm64.tar.gz
#https://github.com/wandb/wsm/releases/download/v0.1.0/wsm_Linux_amd64.tar.gz

# ------------------- MicroK8s Installation -------------------

read -p "Do you want to install MicroK8s? (y/n): " INSTALL_MICROK8S

if [[ $INSTALL_MICROK8S == "y" || $INSTALL_MICROK8S == "Y" ]]; then
    if [[ "$OS" != "Linux" ]]; then
        echo "Error: For macOS or Windows installation, please visit: https://microk8s.io/#install-microk8s."
        exit 1
    fi
    
    echo "Checking for Snap..."
    if ! command -v snap &> /dev/null; then
        echo "Snap not found. Installing Snap..."
        sudo apt update
        sudo apt install snapd -y
    else
        echo "Snap is already installed."
    fi

    # Step 1: Install MicroK8s

    echo "Starting MicroK8s installation..."
    sudo snap install microk8s --classic

    # Step 4: Set up Aliases
    echo "Setting up aliases..."
    sudo snap alias microk8s.kubectl kubectl
    sudo snap alias microk8s.helm helm

    # Step 2: Configure Permissions
    echo "Configuring permissions for MicroK8s..."
    sudo usermod -a -G microk8s $USER
    sudo mkdir -p $HOME/.kube
    sudo touch $HOME/.kube/config
    sudo chown -R $USER:$USER $HOME/.kube
    
    # Step 3: Start Microk8s
    echo "Starting MicroK8s..."
    sudo microk8s.start
    
    # Step 3: Enable Required Add-ons
    echo "Enabling MicroK8s add-ons..."
    sudo microk8s.enable dns ingress hostpath-storage dashboard

    # Wait for MicroK8s to be ready
    echo "Waiting for MicroK8s to be ready..."
    sudo microk8s.status --wait-ready

    # Step 5: Configure Kubeconfig
    echo "Configuring kubectl to use MicroK8s..."
    sudo microk8s.kubectl config view --raw > $HOME/.kube/config
    echo "MicroK8s installation and configuration completed. Please logout and login again or run 'newgrp microk8s'"

else
    echo "Skipping MicroK8s installation."
fi