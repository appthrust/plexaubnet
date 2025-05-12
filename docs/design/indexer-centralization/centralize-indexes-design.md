# Task 2 ― Centralize FieldIndexer Registrations [IMPLEMENTED]
*Design & Implementation Specification*

---

## 1. Problem Statement
Multiple controllers (`SubnetReconciler`, `CIDRAllocatorReconciler`, `PoolStatusReconciler`) register identical FieldIndexers individually.
This produces *indexer conflict / already exists* warnings, scatters error-handling logic, and complicates future maintenance.

## 2. Goals & Non-Goals
| Item | Goal | Notes |
|------|------|-------|
| Eliminate duplicate index registration | ✅ | One authoritative place only |
| Provide uniform error handling | ✅ | Ignore "already exists", fail fast otherwise |
| Offer single source for index key constants | ✅ | `PoolRefField`, `ParentPoolLabelIndex` |
| Keep controller `SetupWithManager` minimal | ✅ | Controllers *use*, never *register* |
| **Non-Goal:** change existing reconciliation logic | ❌ | Functional behaviour unchanged |

## 3. Current State Analysis

| File (Line) | Resource | Index Key | Notes |
|-------------|----------|-----------|-------|
| `subnet_controller.go:117` | `Subnet` | `spec.poolRef` | Local ignore logic |
| `controller.go:153` | `Subnet` | `spec.poolRef` | |
| `controller.go:162` | `SubnetClaim` | `spec.poolRef` | |
| `poolstatus_controller.go:242` | `Subnet` | `spec.poolRef` | Uses `handleIndexError()` |
| `poolstatus_controller.go:252` | `SubnetPool` | `metadata.labels[plexaubnet.io/parent]` | |

*Observations*
- Same index registered **4×** (`spec.poolRef`) with slightly different helper closures.
- Two styles of error handling (`handleIndexError`, ad-hoc).
- **Race window**: depending on init order, first controller wins, others warn.

## 4. Proposed Architecture

```mermaid
flowchart TD
    subgraph Bootstrap
        Main(cmd) --> SetupIndexes[SetupFieldIndexes()]
    end
    SetupIndexes -->|register once| FieldIndexer
    subgraph Controllers
        Subnet --> FieldIndexer
        CIDRAllocator --> FieldIndexer
        PoolStatus --> FieldIndexer
    end
```

### 4.1 New Component

`internal/controller/init_index.go`

```go
// Package cidrallocator – centralized index registration
package cidrallocator

const (
    PoolRefField         = "spec.poolRef"
    ParentPoolLabelIndex = "metadata.labels[plexaubnet.io/parent]" 
    PageSize             = 1000
)

// SetupFieldIndexes registers all FieldIndexers once.
// Safe to call exactly one time during manager startup.
func SetupFieldIndexes(ctx context.Context, mgr ctrl.Manager, log logr.Logger) error { ... }
```

*Highlights*
- **Single function** invoked from `cmd/main.go` after manager instantiation.
- Shared `handle` closure implements uniform error filtering.
- Exports constants for controllers to reference.
- Added `PageSize` constant for paginated list operations.

### 4.2 Call Site Modification

```go
// cmd/main.go
if err := cidrallocator.SetupFieldIndexes(ctx, mgr, ctrl.Log.WithName("index-setup")); err != nil {
    setupLog.Error(err, "unable to register field indexes; exiting")
    os.Exit(1)
}
```

### 4.3 Controller Cleanup
Remove all `mgr.GetFieldIndexer().IndexField(...` blocks and helper functions from:

1. `internal/controller/subnet_controller.go`
2. `internal/controller/controller.go` (CIDRAllocatorReconciler)
3. `internal/controller/poolstatus_controller.go`

Controllers import `cidrallocator.PoolRefField` constant when needed.

## 5. Detailed Code Diff (excerpt)

<details>
<summary>subnet_controller.go (before → after)</summary>

```diff
- indexErr := mgr.GetFieldIndexer().IndexField(
-   context.Background(),
-   &ipamv1.Subnet{},
-   r.PoolRefField,
-   func(obj client.Object) []string { ... })
- if indexErr != nil { ... }
+ // Indexes registered globally by cidrallocator.SetupFieldIndexes
```
</details>

Similar deletions apply to other controllers.

## 6. Test Strategy

| Level | Tool / Command | Success Criteria |
|-------|----------------|------------------|
| Unit | `go test ./internal/controller/...` | Pass; no new failures |
| EnvTest | `make test` | Startup without `indexer conflict` messages |
| Static | `go vet ./...`, `golangci-lint run` | No issues |
| Manual | Deploy to kind cluster | Controllers reconcile normally |

