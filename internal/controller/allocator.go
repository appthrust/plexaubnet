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

// Package cidrallocator implements the CIDR allocation controller for Aquanaut IPAM.
// It provides automatic allocation of CIDR blocks for clusters using a bitmap-based
// allocation algorithm with conflict resolution and retry mechanisms.
package cidrallocator

import (
	"context"
	"crypto/sha1"
	"fmt"
	"math"
	"math/rand"
	"net"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/appthrust/plexaubnet/internal/netutil"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"github.com/go-logr/logr"
)

// Error types for the CIDR allocator
type allocatorError struct {
	message     string
	isExhausted bool // indicates pool exhaustion
	isConflict  bool // indicates allocation conflict
}

func (e *allocatorError) Error() string {
	return e.message
}

// NewExhaustedError creates a new pool exhausted error
// Used when no more CIDRs are available in the pool
func NewExhaustedError(msg string) error {
	return &allocatorError{
		message:     msg,
		isExhausted: true,
	}
}

// NewConflictError creates a new conflict error
// Used when there's a conflict during allocation (e.g., concurrent allocation)
func NewConflictError(msg string) error {
	return &allocatorError{
		message:    msg,
		isConflict: true,
	}
}

// IsExhaustedError checks if an error is a pool exhausted error
// Returns true if the error indicates the pool is exhausted
func IsExhaustedError(err error) bool {
	if aErr, ok := err.(*allocatorError); ok {
		return aErr.isExhausted
	}
	return false
}

// IsConflictError checks if an error is a conflict error
// Returns true if the error indicates a conflict or if it's an AlreadyExists error
func IsConflictError(err error) bool {
	if aErr, ok := err.(*allocatorError); ok {
		return aErr.isConflict
	}
	return errors.IsAlreadyExists(err)
}

// EncodeAllocationName creates a deterministic name for a Subnet based on pool and CIDR
// Format: <pool>-<cidr> with '/' replaced by '-'
// Example: test-pool-10.0.49.0-24
//
// Note: Currently this function is not used as we directly use ClusterID as the name,
// but it's kept for future use if a different naming scheme is needed.
func EncodeAllocationName(pool, cidr string) string {
	// Remove characters unusable in Kubernetes names from CIDR
	// Replace '/' -> '-' and '.' -> '-'
	normalizedCIDR := strings.ReplaceAll(cidr, "/", "-")
	normalizedCIDR = strings.ReplaceAll(normalizedCIDR, ".", "-")
	// Replace ':' as it is also used in IPv6 addresses
	normalizedCIDR = strings.ReplaceAll(normalizedCIDR, ":", "-")

	name := fmt.Sprintf("%s-%s", pool, normalizedCIDR)

	// Kubernetes resource names have a limit of 63 characters or less
	if len(name) <= 63 {
		return name
	}

	// If too long, hash and shorten
	h := sha1.Sum([]byte(name))
	hashStr := fmt.Sprintf("%x", h[:8]) // 16-character hash

	// Combine the beginning of the pool name (max 40 characters) and the hash
	maxPoolLen := 40
	if len(pool) > maxPoolLen {
		pool = pool[:maxPoolLen]
	}

	shortName := fmt.Sprintf("%s-%s", pool, hashStr)

	// Limit to 63 characters just in case
	if len(shortName) > 63 {
		return shortName[:63]
	}
	return shortName
}

// Constants for allocation retry and backoff configuration
const (
	// AllocationMaxRetries is the maximum number of retries for CIDR allocation
	// Increased from 10 to 50 in Allocation-First design for better concurrency handling
	AllocationMaxRetries = 50

	// AllocationBackoffBaseMs is the base value for retry backoff in milliseconds
	// Reduced from 50ms to 20ms for performance improvement
	AllocationBackoffBaseMs = 20

	// AllocationBackoffFactor is the multiplier for exponential backoff
	// Reduced from 2.0 to 1.5 for better distribution during contention
	AllocationBackoffFactor = 1.5

	// AllocationJitterRatio is the fixed ratio for jitter calculation (10%)
	// Used to add random jitter to backoff duration to prevent thundering herd
	AllocationJitterRatio = 0.1
)

