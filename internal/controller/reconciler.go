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
	"math/rand"
	"net"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

const (
	// ReadyCondition is the type for CIDR claim readiness
	ReadyCondition = "Ready"
	// MaxRetries is the maximum number of allocation retries
	MaxRetries = 65
	// DefaultRequeueAfter is the default requeue period
	DefaultRequeueAfter = time.Second * 10
	// ClaimStatusMaxRetries is the maximum number of status update retries
	ClaimStatusMaxRetries = 10
)

// processClaimReconciliation handles the main CIDR claim reconciliation logic
func (r *CIDRAllocatorReconciler) processClaimReconciliation(ctx context.Context, claim *ipamv1.SubnetClaim) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues(
		"claim", claim.Name,
		"clusterID", claim.Spec.ClusterID,
	)

	// Check ObservedGeneration
	// Skip processing if the current generation has already been processed
	// However, always process if not in Bound state (for recovery purposes)
	currentGeneration := claim.GetGeneration()
	if claim.Status.Phase == ipamv1.ClaimBound &&
		claim.Status.AllocatedCIDR != "" &&
		claim.Status.ObservedGeneration == currentGeneration {
		logger.V(1).Info("SubnetClaim already processed for this generation",
			"observedGeneration", claim.Status.ObservedGeneration,
			"currentGeneration", currentGeneration)
		return ctrl.Result{}, nil
	}

	// If claim is already bound, nothing to do for allocation
	if claim.Status.Phase == ipamv1.ClaimBound && claim.Status.AllocatedCIDR != "" {
		// Already allocated and bound, nothing to do
		// If already bound but new generation, update ObservedGeneration
		if claim.Status.ObservedGeneration != currentGeneration {
			logger.Info("Updating ObservedGeneration for already bound claim",
				"oldObservedGeneration", claim.Status.ObservedGeneration,
				"newObservedGeneration", currentGeneration)
			return r.updateClaimStatus(ctx, claim, ipamv1.ClaimBound, claim.Status.AllocatedCIDR, "")
		}
		logger.Info("SubnetClaim already bound", "CIDR", claim.Status.AllocatedCIDR)
		return ctrl.Result{}, nil
	}

	// Step 1: Fetch the pool
	pool := &ipamv1.SubnetPool{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: claim.Namespace,
		Name:      claim.Spec.PoolRef,
	}, pool); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Pool not found, requeuing", "poolRef", claim.Spec.PoolRef)
			// Update claim status with error if it's not already
			if claim.Status.Phase != ipamv1.ClaimError ||
				claim.Status.Message != fmt.Sprintf("Pool %s not found", claim.Spec.PoolRef) {
				return r.updateClaimStatus(ctx, claim, ipamv1.ClaimError, "",
					fmt.Sprintf("Pool %s not found", claim.Spec.PoolRef))
			}
			return ctrl.Result{RequeueAfter: DefaultRequeueAfter}, nil
		}
		logger.Error(err, "Failed to get SubnetPool")
		return ctrl.Result{}, err
	}
	// Note: AllocatedCount update has been consolidated in SubnetReconciler
	// To prevent redundancy and conflicts in pool status updates, Allocator does not update it from this side

	// Step 2: Validate claim request
	if err := validateClaim(claim, pool); err != nil {
		logger.Info("Invalid claim request", "error", err.Error())
		r.eventEmitter.EmitValidationFailed(claim, err.Error())
		r.recordAllocationError(claim, "validation")
		return r.updateClaimStatus(ctx, claim, ipamv1.ClaimError, "", err.Error())
	}

	// Step 3: Check for existing allocation by clusterID (idempotency)
	existingAllocation := &ipamv1.Subnet{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: claim.Namespace,
		Name:      claim.Spec.ClusterID,
	}, existingAllocation)

	if err == nil {
		// Allocation exists - verify it belongs to this pool and is valid
		if existingAllocation.Spec.PoolRef != claim.Spec.PoolRef {
			errMsg := fmt.Sprintf("Existing allocation for ClusterID %s found but in different pool: %s",
				claim.Spec.ClusterID, existingAllocation.Spec.PoolRef)
			logger.Info(errMsg)
			r.eventEmitter.EmitAllocationConflict(claim, errMsg)
			return r.updateClaimStatus(ctx, claim, ipamv1.ClaimError, "", errMsg)
		}

		// Valid existing allocation found - update claim status
		logger.Info("Found existing allocation for ClusterID",
			"clusterID", claim.Spec.ClusterID,
			"CIDR", existingAllocation.Spec.CIDR)

		r.eventEmitter.EmitAllocationSuccess(claim, existingAllocation.Spec.CIDR)

		return r.updateClaimStatus(ctx, claim, ipamv1.ClaimBound, existingAllocation.Spec.CIDR, "")
	} else if !errors.IsNotFound(err) {
		// Unexpected error
		logger.Error(err, "Failed to check for existing allocation")
		return ctrl.Result{}, err
	}

	// Step 4: List existing allocations from the pool
	// Note: PoolRefField is a constant defined in cidrallocator (init_index.go)
	allocationList := &ipamv1.SubnetList{}
	if err := r.List(ctx, allocationList,
		client.InNamespace(claim.Namespace),
		client.MatchingFields{PoolRefField: claim.Spec.PoolRef}); err != nil {
		logger.Error(err, "Failed to list allocations", "PoolRefField", PoolRefField)
		return ctrl.Result{}, err
	}

	// Step 5: Allocate CIDR (with retry)
	for retry := 0; retry < MaxRetries; retry++ {
		time.Sleep(30 * time.Millisecond)
		// Get the latest allocation state before each retry
		// This also reflects allocations created by other controller instances
		if retry > 0 {
			// Refresh the allocation list
			logger.Info("Refreshing allocation list for retry", "retry", retry)
			if err := r.List(ctx, allocationList,
				client.InNamespace(claim.Namespace),
				client.MatchingFields{PoolRefField: claim.Spec.PoolRef}); err != nil {
				logger.Error(err, "Failed to refresh allocations during retry",
					"PoolRefField", PoolRefField, "poolRef", claim.Spec.PoolRef)
				return ctrl.Result{}, err
			}
		}

		allocated, allocatedCIDR, err := r.allocateCIDR(ctx, claim, pool, allocationList.Items)
		if err != nil {
			if IsExhaustedError(err) {
				logger.Info("Pool exhausted", "pool", pool.Name)
				r.eventEmitter.EmitPoolExhausted(claim, pool.Name)
				r.recordAllocationError(claim, "exhausted")
				return r.updateClaimStatus(ctx, claim, ipamv1.ClaimError, "",
					fmt.Sprintf("Pool %s is exhausted: %v", pool.Name, err))
			}

			if IsConflictError(err) && retry < MaxRetries-1 {
				// Conflict detected, retry with backoff
				jitter := time.Duration(rand.Intn(70)+10) * time.Millisecond
				logger.Info("Allocation conflict, retrying", "retry", retry+1, "backoff", jitter)
				r.recordRetry(retry + 1)
				time.Sleep(jitter)

				// Check for existing Allocation - in case another controller instance created it for the same clusterID
				existingAlloc := &ipamv1.Subnet{}
				err := r.Get(ctx, types.NamespacedName{
					Namespace: claim.Namespace,
					Name:      claim.Spec.ClusterID,
				}, existingAlloc)

				if err == nil {
					// Another process has already created an allocation - reuse it
					logger.Info("Found allocation created by another controller instance",
						"clusterID", claim.Spec.ClusterID,
						"CIDR", existingAlloc.Spec.CIDR)

					return r.updateClaimStatus(ctx, claim, ipamv1.ClaimBound, existingAlloc.Spec.CIDR, "")
				}

				continue
			}

			// Other errors or max retries reached
			logger.Error(err, "Failed to allocate CIDR", "retry", retry)
			r.eventEmitter.EmitAllocationConflict(claim, err.Error())
			r.recordAllocationError(claim, "conflict")
			// If max retries reached but still conflict, maintain Pending status and requeue rather than Error
			if retry == MaxRetries-1 && IsConflictError(err) {
				logger.Info("Max retries reached, requeuing after longer backoff")
				return ctrl.Result{RequeueAfter: time.Second * 30}, nil
			}
			return r.updateClaimStatus(ctx, claim, ipamv1.ClaimError, "", err.Error())
		}

		if allocated {
			// Allocation succeeded
			r.recordAllocSuccess(claim, allocatedCIDR)
			r.eventEmitter.EmitAllocationSuccess(claim, allocatedCIDR)

			// Note: Pool status update responsibility has been consolidated in SubnetReconciler,
			// so we don't update Status here (to avoid conflicts)

			// Update metrics for free blocks
			cidrMap, err := NewCIDRMap(pool.Spec.CIDR)
			if err == nil {
				// Mark existing allocations to calculate available count
				for _, alloc := range allocationList.Items {
					_ = cidrMap.MarkAllocated(alloc.Spec.CIDR)
				}
				// Mark the newly allocated CIDR too
				_ = cidrMap.MarkAllocated(allocatedCIDR)

				// Get free block count
				freeCount := cidrMap.GetFreeBlockCount()
				// Record metrics for each size
				for sizeKey, free := range freeCount {
					recordPoolMetrics(pool, fmt.Sprintf("/%s", sizeKey), free)
				}
			}

			return r.updateClaimStatus(ctx, claim, ipamv1.ClaimBound, allocatedCIDR, "")
		}
	}

	// If we got here, max retries exceeded
	errMsg := fmt.Sprintf("Failed to allocate CIDR after %d retries due to conflicts", MaxRetries)
	logger.Info(errMsg)
	r.eventEmitter.EmitAllocationConflict(claim, errMsg)
	r.recordAllocationError(claim, "max_retries")
	return r.updateClaimStatus(ctx, claim, ipamv1.ClaimError, "", errMsg)
}

