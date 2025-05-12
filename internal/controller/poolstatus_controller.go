/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cidrallocator

import (
	"context"
	"strings"
	"time"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"github.com/appthrust/plexaubnet/internal/controller/statuscalc"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlbuilder "sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlpkg "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

//+kubebuilder:rbac:groups=plexaubnet.io,resources=subnetpools,verbs=get;list;watch;patch;update
//+kubebuilder:rbac:groups=plexaubnet.io,resources=subnetpools/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=plexaubnet.io,resources=subnets,verbs=get;list;watch

// PoolStatusReconciler reconciles SubnetPool status based on allocations and child pools
type PoolStatusReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// ControllerName is used to specify a unique controller name during testing.
	// If not specified, the default name "poolstatus-controller" is used.
	ControllerName string
}

// Reconcile handles SubnetPool status reconciliation
func (r *PoolStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("subnetpool", req.NamespacedName)
	logger.Info("Starting PoolStatus reconciliation", "request", req)

	// 1. Get the target SubnetPool
	pool := &ipamv1.SubnetPool{}
	if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("SubnetPool not found, it may have been deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get SubnetPool")
		return ctrl.Result{}, err
	}

	// 2. List related Subnets (using PoolRef index)

	// The manualSubnetScan function is already implemented to switch based on build tags.
	// In debug builds, it fetches and verifies all items; in production builds, it returns 0.
	manualMatchCount := manualSubnetScan(ctx, r.Client, pool)

	// This call itself is kept to maintain existing logic.
	// In production builds, the function's internals are optimized, so the impact is small.
	logger.V(1).Info("Subnet allocation check",
		"namespace", pool.Namespace,
		"poolName", pool.Name,
		"manualMatches", manualMatchCount)

	// 2.2 Get using field index (original code)
	// Memory optimization: Limit memory usage with pagination.
	// Use listSubnetsPaged to process in pages of 5000 items.
	allSubnets, err := listSubnetsPaged(ctx, r.Client, pool)
	if err != nil {
		logger.Error(err, "Failed to list Subnets with paged indexer")
		return ctrl.Result{}, err
	}

	logger.V(1).Info("Subnet list comparison",
		"withIndex", len(allSubnets),
		"manualCheck", manualMatchCount,
		"poolName", pool.Name)

	// Note: The process to get child SubnetPool CIDRs (former getChildPoolCIDRs function) has been removed (Task 1.2).
	// See docs/issues/child-pool-exclusion.md and docs/parent-pool-requeue-strategy.md for details.
	// Based on the policy of not including child pool CIDRs, pass an empty slice to statuscalc.
	childCIDRs := []string{}
	logger.V(1).Info("Child Pool CIDRs excluded from calculation", "childCIDRs", childCIDRs)

	// 4. Call statuscalc.Calculate() and get the result.
	// Memory optimization: Convert to a list of pointers to slice elements and pass it.
	itemsPtr := make([]*ipamv1.Subnet, len(allSubnets))
	for i := range allSubnets {
		itemsPtr[i] = &allSubnets[i]
	}

	calcResult, err := statuscalc.Calculate(pool.Spec.CIDR, itemsPtr, childCIDRs)
	if err != nil {
		logger.Error(err, "Failed to calculate status", "cidr", pool.Spec.CIDR)
		return ctrl.Result{}, err
	}

	// Construct status from the calculation result.
	newStatus := ipamv1.SubnetPoolStatus{
		AllocatedCount:  calcResult.AllocatedCount,
		FreeCountBySize: calcResult.FreeCountBySize,
		AllocatedCIDRs:  calcResult.AllocatedCIDRs,
	}

	// 5. Compare with the current status and update if there are changes.
	changed := !equality.Semantic.DeepEqual(pool.Status, newStatus)
	if changed {
		logger.Info("Status change detected", "old", pool.Status, "new", newStatus)

		// Execute PATCH update.
		startTime := time.Now()

		// Track the actual number of retries.
		retryCount := 0

		// Implement retry logic for conflicts.
		err := retry.RetryOnConflict(wait.Backoff{
			Steps:    5,                     // Max 5 retries
			Duration: 50 * time.Millisecond, // Exponential backoff
			Factor:   2.0,                   // Jitter of 10%
			Jitter:   0.1,
		}, func() error {
			// When retrying, re-fetch the latest object.
			if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
				// If the resource has already been deleted, skip processing.
				if errors.IsNotFound(err) {
					logger.V(1).Info("SubnetPool not found during status retry, already deleted",
						"poolName", req.Name, "namespace", req.Namespace)
					// If deleted, return normally.
					return nil
				}
				return err
			}

			// If the resource is scheduled for deletion, skip the patch.
			if pool.GetDeletionTimestamp() != nil {
				logger.V(1).Info("SubnetPool marked for deletion, skipping status patch",
					"poolName", pool.Name, "namespace", pool.Namespace)
				return nil
			}

			// Memory optimization: Create a minimal patch instead of a full copy of the pool.
			// Since only the Status field is changed, use the StatusOnly strategy.
			patch := client.MergeFrom(pool.DeepCopy())

			// Update the status.
			pool.Status = newStatus

			// PATCH the status.
			err := r.Status().Patch(ctx, pool, patch)
			if err != nil {
				// NotFound error (if deleted during patch application)
				if errors.IsNotFound(err) {
					logger.V(1).Info("SubnetPool not found during status patch application",
						"poolName", pool.Name, "namespace", pool.Namespace)
					return nil // If deleted, return normally.
				}

				// Conflict error
				if errors.IsConflict(err) {
					// If a conflict occurs, increment the counter and record metrics.
					retryCount++
					logger.V(1).Info("Conflict detected, retrying", "retry", retryCount)
					r.recordPoolStatusRetry(pool.Name, retryCount)
				}
			}
			return err
		})

		duration := time.Since(startTime)

		if err != nil {
			// Update failed.
			logger.Error(err, "Failed to update SubnetPool status")
			// Record failure in metrics.
			r.recordPoolStatusUpdate(pool.Name, "failure", duration)
			// Pass the actual number of retries.
			if retryCount > 0 {
				logger.Info("Status update failed after retries", "retryCount", retryCount)
			}
			return ctrl.Result{}, err
		}

		// Update successful.
		logger.Info("Successfully updated SubnetPool status", "duration", duration)
		// Record success in metrics.
		r.recordPoolStatusUpdate(pool.Name, "success", duration)

		// Phase-4b done log output.
		logger.Info("Phase-4b done: successfully updated pool status via PATCH")
	} else {
		logger.V(1).Info("No status changes detected")
	}

	return ctrl.Result{}, nil
}

