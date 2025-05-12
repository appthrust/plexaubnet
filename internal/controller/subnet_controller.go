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
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"github.com/appthrust/plexaubnet/internal/config"
	"github.com/appthrust/plexaubnet/internal/controller/statusutil"
)

// SubnetReconciler reconciles a Subnet object
type SubnetReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Config controller configuration
	Config *config.IPAMConfig

	// ControllerName is used to specify a unique controller name during testing.
	// If not specified, the default name "subnet-controller" is used.
	ControllerName string
}

// NewSubnetReconciler creates a new SubnetReconciler
func NewSubnetReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	config *config.IPAMConfig,
) *SubnetReconciler {
	return &SubnetReconciler{
		Client: client,
		Scheme: scheme,
		Config: config,
	}
}

// Reconcile adjusts Subnet resources.
// Main roles:
// 1. Get/check existence of Subnet.
func (r *SubnetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Get Subnet.
	allocation := &ipamv1.Subnet{}
	if err := r.Get(ctx, req.NamespacedName, allocation); err != nil {
		if errors.IsNotFound(err) {
			// If Allocation is deleted, forget it (processed by GC).
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get Subnet")
		return ctrl.Result{}, err
	}

	// 1.5 Check Status field update.
	if allocation.Status.Phase == "" {
		// If it's the first Reconcile or Phase is not set, set Phase to Allocated.
		if err := r.updateSubnetPhase(ctx, allocation, ipamv1.SubnetPhaseAllocated); err != nil {
			// Ignore NotFound error (if resource was deleted).
			if errors.IsNotFound(err) {
				logger.V(1).Info("Subnet deleted during phase update, ignoring",
					"subnet", allocation.Name, "namespace", allocation.Namespace)
				return ctrl.Result{}, nil
			}
			logger.Error(err, "Failed to update Subnet status")
			return ctrl.Result{}, err
		}
		logger.Info("Updated Subnet status to Allocated", "subnet", allocation.Name)
		// Note: Do not return here as parent Pool re-queue is still needed after Status update.
	}

	// 2. Check parent Pool reference.
	if allocation.Spec.PoolRef == "" {
		// PoolRef not set.
		logger.Info("Subnet has no PoolRef, nothing to do")
		// If PoolRef is not set, set to Failed.
		if err := r.updateSubnetPhase(ctx, allocation, ipamv1.SubnetPhaseFailed); err != nil {
			// Ignore NotFound error (if resource was deleted).
			if !errors.IsNotFound(err) {
				logger.Error(err, "Failed to update Subnet status")
			}
		}
		return ctrl.Result{}, nil
	}

	// 3. Check parent Pool existence.
	parentPool := &ipamv1.SubnetPool{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      allocation.Spec.PoolRef,
		Namespace: allocation.Namespace,
	}, parentPool); err != nil {
		logger.Error(err, "Failed to get parent pool")
		// If failed to get parent Pool, set to Failed.
		if updateErr := r.updateSubnetPhase(ctx, allocation, ipamv1.SubnetPhaseFailed); updateErr != nil {
			// Ignore NotFound error (if resource was deleted).
			if !errors.IsNotFound(updateErr) {
				logger.Error(updateErr, "Failed to update Subnet status")
			}
		}
		return ctrl.Result{}, err
	}

	// Normal termination (in the future, branching according to Allocation status can be added).
	return ctrl.Result{}, nil
}

// updateSubnetPhase is a helper function to update the Phase of a Subnet.
// Uses statusutil.PatchSubnetStatus to utilize reusable status update logic.
func (r *SubnetReconciler) updateSubnetPhase(ctx context.Context, subnet *ipamv1.Subnet, phase string) error {
	return statusutil.PatchSubnetStatus(ctx, r.Client, subnet, func(status *ipamv1.SubnetStatus) {
		// Update Phase.
		if status.Phase != phase {
			status.Phase = phase
		}

		// Set AllocatedAt (only on first time and if in Allocated phase).
		if phase == ipamv1.SubnetPhaseAllocated && status.AllocatedAt == nil {
			now := metav1.NewTime(time.Now())
			status.AllocatedAt = &now
		}
	})
}

// updateSubnetStatus updates the Status field of a Subnet.
// Deprecated: Use updateSubnetPhase instead (to be removed in PR after 2025-05-06).
func (r *SubnetReconciler) updateSubnetStatus(ctx context.Context, subnet *ipamv1.Subnet, phase string) error {
	return r.updateSubnetPhase(ctx, subnet, phase)
}

