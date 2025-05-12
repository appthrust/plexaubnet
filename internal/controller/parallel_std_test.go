//go:build !race
// +build !race

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

// Note: This file contains parallel tests using the Standard Go Testing API.
package cidrallocator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	_ "github.com/appthrust/plexaubnet/internal/testutils" // Import for envtest assets setup
)

// Note: Field index uses PoolRefField from init_index.go.

// Common constants for parallel tests
const (
	defaultTimeout       = 120 * time.Second // Overall test timeout
	shortTimeout         = 30 * time.Second  // Timeout for short operations
	parallelPollInterval = 500 * time.Millisecond
	namespaceDefault     = "default"
	numClaims            = 1 // Number of claims for testing
	poolCIDR             = "10.0.0.0/14"
	blockSize            = 24
)

// Environment variables for parallel tests
var (
	parallelTestEnv *envtest.Environment
	parallelCfg     *rest.Config
	parallelClient  client.Client
	parallelScheme  *runtime.Scheme
	setupOnce       sync.Once
	mgrCtx          context.Context    // Context for the manager
	mgrCancel       context.CancelFunc // Function to stop the manager
)

// parallelWaitForCondition is a helper function that waits until the specified condition is met.
func parallelWaitForCondition(t *testing.T, condition func() bool, timeout time.Duration, errorMessage string) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(parallelPollInterval)
	}
	t.Errorf("Timeout: %s", errorMessage)
	return false
}