// The getChildPoolCIDRs function was removed (Task 1.2).
// This function used to aggregate CIDRs of child pools for updating the parent pool's Status,
// but it was removed to improve performance and avoid race conditions.
// In the current architecture, child pool CIDRs are excluded from Status calculation,
// and only requeueing of the parent pool is done from SubnetReconciler.
// See docs/issues/child-pool-exclusion.md and docs/parent-pool-requeue-strategy.md for details.

// mapAllocToPool maps Subnet events to their parent Pool
func (r *PoolStatusReconciler) mapAllocToPool(ctx context.Context, obj client.Object) []ctrl.Request {
	logger := log.Log.WithValues("func", "mapAllocToPool")
	// Log only important events at INFO level.
	logger.Info("mapAllocToPool called", "objName", obj.GetName())

	// Attempt to convert to standard type.
	allocation, ok := obj.(*ipamv1.Subnet)
	if !ok {
		// Event not applicable.
		logger.Error(nil, "Failed to convert to Subnet", "object", obj)
		return nil
	}

	// Log output.
	logger.V(1).Info("Subnet event details",
		"name", allocation.Name,
		"namespace", allocation.Namespace,
		"poolRef", allocation.Spec.PoolRef,
		"cidr", allocation.Spec.CIDR,
		"clusterID", allocation.Spec.ClusterID)

	// If PoolRef is empty, return early.
	if allocation.Spec.PoolRef == "" {
		logger.V(1).Info("Empty PoolRef, skipping mapping")
		return nil
	}

	// Get Namespace - use client.Object interface method.
	ns := allocation.GetNamespace()

	// Safety measure: If GetNamespace() returns empty, re-fetch from standard fields.
	if ns == "" {
		ns = allocation.Namespace
		logger.Info("GetNamespace() returned empty string, using allocation.Namespace",
			"subnet", allocation.Name, "namespace", ns)
	}

	// Final check: If Namespace fetched from both sources is empty, abort mapping.
	if ns == "" {
		logger.Error(nil, "Cannot map to parent Pool: namespace is empty",
			"subnet", allocation.Name)
		return nil
	}

	// Generate request.
	req := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      allocation.Spec.PoolRef,
			Namespace: ns,
		},
	}

	logger.V(1).Info("Mapped Subnet to parent Pool request",
		"request", req.NamespacedName.String(),
		"poolName", allocation.Spec.PoolRef,
		"namespace", req.Namespace)

	return []ctrl.Request{req}
}

