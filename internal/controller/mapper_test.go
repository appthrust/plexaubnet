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
	"testing"
	"time"

	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

// TestMapSubnetToParentPoolFullSuite is a comprehensive test for the parent pool mapper functionality
func TestMapSubnetToParentPoolFullSuite(t *testing.T) {
	// Define a test mapper function to execute the mapper function in this test
	// Mirrors the mapSubnetToParentPool function in the production code
	mapSubnetToParentPool := func(ctx context.Context, obj client.Object) []ctrl.Request {
		subnet, ok := obj.(*ipamv1.Subnet)
		if !ok {
			// Note: This implementation cannot directly test Tombstone cases, so
			// it is necessary to add handling for cast failures in the actual controller code
			return nil
		}

		if subnet.Spec.PoolRef == "" {
			return nil // Ignore if PoolRef is missing
		}

		// Requeue parent pool
		return enqueueParentPool(subnet.GetNamespace(), subnet.Spec.PoolRef)
	}

	tests := []struct {
		name       string
		object     client.Object
		wantLen    int    // Expected length of the result
		wantParent string // Expected parent pool name (if any)
		wantNs     string // Expected namespace (if any)
	}{
		{
			name: "Subnet with PoolRef",
			object: &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-subnet",
					Namespace: "test-ns",
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef: "parent-pool",
					CIDR:    "10.0.0.0/24",
				},
			},
			wantLen:    1,
			wantParent: "parent-pool",
			wantNs:     "test-ns",
		},
		{
			name: "Subnet without PoolRef",
			object: &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-subnet-no-pool",
					Namespace: "test-ns",
				},
				Spec: ipamv1.SubnetSpec{
					CIDR: "10.0.0.0/24",
					// PoolRef not specified
				},
			},
			wantLen: 0,
		},
		// Note: In the actual code, DeletedFinalStateUnknown processing is necessary, but
		// since it cannot be passed as client.Object, it is not tested directly in unit tests but confirmed in integration tests
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Execute mapper function
			got := mapSubnetToParentPool(context.Background(), tt.object)

			// Validate length of the result
			if len(got) != tt.wantLen {
				t.Errorf("Length of result is %d, expected %d", len(got), tt.wantLen)
				return
			}

			// If there is a result, validate its content
			if tt.wantLen > 0 {
				// Validate parent pool name
				if got[0].Name != tt.wantParent {
					t.Errorf("Parent pool name is %q, expected %q", got[0].Name, tt.wantParent)
				}

				// Validate namespace
				if got[0].Namespace != tt.wantNs {
					t.Errorf("Namespace is %q, expected %q", got[0].Namespace, tt.wantNs)
				}

				// Validate that namespace is not empty
				if got[0].Namespace == "" {
					t.Error("Namespace is empty - violates controller-development.md Rule#1")
				}
			}
		})
	}
}