// enqueueParentPool is a common utility to generate re-queue requests for the parent pool.
// Important: Always set Namespace.
func enqueueParentPool(ns, name string) []ctrl.Request {
	return []ctrl.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: ns, // Correct Namespace setting
			Name:      name,
		},
	}}
}

// mapSubnetToParentPool is a mapping function from Subnet object to parent pool.
//
// controller-runtime internally converts DeletedFinalStateUnknown type to *ipamv1.Subnet
// before calling this function, so simple type checking is OK.
// In that case, always set Namespace (controller-development.md Rule#1).
func mapSubnetToParentPool(ctx context.Context, obj client.Object) []ctrl.Request {
	logger := log.FromContext(ctx)

	// Subnet type check.
	subnet, ok := obj.(*ipamv1.Subnet)
	if !ok {
		// If not Subnet, ignore (should not normally happen).
		logger.V(1).Info("received non-Subnet object, ignoring")
		return nil
	}

	// If PoolRef is missing, ignore.
	if subnet.Spec.PoolRef == "" {
		logger.V(1).Info("subnet has no PoolRef, ignoring", "subnet", subnet.Name)
		return nil
	}

	// Determine event type.
	var eventType string
	if !subnet.DeletionTimestamp.IsZero() {
		eventType = "delete"
	} else if subnet.Generation == 1 {
		eventType = "create"
	} else {
		eventType = "update"
	}

	// Record metrics.
	RecordParentPoolRequeue(eventType)

	// Re-queue parent pool.
	logger.Info("parent_pool_mapped", "ns", subnet.GetNamespace(), "pool", subnet.Spec.PoolRef,
		"event", eventType, "subnet", subnet.Name)
	logger.V(1).Info("enqueue parent pool details",
		"subnet", subnet.Name,
		"poolRef", subnet.Spec.PoolRef,
		"event", eventType,
		"generation", subnet.Generation)

	return enqueueParentPool(subnet.GetNamespace(), subnet.Spec.PoolRef)
}

// SetupWithManager sets up the controller with the Manager.
func (r *SubnetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	logger := log.Log.WithName("subnet-controller-setup")
	logger.Info("Starting Subnet controller setup")

	logger.Info("Constructing Subnet controller")
	// Controller configuration.
	cOpts := controller.Options{
		// Use package constant MaxConcurrentReconciles.
		MaxConcurrentReconciles: MaxConcurrentReconciles,
		// Set retry interval on error.
		RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](
			time.Second*5, // Initial retry time: 5 seconds
			time.Minute*5, // Maximum retry interval: 5 minutes
		),
	}

	// Field indexer for Subnet -> parent pool mapping is centrally managed in init_index.go.

	// Configure Watch targets.
	builder := ctrl.NewControllerManagedBy(mgr)

	// Use custom controller name if available (to avoid name conflicts during testing).
	if r.ControllerName != "" {
		builder = builder.Named(r.ControllerName)
	} else {
		builder = builder.Named("subnet-controller")
	}

	return builder.
		WithOptions(cOpts).
		For(&ipamv1.Subnet{}).
		// Add generation change filter: Do not trigger readjustment only by Status update.
		WithEventFilter(SubnetEvents()).
		// 1. Parent pool re-queue Mapper: Watch Subnet changes and trigger recalculation of parent Pool.
		Watches(
			&ipamv1.Subnet{},
			handler.EnqueueRequestsFromMapFunc(mapSubnetToParentPool),
		).
		// 2. Keep existing "Pool -> Allocation reverse watch".
		Watches(
			&ipamv1.SubnetPool{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []ctrl.Request {
				pool, ok := obj.(*ipamv1.SubnetPool)
				if !ok {
					return nil
				}

				logger := log.FromContext(ctx).WithValues("pool", pool.Name)

				// Get all Allocations related to this Pool.
				allocList := &ipamv1.SubnetList{}
				if err := mgr.GetClient().List(ctx, allocList,
					client.InNamespace(pool.Namespace),
					client.MatchingFields{PoolRefField: pool.Name}); err != nil {
					logger.Error(err, "Failed to list allocations for pool")
					return nil
				}

				// Generate Reconcile requests for each Allocation.
				var requests []ctrl.Request
				for _, alloc := range allocList.Items {
					requests = append(requests, ctrl.Request{
						NamespacedName: types.NamespacedName{
							Namespace: alloc.Namespace,
							Name:      alloc.Name,
						},
					})
				}
				return requests
			}),
		).
		Complete(r)
}