// NotFoundRetryMax is the maximum number of retries for NotFound errors
const NotFoundRetryMax = 3

// updateClaimStatus updates the status of a SubnetClaim with retry for conflicts
func (r *CIDRAllocatorReconciler) updateClaimStatus(
	ctx context.Context,
	claim *ipamv1.SubnetClaim,
	phase ipamv1.SubnetClaimPhase,
	allocatedCIDR string,
	message string,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check if status actually changed to avoid unnecessary updates
	if claim.Status.Phase == phase &&
		claim.Status.AllocatedCIDR == allocatedCIDR &&
		claim.Status.Message == message {
		return ctrl.Result{}, nil
	}

	// Define backoff parameters
	backoff := wait.Backoff{
		Steps:    5,
		Duration: 50 * time.Millisecond,
		Factor:   1.5,
		Jitter:   0.1,
	}

	// Outer retry loop for handling NotFound errors
	var lastErr error
	for notFoundRetry := 0; notFoundRetry <= NotFoundRetryMax; notFoundRetry++ {
		// Wait a bit before retrying from the second attempt onwards
		if notFoundRetry > 0 {
			logger.Info("Retrying status update after NotFound error",
				"retry", notFoundRetry, "claim", claim.Name)
			time.Sleep(100 * time.Millisecond)
		}

		key := types.NamespacedName{Namespace: claim.Namespace, Name: claim.Name}
		err := retry.RetryOnConflict(backoff, func() error {
			// Get the latest version of the claim
			latest := &ipamv1.SubnetClaim{}
			if err := r.Get(ctx, key, latest); err != nil {
				return err
			}

			// Apply our changes to the latest version
			latest.Status.Phase = phase
			latest.Status.AllocatedCIDR = allocatedCIDR
			latest.Status.Message = message
			latest.Status.ObservedGeneration = latest.Generation

			// Update Ready condition based on phase
			readyStatus := metav1.ConditionFalse
			readyReason := "Pending"
			readyMessage := "Allocation in progress"

			switch phase {
			case ipamv1.ClaimBound:
				readyStatus = metav1.ConditionTrue
				readyReason = "Allocated"
				readyMessage = fmt.Sprintf("CIDR %s allocated", allocatedCIDR)
			case ipamv1.ClaimError:
				readyReason = "Error"
				readyMessage = message
			default:
				// For Pending or any other phase, use default values
			}

			// Set/update the Ready condition
			meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
				Type:               ReadyCondition,
				Status:             readyStatus,
				ObservedGeneration: latest.Generation,
				LastTransitionTime: metav1.Now(),
				Reason:             readyReason,
				Message:            readyMessage,
			})

			// Update status with the latest version
			return r.Client.Status().Update(ctx, latest)
		})

		if err == nil {
			// Exit retry loop on success
			lastErr = nil
			break
		}

		lastErr = err
		if errors.IsNotFound(err) && notFoundRetry < NotFoundRetryMax {
			// Continue if NotFound error and retry count not exceeded
			continue
		}

		// Exit immediately on other errors or if retry limit reached
		break
	}

	if lastErr != nil {
		logger.Error(lastErr, "Failed to update SubnetClaim status after retries")
		return ctrl.Result{}, lastErr
	}

	logger.Info("Updated SubnetClaim status",
		"phase", phase,
		"allocatedCIDR", allocatedCIDR)

	// Set requeue policy based on phase
	switch phase {
	case ipamv1.ClaimError:
		// Don't requeue immediately for errors
		return ctrl.Result{}, nil
	case ipamv1.ClaimPending:
		// Requeue after a delay for Pending status
		return ctrl.Result{RequeueAfter: DefaultRequeueAfter}, nil
	default:
		// Don't requeue for other phases (e.g., Bound)
		return ctrl.Result{}, nil
	}
}