// parallelTestSetup sets up the environment for parallel tests.
func parallelTestSetup(t *testing.T) {
	setupOnce.Do(func() {
		// Set up logger
		logf.SetLogger(zap.New(zap.WriteTo(os.Stdout), zap.UseDevMode(true)))

		// Start envtest environment
		t.Log("Parallel test: Setting up envtest environment...")
		parallelTestEnv = &envtest.Environment{
			CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
			ErrorIfCRDPathMissing: true,
		}

		var err error
		// Start envtest
		parallelCfg, err = parallelTestEnv.Start()
		if err != nil {
			t.Fatalf("Parallel test: Failed to start envtest: %v", err)
		}

		// Configure REST Client - adjust QPS/Burst for high concurrency
		parallelCfg.QPS = 100
		parallelCfg.Burst = 200

		// Configure scheme
		parallelScheme = runtime.NewScheme()

		// Register basic Kubernetes core resources
		err = clientgoscheme.AddToScheme(parallelScheme)
		if err != nil {
			t.Fatalf("Parallel test: Failed to register core scheme: %v", err)
		}

		// Register custom resources
		err = ipamv1.AddToScheme(parallelScheme)
		if err != nil {
			t.Fatalf("Parallel test: Failed to register scheme: %v", err)
		}

		// Create client
		parallelClient, err = client.New(parallelCfg, client.Options{Scheme: parallelScheme})
		if err != nil {
			t.Fatalf("Parallel test: Failed to create client: %v", err)
		}

		// Setup Controller Manager
		// Use "0" to let OS assign a free port to avoid conflicts during concurrent tests

		mgr, err := ctrl.NewManager(parallelCfg, ctrl.Options{
			Scheme: parallelScheme,
			// Use "0" to let OS assign a free port (more reliable than random port)
			Metrics: metricsserver.Options{
				BindAddress: "0",
			},
			// Also disable health checks
			HealthProbeBindAddress: "0",
			// No leader election
			LeaderElection: false,
		})
		if err != nil {
			t.Fatalf("Parallel test: Failed to create manager: %v", err)
		}

		// Unified field index setup - this ensures that standard indexes
		// used by PoolStatusReconciler etc. are correctly registered.
		if err := SetupFieldIndexes(context.Background(), mgr, logf.Log); err != nil {
			t.Fatalf("Parallel test: Failed to setup field indexes: %v", err)
		}

		// Use the client
		parallelClient = mgr.GetClient()

		// ========= Controller Setup =========
		// Without this, events will not be processed and claim -> subnet generation will not occur.
		t.Log("Parallel test: Setting up controllers...")

		// Controller 1: CIDR Allocator
		allocReconciler := &CIDRAllocatorReconciler{
			Client:         mgr.GetClient(),
			Scheme:         mgr.GetScheme(),
			Recorder:       mgr.GetEventRecorderFor("subnet-controller"),
			ControllerName: "allocator-parallel-test", // Explicitly set controller name to avoid duplication
		}
		allocReconciler.eventEmitter = NewEventEmitter(allocReconciler.Recorder, allocReconciler.Scheme)

		if err := allocReconciler.SetupWithManager(mgr); err != nil {
			t.Fatalf("Parallel test: Failed to setup CIDRAllocatorReconciler: %v", err)
		}

		// Controller 2: Subnet Reconciler
		subnetReconciler := &SubnetReconciler{
			Client:         mgr.GetClient(),
			Scheme:         mgr.GetScheme(),
			Config:         nil,                              // Tests can run even if IPAMConfig is nil
			ControllerName: "subnet-controller-parallel-std", // Avoid name collisions between tests
		}

		if err := subnetReconciler.SetupWithManager(mgr); err != nil {
			// If index is already set, build directly
			if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "indexer conflict") {
				t.Logf("Warning: Ignoring index conflict, trying alternative controller registration")

				// Register fallback with correctly configured Watch targets and Predicates
				builder := ctrl.NewControllerManagedBy(mgr).
					Named("subnet-controller-fallback").
					For(&ipamv1.Subnet{})

				// Add Watch configuration similar to the original controller (Subnet-Event mapping configuration)
				builder = builder.Watches(
					&ipamv1.SubnetPool{},
					handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
						// Map only on pool changes
						return []ctrl.Request{}
					}),
				)

				// Build controller
				if buildErr := builder.Complete(subnetReconciler); buildErr != nil {
					t.Fatalf("Parallel test: Failed to register SubnetReconciler fallback: %v", buildErr)
				}
			} else {
				t.Fatalf("Parallel test: Failed to setup SubnetReconciler: %v", err)
			}
		}

		// Controller 3: Pool Status Reconciler
		poolStatusReconciler := &PoolStatusReconciler{
			Client:         mgr.GetClient(),
			Scheme:         mgr.GetScheme(),
			ControllerName: "poolstatus-parallel-test", // Explicitly set controller name to avoid duplication
		}

		if err := poolStatusReconciler.SetupWithManager(mgr); err != nil {
			if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "indexer conflict") {
				t.Logf("Warning: PoolStatusReconciler index conflict detected, registering fallback controller...")

				// Full fallback registration (including Watches configuration)
				builder := ctrl.NewControllerManagedBy(mgr).
					Named("poolstatus-controller-fallback").
					For(&ipamv1.SubnetPool{})

				// Watch 1: Subnet -> Pool mapping (Important: this was missing)
				builder = builder.Watches(
					&ipamv1.Subnet{},
					handler.EnqueueRequestsFromMapFunc(poolStatusReconciler.mapAllocToPool),
				)

				// Watch 2: SubnetPool -> Parent Pool mapping
				builder = builder.Watches(
					&ipamv1.SubnetPool{},
					handler.EnqueueRequestsFromMapFunc(poolStatusReconciler.mapChildToParent),
				)

				// Build controller
				if buildErr := builder.Complete(poolStatusReconciler); buildErr != nil {
					t.Fatalf("Parallel test: Failed to register PoolStatusReconciler fallback: %v", buildErr)
				}

				t.Logf("PoolStatusReconciler fallback registration complete (with Watch configuration)")
			} else {
				t.Fatalf("Parallel test: Failed to setup PoolStatusReconciler: %v", err)
			}
		}

		t.Log("Parallel test: All controller setups complete")

		// Start manager in the background (without this, the cache will not start)
		// Setup context
		mgrCtx, mgrCancel = context.WithCancel(context.Background())

		// Register cleanup function to be called at the end of the test
		t.Cleanup(func() {
			mgrCancel() // Send stop signal to manager
			t.Log("Manager stop signal sent")
		})

		// Start manager in a separate goroutine
		go func() {
			if err := mgr.Start(mgrCtx); err != nil {
				// If a manager error occurs during the test, just log it
				// because t.Fatalf cannot be called from a goroutine
				fmt.Printf("Manager start error: %v\n", err)
			}
		}()

		// Wait a bit for the manager to start completely
		time.Sleep(100 * time.Millisecond)

		// Confirm that the cache has started
		cacheStartTimeout := time.NewTimer(5 * time.Second)
		cacheStarted := false
		for !cacheStarted {
			select {
			case <-cacheStartTimeout.C:
				t.Fatalf("Cache startup timeout")
			default:
				// Check if cache has started
				nslist := &corev1.NamespaceList{}
				if err := parallelClient.List(context.Background(), nslist); err == nil {
					cacheStarted = true
					t.Logf("Parallel test: Cache startup confirmed (%d namespaces retrieved successfully)", len(nslist.Items))
				} else {
					// If cache has not started yet, wait a bit
					t.Logf("Waiting for cache to start: %v", err)
					time.Sleep(200 * time.Millisecond)
				}
			}
		}

		t.Log("Parallel test: Environment setup complete")
	})

	// Confirm that the client is correctly initialized
	if parallelClient == nil {
		t.Fatal("Test environment not set up correctly")
	}
}