// TestDuplicateRequestSuppression tests that duplicate requests are suppressed in the workqueue
func TestDuplicateRequestSuppression(t *testing.T) {
	// Test queue (using Typed API)
	rl := workqueue.NewTypedMaxOfRateLimiter(
		workqueue.NewTypedItemExponentialFailureRateLimiter[types.NamespacedName](5*time.Millisecond, 1000*time.Second),
		&workqueue.TypedBucketRateLimiter[types.NamespacedName]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
	)
	queue := workqueue.NewTypedRateLimitingQueue[types.NamespacedName](rl)
	defer queue.ShutDown()

	// Test mapper function
	mapperFunc := func(ctx context.Context, obj client.Object) []ctrl.Request {
		subnet, ok := obj.(*ipamv1.Subnet)
		if !ok {
			return nil
		}

		return []ctrl.Request{
			{
				NamespacedName: types.NamespacedName{
					Namespace: subnet.GetNamespace(),
					Name:      subnet.Spec.PoolRef,
				},
			},
		}
	}

	// Test Subnet
	subnet := &ipamv1.Subnet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-subnet",
			Namespace: "test-ns",
		},
		Spec: ipamv1.SubnetSpec{
			PoolRef: "parent-pool",
			CIDR:    "10.0.0.0/24",
		},
	}

	// Execute mapper twice with the same object
	requests := mapperFunc(context.Background(), subnet)
	for _, req := range requests {
		// Add only NamespacedName to the queue
		queue.Add(req.NamespacedName)
	}

	// First time: Should be added to the queue
	if got, want := queue.Len(), 1; got != want {
		t.Errorf("Queue length is %d, expected %d", got, want)
	}

	// Add the same request again
	for _, req := range requests {
		queue.Add(req.NamespacedName)
	}

	// Second time: Queue length should not change (due to duplicate suppression)
	if got, want := queue.Len(), 1; got != want {
		t.Errorf("Queue length after duplicate addition is %d, expected %d (duplicates should be suppressed)", got, want)
	}

	// Get from queue and check content
	item, _ := queue.Get()
	// Can be retrieved directly as NamespacedName from Typed queue
	nsName := item // types.NamespacedName 型

	// Check if the correct request is registered
	if got, want := nsName.Name, "parent-pool"; got != want {
		t.Errorf("Name from queue is %q, expected %q", got, want)
	}
	if got, want := nsName.Namespace, "test-ns"; got != want {
		t.Errorf("Namespace from queue is %q, expected %q", got, want)
	}

	// Queue should be empty
	if queue.Len() != 0 {
		t.Error("Queue is not empty")
	}
}

// TestPoolStatusMapAllocToPool tests the PoolStatusReconciler.mapAllocToPool function
func TestPoolStatusMapAllocToPool(t *testing.T) {
	// Create a test PoolStatusReconciler instance
	reconciler := &PoolStatusReconciler{}

	tests := []struct {
		name       string
		object     client.Object
		wantLen    int    // Expected length of the result
		wantParent string // Expected parent pool name (if any)
		wantNs     string // Expected namespace (if any)
	}{
		{
			name: "Subnet with PoolRef",
			object: &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-subnet",
					Namespace: "test-ns",
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef: "parent-pool",
					CIDR:    "10.0.0.0/24",
				},
				Status: ipamv1.SubnetStatus{
					Phase: ipamv1.SubnetPhaseAllocated,
				},
			},
			wantLen:    1,
			wantParent: "parent-pool",
			wantNs:     "test-ns",
		},
		{
			name: "Subnet without PoolRef",
			object: &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-subnet-no-pool",
					Namespace: "test-ns",
				},
				Spec: ipamv1.SubnetSpec{
					CIDR: "10.0.0.0/24",
					// PoolRef not specified
				},
				Status: ipamv1.SubnetStatus{
					Phase: ipamv1.SubnetPhaseFailed,
				},
			},
			wantLen: 0,
		},
		{
			name:    "Object other than Subnet",
			object:  &ipamv1.SubnetPool{}, // Case where type assertion fails
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Execute mapAllocToPool function
			got := reconciler.mapAllocToPool(context.Background(), tt.object)

			// Validate length of the result
			if len(got) != tt.wantLen {
				t.Errorf("Length of mapAllocToPool() result is %d, expected %d", len(got), tt.wantLen)
				return
			}

			// If there is a result, validate its content
			if tt.wantLen > 0 {
				// Validate parent pool name
				if got[0].Name != tt.wantParent {
					t.Errorf("Parent pool name is %q, expected %q", got[0].Name, tt.wantParent)
				}

				// Validate namespace
				if got[0].Namespace != tt.wantNs {
					t.Errorf("Namespace is %q, expected %q", got[0].Namespace, tt.wantNs)
				}

				// Rule#1: Validate that namespace is not empty
				if got[0].Namespace == "" {
					t.Error("Namespace is empty - violates controller-development.md Rule#1")
				}
			}
		})
	}
}