// mapChildToParent maps child Pool deletion events to their parent Pool
func (r *PoolStatusReconciler) mapChildToParent(ctx context.Context, obj client.Object) []ctrl.Request {
	logger := log.Log.WithValues("func", "mapChildToParent")
	// Log only important events at INFO level.
	logger.Info("mapChildToParent called", "objName", obj.GetName())

	// Attempt to convert to standard type.
	childPool, ok := obj.(*ipamv1.SubnetPool)
	if !ok {
		// Event not applicable.
		logger.Error(nil, "Failed to convert to SubnetPool", "object", obj)
		return nil
	}

	// Get parent pool name.
	parentName, exists := childPool.Labels["plexaubnet.io/parent"]
	if !exists || parentName == "" {
		logger.V(1).Info("No parent label, skipping", "childPool", childPool.Name)
		return nil
	}

	// Get Namespace - use client.Object interface method.
	ns := childPool.GetNamespace()

	// Safety measure: If GetNamespace() returns empty, re-fetch from standard fields.
	if ns == "" {
		ns = childPool.Namespace
		logger.Info("GetNamespace() returned empty string, using childPool.Namespace",
			"childPool", childPool.Name, "namespace", ns)
	}

	// Final check: If Namespace fetched from both sources is empty, abort mapping.
	if ns == "" {
		logger.Error(nil, "Cannot map to parent Pool: namespace is empty",
			"childPool", childPool.Name)
		return nil
	}

	req := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      parentName,
			Namespace: ns,
		},
	}

	logger.V(1).Info("Mapped child Pool to parent Pool",
		"request", req.NamespacedName.String(),
		"childPool", childPool.Name,
		"parentPool", parentName,
		"namespace", ns)

	return []ctrl.Request{req}
}

// Define Predicate for watching child pools.
// Only enqueue for subpool deletion and parent label changes.
func childPoolPredicate() predicate.Predicate {
	return predicate.Funcs{
		DeleteFunc: func(e event.DeleteEvent) bool { return true },
		CreateFunc: func(e event.CreateEvent) bool {
			// Child pool creation: enqueue only if plexaubnet.io/parent label exists.
			p, ok := e.Object.(*ipamv1.SubnetPool)
			if !ok {
				return false
			}
			_, hasParent := p.Labels["plexaubnet.io/parent"]
			return hasParent
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Type assertion.
			oldPool, ok1 := e.ObjectOld.(*ipamv1.SubnetPool)
			newPool, ok2 := e.ObjectNew.(*ipamv1.SubnetPool)
			if !ok1 || !ok2 {
				return false
			}

			// 1. Value of parent label changed.
			oldParent, oldHasParent := oldPool.Labels["plexaubnet.io/parent"]
			newParent, newHasParent := newPool.Labels["plexaubnet.io/parent"]

			parentChanged := (oldHasParent != newHasParent) || (oldParent != newParent)

			// 2. CIDR changed.
			cidrChanged := oldPool.Spec.CIDR != newPool.Spec.CIDR

			// 3. Deletion mark added (DeletionTimestamp changed from nil to not nil).
			// This also detects the start of deletion for pools with finalizers.
			deletionMarked := oldPool.DeletionTimestamp == nil && newPool.DeletionTimestamp != nil

			return parentChanged || cidrChanged || deletionMarked
		},
		GenericFunc: func(e event.GenericEvent) bool { return false },
	}
}

