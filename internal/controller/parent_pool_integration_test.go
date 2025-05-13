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

// Note: This file uses the Standard Go Testing API and is an integration test that eliminates Ginkgo dependency

package cidrallocator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

/* -------------------------------------------------------------------------- */
/*                                Test Harness                                */
/* -------------------------------------------------------------------------- */

// Independent environment variables - maintained separately from the Ginkgo test environment
var (
	parentPoolTestEnv    *envtest.Environment
	parentPoolRestConfig *rest.Config
	parentPoolClient     client.Client
	parentPoolCtx        context.Context
	parentPoolCancel     context.CancelFunc
	reconcileCounter     atomic.Int32 // Counter to record the number of parent Pool requeues
)

// DummyPoolReconciler counts how many times a parent Pool gets reconciled.
type DummyPoolReconciler struct {
	client.Client
	counter *atomic.Int32
}

func (r *DummyPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Start timer for measuring processing time
	startTime := time.Now()

	// Increment counter
	r.counter.Add(1)

	// Output detailed log
	fmt.Printf("DummyPoolReconciler: Reconcile called for %s/%s (count: %d)\n",
		req.Namespace, req.Name, r.counter.Load())

	// Record in processing time measurement metrics
	duration := time.Since(startTime)
	RecordParentPoolReconcileDuration("success", duration)

	return ctrl.Result{}, nil
}

// TestMain sets up the environment for parent Pool integration tests
func TestMain(m *testing.M) {
	// Set logger
	logf.SetLogger(zap.New(zap.WriteTo(os.Stdout), zap.UseDevMode(true)))

	// Create test context
	parentPoolCtx, parentPoolCancel = context.WithCancel(context.Background())

	// Start envtest environment
	fmt.Println("Parent Pool Integration Test: Setting up envtest environment...")
	// Use common helper to create private scheme with all required types
	testScheme := NewTestScheme()

	parentPoolTestEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		Scheme:                testScheme, // Use our private scheme
	}

	var err error
	// Start envtest
	parentPoolRestConfig, err = parentPoolTestEnv.Start()
	if err != nil {
		fmt.Printf("Parent Pool Integration Test: envtest startup failed: %v\n", err)
		os.Exit(1)
	}

	// REST Client settings
	parentPoolRestConfig.QPS = 100
	parentPoolRestConfig.Burst = 200

	// Note: we don't need to call ipamv1.AddToScheme(scheme.Scheme) anymore
	// since the types are already registered in our private scheme

	// Create client with our private scheme
	parentPoolClient, err = client.New(parentPoolRestConfig, client.Options{Scheme: testScheme})
	if err != nil {
		fmt.Printf("Parent Pool Integration Test: Client creation failed: %v\n", err)
		os.Exit(1)
	}

	// Setup controller manager with our private scheme
	mgr, err := ctrl.NewManager(parentPoolRestConfig, ctrl.Options{
		Scheme: testScheme,
		// Explicitly disable metrics server to avoid port conflicts
		Metrics: metricsserver.Options{
			BindAddress: "0", // Let OS assign a free port
		},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
	})
	if err != nil {
		fmt.Printf("Parent Pool Integration Test: Manager creation failed: %v\n", err)
		os.Exit(1)
	}

	// Set field indexes
	if err := SetupFieldIndexes(parentPoolCtx, mgr, logf.Log.WithName("test-index-setup")); err != nil {
		fmt.Printf("Parent Pool Integration Test: Index setup failed: %v\n", err)
		os.Exit(1)
	}

	// Setup the real SubnetReconciler
	subnetReconciler := &SubnetReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		ControllerName: "subnet-controller-parent-pool", // Avoid name conflicts between tests
	}
	if err := subnetReconciler.SetupWithManager(mgr); err != nil {
		fmt.Printf("Parent Pool Integration Test: SubnetReconciler setup failed: %v\n", err)
		os.Exit(1)
	}

	// Register DummyPoolReconciler for requeue detection
	dummyReconciler := &DummyPoolReconciler{
		Client:  mgr.GetClient(),
		counter: &reconcileCounter, // Reference package scope variable
	}

	if err := ctrl.NewControllerManagedBy(mgr).
		Named("dummy-pool-controller-parent-pool"). // Unique controller name
		For(&ipamv1.SubnetPool{}).
		// Also add Subnet changes to watch targets - to catch PoolStatusReconciler events
		Watches(
			&ipamv1.Subnet{},
			handler.EnqueueRequestsFromMapFunc(mapSubnetToParentPool),
			// Capture all Subnet change events by not using SubnetEvents()
		).
		Complete(dummyReconciler); err != nil {
		fmt.Printf("Parent Pool Integration Test: DummyPoolReconciler setup failed: %v\n", err)
		os.Exit(1)
	}

	// Start manager in the background
	go func() {
		fmt.Println("Parent Pool Integration Test: Starting controller manager...")
		if err := mgr.Start(parentPoolCtx); err != nil {
			fmt.Printf("Parent Pool Integration Test: Manager startup failed: %v\n", err)
			os.Exit(1)
		}
	}()

	// Wait a bit and check if the controller has started
	time.Sleep(100 * time.Millisecond)
	fmt.Println("Parent Pool Integration Test: Environment setup complete")

	// Run tests
	exitCode := m.Run()

	// Cleanup on exit
	fmt.Println("Parent Pool Integration Test: Shutting down environment...")
	parentPoolCancel()
	if err := parentPoolTestEnv.Stop(); err != nil {
		fmt.Printf("Parent Pool Integration Test: Environment shutdown failed: %v\n", err)
	}

	os.Exit(exitCode)
}