// allocateCIDR allocates a CIDR block for a claim
// It attempts to find an existing allocation for the claim's ClusterID first,
// and if none exists, it allocates a new CIDR block.
//
// Parameters:
// - ctx: Context for the operation
// - claim: The SubnetClaim requesting allocation
// - pool: The SubnetPool to allocate from
// - existingAllocations: List of existing allocations to check against
//
// Returns:
// - success: Whether allocation was successful
// - allocatedCIDR: The allocated CIDR block (empty if not successful)
// - error: Any error that occurred during allocation
func (r *CIDRAllocatorReconciler) allocateCIDR(
	ctx context.Context,
	claim *ipamv1.SubnetClaim,
	pool *ipamv1.SubnetPool,
	existingAllocations []ipamv1.Subnet,
) (bool, string, error) {
	logger := log.FromContext(ctx).WithValues(
		"claim", claim.Name,
		"clusterID", claim.Spec.ClusterID,
		"pool", pool.Name,
	)

	// Record allocation latency for metrics
	startTime := time.Now()
	defer func() {
		r.recordLatency(time.Since(startTime))
	}()

	// Step 1: Check for existing allocation by ClusterID
	// This ensures idempotency - the same cluster always gets the same CIDR
	for _, alloc := range existingAllocations {
		if alloc.Spec.ClusterID == claim.Spec.ClusterID && alloc.Spec.PoolRef == pool.Name {
			logger.Info("Found existing allocation by ClusterID",
				"clusterID", claim.Spec.ClusterID,
				"CIDR", alloc.Spec.CIDR)

			return true, alloc.Spec.CIDR, nil
		}
	}

	// Step 2: Initialize local map of used CIDRs for retry handling
	// This map is maintained across retries to avoid repeatedly trying the same CIDRs
	localUsedCIDRs := make(map[string]bool)
	for _, alloc := range existingAllocations {
		localUsedCIDRs[alloc.Spec.CIDR] = true
	}

	// Step 3: Start allocation retry loop
	for retry := 0; retry < AllocationMaxRetries; retry++ {
		// Apply backoff with jitter for retries
		if retry > 0 {
			waitTime := r.calculateBackoffWithJitter(retry)
			logger.V(1).Info("Waiting before retry",
				"retry", retry,
				"waitMs", int(waitTime.Milliseconds()))
			time.Sleep(waitTime)

			// Refresh pool data on each retry
			if err := r.Get(ctx, types.NamespacedName{
				Namespace: pool.Namespace,
				Name:      pool.Name,
			}, pool); err != nil {
				return false, "", fmt.Errorf("failed to get latest pool state: %w", err)
			}

			// Optionally refresh allocation list to get the latest state
			if err := r.refreshAllocationList(ctx, pool, &existingAllocations, localUsedCIDRs); err != nil {
				logger.V(1).Info("Failed to refresh allocation list", "error", err)
				// Continue anyway - we'll use our local cache
			}
		}

		logger.V(1).Info("Attempting CIDR allocation", "retry", retry+1, "maxRetries", AllocationMaxRetries)

		// Step 4: Select candidate CIDR
		candidateCIDR, err := r.selectCandidateCIDR(ctx, claim, pool, localUsedCIDRs, existingAllocations, logger)
		if err != nil {
			return false, "", err
		}

		// Mark as in use immediately (reduce contention during concurrent execution)
		localUsedCIDRs[candidateCIDR] = true
		reserved := true

		// Step 5: Create Subnet resource to ensure uniqueness
		allocationName := EncodeAllocationName(pool.Name, candidateCIDR)
		logger.V(1).Info("Creating allocation", "name", allocationName, "CIDR", candidateCIDR)

		// Attempt to create the allocation
		success, cidr, err := r.createAllocation(ctx, claim, pool, candidateCIDR, allocationName, retry, localUsedCIDRs, logger)
		if err != nil {
			// Rollback reservation if failed
			if reserved {
				delete(localUsedCIDRs, candidateCIDR)
			}

			// If it's not a conflict error, return the error
			if !IsConflictError(err) {
				return false, "", err
			}
			// For conflict errors, we'll continue the loop and try again
			continue
		}

		// If successful, return the allocated CIDR
		if success {
			return true, cidr, nil
		}
	}

	// Maximum retries reached
	return false, "", fmt.Errorf("failed to allocate CIDR after %d retries", AllocationMaxRetries)
}

// calculateBackoffWithJitter calculates the backoff duration with jitter for retries
func (r *CIDRAllocatorReconciler) calculateBackoffWithJitter(retry int) time.Duration {
	// Calculate exponential backoff: baseMs * factor^retry using math.Pow
	backoffMs := float64(AllocationBackoffBaseMs) * math.Pow(AllocationBackoffFactor, float64(retry))

	// Add random jitter (0-10% of backoffMs) to prevent thundering herd
	// Only positive jitter (upper bound only) based on fixed ratio
	jitterMs := rand.Intn(int(backoffMs * AllocationJitterRatio))

	return time.Duration(backoffMs+float64(jitterMs)) * time.Millisecond
}

