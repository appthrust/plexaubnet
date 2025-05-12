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

package statuscalc

import (
	"testing"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestEstimateFreeCountCap tests the capacity estimation function for FreeCountBySize
func TestEstimateFreeCountCap(t *testing.T) {
	testCases := []struct {
		name       string
		blockSize  int
		childCIDRs []string
		expected   int
	}{
		{
			name:       "Empty child pool",
			blockSize:  24,
			childCIDRs: []string{},
			expected:   2, // Basic 2 entries ("/24" and "24")
		},
		{
			name:       "Small number of child pools",
			blockSize:  24,
			childCIDRs: []string{"10.0.1.0/24", "10.0.2.0/24"},
			expected:   defaultFreeCountKeyCap, // Default value if small
		},
		{
			name:       "Large number of child pools",
			blockSize:  24,
			childCIDRs: make([]string, 100), // 100 dummy CIDRs
			expected:   12,                  // 2 + 100/10 = 12
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := estimateFreeCountCap(tc.blockSize, tc.childCIDRs)
			assert.Equal(t, tc.expected, result, "Estimated capacity does not match expected value")
		})
	}
}

func TestCalculate(t *testing.T) {
	// Test case 1: Empty allocations
	t.Run("EmptyAllocations", func(t *testing.T) {
		poolCIDR := "10.0.0.0/16"
		allocations := []*ipamv1.Subnet{}

		result, err := Calculate(poolCIDR, allocations, []string{})
		assert.NoError(t, err)
		assert.Equal(t, 0, result.AllocatedCount)
		assert.Equal(t, 256, result.FreeCountBySize["/24"])
		assert.Empty(t, result.AllocatedCIDRs)
	})

	// Test case 2: Some allocations
	t.Run("SomeAllocations", func(t *testing.T) {
		poolCIDR := "10.0.0.0/16"
		allocations := []*ipamv1.Subnet{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "alloc1",
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef:   "test-pool",
					CIDR:      "10.0.1.0/24",
					ClusterID: "cluster-1",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "alloc2",
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef:   "test-pool",
					CIDR:      "10.0.2.0/24",
					ClusterID: "cluster-2",
				},
			},
		}

		result, err := Calculate(poolCIDR, allocations, []string{})
		assert.NoError(t, err)
		assert.Equal(t, 2, result.AllocatedCount)
		assert.Equal(t, 254, result.FreeCountBySize["/24"])
		assert.Equal(t, "cluster-1", result.AllocatedCIDRs["10.0.1.0/24"])
		assert.Equal(t, "cluster-2", result.AllocatedCIDRs["10.0.2.0/24"])
	})

	// Test case 3: Invalid pool CIDR
	t.Run("InvalidPoolCIDR", func(t *testing.T) {
		poolCIDR := "invalid-cidr"
		allocations := []*ipamv1.Subnet{}

		result, err := Calculate(poolCIDR, allocations, []string{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid pool CIDR format")
		// Even with error, we should get a valid result object
		assert.Equal(t, 0, result.AllocatedCount)
	})

	// Test case 4: Full pool (more allocations than theoretical max)
	t.Run("FullPool", func(t *testing.T) {
		poolCIDR := "10.0.0.0/24"
		// Create more allocations than theoretical max for a /24 pool
		allocations := make([]*ipamv1.Subnet, 300)
		for i := 0; i < 300; i++ {
			allocations[i] = &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "alloc" + string(rune(i+48)),
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef:   "test-pool",
					CIDR:      "10.0.0." + string(rune(i+48)) + "/32",
					ClusterID: "cluster-" + string(rune(i+48)),
				},
			}
		}

		result, err := Calculate(poolCIDR, allocations, []string{})
		assert.NoError(t, err)
		assert.Equal(t, 300, result.AllocatedCount)
		// Free count should be 0 when allocations exceed theoretical max
		assert.Equal(t, 0, result.FreeCountBySize["/24"])
		assert.Len(t, result.AllocatedCIDRs, 300)
	})
	// Test case 5: With child SubnetPools
	t.Run("WithChildPools", func(t *testing.T) {
		poolCIDR := "10.0.0.0/16"
		allocations := []*ipamv1.Subnet{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "alloc1",
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef:   "test-pool",
					CIDR:      "10.0.1.0/24",
					ClusterID: "cluster-1",
				},
			},
		}

		// Child SubnetPool that uses part of the parent pool
		childCIDRs := []string{
			"10.0.2.0/24",
			"10.0.3.0/24",
		}

		result, err := Calculate(poolCIDR, allocations, childCIDRs)
		assert.NoError(t, err)
		// 1 Subnet + 2 child SubnetPools = 3 in total
		assert.Equal(t, 3, result.AllocatedCount)
		// Since 3 blocks are used in total, 256-3=253 remain
		assert.Equal(t, 253, result.FreeCountBySize["/24"])
		// Both should be included in the allocation map
		assert.Equal(t, "cluster-1", result.AllocatedCIDRs["10.0.1.0/24"])
		assert.Equal(t, "child-pool", result.AllocatedCIDRs["10.0.2.0/24"])
		assert.Equal(t, "child-pool", result.AllocatedCIDRs["10.0.3.0/24"])
	})
}