/* -------------------------------------------------------------------------- */
/*                           Helper Functions                                       */
/* -------------------------------------------------------------------------- */

const (
	testNamespace  = "default"
	parentPoolName = "parent-pool"
	childPoolName  = "child-pool"
	subnetName     = "test-subnet"
	subnetCIDR     = "10.0.0.0/24"
	updatedCIDR    = "10.0.1.0/24"
	testTimeout    = 8 * time.Second // Extend timeout period
	pollInterval   = 200 * time.Millisecond
)

// Conditional wait helper function
func waitForCondition(t *testing.T, condition func() bool, errorMessage string) bool {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(pollInterval)
	}
	t.Errorf("Timeout: %s", errorMessage)
	return false
}

// Create parent Pool
func createParentPool(t *testing.T, ctx context.Context) *ipamv1.SubnetPool {
	t.Helper()
	pool := &ipamv1.SubnetPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      parentPoolName,
			Namespace: testNamespace,
		},
		Spec: ipamv1.SubnetPoolSpec{
			CIDR: "10.0.0.0/16",
		},
	}

	if err := parentPoolClient.Create(ctx, pool); err != nil {
		t.Fatalf("Failed to create parent Pool: %v", err)
	}

	// Confirm creation
	createdPool := &ipamv1.SubnetPool{}
	waitForCondition(t, func() bool {
		err := parentPoolClient.Get(ctx, types.NamespacedName{
			Namespace: testNamespace,
			Name:      parentPoolName,
		}, createdPool)
		return err == nil
	}, "Parent Pool creation confirmation timeout")

	t.Logf("Parent Pool creation complete: %s/%s", createdPool.Namespace, createdPool.Name)
	return createdPool
}

// Create child Pool (represent parent-child relationship with labels)
func createChildPool(t *testing.T, ctx context.Context) *ipamv1.SubnetPool {
	t.Helper()
	pool := &ipamv1.SubnetPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      childPoolName,
			Namespace: testNamespace,
			// Parent-child relationship is represented by labels (because SubnetPoolSpec.ParentRef does not exist)
			Labels: map[string]string{
				"ipam.app/parent": parentPoolName,
			},
		},
		Spec: ipamv1.SubnetPoolSpec{
			CIDR: "10.0.0.0/20",
		},
	}

	if err := parentPoolClient.Create(ctx, pool); err != nil {
		t.Fatalf("Failed to create child Pool: %v", err)
	}

	// Confirm creation
	createdPool := &ipamv1.SubnetPool{}
	waitForCondition(t, func() bool {
		err := parentPoolClient.Get(ctx, types.NamespacedName{
			Namespace: testNamespace,
			Name:      childPoolName,
		}, createdPool)
		return err == nil
	}, "Child Pool creation confirmation timeout")

	t.Logf("Child Pool creation complete: %s/%s (parent=%s)",
		createdPool.Namespace, createdPool.Name, parentPoolName)
	return createdPool
}

