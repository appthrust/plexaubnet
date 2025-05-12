# API Reference (CRDs)

This page provides an excerpt of the CustomResourceDefinition schema shipped with Plexaubnet. For the authoritative definitions, see the YAML files located under [`config/crd/bases`](../config/crd/bases).

---

## SubnetPool

```yaml
# ... stripped for brevity ...
kind: CustomResourceDefinition
metadata:
  name: subnetpools.plexaubnet.io
spec:
  group: plexaubnet.io
  names:
    kind: SubnetPool
    plural: subnetpools
  versions:
  - name: v1alpha1
    schema:
      openAPIV3Schema:
        properties:
          spec:
            properties:
              cidr:
                type: string
                pattern: "^(?:[0-9]{1,3}\.){3}[0-9]{1,3}/[0-9]+$"
              defaultBlockSize:
                type: integer
              strategy:
                type: string
                enum: [Linear, Buddy]
          status:
            properties:
              allocatedCount:
                type: integer
```

---

## SubnetClaim

```yaml
kind: CustomResourceDefinition
metadata:
  name: subnetclaims.plexaubnet.io
spec:
  group: plexaubnet.io
  names:
    kind: SubnetClaim
  # ...
```

> The full schema includes validation, defaulting, and subresources. Use `kubectl explain <resource>` for on-cluster details.

---

## Download CRD YAMLs

```bash
git clone https://github.com/appthrust/plexaubnet.git
less config/crd/bases/subnetpools.plexaubnet.io.yaml
```

---

_Last updated: 2025-05-12_ 