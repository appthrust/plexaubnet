# CLI Reference

Plexaubnet itself does not ship a dedicated CLI. Day-to-day operations are performed with standard Kubernetes tools:

* `kubectl` — create, edit, and inspect Plexaubnet resources
* `kustomize` — install manifests from the repo
* `helm` — install / upgrade the Helm chart (preview)

Below are a few handy commands.

---

## Resource Inspection

```bash
# Show all SubnetPools
kubectl get subnetpools

# Explain CRD schema
kubectl explain subnetpool.spec

# Watch SubnetClaims and Subnets together
watch -n5 'kubectl get subnetclaims; echo; kubectl get subnets'
```

---

## Bulk Deletion (Cleanup)

```bash
# WARNING: removes all allocated subnets
a=$(kubectl get subnets -o name); [ -n "$a" ] && kubectl delete $a
```

---

## Metrics Port-Forward

```bash
kubectl -n plexaubnet-system port-forward deployment/plexaubnet-controller-manager 8443:8443
curl -s http://localhost:8443/metrics | head
```

---

_Last updated: 2025-05-12_ 