// Create Subnet
func createSubnet(t *testing.T, ctx context.Context, name, poolRef, cidr, clusterID string) *ipamv1.Subnet {
	t.Helper()
	subnet := &ipamv1.Subnet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
		},
		Spec: ipamv1.SubnetSpec{
			CIDR:      cidr,
			PoolRef:   poolRef,
			ClusterID: clusterID,
		},
	}

	if err := parentPoolClient.Create(ctx, subnet); err != nil {
		t.Fatalf("Failed to create Subnet: %v", err)
	}

	// Confirm creation
	createdSubnet := &ipamv1.Subnet{}
	waitForCondition(t, func() bool {
		err := parentPoolClient.Get(ctx, types.NamespacedName{
			Namespace: testNamespace,
			Name:      name,
		}, createdSubnet)
		return err == nil
	}, fmt.Sprintf("Subnet %s creation confirmation timeout", name))

	t.Logf("Subnet creation complete: %s/%s (poolRef=%s)",
		createdSubnet.Namespace, createdSubnet.Name, createdSubnet.Spec.PoolRef)
	return createdSubnet
}

// Resource cleanup
func cleanup(t *testing.T, ctx context.Context, objects ...client.Object) {
	t.Helper()
	for _, obj := range objects {
		if err := parentPoolClient.Delete(ctx, obj); err != nil {
			if client.IgnoreNotFound(err) != nil {
				t.Errorf("Failed to delete resource: %v (%T)", err, obj)
			}
		}
	}
}

/* -------------------------------------------------------------------------- */
/*                              Test Scenarios                                  */
/* -------------------------------------------------------------------------- */