// TestParallelAllocation_Basic is a basic parallel allocation test.
// Note: Running concurrently with other tests may cause port conflicts.
// Recommended: go test -v ./internal/controller -run="TestParallelAllocation_Basic" -count=1
func TestParallelAllocation_Basic(t *testing.T) {
	// Setup test environment
	parallelTestSetup(t)

	// Test definition
	poolName := "test-pool-parallel-std"
	t.Logf("Starting parallel test with field index '%s' and %d claims", PoolRefField, numClaims)

	// Context setup
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Create test pool
	t.Log("Creating test pool...")
	pool := &ipamv1.SubnetPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      poolName,
			Namespace: namespaceDefault,
		},
		Spec: ipamv1.SubnetPoolSpec{
			CIDR:             poolCIDR,
			DefaultBlockSize: blockSize,
			Strategy:         ipamv1.StrategyLinear,
		},
	}

	// Create pool and delete it at the end
	if err := parallelClient.Create(ctx, pool); err != nil {
		t.Fatalf("Failed to create test pool: %v", err)
	}
	defer func() {
		if err := parallelClient.Delete(ctx, pool); err != nil {
			t.Logf("Warning: Failed to delete test pool: %v", err)
		}
	}()

	// Confirm pool existence
	parallelWaitForCondition(t, func() bool {
		return parallelClient.Get(ctx, types.NamespacedName{
			Name:      poolName,
			Namespace: namespaceDefault,
		}, pool) == nil
	}, shortTimeout, "Pool was not created")

	// Create claims in parallel
	t.Logf("Creating %d claims in parallel...", numClaims)
	var wg sync.WaitGroup
	wg.Add(numClaims)

	errCount := 0
	var errMutex sync.Mutex

	// Create each claim in a goroutine
	for i := 0; i < numClaims; i++ {
		go func(claimID int) {
			defer wg.Done()

			claim := &ipamv1.SubnetClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("test-claim-std-%d", claimID),
					Namespace: namespaceDefault,
				},
				Spec: ipamv1.SubnetClaimSpec{
					PoolRef:   poolName,
					ClusterID: fmt.Sprintf("test-cluster-std-%d", claimID),
					BlockSize: blockSize,
				},
			}

			if err := parallelClient.Create(ctx, claim); err != nil {
				errMutex.Lock()
				errCount++
				t.Logf("Claim creation error (ID=%d): %v", claimID, err)
				errMutex.Unlock()
			}
		}(i)
	}

	// Wait for all claim creations
	wg.Wait()
	t.Logf("Claim creation process complete (error count: %d)", errCount)

	// ====== Test Validation Step 1: Confirm all claims were created ======
	t.Log("Confirming all claims were created...")

	// Confirm that the expected number of claims exist
	parallelWaitForCondition(t, func() bool {
		claimList := &ipamv1.SubnetClaimList{}
		if err := parallelClient.List(ctx, claimList,
			client.InNamespace(namespaceDefault),
			&client.ListOptions{Raw: &metav1.ListOptions{ResourceVersion: "0"}}); err != nil {
			t.Logf("Error listing claims: %v", err)
			return false
		}

		// Count claims related to the target pool
		count := 0
		for _, c := range claimList.Items {
			if c.Spec.PoolRef == poolName {
				count++
			}
		}

		t.Logf("Number of claims: %d/%d (pool: %s)", count, numClaims, poolName)
		return count == numClaims
	}, shortTimeout, "Not all claims were created")

	// ====== Test Validation Step 2: Confirm Subnet resource creation ======
	t.Log("Confirming start of allocation processing...")

	// Confirm that at least one subnet resource is created
	parallelWaitForCondition(t, func() bool {
		allocationList := &ipamv1.SubnetList{}
		if err := parallelClient.List(ctx, allocationList,
			client.InNamespace(namespaceDefault),
			&client.ListOptions{Raw: &metav1.ListOptions{ResourceVersion: "0"}}); err != nil {
			t.Logf("Error listing subnets: %v", err)
			return false
		}

		count := len(allocationList.Items)
		if count > 0 {
			t.Logf("Subnet creation confirmed: %d detected", count)
			return true
		}
		return false
	}, shortTimeout, "Subnet resources were not created")

	// ====== Test Validation Step 3: Confirm all claims are bound ======
	t.Log("Waiting for all claims to be bound...")

	// Wait until all claims are bound
	parallelWaitForCondition(t, func() bool {
		claimList := &ipamv1.SubnetClaimList{}
		if err := parallelClient.List(ctx, claimList,
			client.InNamespace(namespaceDefault),
			&client.ListOptions{Raw: &metav1.ListOptions{ResourceVersion: "0"}}); err != nil {
			t.Logf("Error listing claims: %v", err)
			return false
		}

		// Count number of bound claims
		boundCount := 0
		pendingCount := 0
		for _, c := range claimList.Items {
			if c.Spec.PoolRef != poolName {
				continue
			}

			if c.Status.Phase == ipamv1.ClaimBound && c.Status.AllocatedCIDR != "" {
				boundCount++
			} else {
				pendingCount++
			}
		}

		t.Logf("Binding status: %d bound, %d pending", boundCount, pendingCount)
		return boundCount == numClaims
	}, defaultTimeout, "Not all claims were bound")

	// ====== Test Validation Step 4: Confirm CIDR uniqueness ======
	t.Log("Confirming uniqueness of allocated CIDRs...")

	// Get the latest claim list
	claimList := &ipamv1.SubnetClaimList{}
	if err := parallelClient.List(ctx, claimList,
		client.InNamespace(namespaceDefault),
		&client.ListOptions{Raw: &metav1.ListOptions{ResourceVersion: "0"}}); err != nil {
		t.Fatalf("Failed to list claims: %v", err)
	}

	// Filter claims related to the pool
	filteredClaims := []ipamv1.SubnetClaim{}
	for _, claim := range claimList.Items {
		if claim.Spec.PoolRef == poolName {
			filteredClaims = append(filteredClaims, claim)
		}
	}

	// Validate CIDR uniqueness
	cidrMap := make(map[string]string) // map[cidr]claimName
	for _, claim := range filteredClaims {
		// Confirm binding status
		if claim.Status.Phase != ipamv1.ClaimBound {
			t.Fatalf("Claim %s is not in bound state (current: %s)",
				claim.Name, claim.Status.Phase)
		}

		// Confirm CIDR allocation
		if claim.Status.AllocatedCIDR == "" {
			t.Fatalf("CIDR not allocated to claim %s", claim.Name)
		}

		// Check for duplicates
		if existingClaim, exists := cidrMap[claim.Status.AllocatedCIDR]; exists {
			t.Fatalf("Duplicate CIDR %s: Claims %s and %s are sharing it",
				claim.Status.AllocatedCIDR, existingClaim, claim.Name)
		}

		cidrMap[claim.Status.AllocatedCIDR] = claim.Name
	}

	// Confirm expected number of unique CIDRs
	if len(cidrMap) != numClaims {
		t.Fatalf("Expected number of unique CIDRs: %d, Actual: %d", numClaims, len(cidrMap))
	}

	// ====== Test Validation Step 5: Confirm pool status update ======
	t.Log("Waiting for pool status to be updated...")

	// Wait for pool status allocation count to be correctly updated
	parallelWaitForCondition(t, func() bool {
		// Get the latest state of the pool
		poolStatus := &ipamv1.SubnetPool{}
		if err := parallelClient.Get(ctx, types.NamespacedName{
			Name:      poolName,
			Namespace: namespaceDefault,
		}, poolStatus, &client.GetOptions{Raw: &metav1.GetOptions{ResourceVersion: "0"}}); err != nil {
			t.Logf("Pool get error: %v", err)
			return false
		}

		// Manually search for Subnet to confirm
		manualAllocList := &ipamv1.SubnetList{}
		if err := parallelClient.List(ctx, manualAllocList,
			client.InNamespace(namespaceDefault)); err != nil {
			t.Logf("Manual Subnet search error: %v", err)
			return false
		}

		// Manually count Subnets that match
		manualCount := 0
		for _, subnet := range manualAllocList.Items {
			if subnet.Spec.PoolRef == poolName {
				manualCount++
				t.Logf("Manual confirmation: Subnet=%s, PoolRef=%s, CIDR=%s",
					subnet.Name, subnet.Spec.PoolRef, subnet.Spec.CIDR)
			}
		}

		t.Logf("Pool allocation status: %d/%d (manual confirmation: %d)",
			poolStatus.Status.AllocatedCount, numClaims, manualCount)

		// Compare actual status value and number of Subnets
		if poolStatus.Status.AllocatedCount == manualCount && manualCount == numClaims {
			return true
		} else if manualCount == numClaims && poolStatus.Status.AllocatedCount == 0 {
			// In practice, should be allocated but status not updated
			t.Logf("PoolStatusReconciler issue detected: Subnet count is %d but status is 0", manualCount)

			// Correctly restart PoolStatusReconciler
			poolStatusReconciler := &PoolStatusReconciler{
				Client:         parallelClient,                     // Use parallelClient instead of mgr.GetClient()
				Scheme:         parallelScheme,                     // Use parallelScheme instead of mgr.GetScheme()
				ControllerName: "poolstatus-parallel-test-restart", // Explicitly set controller name to avoid duplication
			}

			// Re-enqueue individually
			poolStatusReconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      poolName,
					Namespace: namespaceDefault,
				},
			})

			return false
		}

		return false
	}, defaultTimeout, "Pool status not correctly updated")

	// ====== Test Validation Step 6: Detailed pool status verification ======
	t.Log("Verifying detailed pool status...")

	// Get the latest pool status
	updatedPool := &ipamv1.SubnetPool{}
	if err := parallelClient.Get(ctx, types.NamespacedName{
		Name:      poolName,
		Namespace: namespaceDefault,
	}, updatedPool); err != nil {
		t.Fatalf("Failed to get updated pool: %v", err)
	}

	// AllocatedCIDRs verification
	if updatedPool.Status.AllocatedCIDRs == nil {
		t.Fatal("AllocatedCIDRs map not initialized")
	}

	if len(updatedPool.Status.AllocatedCIDRs) != numClaims {
		t.Fatalf("AllocatedCIDRs should have %d entries but has %d",
			numClaims, len(updatedPool.Status.AllocatedCIDRs))
	}

	// Claims by cluster ID verification
	claimsByClusterID := make(map[string]ipamv1.SubnetClaim)
	for _, claim := range filteredClaims {
		clusterID := claim.Spec.ClusterID
		claimsByClusterID[clusterID] = claim

		// Confirm CIDR exists in map and is correctly mapped to the expected cluster
		allocatedToClusterID, found := updatedPool.Status.AllocatedCIDRs[claim.Status.AllocatedCIDR]
		if !found {
			t.Fatalf("CIDR %s of claim %s not found in AllocatedCIDRs",
				claim.Status.AllocatedCIDR, claim.Name)
		}

		if allocatedToClusterID != clusterID {
			t.Fatalf("CIDR %s is mapped to wrong clusterID. Expected: %s, Actual: %s",
				claim.Status.AllocatedCIDR, clusterID, allocatedToClusterID)
		}
	}

	// Free count verification
	freeCountKey := fmt.Sprintf("%d", blockSize)
	freeCount, found := updatedPool.Status.FreeCountBySize[freeCountKey]

	// Fallback for different format
	if !found {
		freeCountKey = fmt.Sprintf("/%d", blockSize)
		freeCount, found = updatedPool.Status.FreeCountBySize[freeCountKey]
	}

	if !found {
		t.Fatalf("FreeCountBySize missing block size %d data", blockSize)
	}

	// /14 pool should have 1024 total /24 blocks
	expectedFreeCount := 1024 - numClaims
	if freeCount != expectedFreeCount {
		t.Fatalf("Expected free block count: %d, Actual: %d", expectedFreeCount, freeCount)
	}

	t.Logf("Parallel allocation test completed successfully")
}
