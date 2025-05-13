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
	"sigs.k8s.io/controller-runtime/pkg/client"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

// TestWorkQueue_DuplicateRequestSuppression ensures that the typed workqueue
// does not enqueue the same NamespacedName more than once.
func TestWorkQueue_DuplicateRequestSuppression(t *testing.T) {
	rl := workqueue.NewTypedMaxOfRateLimiter(
		workqueue.NewTypedItemExponentialFailureRateLimiter[types.NamespacedName](5*time.Millisecond, 1000*time.Second),
		&workqueue.TypedBucketRateLimiter[types.NamespacedName]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
	)
	queue := workqueue.NewTypedRateLimitingQueue[types.NamespacedName](rl)
	defer queue.ShutDown()

	mapperFunc := func(ctx context.Context, obj client.Object) []types.NamespacedName {
		subnet, ok := obj.(*ipamv1.Subnet)
		if !ok {
			return nil
		}
		return []types.NamespacedName{{Namespace: subnet.GetNamespace(), Name: subnet.Spec.PoolRef}}
	}

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

	// First enqueue
	for _, nn := range mapperFunc(context.Background(), subnet) {
		queue.Add(nn)
	}
	if l := queue.Len(); l != 1 {
		t.Fatalf("queue length = %d, want 1", l)
	}

	// Second (duplicate) enqueue
	for _, nn := range mapperFunc(context.Background(), subnet) {
		queue.Add(nn)
	}
	if l := queue.Len(); l != 1 {
		t.Fatalf("after duplicate add, queue length = %d, want 1", l)
	}

	// Pop and verify content
	item, _ := queue.Get()
	nn := item // already types.NamespacedName
	if nn.Name != "parent-pool" || nn.Namespace != "test-ns" {
		t.Errorf("unexpected item from queue: %+v", nn)
	}

	if queue.Len() != 0 {
		t.Error("queue should be empty after Get()")
	}
}

// TestWorkQueue_DuplicateRequestSuppressionRateLimited checks duplicate suppression when using AddRateLimited.
func TestWorkQueue_DuplicateRequestSuppressionRateLimited(t *testing.T) {
	rl := workqueue.NewTypedMaxOfRateLimiter(
		workqueue.NewTypedItemExponentialFailureRateLimiter[types.NamespacedName](5*time.Millisecond, 1000*time.Second),
		&workqueue.TypedBucketRateLimiter[types.NamespacedName]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
	)
	queue := workqueue.NewTypedRateLimitingQueue[types.NamespacedName](rl)
	defer queue.ShutDown()

	key := types.NamespacedName{Name: "parent-pool", Namespace: "test-ns"}

	// The first call schedules the item with a delay determined by the rate-limiter. The
	// item will not be visible via Len() until that delay expires. Wait slightly longer
	// than the base delay (5 ms) so that the item migrates into the ready queue before
	// checking the length.
	queue.AddRateLimited(key)
	time.Sleep(10 * time.Millisecond)
	if l := queue.Len(); l != 1 {
		t.Fatalf("queue length after initial AddRateLimited = %d, want 1", l)
	}

	// Add the same key again. Because it is already either queued or waiting in the
	// delaying queue, the duplicate should be suppressed and the total length should
	// remain 1.
	queue.AddRateLimited(key)
	time.Sleep(10 * time.Millisecond)
	if l := queue.Len(); l != 1 {
		t.Fatalf("after duplicate AddRateLimited, queue length = %d, want 1", l)
	}
}

// TestWorkQueue_ForgetAllowsReenqueue verifies that calling Forget on an item allows it to be enqueued again.
func TestWorkQueue_ForgetAllowsReenqueue(t *testing.T) {
	rl := workqueue.NewTypedMaxOfRateLimiter(
		workqueue.NewTypedItemExponentialFailureRateLimiter[types.NamespacedName](5*time.Millisecond, 1000*time.Second),
		&workqueue.TypedBucketRateLimiter[types.NamespacedName]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
	)
	queue := workqueue.NewTypedRateLimitingQueue[types.NamespacedName](rl)
	defer queue.ShutDown()

	key := types.NamespacedName{Name: "parent-pool", Namespace: "test-ns"}

	queue.Add(key)
	if l := queue.Len(); l != 1 {
		t.Fatalf("queue length = %d, want 1", l)
	}

	item, _ := queue.Get()
	queue.Forget(item)
	queue.Done(item)

	// After Forget(), we should be able to enqueue again.
	queue.Add(key)
	if l := queue.Len(); l != 1 {
		t.Fatalf("after Forget and re-add, queue length = %d, want 1", l)
	}
}
