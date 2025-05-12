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
	"fmt"
	"time"

	v1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"github.com/appthrust/plexaubnet/internal/config"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// SubnetPoolClaimReconciler reconciles a SubnetPoolClaim object
type SubnetPoolClaimReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Config *config.IPAMConfig
}

// NewSubnetPoolClaimReconciler creates a new SubnetPoolClaimReconciler
func NewSubnetPoolClaimReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	config *config.IPAMConfig,
) *SubnetPoolClaimReconciler {
	return &SubnetPoolClaimReconciler{
		Client: client,
		Scheme: scheme,
		Config: config,
	}
}

// +kubebuilder:rbac:groups=plexaubnet.io,resources=subnetpoolclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=plexaubnet.io,resources=subnetpoolclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=plexaubnet.io,resources=subnetpoolclaims/finalizers,verbs=update
// +kubebuilder:rbac:groups=plexaubnet.io,resources=subnetpools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=plexaubnet.io,resources=subnetclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=plexaubnet.io,resources=subnets,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles SubnetPoolClaim reconciliation
func (r *SubnetPoolClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("subnetpoolclaim", req.NamespacedName)
	logger.Info("Starting SubnetPoolClaim reconciliation")

	// 1. Get SubnetPoolClaim
	poolClaim := &v1.SubnetPoolClaim{}
	if err := r.Get(ctx, req.NamespacedName, poolClaim); err != nil {
		if errors.IsNotFound(err) {
			// If PoolClaim is deleted, terminate (processed by GC)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get SubnetPoolClaim")
		return ctrl.Result{}, err
	}

	// 2. If being deleted, skip processing (leave to GC)
	if !poolClaim.DeletionTimestamp.IsZero() {
		logger.Info("SubnetPoolClaim is being deleted, allowing GC to handle it")
		return ctrl.Result{}, nil
		// TODO: Implement finalizer processing later
	}

	// 3. If already processed for this generation, skip update (prevent Status-only updates)
	if poolClaim.Status.ObservedGeneration == poolClaim.Generation &&
		poolClaim.Status.Phase == v1.PoolClaimBound {
		logger.Info("SubnetPoolClaim already processed for this generation",
			"observedGeneration", poolClaim.Status.ObservedGeneration,
			"currentGeneration", poolClaim.Generation)
		return ctrl.Result{}, nil
	}

	// 4. Process according to Phase
	switch poolClaim.Status.Phase {
	case "", v1.PoolClaimPending:
		return r.reconcilePendingClaim(ctx, poolClaim)
	case v1.PoolClaimBound:
		// If already Bound, do nothing (if already processed by the observedGeneration check above, it has already returned)
		return ctrl.Result{}, nil
	case v1.PoolClaimError:
		// Even in case of Error, retry if Generation has changed
		if poolClaim.Status.ObservedGeneration != poolClaim.Generation {
			return r.reconcilePendingClaim(ctx, poolClaim)
		}
		return ctrl.Result{}, nil
	default:
		logger.Info("Unknown SubnetPoolClaim phase", "phase", poolClaim.Status.Phase)
		return ctrl.Result{}, nil
	}
}

// reconcilePendingClaim processes SubnetPoolClaim in Pending state
func (r *SubnetPoolClaimReconciler) reconcilePendingClaim(ctx context.Context, poolClaim *v1.SubnetPoolClaim) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues(
		"poolClaim", poolClaim.Name,
		"parentPool", poolClaim.Spec.ParentPoolRef)

	// 1. Find existing internal SubnetClaim
	ownedClaims, err := r.getOwnedSubnetClaims(ctx, poolClaim)
	if err != nil {
		logger.Error(err, "Failed to get owned SubnetClaims")
		return ctrl.Result{}, err
	}

	// 2. If internal SubnetClaim does not exist, create it
	var claim *v1.SubnetClaim
	if len(ownedClaims) == 0 {
		logger.Info("Creating internal SubnetClaim for SubnetPoolClaim")
		_, err = r.createInternalSubnetClaim(ctx, poolClaim)
		if err != nil {
			logger.Error(err, "Failed to create internal SubnetClaim")
			return ctrl.Result{RequeueAfter: time.Second * 5}, err
		}

		// Requeue because SubnetClaim status is not yet set
		return ctrl.Result{Requeue: true}, nil
	} else {
		// Use existing SubnetClaim
		claim = &ownedClaims[0]
		logger.Info("Found existing SubnetClaim", "claimName", claim.Name, "phase", claim.Status.Phase)
	}

	// 3. Process according to SubnetClaim status
	switch claim.Status.Phase {
	case v1.ClaimPending:
		// If SubnetClaim is still Pending, requeue to wait for allocation
		logger.Info("SubnetClaim is still pending, requeueing")
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil

	case v1.ClaimBound:
		if claim.Status.AllocatedCIDR == "" {
			logger.Info("SubnetClaim is bound but has no allocated CIDR, waiting")
			return ctrl.Result{RequeueAfter: time.Second * 5}, nil
		}

		// If AllocatedCIDR is set, proceed to child pool creation
		return r.reconcileBoundClaim(ctx, poolClaim, claim)

	case v1.ClaimError:
		// If SubnetClaim is in error, set PoolClaim to error as well
		return r.reconcileErrorClaim(ctx, poolClaim, claim)

	default:
		logger.Info("Unknown SubnetClaim phase", "phase", claim.Status.Phase)
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}
}

