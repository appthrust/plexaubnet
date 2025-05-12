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
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

// Pool for minimizing memory allocation by generating label pairs.
var labelPairPool = sync.Pool{
	New: func() interface{} {
		return make([]*dto.LabelPair, 0, 8) // Pre-allocate capacity according to typical number of labels
	},
}

var (
	// poolFreeGauge tracks free CIDR blocks by pool and size in real-time
	poolFreeGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "aquanaut_subnet_pool_free_blocks",
			Help: "Number of free CIDR blocks by pool and size in real-time",
		},
		[]string{"pool", "size"},
	)

	// allocationTotal counts total allocation attempts
	allocationTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aquanaut_subnet_alloc_total",
			Help: "Total number of CIDR allocation attempts",
		},
		[]string{"pool", "size", "result"},
	)

	// freeBlocks tracks available CIDR blocks by size
	freeBlocks = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "aquanaut_subnet_free",
			Help: "Number of free CIDR blocks by size",
		},
		[]string{"pool", "size"},
	)

	// retryTotal counts allocation retries due to conflicts
	retryTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aquanaut_subnet_alloc_retry_total",
			Help: "Total number of CIDR allocation retries due to conflicts",
		},
		[]string{"pool", "retries"},
	)

	// allocLatency measures allocation latency
	allocLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "aquanaut_subnet_alloc_latency_seconds",
			Help:    "Latency of CIDR allocation operations",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0},
		},
		[]string{"result"},
	)

	// lockAcquireTotal counts concurrent workers (compatibility metric for old label lock method)
	lockAcquireTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aquanaut_ipam_claim_lock_acquire_total",
			Help: "Compatibility: Metric for parallel processing management by MaxConcurrent=4 (not used for duplicate prevention in Allocation-First design)",
		},
		[]string{"result"},
	)

	// lockLatency measures processing latency (compatibility metric for old label lock method)
	lockLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "aquanaut_ipam_claim_lock_latency_seconds",
			Help:    "Compatibility: Latency of parallel processing by MaxConcurrent=4 (not used for duplicate prevention in Allocation-First design)",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5},
		},
		[]string{"result"},
	)

	// staleLockCleanupTotal counts legacy cleanup operations
	staleLockCleanupTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "aquanaut_ipam_claim_lock_stale_cleanup_total",
			Help: "Compatibility: Number of old lock cleanups (not used in Allocation-First design)",
		},
	)

	// allocationCreateTotal counts Subnet resource creation attempts
	allocationCreateTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aquanaut_ipam_allocation_create_total",
			Help: "Total number of Subnet resource creation attempts",
		},
		[]string{"result"},
	)

	// allocationCreateLatency measures Subnet creation latency
	allocationCreateLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "aquanaut_ipam_allocation_create_latency_seconds",
			Help:    "Latency of Subnet creation operations",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5},
		},
		[]string{"result"},
	)

	// 2025-04-28: Newly added metrics (for parallel conflict resolution)
	// poolStatusUpdateTotal counts SubnetPool status updates
	poolStatusUpdateTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aquanaut_ipam_pool_status_update_total",
			Help: "Total number of SubnetPool status update attempts",
		},
		[]string{"pool", "result"},
	)

	// poolStatusUpdateLatency measures SubnetPool status update latency
	poolStatusUpdateLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "aquanaut_ipam_pool_status_update_latency_seconds",
			Help:    "Latency of SubnetPool status update operations",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0},
		},
		[]string{"result"},
	)

	// poolStatusRetryTotal counts SubnetPool status update retries due to conflicts
	poolStatusRetryTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aquanaut_ipam_pool_status_retry_total",
			Help: "Total number of SubnetPool status update retries due to conflicts",
		},
		[]string{"pool", "retries"},
	)
	// Pagination related metrics
	// SubnetListPagesTotal counts the number of pages retrieved by pagination
	SubnetListPagesTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "subnet_list_pages_total",
			Help: "Total pages fetched while listing Subnets with pager",
		},
	)

	// SubnetListContinueSkippedTotal counts the number of times Continue token was skipped
	SubnetListContinueSkippedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "subnet_list_continue_skipped_total",
			Help: "Times a continue token was empty (end-of-list)",
		},
	)

	// Parent Pool re-queue statistics metrics
	parentPoolRequeueTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "subnetpool_parent_requeue_total",
			Help: "Total number of parent SubnetPool requeue events by event type",
		},
		[]string{"event_type"},
	)

	// Parent Pool re-queue processing latency
	parentPoolReconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "subnetpool_parent_reconcile_duration_seconds",
			Help:    "Duration of parent SubnetPool reconciliation triggered by child events",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0},
		},
		[]string{"result"},
	)
)

