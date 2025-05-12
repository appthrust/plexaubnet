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
	"os"
	"sync"
	"testing"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// mockVecRecorder simulates the legacy implementation using GaugeVec+WithLabelValues.
// This is a memory-intensive approach.
func mockVecRecorder(pool *ipamv1.SubnetPool, sizes []string, values []int) {
	// Create a new Vec each time (normally a shared instance).
	gaugeVec := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mock_subnet_pool_free_blocks",
			Help: "For testing allocations in the old pattern",
		},
		[]string{"pool", "size"},
	)

	// Set metrics for each size using WithLabelValues.
	for i, size := range sizes {
		gaugeVec.WithLabelValues(pool.Name, size).Set(float64(values[i]))
	}
}

// staticRecorder simulates the optimized static gauge implementation.
// This is a memory-efficient approach.
func staticRecorder(pool *ipamv1.SubnetPool, sizes []string, values []int) {
	// Simulate pre-registration processing.
	RegisterPoolGauges(pool.Name)

	// Set metrics for each size.
	for i, size := range sizes {
		g := ensurePoolGauge(pool.Name, size)
		g.free.Set(float64(values[i]))
	}
}

// BenchmarkPrometheusMetricsImplementations compares the memory allocation of new and old implementations.
func BenchmarkPrometheusMetricsImplementations(b *testing.B) {
	// SubnetPool for testing.
	pool := &ipamv1.SubnetPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "benchmark-pool",
			Namespace: "default",
		},
	}

	// Sizes and their respective values.
	sizes := []string{"16", "20", "24", "28", "29"}
	values := []int{10, 20, 30, 40, 50}

	// Initialization - test configuration.
	os.Setenv("ENABLE_LEGACY_VEC", "true")

	// Manually simulate start registration processing (mimicking initialization in a real production environment).
	RegisterPoolGauges(pool.Name)

	// 1. Legacy GaugeVec pattern.
	b.Run("LegacyVec", func(b *testing.B) {
		b.ReportAllocs() // Measure allocations.
		for i := 0; i < b.N; i++ {
			mockVecRecorder(pool, sizes, values)
		}
	})

	// 2. Static gauge pattern.
	b.Run("StaticGauge", func(b *testing.B) {
		b.ReportAllocs() // Measure allocations.
		for i := 0; i < b.N; i++ {
			staticRecorder(pool, sizes, values)
		}
	})

	// Cleanup.
	UnregisterPoolGauges(pool.Name)
}

// TestMetricsEfficiencyInProduction directly compares allocation counts without using test tools.
func TestMetricsEfficiencyInProduction(t *testing.T) {
	// Directly verify memory efficiency in a real production environment.
	if testing.Short() {
		t.Skip("Skipping in short mode")
	}

	// Pool for testing.
	pool := &ipamv1.SubnetPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "efficiency-test-pool-" + metav1.Now().Format("150405"),
			Namespace: "default",
		},
	}

	// Measure allocation count for the legacy pattern.
	os.Setenv("ENABLE_LEGACY_VEC", "true")
	vecAllocations := testing.AllocsPerRun(1000, func() {
		recordPoolMetrics(pool, "/24", 100)
	})

	// Measure allocation count for the optimized pattern.
	os.Setenv("ENABLE_LEGACY_VEC", "")
	RegisterPoolGauges(pool.Name) // Pre-register.
	staticAllocations := testing.AllocsPerRun(1000, func() {
		recordPoolMetrics(pool, "/24", 100)
	})

	// Calculate reduction rate.
	reduction := 1.0 - (staticAllocations / vecAllocations)

	// Output.
	t.Logf("Allocations comparison:")
	t.Logf("  Legacy Vec: %.1f allocs/op", vecAllocations)
	t.Logf("  Static:     %.1f allocs/op", staticAllocations)
	t.Logf("  Reduction:  %.1f%% (%.1f ➔ %.1f)", reduction*100, vecAllocations, staticAllocations)

	// Validation - should always be at least 30% reduction (or more depending on implementation).
	if reduction < 0.30 {
		t.Errorf("Expected at least 30%% allocation reduction, got %.1f%%", reduction*100)
	}

	// Cleanup.
	UnregisterPoolGauges(pool.Name)
}

