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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

func TestPoolGaugeEventHandler_Create(t *testing.T) {
	// Cleanup before test
	globalGaugeRegistry = newStaticGaugeRegistry()
	// Reset Prometheus registry for testing
	registry := prometheus.NewRegistry()
	metrics.Registry = registry

	// Pool for testing
	pool := &ipamv1.SubnetPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-create-pool",
			Namespace: "default",
		},
	}

	// Create event handler and Create event
	handler := &PoolGaugeEventHandler{}
	createEvent := event.CreateEvent{
		Object: pool,
	}

	// Event processing (Create event should return false as it does not continue)
	result := handler.Create(createEvent)
	assert.False(t, result, "Create event should return false")

	// Check if gauge for the target pool was registered
	gauge := globalGaugeRegistry.getPoolGauge(pool.Name, "24")
	require.NotNil(t, gauge, "Static gauge should be registered for pool")

	// Check if the registered gauge actually exists in the Prometheus registry
	// Set a value on the gauge and confirm that the value can be retrieved
	gauge.free.Set(42.0)
	gaugeValue := testutil.ToFloat64(gauge.free)
	assert.Equal(t, 42.0, gaugeValue, "Gauge should be registered and hold value")
}

func TestPoolGaugeEventHandler_Delete(t *testing.T) {
	// Cleanup before test
	globalGaugeRegistry = newStaticGaugeRegistry()
	// Reset Prometheus registry for testing
	registry := prometheus.NewRegistry()
	metrics.Registry = registry

	// Pool for testing
	pool := &ipamv1.SubnetPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-delete-pool",
			Namespace: "default",
		},
	}

	// Register gauge beforehand
	RegisterPoolGauges(pool.Name)

	// Confirm registration
	gauge := globalGaugeRegistry.getPoolGauge(pool.Name, "24")
	require.NotNil(t, gauge, "Static gauge should be registered before delete")

	// Create event handler and Delete event
	handler := &PoolGaugeEventHandler{}
	deleteEvent := event.DeleteEvent{
		Object: pool,
	}

	// Event processing
	result := handler.Delete(deleteEvent)
	assert.False(t, result, "Delete event should return false")

	// Check if gauge for the target pool was unregistered
	gauge = globalGaugeRegistry.getPoolGauge(pool.Name, "24")
	assert.Nil(t, gauge, "Static gauge should be unregistered after delete")
}

func TestPoolGaugeEventHandler_UpdateAndGeneric(t *testing.T) {
	handler := &PoolGaugeEventHandler{}

	pool := &ipamv1.SubnetPool{
		ObjectMeta: metav1.ObjectMeta{Name: "test-other-pool"},
	}

	// Update events are ignored
	updateEvent := event.UpdateEvent{
		ObjectOld: pool,
		ObjectNew: pool,
	}
	assert.False(t, handler.Update(updateEvent), "Update event should be ignored")

	// Generic events are ignored
	genericEvent := event.GenericEvent{
		Object: pool,
	}
	assert.False(t, handler.Generic(genericEvent), "Generic event should be ignored")
}

func TestRegisterAllPoolGauges(t *testing.T) {
	// Cleanup before test
	globalGaugeRegistry = newStaticGaugeRegistry()
	// Reset Prometheus registry for testing
	registry := prometheus.NewRegistry()
	metrics.Registry = registry

	// Pools for testing
	pools := []ipamv1.SubnetPool{
		{ObjectMeta: metav1.ObjectMeta{Name: "pool-1", Namespace: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pool-2", Namespace: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pool-3", Namespace: "kube-system"}},
	}

	// Create Fake Client and set initial data
	scheme := testScheme()
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for i := range pools {
		builder = builder.WithObjects(&pools[i])
	}
	fakeClient := builder.Build()

	// Register gauges for all pools
	err := RegisterAllPoolGauges(context.Background(), fakeClient)
	require.NoError(t, err, "RegisterAllPoolGauges should not return error")

	// Check if gauges were registered for all pools
	for i := range pools {
		gauge := globalGaugeRegistry.getPoolGauge(pools[i].Name, "24")
		require.NotNil(t, gauge, "Static gauge should be registered for pool %s", pools[i].Name)
	}
}

func TestNewPoolGaugePredicate(t *testing.T) {
	// Check if Predicate satisfies the interface
	pred := NewPoolGaugePredicate()
	require.NotNil(t, pred, "NewPoolGaugePredicate should return non-nil predicate")
}

// Helper function to create a scheme for testing
func testScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = ipamv1.AddToScheme(scheme)
	return scheme
}

func TestPoolGaugeEventHandler_NonPoolObject(t *testing.T) {
	handler := &PoolGaugeEventHandler{}

	// Object other than SubnetPool (that implements RuntimeObject)
	nonPool := &ipamv1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: "not-a-pool"},
	}

	// Create event returns false and does not continue
	createEvent := event.CreateEvent{Object: nonPool}
	assert.False(t, handler.Create(createEvent), "Create with non-pool should return false")

	// Delete event also returns false and does not continue
	deleteEvent := event.DeleteEvent{Object: nonPool}
	assert.False(t, handler.Delete(deleteEvent), "Delete with non-pool should return false")
}
