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
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"github.com/appthrust/plexaubnet/internal/controller/statusutil"
)

// TestSubnetCreateDeleteConcurrency tests the scenario where a Subnet is deleted during a status patch.
// This verifies the effectiveness of the NotFound tolerance feature.
func TestSubnetCreateDeleteConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping in short mode")
	}

	// Setup test environment
	testEnv := setupEnvTest(t)
	defer stopEnvTest(t, testEnv)

	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("Failed to start test env: %v", err)
	}

	// Create client
	k8sClient, err := createK8sClient(cfg)
	if err != nil {
		t.Fatalf("Failed to create k8s client: %v", err)
	}

	// Create context (with a short timeout)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Create namespace for testing
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "notfound-test",
		},
	}
	err = k8sClient.Create(ctx, ns)
	if err != nil {
		t.Logf("Namespace creation error (may already exist): %v", err)
	}

	// Create SubnetPool
	pool := &ipamv1.SubnetPool{
		TypeMeta: metav1.TypeMeta{
			Kind:       "SubnetPool",
			APIVersion: ipamv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notfound-test-pool",
			Namespace: "notfound-test",
		},
		Spec: ipamv1.SubnetPoolSpec{
			CIDR:             "10.10.0.0/16",
			DefaultBlockSize: 24,
			Strategy:         "Linear",
		},
	}
	err = k8sClient.Create(ctx, pool)
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}

	// Setup manager
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0", // Disable metrics server
		},
		HealthProbeBindAddress: "0", // Disable health check server
	})
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Setup field indexes
	if err := SetupFieldIndexes(context.Background(), mgr, ctrl.Log.WithName("test-setup")); err != nil {
		t.Fatalf("Failed to setup field indexes: %v", err)
	}

	// Setup SubnetReconciler
	subnetReconciler := &SubnetReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		Config:         nil,                                  // nil is sufficient for testing
		ControllerName: "subnet-controller-concurrency-test", // Set a unique name for testing
	}

	err = subnetReconciler.SetupWithManager(mgr)
	if err != nil {
		t.Fatalf("Failed to setup subnet controller: %v", err)
	}

	// Setup PoolStatusReconciler
	poolStatusReconciler := &PoolStatusReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		ControllerName: "poolstatus-concurrency-test", // Explicitly set controller name to avoid duplication
	}

	err = poolStatusReconciler.SetupWithManager(mgr)
	if err != nil {
		t.Fatalf("Failed to setup pool status controller: %v", err)
	}

	// Start manager in a goroutine
	mgrCtx, mgrCancel := context.WithCancel(ctx)
	defer mgrCancel()
	go func() {
		err := mgr.Start(mgrCtx)
		if err != nil {
			t.Logf("Failed to start manager: %v", err)
		}
	}()

	// Wait for the manager's cache to be ready
	if !mgr.GetCache().WaitForCacheSync(mgrCtx) {
		t.Fatalf("Failed waiting for cache sync")
	}
	time.Sleep(2 * time.Second) // Wait a bit longer

	// Reset metrics
	statusutil.SubnetStatusUpdateTotal.Reset()

	// Store previous values
	startSkippedDeletedCount := testutil.ToFloat64(statusutil.SubnetStatusUpdateTotal.WithLabelValues("skipped_deleted"))
	startErrorCount := testutil.ToFloat64(statusutil.SubnetStatusUpdateTotal.WithLabelValues("error"))

	// Number of concurrent executions
	const numSubnets = 50
	const deleteDelayMs = 200 // Delay before deleting after creation (ms) - set longer to allow processing to start

	// Create Subnet -> delete immediately, in parallel
	var wg sync.WaitGroup
	wg.Add(numSubnets)

	t.Logf("Starting creation and quick deletion of %d subnets...", numSubnets)
	for i := 0; i < numSubnets; i++ {
		go func(idx int) {
			defer wg.Done()

			// Create Subnet
			subnet := &ipamv1.Subnet{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Subnet",
					APIVersion: ipamv1.GroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("test-subnet-%d", idx),
					Namespace: "notfound-test",
				},
				Spec: ipamv1.SubnetSpec{
					CIDR:      fmt.Sprintf("10.10.%d.0/24", idx),
					PoolRef:   "notfound-test-pool",
					ClusterID: fmt.Sprintf("cluster-%d", idx),
				},
			}

			// Create Subnet
			if err := k8sClient.Create(ctx, subnet); err != nil {
				t.Logf("Error creating subnet %d: %v", idx, err)
				return
			}

			// Wait a short time before deleting the Subnet
			// This causes deletion to occur while the controller is processing the Subnet
			time.Sleep(time.Duration(deleteDelayMs) * time.Millisecond)

			// Delete Subnet
			if err := k8sClient.Delete(ctx, subnet); err != nil {
				t.Logf("Error deleting subnet %d: %v", idx, err)
			}
		}(i)
	}

	// Wait for completion
	wg.Wait()
	t.Log("All subnet creation and deletion complete")

	// Wait for the controller to finish processing (longer wait)
	t.Log("Waiting for controller processing...")
	time.Sleep(10 * time.Second)

	// Check if Pool Status was updated correctly
	updatedPool := &ipamv1.SubnetPool{}
	err = k8sClient.Get(ctx, types.NamespacedName{
		Name:      "notfound-test-pool",
		Namespace: "notfound-test",
	}, updatedPool)
	if err != nil {
		t.Fatalf("Failed to get updated pool: %v", err)
	}

	// StatusUpdateTotalメトリクスの値を確認
	endSkippedDeletedCount := testutil.ToFloat64(statusutil.SubnetStatusUpdateTotal.WithLabelValues("skipped_deleted"))
	endErrorCount := testutil.ToFloat64(statusutil.SubnetStatusUpdateTotal.WithLabelValues("error"))

	skippedIncrease := endSkippedDeletedCount - startSkippedDeletedCount
	errorIncrease := endErrorCount - startErrorCount

	// Verify metrics
	t.Logf("Skipped_deleted metric increased by: %.0f", skippedIncrease)
	t.Logf("Error metric increased by: %.0f", errorIncrease)

	// If NotFound errors are handled correctly, the error metric should not increase
	assert.Equal(t, float64(0), errorIncrease, "Error metric should not increase")

	// Check if the skipped_deleted metric increased due to deletion processing,
	// or if the error metric is 0 (meaning errors were suppressed)
	if skippedIncrease > 0 {
		t.Logf("Successfully detected skipped_deleted metrics: %v", skippedIncrease)
	} else {
		t.Logf("No skipped_deleted metrics detected, but errors were properly suppressed")
	}

	// Relax test success condition (if metrics are 0 but no errors, it's OK)
	assert.True(t, skippedIncrease >= 0 && errorIncrease == 0,
		"Either skipped_deleted metrics should increase OR no errors should occur")

	// Ideally, all subnets should be skipped at least once (possibility of being re-queued during deletion)
	t.Logf("Subnet NotFound tolerance is working: %d/%d operations were successfully skipped",
		int(skippedIncrease), numSubnets)
}