// setupPoolStatusBuilder builds the controller builder for PoolStatusReconciler.
// Centralizes common processing for fallback and normal builders.
func (r *PoolStatusReconciler) setupPoolStatusBuilder(mgr ctrl.Manager, controllerName string) *ctrlbuilder.Builder {
	// Important fix: Apply Predicate only to For, not to Watch.
	// The key is not to apply WithEventFilter to the entire builder.
	builder := ctrl.NewControllerManagedBy(mgr).
		Named(controllerName).
		// Apply filter only to parent pool targets (directly associate For and Predicate).
		For(&ipamv1.SubnetPool{},
			ctrlbuilder.WithPredicates(predicate.And(
				PoolStatusPred(),
				NewPoolGaugePredicate(),
			))).
		// Do not attach a predicate to Subnet Watch (process all events).
		// -> This allows mapAllocToPool to be called for Subnet Create/Update/Delete events.
		Watches(
			&ipamv1.Subnet{},
			handler.EnqueueRequestsFromMapFunc(r.mapAllocToPool),
			// Do not use predicate because parent pool needs to be re-queued even if Generation does not change.
		).
		// Set child pool predicate for child pool Watch.
		Watches(
			&ipamv1.SubnetPool{},
			handler.EnqueueRequestsFromMapFunc(r.mapChildToParent),
			ctrlbuilder.WithPredicates(childPoolPredicate()),
		).
		WithOptions(ctrlpkg.Options{
			MaxConcurrentReconciles: MaxConcurrentReconciles,
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](
				5*time.Second,   // Wait time until the first retry.
				300*time.Second, // Maximum wait time.
			),
		})

	return builder
}

// SetupWithManager sets up the controller with the Manager.
func (r *PoolStatusReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Indexes are now registered centrally in init_index.go,
	// so registration code in individual controllers is no longer needed.
	// Constants also refer to PoolRefField and ParentPoolLabelIndex in init_index.go.

	// Register static gauges for existing SubnetPools at startup.
	// Explicit error checking is not needed as it does not return an error.
	ctx := context.Background()
	RegisterAllPoolGauges(ctx, r.Client)

	// Use custom controller name if available (to avoid name conflicts during testing).
	controllerName := "poolstatus-controller"
	if r.ControllerName != "" {
		controllerName = r.ControllerName
	}

	// Build the normal builder.
	builder := r.setupPoolStatusBuilder(mgr, controllerName)

	// Attempt setup with the normal builder.
	if err := builder.Complete(r); err != nil {
		// Log error details.
		log.Log.Error(err, "Failed to setup regular PoolStatusReconciler builder",
			"controller", controllerName,
			"error", err.Error())

		// If indexer conflict error, try fallback builder.
		if strings.Contains(err.Error(), "indexer conflict") ||
			strings.Contains(err.Error(), "controller name conflict") {
			log.Log.Info("Controller setup conflict detected, using fallback builder",
				"controller", controllerName, "error", err.Error())

			// Create a renamed builder for fallback.
			fallbackName := controllerName + "-fallback"
			fallbackBuilder := r.setupPoolStatusBuilder(mgr, fallbackName)

			// Build with fallback.
			err := fallbackBuilder.Complete(r)
			if err != nil {
				log.Log.Error(err, "Failed to setup fallback PoolStatusReconciler builder",
					"controller", fallbackName,
					"error", err.Error())
				return err
			}
			log.Log.Info("Successfully setup fallback PoolStatusReconciler builder", "controller", fallbackName)
			return nil
		}
		return err
	} else {
		log.Log.Info("Successfully setup regular PoolStatusReconciler builder", "controller", controllerName)
	}

	// Wait for the manager's cache to be synced.
	// This allows RegisterAllPoolGauges to use the cache.
	// This call is expected to be made after mgr.Start() and before the controller actually starts running.
	// Since SetupWithManager is called before mgr.Start(), WaitForCacheSync here is
	// strictly speaking, closer to "wait until the manager can start preparing the cache" rather than
	// "wait until the cache is available".
	// A more reliable method is to use mgr.Add(manager.RunnableFunc(func(ctx context.Context) error { ... })),
	// but first, let's check if this resolves the error.
	/*
		if !mgr.GetCache().WaitForCacheSync(context.Background()) {
			// This error might also indicate a problem with the overall test suite setup.
			return fmt.Errorf("failed to wait for cache sync for controller %s before registering pool gauges. This might indicate an issue with the test manager setup or a very early call to SetupWithManager", controllerName)
		}

		// Register static gauges for existing SubnetPools at startup.
		// At this point, the cache should be available.
		// If an error occurs within RegisterAllPoolGauges, it is logged, but by design, no error is returned here.
		// This is because it is expected to be processed in the reconcile loop after controller startup.
		// From a testing perspective, it might be better to fail the test if an error occurs here,
		// but for now, we will follow the existing behavior.
		RegisterAllPoolGauges(ctx, r.Client) // Use ctx defined at the beginning of SetupWithManager.
	*/
	return nil
}