func TestNormalizeBlockSizeKey(t *testing.T) {
	testCases := []struct {
		name        string
		input       string
		expected    string
		expectError bool
	}{
		{
			name:        "Already normalized",
			input:       "24",
			expected:    "24",
			expectError: false,
		},
		{
			name:        "With leading slash",
			input:       "/24",
			expected:    "24",
			expectError: false,
		},
		{
			name:        "Invalid format",
			input:       "abc",
			expected:    "",
			expectError: true,
		},
		{
			name:        "Empty string",
			input:       "",
			expected:    "",
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := NormalizeBlockSizeKey(tc.input)
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expected, result)
			}
		})
	}
}

// BenchmarkCalculate is a benchmark test for pool calculation
func BenchmarkCalculate(b *testing.B) {
	// Prepare a large amount of test data
	poolCIDR := "10.0.0.0/16"
	allocations := make([]*ipamv1.Subnet, 100)
	for i := 0; i < 100; i++ {
		allocations[i] = &ipamv1.Subnet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "alloc" + string(rune(i+48)),
			},
			Spec: ipamv1.SubnetSpec{
				PoolRef:   "test-pool",
				CIDR:      "10.0." + string(rune(i/100+48)) + "." + string(rune(i%100+48)) + "/24",
				ClusterID: "cluster-" + string(rune(i+48)),
			},
		}
	}

	// Prepare CIDRs for child pools
	childCIDRs := make([]string, 50)
	for i := 0; i < 50; i++ {
		childCIDRs[i] = "10.1." + string(rune(i/100+48)) + "." + string(rune(i%100+48)) + "/24"
	}

	b.ResetTimer()
	b.Run("Standard", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			result, _ := Calculate(poolCIDR, allocations, childCIDRs)
			// Use results to prevent optimization
			if result.AllocatedCount <= 0 {
				b.Fatalf("unexpected result: %v", result)
			}
		}
	})
}

// BenchmarkCalculateFreeCountBySize is a benchmark test specialized for FreeCountBySize map generation
func BenchmarkCalculateFreeCountBySize(b *testing.B) {
	// Measure with child pools of different sizes
	benchmarks := []struct {
		name            string
		childPoolsCount int
	}{
		{"NoChildPools", 0},
		{"SmallChildPools", 10},
		{"MediumChildPools", 50},
		{"LargeChildPools", 200},
	}

	poolCIDR := "10.0.0.0/16"

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			// Fixed Subnet list
			allocations := make([]*ipamv1.Subnet, 20)
			for i := 0; i < 20; i++ {
				allocations[i] = &ipamv1.Subnet{
					Spec: ipamv1.SubnetSpec{
						CIDR:      "10.0.1." + string(rune(i+48)) + "/32",
						ClusterID: "cluster-" + string(rune(i+48)),
					},
				}
			}

			// Variable child pool list
			childCIDRs := make([]string, bm.childPoolsCount)
			for i := 0; i < bm.childPoolsCount; i++ {
				childCIDRs[i] = "10.2." + string(rune(i/100+48)) + "." + string(rune(i%100+48)) + "/24"
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result, _ := Calculate(poolCIDR, allocations, childCIDRs)
				if len(result.FreeCountBySize) < 2 {
					b.Fatalf("unexpected map size: %v", len(result.FreeCountBySize))
				}
			}
		})
	}
}