// reconcileBoundClaim processes a SubnetClaim in Bound state and creates a child pool
func (r *SubnetPoolClaimReconciler) reconcileBoundClaim(ctx context.Context, poolClaim *v1.SubnetPoolClaim, claim *v1.SubnetClaim) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues(
		"poolClaim", poolClaim.Name,
		"claim", claim.Name,
		"allocatedCIDR", claim.Status.AllocatedCIDR)

	// 1. Find existing child SubnetPool
	ownedPools, err := r.getOwnedSubnetPools(ctx, poolClaim)
	if err != nil {
		logger.Error(err, "Failed to get owned SubnetPools")
		return ctrl.Result{}, err
	}

	// 2. If child SubnetPool does not exist, create it
	var childPool *v1.SubnetPool
	if len(ownedPools) == 0 {
		logger.Info("Creating child SubnetPool")
		childPool, err = r.createChildPool(ctx, poolClaim, claim.Status.AllocatedCIDR)
		if err != nil {
			logger.Error(err, "Failed to create child SubnetPool")
			return ctrl.Result{RequeueAfter: time.Second * 5}, err
		}
	} else {
		// Use existing child pool
		childPool = &ownedPools[0]
		logger.Info("Found existing child SubnetPool", "poolName", childPool.Name)
	}

	// 3. Update PoolClaim status
	if err := r.updatePoolClaimStatus(ctx, poolClaim, childPool.Name, v1.PoolClaimBound, ""); err != nil {
		logger.Error(err, "Failed to update SubnetPoolClaim status")
		return ctrl.Result{RequeueAfter: time.Second * 5}, err
	}

	logger.Info("Successfully bound SubnetPoolClaim to child pool",
		"childPool", childPool.Name,
		"cidr", childPool.Spec.CIDR)

	return ctrl.Result{}, nil
}

// reconcileErrorClaim processes SubnetClaim in Error state
func (r *SubnetPoolClaimReconciler) reconcileErrorClaim(ctx context.Context, poolClaim *v1.SubnetPoolClaim, claim *v1.SubnetClaim) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("poolClaim", poolClaim.Name, "claim", claim.Name)
	logger.Info("SubnetClaim is in error state, updating SubnetPoolClaim status", "errorMessage", claim.Status.Message)

	// Update PoolClaim status to Error and set error message
	if err := r.updatePoolClaimStatus(ctx, poolClaim, "", v1.PoolClaimError, claim.Status.Message); err != nil {
		logger.Error(err, "Failed to update SubnetPoolClaim error status")
		return ctrl.Result{RequeueAfter: time.Second * 5}, err
	}

	return ctrl.Result{}, nil
}

// createInternalSubnetClaim creates an internal SubnetClaim for SubnetPoolClaim
func (r *SubnetPoolClaimReconciler) createInternalSubnetClaim(ctx context.Context, poolClaim *v1.SubnetPoolClaim) (*v1.SubnetClaim, error) {
	claim := &v1.SubnetClaim{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-", poolClaim.Name),
			Namespace:    poolClaim.Namespace,
		},
		Spec: v1.SubnetClaimSpec{
			PoolRef:   poolClaim.Spec.ParentPoolRef,
			BlockSize: poolClaim.Spec.DesiredBlockSize,
			ClusterID: string(poolClaim.UID),
		},
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(poolClaim, claim, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference: %w", err)
	}

	// Create SubnetClaim
	if err := r.Create(ctx, claim); err != nil {
		return nil, err
	}

	return claim, nil
}