## 7. Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|-----------|
| Forgot to delete a legacy registration | duplicate warnings persist | `grep "IndexField("` in CI |
| Future dev adds new index locally | design drift | Add linter rule / PR template check |
| Breaking change in index keys | controllers fail lookup | Document procedure & version bump |

## 8. Implementation Status

1. Centralization code deployed with cleanup of all controllers ✅
2. CI passed with no warnings ✅
3. Deployed to all environments with monitoring ✅
4. Released in `v0.9.0` ✅

Current implementation patterns:

```go
// Controller using the centralized index
func (r *PoolStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // All controllers now use constants from cidrallocator package
    var allocations ipamv1.SubnetList
    if err := r.List(ctx, &allocations, 
                     client.MatchingFields{cidrallocator.PoolRefField: pool.Name}); err != nil {
        return ctrl.Result{}, err
    }
    
    // Large lists can be paginated using PageSize
    var page ipamv1.SubnetList
    if err := r.List(ctx, &page, 
                     client.MatchingFields{cidrallocator.PoolRefField: pool.Name},
                     client.Limit(cidrallocator.PageSize)); err != nil {
        return ctrl.Result{}, err
    }
}
```

## 9. Additional Benefits

- **Centralized Documentation:** All indexes are now documented in one place with consistent naming patterns
- **Reduced Startup Time:** Fewer indexer conflicts leads to faster controller initialization 
- **Simplified Testing:** Fewer warnings in test logs makes issue diagnosis easier
- **Standardized Pagination:** PageSize constant ensures consistent memory usage across large list operations

---

## 10. Appendix – Full Source Listing

```go
// internal/controller/init_index.go (key segments)

// Package cidrallocator - centralized index registration for CIDR allocation
package cidrallocator

import (
    "context"
    "strings"

    ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
    "github.com/go-logr/logr"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
    // PoolRefField is the field index for spec.poolRef
    PoolRefField = "spec.poolRef"
    // ParentPoolLabelIndex is the field index for metadata.labels[plexaubnet.io/parent]
    ParentPoolLabelIndex = "metadata.labels[plexaubnet.io/parent]"
    // PageSize is the default page size for List operations to limit memory usage
    PageSize = 1000
)

// SetupFieldIndexes registers all FieldIndexers once.
// Safe to call exactly one time during manager startup.
func SetupFieldIndexes(ctx context.Context, mgr ctrl.Manager, log logr.Logger) error {
    idx := mgr.GetFieldIndexer()

    // Common error handler
    handle := func(err error, name string) error {
        if err == nil {
            return nil
        }
        if strings.Contains(err.Error(), "already exists") ||
            strings.Contains(err.Error(), "indexer conflict") {
            log.V(1).Info("Index already registered, reusing", "index", name)
            return nil
        }
        log.Error(err, "Cannot register field index", "index", name)
        return err
    }

    // Register Subnet -> spec.poolRef
    if err := handle(idx.IndexField(ctx,
        &ipamv1.Subnet{}, PoolRefField,
        func(o client.Object) []string {
            return []string{o.(*ipamv1.Subnet).Spec.PoolRef}
        }), PoolRefField); err != nil {
        return err
    }

    // Register SubnetClaim -> spec.poolRef
    if err := handle(idx.IndexField(ctx,
        &ipamv1.SubnetClaim{}, PoolRefField,
        func(o client.Object) []string {
            return []string{o.(*ipamv1.SubnetClaim).Spec.PoolRef}
        }), PoolRefField+"(SubnetClaim)"); err != nil {
        return err
    }

    // Register SubnetPool -> metadata.labels[plexaubnet.io/parent]
    if err := handle(idx.IndexField(ctx,
        &ipamv1.SubnetPool{}, ParentPoolLabelIndex,
        func(o client.Object) []string {
            if p, ok := o.(*ipamv1.SubnetPool); ok {
                if parent, ok := p.Labels["plexaubnet.io/parent"]; ok {
                    return []string{parent}
                }
            }
            return nil
        }), ParentPoolLabelIndex); err != nil {
        return err
    }

    return nil
}
```

## 11. Mermaid Diagram (Post-Implementation Responsibility)

```mermaid
flowchart LR 
  subgraph "Controller Layer" 
    A[SubnetReconciler]
    B[CIDRAllocatorReconciler] 
    C[PoolStatusReconciler]
  end
  
  subgraph "Shared" 
    X[SetupFieldIndexes()]
  end
  
  MGR[(ctrl.Manager)]
  
  MGR --> X
  X --> A
  X --> B 
  X --> C
  
  A -->|uses indexes| MGR
  B -->|uses indexes| MGR
  C -->|uses indexes| MGR