// refreshAllocationList refreshes the list of existing allocations
func (r *CIDRAllocatorReconciler) refreshAllocationList(
	ctx context.Context,
	pool *ipamv1.SubnetPool,
	existingAllocations *[]ipamv1.Subnet,
	localUsedCIDRs map[string]bool,
) error {
	allocList := &ipamv1.SubnetList{}
	if err := r.List(ctx, allocList,
		client.MatchingFields{
			PoolRefField: pool.Name,
		},
		client.InNamespace(pool.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list allocations in refreshAllocationList",
			"poolName", pool.Name, "PoolRefField", PoolRefField)
		return err
	}

	// Update existing allocations list
	*existingAllocations = allocList.Items

	// Update local map of used CIDRs
	for _, alloc := range allocList.Items {
		localUsedCIDRs[alloc.Spec.CIDR] = true
	}

	return nil
}

// selectCandidateCIDR selects a candidate CIDR for allocation
func (r *CIDRAllocatorReconciler) selectCandidateCIDR(
	ctx context.Context,
	claim *ipamv1.SubnetClaim,
	pool *ipamv1.SubnetPool,
	localUsedCIDRs map[string]bool,
	existingAllocations []ipamv1.Subnet,
	logger logr.Logger,
) (string, error) {
	// Case 1: Explicit CIDR requested
	if claim.Spec.RequestedCIDR != "" {
		return r.validateRequestedCIDR(claim.Spec.RequestedCIDR, pool.Spec.CIDR, existingAllocations)
	}

	// Case 2: Dynamic allocation
	cidrTimeStart := time.Now()

	// Determine block size (claim, pool default, or global default)
	blockSize := claim.Spec.BlockSize
	if blockSize == 0 {
		blockSize = pool.Spec.DefaultBlockSize
		if blockSize == 0 {
			blockSize = 24 // Default is /24
		}
	}

	// Find available CIDR block
	availableCIDR, err := findAvailableCIDR(pool.Spec.CIDR, blockSize, localUsedCIDRs)
	if err != nil {
		return "", r.handleCIDRSelectionError(err, blockSize, pool.Spec.CIDR)
	}

	// Log CIDR selection time for performance monitoring
	if !cidrTimeStart.IsZero() {
		logger.V(1).Info("CIDR selection time",
			"durationMs", time.Since(cidrTimeStart).Milliseconds())
	}

	return availableCIDR, nil
}

// validateRequestedCIDR validates that a requested CIDR is valid and within the pool
// Also checks that it doesn't overlap with existing allocations
func (r *CIDRAllocatorReconciler) validateRequestedCIDR(
	requestedCIDR, poolCIDR string,
	existingAllocations []ipamv1.Subnet,
) (string, error) {
	// Parse and validate CIDR format
	_, ipNet, err := net.ParseCIDR(requestedCIDR)
	if err != nil {
		return "", fmt.Errorf("invalid requested CIDR format: %w", err)
	}

	// Parse pool CIDR
	_, poolNet, _ := net.ParseCIDR(poolCIDR)

	// 1. Check if CIDR's first IP (network address) is within pool range
	if !poolNet.Contains(ipNet.IP) {
		return "", fmt.Errorf("requested CIDR %s is outside pool %s (network address outside pool)",
			requestedCIDR, poolCIDR)
	}

	// 2. Check if CIDR's last IP is also within pool range (ensure entire CIDR block is within pool)
	lastIP := netutil.CalculateLastIP(ipNet)
	if !poolNet.Contains(lastIP) {
		return "", fmt.Errorf("requested CIDR %s is outside pool %s (last address outside pool)",
			requestedCIDR, poolCIDR)
	}

	// 3. Check for overlap with existing allocations
	for _, alloc := range existingAllocations {
		if netutil.CIDROverlaps(requestedCIDR, alloc.Spec.CIDR) {
			return "", fmt.Errorf("requested CIDR %s overlaps with existing allocation %s",
				requestedCIDR, alloc.Spec.CIDR)
		}
	}

	return requestedCIDR, nil
}

// handleCIDRSelectionError handles errors from CIDR selection
func (r *CIDRAllocatorReconciler) handleCIDRSelectionError(err error, blockSize int, poolCIDR string) error {
	if strings.Contains(err.Error(), "no available CIDR blocks") ||
		strings.Contains(err.Error(), "pool exhausted") {
		return NewExhaustedError(fmt.Sprintf(
			"failed to find available CIDR of size /%d: pool exhausted",
			blockSize,
		))
	} else if strings.Contains(err.Error(), "prefix length") ||
		strings.Contains(err.Error(), "invalid") {
		return fmt.Errorf(
			"invalid block size /%d for pool %s: %v",
			blockSize, poolCIDR, err,
		)
	}
	return fmt.Errorf("failed to find available CIDR of size /%d: %v", blockSize, err)
}

