# Installation

Plexaubnet supports multiple installation methods to fit different deployment preferences.

---

## 1. Kustomize (Recommended)

```bash
kubectl apply -k github.com/appthrust/plexaubnet/config/default
```

This single command installs CRDs, RBAC, the controller, and webhook configurations using published overlays.

---

## 2. Helm Chart (Preview)

```bash
helm repo add plexaubnet https://appthrust.github.io/plexaubnet-helm
helm repo update
helm install plexaubnet plexaubnet/plexaubnet --namespace plexaubnet-system --create-namespace
```

| Parameter | Default | Description |
|-----------|---------|-------------|
| `controller.replicaCount` | `1` | Number of controller replicas |
| `metrics.enabled` | `true` | Expose Prometheus metrics |
| `webhook.enableCertManager` | `true` | Use cert-manager for webhook certificates |

> 🛈 The Helm chart is under active development. See the values file for the latest parameters.

---

## 3. Raw Manifests

If you prefer static YAMLs, download the aggregated manifest generated for the latest release tag:

```bash
VERSION=$(curl -s https://api.github.com/repos/appthrust/plexaubnet/releases/latest | jq -r .tag_name)
curl -L -o plexaubnet.yaml \
  https://raw.githubusercontent.com/appthrust/plexaubnet/${VERSION}/release/plexaubnet.yaml
kubectl apply -f plexaubnet.yaml
```

---

## Upgrade Procedure

1. Backup CRDs (optional but recommended).
2. Apply the new version using the same installation method (Kustomize/Helm/Manifest).
3. Verify controller pod restarts and CRD schema changes.
4. Monitor for deprecation warnings in controller logs.

---

_Last updated: 2025-05-12_ 