// validateClaim validates a SubnetClaim against its pool
func validateClaim(claim *ipamv1.SubnetClaim, pool *ipamv1.SubnetPool) error {
	// Check blockSize within range if specified
	if claim.Spec.BlockSize != 0 {
		if pool.Spec.MinBlockSize != 0 && claim.Spec.BlockSize < pool.Spec.MinBlockSize {
			return fmt.Errorf("blockSize %d is smaller than pool minimum %d",
				claim.Spec.BlockSize, pool.Spec.MinBlockSize)
		}
		if pool.Spec.MaxBlockSize != 0 && claim.Spec.BlockSize > pool.Spec.MaxBlockSize {
			return fmt.Errorf("blockSize %d is larger than pool maximum %d",
				claim.Spec.BlockSize, pool.Spec.MaxBlockSize)
		}
	}

	// Validate requestedCIDR is within pool range if specified
	if claim.Spec.RequestedCIDR != "" {
		// Parse requested CIDR
		_, ipNet, err := net.ParseCIDR(claim.Spec.RequestedCIDR)
		if err != nil {
			return fmt.Errorf("invalid requested CIDR format: %w", err)
		}

		// Parse pool CIDR
		_, poolNet, err := net.ParseCIDR(pool.Spec.CIDR)
		if err != nil {
			return fmt.Errorf("invalid pool CIDR format: %w", err)
		}

		// Check if requested CIDR is within pool CIDR
		if !poolNet.Contains(ipNet.IP) {
			return fmt.Errorf("requested CIDR %s is outside pool %s",
				claim.Spec.RequestedCIDR, pool.Spec.CIDR)
		}

		// Validate prefix length if pool has min/max constraints
		requestedPrefixLen, _ := ipNet.Mask.Size()
		if pool.Spec.MinBlockSize != 0 && requestedPrefixLen < pool.Spec.MinBlockSize {
			return fmt.Errorf("requested CIDR prefix length /%d is smaller than pool minimum /%d",
				requestedPrefixLen, pool.Spec.MinBlockSize)
		}
		if pool.Spec.MaxBlockSize != 0 && requestedPrefixLen > pool.Spec.MaxBlockSize {
			return fmt.Errorf("requested CIDR prefix length /%d is larger than pool maximum /%d",
				requestedPrefixLen, pool.Spec.MaxBlockSize)
		}
	}

	return nil
}