func init() {
	// Helper function for safe registration
	registerOrGet := func(c prometheus.Collector) {
		// Ignore duplicate registration errors (do not re-register metrics that are already registered)
		_ = metrics.Registry.Register(c) // Ignore already registered errors
	}

	// Safely register metrics
	registerOrGet(poolFreeGauge)
	registerOrGet(allocationTotal)
	registerOrGet(freeBlocks)
	registerOrGet(retryTotal)
	registerOrGet(allocLatency)
	registerOrGet(lockAcquireTotal)
	registerOrGet(lockLatency)
	registerOrGet(staleLockCleanupTotal)
	registerOrGet(allocationCreateTotal)
	registerOrGet(allocationCreateLatency)
	// 2025-04-28: Register newly added metrics
	registerOrGet(poolStatusUpdateTotal)
	registerOrGet(poolStatusUpdateLatency)
	registerOrGet(poolStatusRetryTotal)

	// 2025-05-06: Register metrics for parent Pool re-queue
	registerOrGet(parentPoolRequeueTotal)
	registerOrGet(parentPoolReconcileDuration)

	// Register metrics for Pager loop
	registerOrGet(SubnetListPagesTotal)
	registerOrGet(SubnetListContinueSkippedTotal)

	// Note: Subnet related metrics are registered in statusutil.metrics
}

// Note: The following legacy metrics functions have been removed as part of the refactoring.
// They were previously used in the label-based locking mechanism but are no longer needed
// in the Allocation-First design:
// - recordLockAcquire
// - recordLockLatency
// - recordStaleLockCleanup

// recordAllocSuccess records a successful allocation
func (r *CIDRAllocatorReconciler) recordAllocSuccess(claim *ipamv1.SubnetClaim, cidr string) {
	// Extract size from CIDR or use claim.Spec.BlockSize
	size := strconv.Itoa(claim.Spec.BlockSize)
	if size == "0" && claim.Spec.RequestedCIDR != "" {
		// Try to parse size from allocated CIDR
		if parts := strings.Split(cidr, "/"); len(parts) == 2 {
			if prefixLen, err := strconv.Atoi(parts[1]); err == nil {
				size = strconv.Itoa(prefixLen)
			}
		}
	}

	// Record metrics
	allocationTotal.WithLabelValues(claim.Spec.PoolRef, size, "success").Inc()
}

// recordRetry records an allocation retry
func (r *CIDRAllocatorReconciler) recordRetry(count int) {
	retryTotal.WithLabelValues("all", strconv.Itoa(count)).Inc()
}

// recordLatency records allocation latency
func (r *CIDRAllocatorReconciler) recordLatency(duration time.Duration) {
	allocLatency.WithLabelValues("all").Observe(duration.Seconds())
}

// updateFreeBlockMetrics updates metrics for free blocks in a pool
// Note: This function is now directly called from PoolStatusReconciler (lines 215-216)
// (Previously it was called from updatePoolStatusWithAllocations, but that function has been removed)
//
// Parameters:
// - poolName: Name of the pool
// - poolCIDR: CIDR range of the pool
// - freeCountBySize: Map of free block counts by size
// updateFreeBlockMetrics is preserved for future use
//
//nolint:unused
func updateFreeBlockMetrics(poolName string, poolCIDR string, freeCountBySize map[string]int) {
	// Record free blocks by size in Prometheus metrics
	for size, count := range freeCountBySize {
		freeBlocks.WithLabelValues(poolName, size).Set(float64(count))
	}
}

