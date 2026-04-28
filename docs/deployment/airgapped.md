# Airgapped Deployments

For environments without internet access, WSM supports downloading all required artifacts ahead of time and deploying from a local bundle.

## Prerequisites

- [WSM installed](../getting-started/installation.md)
- A machine with internet access to perform the download
- Docker (for pulling and saving images)
- Target environment with Docker registry or image-loading capability

## Step 1: List Required Images

From the internet-connected machine:

```bash
wsm list
```

This outputs a list of container images required for the deployment. Review the list to ensure your registry can host them.

## Step 2: Download Images

```bash
wsm download --platform linux/amd64
```

This creates a `bundle/` directory containing:

```
bundle/
├── spec.yaml           # Deployment specification
├── charts/
│   ├── operator-*.tgz
│   └── operator-wandb-*.tgz
└── images/
    ├── <image-name>/
    │   └── image.tgz
    └── ...
```

The default platform is `linux/amd64`. For ARM-based nodes, use:

```bash
wsm download --platform linux/arm64
```

## Step 3: Transfer the Bundle

Copy the entire `bundle/` directory to your airgapped environment using your preferred secure transfer mechanism (USB, secure file transfer, etc.).

## Step 4: Load Images

In the airgapped environment, load the images into your container runtime or private registry:

### Option A: Load directly into Docker

```bash
for image_dir in bundle/images/*/; do
  docker load -i "${image_dir}/image.tgz"
done
```

### Option B: Push to a private registry

```bash
# Tag and push each image to your registry
REGISTRY="my-registry.example.com/wandb"
for image_dir in bundle/images/*/; do
  image_name=$(basename "$image_dir")
  docker load -i "${image_dir}/image.tgz"
  docker tag "$image_name" "$REGISTRY/$image_name"
  docker push "$REGISTRY/$image_name"
done
```

If using a private registry, you will need to configure image pull secrets or ensure your cluster nodes are authenticated to the registry.

## Step 5: Deploy from Bundle

Use the legacy `wsm deploy` command with the bundle:

```bash
wsm deploy \
  --airgapped \
  --bundle bundle/ \
  --namespace wandb
```

## Customizing Values in Airgapped Mode

You can supply a values file to override chart defaults:

```bash
wsm deploy \
  --airgapped \
  --bundle bundle/ \
  --values custom-values.yaml \
  --namespace wandb
```

## Considerations for Airgapped Environments

- **Certificate Management**: Let's Encrypt will not work without internet access. Use the internal CA (`--create-ca`) or provide your own certificates.
- **Image Updates**: When upgrading, repeat the download and load steps with the new versions.
- **Helm Chart Updates**: WSM downloads the charts specified in the bundle. Ensure your bundle is generated with the desired chart versions.

## See Also

- [Installation Guide](../getting-started/installation.md)
- [SSL / TLS Configuration](../configuration/ssl-tls.md)
- [Managing the Deployment](../operations/managing.md)
