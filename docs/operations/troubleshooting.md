# Troubleshooting

Common issues and their solutions when deploying or operating W&B with WSM.

## Common Issues

### Deployment Failures

#### "context is required"

**Symptom**: WSM exits with `context is required`.

**Cause**: The `--context` flag is missing and there is no current kubectl context set.

**Fix**:
```bash
# Set a specific context
wsm deploy-v2 operator --context <your-context>

# Or set the default context first
kubectl config use-context <your-context>
```

---

#### "failed to create CA issuer" / Certificate not ready

**Symptom**: WSM fails during W&B CR deployment with CA issuer errors.

**Cause**: cert-manager is not fully ready, or CRDs are missing.

**Fix**:
```bash
# Check cert-manager pods
kubectl get pods -n cert-manager

# Check cert-manager CRDs
kubectl get crds | grep cert-manager

# If cert-manager is not running, reinstall
wsm deploy-v2 operator --context <ctx> --install-cert-manager true
```

---

#### Gateway API CRDs missing

**Symptom**: nginx-gateway-fabric pods fail to start.

**Cause**: Gateway API standard CRDs are not installed.

**Fix**: WSM installs Gateway API CRDs automatically when deploying nginx-gateway-fabric. If you skipped it, manually install:

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.0/standard-install.yaml
```

---

#### "failed to check cert-manager deployment"

**Symptom**: WSM cannot verify cert-manager readiness.

**Cause**: kubectl context not set correctly, or RBAC prevents access.

**Fix**: Verify context and permissions:
```bash
kubectl config current-context
kubectl get deployment -n cert-manager cert-manager
```

---

### Operator Issues

#### Operator webhook not ready

**Symptom**: W&B CR fails to apply with webhook errors.

**Cause**: The operator's mutating webhook CA bundle is not yet injected.

**Fix**: Wait 30–60 seconds and retry. The operator requires time for cert-manager to inject the CA bundle into the webhook configuration. WSM's `WaitForOperator` function handles this automatically, but manual retries may be needed in some environments.

```bash
# Check webhook status
kubectl get mutatingwebhookconfiguration wandb-operator-mutating-webhook-configuration

# Check CA bundle is populated
kubectl get mutatingwebhookconfiguration wandb-operator-mutating-webhook-configuration -o yaml | grep caBundle
```

---

#### Operator pods crash-looping

**Symptom**: `wandb-operator` pods restart repeatedly.

**Cause**: Insufficient resources, missing RBAC permissions, or image pull failures.

**Fix**:
```bash
# Check pod events
kubectl describe pod -n wandb-operators -l app.kubernetes.io/name=wandb-operator

# Check logs
kubectl logs -n wandb-operators deployment/wandb-operator
```

---

### W&B Instance Issues

#### W&B CR not reaching Ready state

**Symptom**: `kubectl get wandb -n wandb` shows `READY` as `Unknown` or `False`.

**Cause**: One or more dependent pods are not healthy (MySQL, Redis, Kafka, etc.).

**Fix**:
```bash
# Check all pods in the W&B namespace
kubectl get pods -n wandb

# Check events for failing pods
kubectl describe pod <pod-name> -n wandb

# Check operator logs for reconciliation errors
kubectl logs -n wandb-operators deployment/wandb-operator
```

Common root causes:
- **Insufficient CPU/memory**: Scale up node pools or use a smaller `--size`.
- **Storage issues**: Ensure a default StorageClass exists and can provision PVCs.
- **Image pull failures**: Check network access to the container registry.

---

#### Cannot access W&B via hostname

**Symptom**: Browser cannot resolve or connect to the W&B hostname.

**Cause**: DNS not configured, or gateway external IP not assigned.

**Fix**:
```bash
# Check gateway has an address
kubectl get gateway -n wandb

# Check service has an external IP
kubectl get service -n nginx-gateway

# For Kind, ensure you are using localhost:8080 (or custom port)
curl http://localhost:8080/healthz
```

---

### Kind-Specific Issues

#### Port 8080/8443 already in use

**Symptom**: Cannot create Kind cluster or access W&B locally.

**Fix**: Use custom ports:
```bash
wsm cluster create --cluster-name wandb-local --http-port 9090 --https-port 9443
wsm deploy-v2 wandb deploy --context kind-wandb-local --wandb-hostname http://localhost:9090
```

---

#### Docker daemon not running

**Symptom**: `wsm cluster create` fails with Docker errors.

**Fix**: Start Docker Desktop or the Docker daemon before running WSM.

---

### Certificate / SSL Issues

#### Browser shows certificate warning

**Symptom**: `NET::ERR_CERT_AUTHORITY_INVALID` or similar.

**Cause**: Using the internal CA without trusting it on the client.

**Fix**: Retrieve and trust the CA certificate:
```bash
wsm deploy-v2 wandb get-ca-cert \
  --wandb-name wandb \
  --wandb-namespace wandb \
  --output-dir ./certs
```

Then follow the [platform-specific trust instructions](../configuration/ssl-tls.md#trusting-the-internal-ca-certificate).

---

#### Let's Encrypt certificate not issued

**Symptom**: Certificate resource stuck in `Pending`.

**Cause**: ACME challenge failing (DNS not propagated, port 80 blocked, wrong solver config).

**Fix**:
```bash
# Check certificate events
kubectl describe certificate wandb-tls-secret -n wandb

# Check Challenge resources
kubectl get challenges -n wandb
kubectl describe challenge <challenge-name> -n wandb

# Verify DNS resolves correctly
nslookup wandb.example.com

# Verify port 80 is reachable from the internet
curl http://wandb.example.com/.well-known/acme-challenge/test
```

---

## Gathering Logs

### Operator Logs

```bash
kubectl logs -n wandb-operators deployment/wandb-operator
kubectl logs -n wandb-operators deployment/wandb-operator --previous
```

### W&B Application Logs

```bash
# Find the main app pod
kubectl get pods -n wandb -l app.kubernetes.io/component=app

# Get logs
kubectl logs -n wandb deployment/wandb-app
```

### Data Store Logs

```bash
kubectl logs -n wandb statefulset/wandb-mysql
kubectl logs -n wandb statefulset/wandb-redis
kubectl logs -n wandb statefulset/wandb-kafka
kubectl logs -n wandb statefulset/wandb-clickhouse
```

### cert-manager Logs

```bash
kubectl logs -n cert-manager deployment/cert-manager
kubectl logs -n cert-manager deployment/cert-manager-webhook
```

### nginx-gateway Logs

```bash
kubectl logs -n nginx-gateway deployment/nginx-gateway-nginx-gateway-fabric
```

## Complete Reset

If you need to start completely fresh:

### Local Kind

```bash
# Destroy everything
wsm cluster destroy --cluster-name wandb-local

# Or cleanup resources but keep cluster
wsm cluster cleanup --context kind-wandb-local
```

### Cloud Clusters

```bash
# Remove everything WSM deployed
wsm cluster cleanup --context <ctx>

# Re-deploy from scratch
wsm deploy-v2 operator --context <ctx> --include-cr
```

---

## Getting Help

If the above steps do not resolve your issue:

1. Gather logs from the relevant components
2. Run `kubectl describe` on failing resources
3. Create an issue at [github.com/wandb/wsm](https://github.com/wandb/wsm) with:
   - WSM version
   - Kubernetes version and platform
   - Relevant error messages and logs
   - Steps to reproduce
