# Quick Start

> Five-minute guide to install Plexaubnet and allocate your first subnet.

## Prerequisites

* Kubernetes **v1.30** or later (tested up to v1.32)
* `kubectl` configured for your cluster
* Cluster-wide permissions (or administration rights)

---

## 1. Install the Operator (Kustomize)

```bash
kubectl apply -k github.com/appthrust/plexaubnet/config/default
```

The command installs:

* CRDs (`SubnetPool`, `SubnetClaim`, `Subnet`, `SubnetPoolClaim`)
* Controller deployment (`plexaubnet-controller-manager`)
* RBAC and webhook configuration

> ℹ️  Alternative installation methods are listed in the [Installation](./installation.md) section.

---

## 2. Create a SubnetPool

```yaml
apiVersion: plexaubnet.io/v1alpha1
kind: SubnetPool
metadata:
  name: example-pool
spec:
  cidr: 10.10.0.0/16
  defaultBlockSize: 24
  strategy: Linear
```

```bash
kubectl apply -f pool.yaml
```

---

## 3. Request a Subnet via SubnetClaim

```yaml
apiVersion: plexaubnet.io/v1alpha1
kind: SubnetClaim
metadata:
  name: cluster-a
spec:
  poolRef: example-pool
  clusterID: cluster-a
  blockSize: 24
```

```bash
kubectl apply -f claim.yaml
```

---

## 4. Verify Allocation

```bash
kubectl get subnetclaims
kubectl get subnetpools example-pool -o yaml | grep allocatedCIDRs -A3
kubectl get subnets -l plexaubnet.io/cluster-id=cluster-a
```

You should see `10.10.0.0/24` allocated to `cluster-a` and `allocatedCount` incremented in the pool status.

---

## Next Steps

* Explore advanced **allocation strategies** and **pool splitting** in the [Concepts](./concepts.md) guide.
* Integrate Plexaubnet metrics with Prometheus using the [Metrics & Observability](./metrics.md) section.
* Check common issues in the [Troubleshooting & FAQ](./troubleshooting.md) page.

---

_Last updated: 2025-05-12_ 