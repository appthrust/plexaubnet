# Metrics & Observability

Plexaubnet exposes Prometheus-format metrics via the controller `/metrics` endpoint.

---

## Key Metrics

| Metric | Labels | Description |
|--------|--------|-------------|
| `plexaubnet_pool_allocated_total` | `pool` | Number of allocated blocks per SubnetPool |
| `plexaubnet_claim_phase` | `phase` | Count of SubnetClaims by phase (Pending, Bound, Error) |
| `subnet_status_update_total` | `result` | Success / failure counter for status updates |
| `subnetpool_parent_requeue_total` | `event_type` | How many times a parent pool was requeued after child changes |
| `subnetpool_parent_reconcile_duration_seconds` | `le` | Histogram of reconcile duration |

---

## Grafana Dashboard

A sample dashboard JSON can be found under [`docs/samples/grafana-dashboard.json`](./samples/grafana-dashboard.json) (add your datasource).

![Dashboard screenshot](./samples/dashboard.png)

---

## Health Probes

| Endpoint | Port | Use |
|----------|------|-----|
| `/metrics` | `8443` | Prometheus scrape |
| `/healthz` | `8443` | Liveness probe |
| `/readyz` | `8443` | Readiness probe |

---

_Last updated: 2025-05-12_ 