// TestPoolGaugeMemoryOptimization tests that lazy registration and common size optimization are working.
func TestPoolGaugeMemoryOptimization(t *testing.T) {
	// Cleanup before test.
	globalGaugeRegistry = newStaticGaugeRegistry()
	registry := prometheus.NewRegistry()
	metrics.Registry = registry

	// Get gauge for common size (created internally by lazy registration).
	commonSizePool := "common-pool-" + metav1.Now().Format("150405.000")
	commonGauge := ensurePoolGauge(commonSizePool, "24") // "24" is included in commonSizes.

	// Confirm gauge is created.
	require.NotNil(t, commonGauge, "ensurePoolGauge should create gauge for common size")
	require.NotNil(t, commonGauge.free, "gauge.free should not be nil")

	// Get gauge for uncommon size.
	uncommonPool := "uncommon-pool-" + metav1.Now().Format("150405.000")
	uncommonGauge := ensurePoolGauge(uncommonPool, "17") // "17" is not included in commonSizes.

	// Confirm nilSafeGauge is returned for uncommon size.
	require.NotNil(t, uncommonGauge, "ensurePoolGauge should never return nil even for uncommon size")
	require.NotNil(t, uncommonGauge.free, "gauge.free should never be nil")
	assert.Same(t, nilSafeGauge, uncommonGauge, "Should return the nilSafeGauge for uncommon size")

	// Confirm setting values does not panic.
	assert.NotPanics(t, func() {
		commonGauge.free.Set(42.0)
		uncommonGauge.free.Set(24.0)
	}, "Setting values should not panic")
}

// TestConcurrentRegisterPoolGauges tests concurrency safety when RegisterPoolGauges
// is called simultaneously by multiple goroutines.
func TestConcurrentRegisterPoolGauges(t *testing.T) {
	// Cleanup before test.
	globalGaugeRegistry = newStaticGaugeRegistry()
	registry := prometheus.NewRegistry()
	metrics.Registry = registry

	// Concurrent registration with 100 workers.
	const numWorkers = 100
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	// Use the same pool name (to cause conflicts).
	poolName := "concurrency-test-pool"

	// Capture errors.
	errs := make(chan error, numWorkers)

	// Concurrent execution.
	for i := 0; i < numWorkers; i++ {
		go func(id int) {
			defer wg.Done()
			// Intentionally register the same pool multiple times.
			RegisterPoolGauges(poolName)
		}(i)
	}

	// Wait for all workers to complete.
	wg.Wait()
	close(errs)

	// Fail if any errors occurred.
	for err := range errs {
		require.NoError(t, err, "Concurrent RegisterPoolGauges should not error")
	}

	// Confirm successful registration.
	gauge := globalGaugeRegistry.getPoolGauge(poolName, "24")
	require.NotNil(t, gauge, "Gauge should exist after concurrent registration")

	// Cleanup (reverse order).
	UnregisterPoolGauges(poolName)

	// Confirm unregistration.
	gauge = globalGaugeRegistry.getPoolGauge(poolName, "24")
	assert.Nil(t, gauge, "Gauge should be unregistered")
}

// TestConcurrentUnregisterPoolGauges tests concurrency safety when UnregisterPoolGauges
// is called simultaneously by multiple goroutines.
func TestConcurrentUnregisterPoolGauges(t *testing.T) {
	// Cleanup before test.
	globalGaugeRegistry = newStaticGaugeRegistry()
	registry := prometheus.NewRegistry()
	metrics.Registry = registry

	// Prepare multiple pools.
	const numPools = 10
	poolNames := make([]string, numPools)
	for i := 0; i < numPools; i++ {
		poolNames[i] = "concurrent-unreg-pool-" + metav1.Now().Format("150405") + "-" + string(rune('A'+i))
		RegisterPoolGauges(poolNames[i])
	}

	// Execute multiple Unregister operations simultaneously for each pool.
	const numWorkers = 5 // 5 workers per pool.
	var wg sync.WaitGroup
	wg.Add(numPools * numWorkers)

	for _, poolName := range poolNames {
		poolName := poolName // Local copy.
		for w := 0; w < numWorkers; w++ {
			go func() {
				defer wg.Done()
				// Unregister the same pool multiple times.
				UnregisterPoolGauges(poolName)
			}()
		}
	}

	// Wait for all workers to complete.
	wg.Wait()

	// Confirm all pools were correctly unregistered.
	for _, poolName := range poolNames {
		gauge := globalGaugeRegistry.getPoolGauge(poolName, "24")
		assert.Nil(t, gauge, "Gauge should be unregistered for pool %s", poolName)
	}
}
