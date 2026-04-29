# SSL / TLS Configuration

WSM supports two TLS modes for terminating SSL at the gateway:

1. **Let's Encrypt** — Automatically provisioned, publicly trusted certificates (recommended for production).
2. **Internal CA** — WSM-managed self-signed certificate authority (default, ideal for local development and testing).

| Mode | Use Case | Trust Required |
|------|----------|--------------|
| Let's Encrypt | Production, public internet | None (automatically trusted) |
| Internal CA | Development, testing | Trust CA on each client |

## How WSM Handles TLS

When you deploy with `--create-ca=true` (default) and specify an `https://` hostname, WSM automatically:

1. Creates a **self-signed Issuer** in the W&B namespace
2. Generates a **root CA Certificate** (stored in `<name>-root-cert` secret)
3. Creates a **CA Issuer** referencing the root certificate
4. Configures the W&B CR to use this CA issuer for TLS

The operator then manages the lifecycle of the certificate and injects it into the gateway.

## Mode 1: Let's Encrypt (Recommended)

### Prerequisites

- A public DNS hostname pointing to your cluster's gateway (e.g., `wandb.example.com`)
- Port 80 and/or 443 reachable from the internet (for ACME validation)
- cert-manager installed with Gateway API support (WSM does this automatically via `--enable-gateway-api`)

### Step 1: Create a Let's Encrypt Issuer

Create an `Issuer` in the same namespace where W&B will be deployed (`wandb`). The `Issuer` is scoped to that namespace and will be referenced by the Gateway.

Save as `letsencrypt-issuer.yaml`:

```yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: letsencrypt-prod
  namespace: wandb
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: your-email@example.com
    privateKeySecretRef:
      name: letsencrypt-prod-key
    solvers:
      - http01:
          gatewayHTTPRoute:
            parentRefs:
              - name: wandb-gateway   # <wandb-name>-gateway
                namespace: wandb      # must match --wandb-namespace
                kind: Gateway
```

> The Gateway created by the W&B operator is named `<wandb-name>-gateway` (default: `wandb-gateway`). The `gatewayHTTPRoute` block tells cert-manager which Gateway to create ACME challenge routes on.
>
> If you use a different `--wandb-name`, update the Gateway name accordingly.

For DNS-01 validation (recommended for wildcard certificates or private clusters), use a DNS provider solver:

```yaml
    solvers:
      - dns01:
          route53:          # For AWS Route 53
            region: us-east-1
          # or cloudDNS:    # For Google Cloud DNS
          # or azureDNS:    # For Azure DNS
```

Apply it:

```bash
kubectl apply -f letsencrypt-issuer.yaml
```

### Step 2: Deploy W&B with Let's Encrypt

Use the `--issuer-name` flag to tell WSM to use your Issuer for TLS. You must also set `--create-ca=false` so WSM does not create its own internal CA.

```bash
wsm deploy-v2 wandb deploy \
  --context <your-context> \
  --wandb-hostname https://wandb.example.com \
  --wandb-namespace wandb \
  --size small \
  --license "YOUR_WANDB_LICENSE" \
  --create-ca=false \
  --issuer-name letsencrypt-prod
```

What this does:
- Disables the internal CA (`--create-ca=false`)
- Tells WSM to configure the CR with `networking.tls.certManager.issuer: letsencrypt-prod`
- The operator creates the Gateway with a listener that cert-manager detects and provisions a certificate for

### Step 3: Verify

```bash
# Check certificate status
kubectl get certificate -n wandb
kubectl describe certificate -n wandb

# Check the TLS secret exists
kubectl get secret wandb-tls-secret -n wandb

# Check gateway status
kubectl get gateway -n wandb
```

### Alternative: Manual Certificate Resource

If you prefer not to use the `--issuer-name` flag, you can create a `Certificate` resource manually and patch the CR to use it. Only use one method — either `--issuer-name` or manual `Certificate`, not both.

Create the `Certificate`:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: wandb-tls
  namespace: wandb
spec:
  secretName: wandb-tls-secret
  issuerRef:
    name: letsencrypt-prod
    kind: Issuer
  dnsNames:
    - wandb.example.com
  usages:
    - digital signature
    - key encipherment