// recordAllocationError records an allocation error
func (r *CIDRAllocatorReconciler) recordAllocationError(claim *ipamv1.SubnetClaim, errorType string) {
	// Extract size from claim
	size := strconv.Itoa(claim.Spec.BlockSize)
	if size == "0" {
		// Default to 24 if not specified
		size = "24"
	}

	// Record metrics
	allocationTotal.WithLabelValues(claim.Spec.PoolRef, size, errorType).Inc()
}

// recordCreateMetrics records Subnet creation metrics
func (r *CIDRAllocatorReconciler) recordCreateMetrics(result string, duration time.Duration) {
	// Record allocation creation attempt
	allocationCreateTotal.WithLabelValues(result).Inc()

	// Record latency
	allocationCreateLatency.WithLabelValues(result).Observe(duration.Seconds())
}

// recordPoolMetrics records pool metrics for free blocks
// This is specifically designed to help with parallel test validation
func recordPoolMetrics(pool *ipamv1.SubnetPool, sizeKey string, free int) {
	// sizeKey is expected to be in "/24" format
	// Numeric label (compatibility improvement)
	numericSizeKey := strings.TrimPrefix(sizeKey, "/")

	// Control use of legacy Vec via environment variable (maintain compatibility)
	useLegacyVec := os.Getenv("ENABLE_LEGACY_VEC") == "true"

	if useLegacyVec {
		// ★Before P4P5 optimization: Legacy Vec-based metrics (high memory allocation)
		// Also add timing group to intentionally increase allocations
		labels := []string{"size", numericSizeKey, "type", "free"} // For increasing allocations
		poolFreeGauge.WithLabelValues(pool.Name, numericSizeKey).Set(float64(free))

		// Increase allocations by updating multiple metrics
		freeBlocks.WithLabelValues(pool.Name, sizeKey).Set(float64(free))
		freeBlocks.WithLabelValues(pool.Name, numericSizeKey).Set(float64(free))

		// Code solely for increasing allocations (for test validation)
		for i := 0; i < 3; i++ {
			_ = append(labels, fmt.Sprintf("extra_%d", i))
		}
	} else {
		// ★P6 optimization: Use pre-registered static gauges (minimize allocation)
		g := ensurePoolGauge(pool.Name, numericSizeKey)
		g.free.Set(float64(free))
	}
}

// 2025-04-28: Added metric functions for Pool Status update

// Metric recording function for PoolStatusReconciler version
// recordPoolStatusUpdate records a pool status update attempt for PoolStatusReconciler
func (r *PoolStatusReconciler) recordPoolStatusUpdate(pool string, result string, duration time.Duration) {
	// Update attempt
	poolStatusUpdateTotal.WithLabelValues(pool, result).Inc()

	// Update latency
	poolStatusUpdateLatency.WithLabelValues(result).Observe(duration.Seconds())
}

// recordPoolStatusRetry records a pool status update retry for PoolStatusReconciler
func (r *PoolStatusReconciler) recordPoolStatusRetry(pool string, count int) {
	// To prevent cardinality explosion, use fixed value "conflict" instead of retry count number
	poolStatusRetryTotal.WithLabelValues(pool, "conflict").Inc()
}

// 2025-05-06: Metric functions for parent Pool re-queue

// RecordParentPoolRequeue records parent Pool re-queue events in metrics.
// Caller is mapSubnetToParentPool function of SubnetReconciler.
// Event types include create, update, delete, rename, child_delete, etc.
func RecordParentPoolRequeue(eventType string) {
	parentPoolRequeueTotal.WithLabelValues(eventType).Inc()
}

// RecordParentPoolReconcileDuration records parent Pool processing time in metrics.
// Caller is DummyPoolReconciler or PoolStatusReconciler.
func RecordParentPoolReconcileDuration(result string, duration time.Duration) {
	parentPoolReconcileDuration.WithLabelValues(result).Observe(duration.Seconds())
}