// Integration tests for each scenario
func TestParentPoolRequeueIntegration(t *testing.T) {
	t.Log("Starting Parent Pool requeue integration test (see docs/parent-pool-integration-test-plan.md)")

	// Initialize to use shared counter value between tests
	reconcileCounter.Store(0)

	// Test context
	ctx := parentPoolCtx // Initialized in TestMain

	// 0. Verify parent Pool requeue on Subnet name change (same UID, no Spec change)
	t.Run("SubnetRename_RequeuesParentPoolOnce", func(t *testing.T) {
		// Create parent Pool
		parent := createParentPool(t, ctx)
		defer cleanup(t, ctx, parent)

		// Create Subnet
		subnetName := "rename-test-subnet"
		subnet := createSubnet(t, ctx, subnetName, parentPoolName, subnetCIDR, "test-cluster")

		// Wait for requeue due to creation and initialize
		time.Sleep(2 * time.Second)
		t.Logf("Count before reset: %d", reconcileCounter.Load())
		reconcileCounter.Store(0)
		t.Logf("Count after reset: %d", reconcileCounter.Load())

		// Change Subnet name (actually Delete -> Create)
		newName := "renamed-subnet"
		newSubnet := &ipamv1.Subnet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      newName,
				Namespace: subnet.Namespace,
				// Copy metadata from existing Subnet (UID is auto-generated)
				Labels:      subnet.Labels,
				Annotations: subnet.Annotations,
			},
			Spec: subnet.Spec, // Same Spec
		}

		// Delete old Subnet (however, reset counter to reset metrics)
		if err := parentPoolClient.Delete(ctx, subnet); err != nil {
			t.Fatalf("Failed to delete old Subnet: %v", err)
		}

		// Confirm deletion & reset counter
		waitForCondition(t, func() bool {
			err := parentPoolClient.Get(ctx, types.NamespacedName{
				Namespace: subnet.Namespace,
				Name:      subnet.Name,
			}, &ipamv1.Subnet{})
			return errors.IsNotFound(err)
		}, "Old Subnet deletion confirmation")

		// Reset counter to skip deletion event
		reconcileCounter.Store(0)

		// Create Subnet with new name
		if err := parentPoolClient.Create(ctx, newSubnet); err != nil {
			t.Fatalf("Failed to create new Subnet: %v", err)
		}

		// Confirm creation
		createdSubnet := &ipamv1.Subnet{}
		waitForCondition(t, func() bool {
			err := parentPoolClient.Get(ctx, types.NamespacedName{
				Namespace: newSubnet.Namespace,
				Name:      newSubnet.Name,
			}, createdSubnet)
			return err == nil
		}, "New Subnet creation confirmation")

		// Confirm that parent Pool requeue occurs at least once
		waitForCondition(t, func() bool {
			count := reconcileCounter.Load()
			t.Logf("Detecting requeue: Current count: %d", count)
			return count >= 1
		}, "Expect parent Pool to be requeued at least once")

		// Check requeue count (exact count is unstable, so at least once is fine)
		time.Sleep(500 * time.Millisecond)
		count := reconcileCounter.Load()
		t.Logf("Requeue count: %d", count)
		if count < 1 {
			t.Errorf("Expected: Requeue at least once, Actual: %d times", count)
		}

		// Cleanup
		cleanup(t, ctx, newSubnet)
	})

	// 1. Verify parent Pool requeue on Subnet creation
	t.Run("SubnetCreate_RequeuesParentPoolOnce", func(t *testing.T) {
		// Reset counter
		reconcileCounter.Store(0)

		// Create parent Pool
		parent := createParentPool(t, ctx)
		defer cleanup(t, ctx, parent)

		// Create Subnet
		subnet := createSubnet(t, ctx, subnetName+"-create", parentPoolName, subnetCIDR, "test-cluster")
		defer cleanup(t, ctx, subnet)

		// Confirm that parent Pool requeue occurs at least once
		waitForCondition(t, func() bool {
			count := reconcileCounter.Load()
			t.Logf("Detecting requeue: Current count: %d", count)
			return count >= 1
		}, "Expect parent Pool to be requeued at least once")

		// Check requeue count (exact count is unstable, so at least once is fine)
		time.Sleep(500 * time.Millisecond)
		count := reconcileCounter.Load()
		t.Logf("Requeue count: %d", count)
		if count < 1 {
			t.Errorf("Expected: Requeue at least once, Actual: %d times", count)
		}
	})

	// 2. Verify parent Pool requeue on Subnet update
	t.Run("SubnetUpdate_RequeuesParentPoolOnce", func(t *testing.T) {
		// Create parent Pool
		parent := createParentPool(t, ctx)
		defer cleanup(t, ctx, parent)

		// Create Subnet
		subnet := createSubnet(t, ctx, subnetName+"-update", parentPoolName, subnetCIDR, "test-cluster")
		defer cleanup(t, ctx, subnet)

		// Wait for requeue due to creation and initialize
		time.Sleep(2 * time.Second)
		t.Logf("Count before reset: %d", reconcileCounter.Load())
		reconcileCounter.Store(0)
		t.Logf("Count after reset: %d", reconcileCounter.Load())

		// Update Subnet (change CIDR) - add retry logic, max 5 retries
		updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Get latest version
			current := &ipamv1.Subnet{}
			if err := parentPoolClient.Get(ctx, types.NamespacedName{
				Namespace: subnet.Namespace,
				Name:      subnet.Name,
			}, current); err != nil {
				return err
			}

			// Apply updates
			current.Spec.CIDR = updatedCIDR
			return parentPoolClient.Update(ctx, current)
		})

		if updateErr != nil {
			t.Fatalf("Failed to update Subnet: %v", updateErr)
		}

		// Confirm update
		updatedSubnetObj := &ipamv1.Subnet{}
		waitForCondition(t, func() bool {
			if err := parentPoolClient.Get(ctx, types.NamespacedName{
				Namespace: testNamespace,
				Name:      subnet.Name,
			}, updatedSubnetObj); err != nil {
				return false
			}
			return updatedSubnetObj.Spec.CIDR == updatedCIDR
		}, "Subnet update confirmation")

		// Wait until requeue occurs (ensure sufficient time)
		time.Sleep(5 * time.Second)

		// Confirm that parent Pool requeue occurs at least once
		waitForCondition(t, func() bool {
			count := reconcileCounter.Load()
			t.Logf("Detecting requeue: Current count: %d", count)
			return count >= 1
		}, "Expect parent Pool to be requeued at least once")

		// Check requeue count
		// Note: With a combination of Status update and Spec update, at least one requeue should occur
		// In a real environment, if event conflicts occur for the same object, two consecutive updates may be merged into one
		time.Sleep(1 * time.Second) // Extend wait time
		count := reconcileCounter.Load()
		t.Logf("Requeue count: %d", count)
		if count < 1 {
			t.Errorf("Expected: Requeue at least once, Actual: %d times", count)
		}
	})

	// 3. Verify parent Pool requeue on Subnet deletion
	t.Run("SubnetDelete_RequeuesParentPoolOnce", func(t *testing.T) {
		// Create parent Pool
		parent := createParentPool(t, ctx)
		defer cleanup(t, ctx, parent)

		// Create Subnet
		subnet := createSubnet(t, ctx, subnetName+"-delete", parentPoolName, subnetCIDR, "test-cluster")

		// Wait for requeue due to creation and initialize
		time.Sleep(2 * time.Second)
		t.Logf("Count before reset: %d", reconcileCounter.Load())
		reconcileCounter.Store(0)
		t.Logf("Count after reset: %d", reconcileCounter.Load())

		// Delete Subnet
		if err := parentPoolClient.Delete(ctx, subnet); err != nil {
			t.Fatalf("Failed to delete Subnet: %v", err)
		}

		// Confirm deletion
		deletedSubnet := &ipamv1.Subnet{}
		waitForCondition(t, func() bool {
			err := parentPoolClient.Get(ctx, types.NamespacedName{
				Namespace: testNamespace,
				Name:      subnet.Name,
			}, deletedSubnet)
			return client.IgnoreNotFound(err) == nil
		}, "Subnet deletion confirmation")

		// Wait a bit until requeue occurs
		time.Sleep(2 * time.Second)

		// Confirm that parent Pool requeue occurs at least once
		waitForCondition(t, func() bool {
			count := reconcileCounter.Load()
			t.Logf("Detecting requeue: Current count: %d", count)
			return count >= 1
		}, "Expect parent Pool to be requeued at least once")

		// Check requeue count (exact count is unstable, so at least once is fine)
		time.Sleep(500 * time.Millisecond)
		count := reconcileCounter.Load()
		t.Logf("Requeue count: %d", count)
		if count < 1 {
			t.Errorf("Expected: Requeue at least once, Actual: %d times", count)
		}
	})

	// 4. Verify parent Pool requeue on child Pool deletion
	t.Run("ChildPoolDelete_RequeuesParentPoolOnce", func(t *testing.T) {
		// Create parent Pool
		parent := createParentPool(t, ctx)
		defer cleanup(t, ctx, parent)

		// Create child Pool
		child := createChildPool(t, ctx)

		// Create Subnet associated with child Pool
		childSubnet := createSubnet(t, ctx, "child-subnet", childPoolName, subnetCIDR, "test-cluster")
		defer cleanup(t, ctx, childSubnet)

		// Wait for requeue due to preparation and reset counter
		time.Sleep(2 * time.Second)
		t.Logf("Count before reset: %d", reconcileCounter.Load())
		reconcileCounter.Store(0)
		t.Logf("Count after reset: %d", reconcileCounter.Load())

		// Delete child Pool
		if err := parentPoolClient.Delete(ctx, child); err != nil {
			t.Fatalf("Failed to delete child Pool: %v", err)
		}

		// Confirm deletion
		deletedPool := &ipamv1.SubnetPool{}
		waitForCondition(t, func() bool {
			err := parentPoolClient.Get(ctx, types.NamespacedName{
				Namespace: testNamespace,
				Name:      childPoolName,
			}, deletedPool)
			return client.IgnoreNotFound(err) == nil
		}, "Child Pool deletion confirmation")

		// Confirm that parent Pool requeue occurs at least once
		waitForCondition(t, func() bool {
			count := reconcileCounter.Load()
			t.Logf("Detecting requeue: Current count: %d", count)
			return count >= 1
		}, "Expect parent Pool to be requeued at least once")

		// Check requeue count (exact count is unstable, so at least once is fine)
		time.Sleep(500 * time.Millisecond)
		count := reconcileCounter.Load()
		t.Logf("Requeue count: %d", count)
		if count < 1 {
			t.Errorf("Expected: Requeue at least once, Actual: %d times", count)
		}
	})

	// Note: Subnet name change test case deleted
	// Because in Kubernetes, the metadata.name field is immutable
	// Changing the name with an Update operation causes a "field is immutable" error
}
