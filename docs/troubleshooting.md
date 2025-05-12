# Troubleshooting & FAQ

Below are common issues and their solutions when operating Plexaubnet.

---

## SubnetClaim Stuck in `Pending`

**Symptoms**
* `status.phase` remains `Pending` beyond a few seconds.
* Controller logs show `no free block` or `strategy error`.

**Resolution**
1. Check that the parent SubnetPool has free capacity:
   ```bash
   kubectl get subnetpools example-pool -o yaml | yq .status
   ```
2. Verify requested `blockSize` fits within pool CIDR and limits.
3. Consider using the `Buddy` strategy to reduce fragmentation.

---

## Webhook Certificate Errors

**Symptoms**
* Pods fail to start with TLS errors.

**Resolution**
* If using self-signed certs, ensure the `cert-manager` is installed and ready.
* For air-gapped clusters, manually supply a secret named `plexaubnet-webhook-server-cert`.

---

## Prometheus Cannot Scrape Metrics

* Ensure port `8443` is allowed by NetworkPolicy.
* Add `insecureSkipVerify: true` on the scrape config if using self-signed certs.

---

## FAQ

**Q. Does Plexaubnet support IPv6?**  
A. IPv6 support is planned for roadmap **v0.7**.

**Q. Can I allocate the same CIDR to multiple clusters?**  
A. Not within the same SubnetPool. Use distinct pools or override `clusterID` if you accept duplicates.

**Q. What happens if I delete a SubnetClaim?**  
A. The corresponding Subnet enters `Released` phase and the block becomes available again.

---

_Last updated: 2025-05-12_ 