// createAllocation attempts to create a Subnet resource
func (r *CIDRAllocatorReconciler) createAllocation(
	ctx context.Context,
	claim *ipamv1.SubnetClaim,
	pool *ipamv1.SubnetPool,
	candidateCIDR string,
	allocationName string,
	retry int,
	localUsedCIDRs map[string]bool,
	logger logr.Logger,
) (bool, string, error) {
	createTimeStart := time.Now()

	// Create allocation resource
	allocation := &ipamv1.Subnet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      allocationName,
			Namespace: claim.Namespace,
		},
		Spec: ipamv1.SubnetSpec{
			PoolRef:   pool.Name,
			CIDR:      candidateCIDR,
			ClusterID: claim.Spec.ClusterID,
			ClaimRef: ipamv1.ClaimReference{
				Name: claim.Name,
				UID:  string(claim.UID),
			},
		},
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(claim, allocation, r.Scheme); err != nil {
		return false, "", fmt.Errorf("failed to set owner reference: %w", err)
	}

	// Create the allocation
	allocCreateErr := r.Create(ctx, allocation)
	createLatency := time.Since(createTimeStart)

	// Case 1: Success
	if allocCreateErr == nil {
		r.recordCreateMetrics("success", createLatency)
		logger.Info("Successfully allocated CIDR",
			"cidr", candidateCIDR,
			"name", allocationName,
			"retries", retry)
		return true, candidateCIDR, nil
	}

	// Case 2: Already exists (conflict)
	if errors.IsAlreadyExists(allocCreateErr) {
		r.recordCreateMetrics("exists", createLatency)
		logger.V(1).Info("Allocation already exists, checking ownership",
			"cidr", candidateCIDR,
			"name", allocationName,
			"retry", retry+1)

		// Check if the existing allocation belongs to this cluster
		existingAlloc := &ipamv1.Subnet{}
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: claim.Namespace,
			Name:      allocationName,
		}, existingAlloc); err == nil {
			if existingAlloc.Spec.ClusterID == claim.Spec.ClusterID {
				logger.Info("Found existing allocation for this cluster",
					"cidr", existingAlloc.Spec.CIDR,
					"name", existingAlloc.Name)
				return true, existingAlloc.Spec.CIDR, nil
			}
		}

		// Note: Since reservation is now done within allocateCIDR(),
		// Reservation processing here is unnecessary (deleted)

		// Return conflict error to trigger retry
		return false, "", NewConflictError(fmt.Sprintf(
			"CIDR %s already allocated to another cluster", candidateCIDR))
	}

	// Case 3: Other error
	r.recordCreateMetrics("error", createLatency)
	return false, "", fmt.Errorf("failed to create allocation: %w", allocCreateErr)
}

// findAvailableCIDR finds the first available CIDR of the specified size
// It uses a bitmap-based approach to efficiently find available blocks
//
// Parameters:
// - poolCIDR: The CIDR range of the pool (e.g., "10.0.0.0/16")
// - blockSize: The desired prefix length (e.g., 24 for a /24 block)
// - usedCIDRs: Map of already allocated CIDRs
//
// Returns:
// - The first available CIDR block of the specified size
// - Error if no blocks are available or if there's an issue with the parameters
func findAvailableCIDR(poolCIDR string, blockSize int, usedCIDRs map[string]bool) (string, error) {
	// Create a CIDR bitmap for efficient allocation
	cidrMap, err := NewCIDRMap(poolCIDR)
	if err != nil {
		return "", fmt.Errorf("failed to create CIDR map: %w", err)
	}

	// Mark all used CIDRs in the bitmap
	for cidr := range usedCIDRs {
		// Ignore errors for already allocated CIDRs - they might overlap
		// or be outside the pool range, which is fine for our purposes
		_ = cidrMap.MarkAllocated(cidr)
	}

	// Find the next available block of the specified size
	cidr, err := cidrMap.AllocateNextAvailable(blockSize)
	if err != nil {
		return "", fmt.Errorf("failed to allocate CIDR: %w", err)
	}

	return cidr, nil
}

// Default number of retries
// Note: This is kept for compatibility and used only if Config is not available
const (
	AsyncTimeoutDefaultSec = 3
)

// Note: SubnetPool.Status update responsibility has been centralized to SubnetReconciler (2025-04-28)
// To avoid conflicts related to Pool.Status updates, update processing from CIDRAllocatorReconciler has been removed