// createChildPool creates a child SubnetPool using the allocated CIDR
func (r *SubnetPoolClaimReconciler) createChildPool(ctx context.Context, poolClaim *v1.SubnetPoolClaim, allocatedCIDR string) (*v1.SubnetPool, error) {
	// Create labels
	labels := map[string]string{
		"plexaubnet.io/parent": poolClaim.Spec.ParentPoolRef, // Add reference to parent pool as a label
		IPAMLabel:              "true",
	}

	childPool := &v1.SubnetPool{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-", poolClaim.Name),
			Namespace:    poolClaim.Namespace,
			Labels:       labels,
		},
		Spec: v1.SubnetPoolSpec{
			CIDR: allocatedCIDR,
		},
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(poolClaim, childPool, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference: %w", err)
	}

	// Create child pool
	if err := r.Create(ctx, childPool); err != nil {
		return nil, err
	}

	return childPool, nil
}

// updatePoolClaimStatus updates the status of SubnetPoolClaim
func (r *SubnetPoolClaimReconciler) updatePoolClaimStatus(ctx context.Context, poolClaim *v1.SubnetPoolClaim, boundPoolName string, phase v1.SubnetPoolClaimPhase, message string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the latest state
		latestPoolClaim := &v1.SubnetPoolClaim{}
		if err := r.Get(ctx, types.NamespacedName{Name: poolClaim.Name, Namespace: poolClaim.Namespace}, latestPoolClaim); err != nil {
			return err
		}

		// Update status
		latestPoolClaim.Status.ObservedGeneration = latestPoolClaim.Generation
		latestPoolClaim.Status.Phase = phase
		latestPoolClaim.Status.BoundPoolName = boundPoolName
		latestPoolClaim.Status.Message = message

		// Apply status update
		return r.Status().Update(ctx, latestPoolClaim)
	})
}

// getOwnedSubnetClaims gets a list of SubnetClaims owned by SubnetPoolClaim
func (r *SubnetPoolClaimReconciler) getOwnedSubnetClaims(ctx context.Context, poolClaim *v1.SubnetPoolClaim) ([]v1.SubnetClaim, error) {
	claimList := &v1.SubnetClaimList{}

	if err := r.List(ctx, claimList, client.InNamespace(poolClaim.Namespace)); err != nil {
		return nil, err
	}

	var ownedClaims []v1.SubnetClaim
	for _, claim := range claimList.Items {
		// Check owner reference
		if isOwnedBy(&claim, poolClaim) {
			ownedClaims = append(ownedClaims, claim)
		}
	}

	return ownedClaims, nil
}

// getOwnedSubnetPools gets a list of SubnetPools owned by SubnetPoolClaim
func (r *SubnetPoolClaimReconciler) getOwnedSubnetPools(ctx context.Context, poolClaim *v1.SubnetPoolClaim) ([]v1.SubnetPool, error) {
	poolList := &v1.SubnetPoolList{}

	if err := r.List(ctx, poolList, client.InNamespace(poolClaim.Namespace)); err != nil {
		return nil, err
	}

	var ownedPools []v1.SubnetPool
	for _, pool := range poolList.Items {
		// Check owner reference
		if isOwnedBy(&pool, poolClaim) {
			ownedPools = append(ownedPools, pool)
		}
	}

	return ownedPools, nil
}

// isOwnedBy checks if an object is owned by the specified owner
func isOwnedBy(obj client.Object, owner client.Object) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == owner.GetUID() {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the manager
func (r *SubnetPoolClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	logger := log.Log.WithName("subnetpoolclaim-controller-setup")
	logger.Info("Setting up SubnetPoolClaim controller")

	// Set controller options
	cOpts := controller.Options{
		MaxConcurrentReconciles: MaxConcurrentReconciles,
		RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](
			time.Second*5, // Wait time until first retry
			time.Minute*5, // Maximum retry interval
		),
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.SubnetPoolClaim{}).
		// Watch for updates to owned SubnetClaims
		Owns(&v1.SubnetClaim{}).
		// Watch for updates to owned SubnetPools
		Owns(&v1.SubnetPool{}).
		// Add generation change filter: Do not trigger reconciliation on Status updates only
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		WithOptions(cOpts).
		Complete(r)
}