```

```bash
kubectl apply -f wandb-certificate.yaml
kubectl wait --for=condition=Ready certificate/wandb-tls -n wandb --timeout=300s
```

Then patch the CR to reference the secret:

```bash
kubectl patch wandb wandb -n wandb --type=merge -p \
  '{"spec":{"networking":{"tls":{"secretName":"wandb-tls-secret"}}}}'
```

## Mode 2: Internal CA (Default)

This is the default behavior when you deploy with `--create-ca=true` and an `https://` hostname. No additional configuration is required.

### What Gets Created

For a W&B instance named `wandb` in namespace `wandb`:

| Resource | Name | Type |
|----------|------|------|
| Self-signed Issuer | `wandb-selfsigned-issuer` | `cert-manager.io/v1` Issuer |
| Root CA Certificate | `wandb-root-cert` | `cert-manager.io/v1` Certificate |
| CA Issuer | `wandb-ca-issuer` | `cert-manager.io/v1` Issuer |
| TLS Secret | `wandb-tls-secret` | Kubernetes Secret |

The `wandb-root-cert` secret contains the CA certificate under the `ca.crt` key.

### Retrieving the CA Certificate

```bash
wsm deploy-v2 wandb get-ca-cert \
  --wandb-name wandb \
  --wandb-namespace wandb \
  --output-dir ./certs
```

This writes the CA certificate to `./certs/wandb.crt`. You can then distribute this file to client machines that need to trust the W&B instance.

### Trusting the Internal CA Certificate

#### macOS

```bash
# Add to keychain
sudo security add-trusted-cert -d -r trustRoot \
  -k /Library/Keychains/System.keychain \
  ./certs/wandb.crt
```

Or open **Keychain Access**, drag the `.crt` file into **System** keychain, and set trust to **Always Trust**.

#### Linux

```bash
# Copy to system CA store
sudo cp ./certs/wandb.crt /usr/local/share/ca-certificates/wandb.crt
sudo update-ca-certificates
```

For different distributions, the CA path may vary:
- Debian/Ubuntu: `/usr/local/share/ca-certificates/`
- RHEL/CentOS/Fedora: `/etc/pki/ca-trust/source/anchors/`, then `sudo update-ca-trust`
- Alpine: `/usr/local/share/ca-certificates/`, then `update-ca-certificates`

#### Windows

1. Open **Certmgr.msc** (Certificate Manager)
2. Expand **Trusted Root Certification Authorities**
3. Right-click **Certificates** → **All Tasks** → **Import**
4. Select `wandb.crt` and confirm

Or via PowerShell (admin):

```powershell
Import-Certificate -FilePath "C:\path\to\wandb.crt" -CertStoreLocation Cert:\LocalMachine\Root
```

## Disabling Automatic CA Creation

To skip CA creation and manage certificates manually:

```bash
wsm deploy-v2 wandb deploy \
  --context <your-context> \
  --wandb-hostname https://wandb.example.com \
  --create-ca=false \
  --cr-file custom-with-my-cert.yaml
```

Your custom CR should reference an existing TLS secret or cert-manager issuer.

## Custom Certificates

If you have your own certificate (e.g., from a corporate CA), create a Kubernetes secret:

```bash
kubectl create secret tls wandb-tls-secret \
  --cert=path/to/cert.pem \
  --key=path/to/key.pem \
  -n wandb
```

Then reference it in your CR:

```yaml
spec:
  networking:
    tls:
      secretName: wandb-tls-secret
```

## Troubleshooting TLS

| Issue | Diagnosis |
|-------|-----------|
| Certificate not ready | `kubectl describe certificate -n wandb` |
| Gateway not serving HTTPS | `kubectl describe gateway -n wandb` |
| CA cert missing | `kubectl get secret wandb-root-cert -n wandb` |
| Browser still warns | Ensure CA was added to system trust store and browser restarted |

## See Also

- [cert-manager Documentation](https://cert-manager.io/docs/)
- [cert-manager + Gateway API](https://cert-manager.io/docs/usage/gateway/)
- [Customizing the Deployment](customizing.md)
