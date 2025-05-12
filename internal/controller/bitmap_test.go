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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCalculatePoolStatus(t *testing.T) {
	tests := []struct {
		name              string
		poolCIDR          string
		allocations       []string
		expectedCount     int
		expectedFreeCount map[string]int
		expectError       bool
	}{
		{
			name:          "empty pool",
			poolCIDR:      "10.0.0.0/16",
			allocations:   []string{},
			expectedCount: 0,
			expectedFreeCount: map[string]int{
				"16": 1,
				"17": 2,
				"18": 4,
				"19": 8,
				"20": 16,
				"21": 32,
				"22": 64,
				"23": 128,
				"24": 256,
				"25": 512,
				"26": 1024,
				"27": 2048,
				"28": 4096,
			},
			expectError: false,
		},
		{
			name:          "one allocation",
			poolCIDR:      "10.0.0.0/16",
			allocations:   []string{"10.0.0.0/24"},
			expectedCount: 1,
			expectedFreeCount: map[string]int{
				"16": 0,    // Entire /16 is not free anymore
				"17": 1,    // The second /17 is still free
				"18": 3,    // Corrected to match the actual value of the bitmap
				"19": 7,    // Corrected to match the actual value of the bitmap
				"20": 15,   // Corrected to match the actual value of the bitmap
				"21": 31,   // Corrected to match the actual value of the bitmap
				"22": 63,   // Corrected to match the actual value of the bitmap
				"23": 127,  // Corrected to match the actual value of the bitmap
				"24": 255,  // One /24 is used, 255 are free
				"25": 510,  // 512-2
				"26": 1020, // 1024-4
				"27": 2040, // 2048-8
				"28": 4080, // 4096-16
			},
			expectError: false,
		},
		{
			name:          "multiple allocations",
			poolCIDR:      "10.0.0.0/16",
			allocations:   []string{"10.0.0.0/24", "10.0.1.0/24", "10.0.2.0/24"},
			expectedCount: 3,
			expectedFreeCount: map[string]int{
				"24": 253, // 256-3
			},
			expectError: false,
		},
		{
			name:          "overlapping allocations",
			poolCIDR:      "10.0.0.0/16",
			allocations:   []string{"10.0.0.0/24", "10.0.0.0/24"}, // Same CIDR twice
			expectedCount: 1,                                      // Count only unique CIDRs
			expectedFreeCount: map[string]int{
				"24": 255, // Only one /24 is marked used
			},
			expectError: false,
		},
		{
			name:          "allocation outside pool",
			poolCIDR:      "10.0.0.0/16",
			allocations:   []string{"10.0.0.0/24", "192.168.0.0/24"}, // Second is outside pool
			expectedCount: 1,                                         // Second one is not counted
			expectedFreeCount: map[string]int{
				"24": 255, // Only one /24 is marked used
			},
			expectError: false, // We don't return error, just skip the invalid allocation
		},
		{
			name:          "Multiple duplicate CIDRs",
			poolCIDR:      "10.0.0.0/16",
			allocations:   []string{"10.0.0.0/24", "10.0.0.0/24", "10.0.0.0/24", "10.0.1.0/24", "10.0.1.0/24"}, // Duplicates exist
			expectedCount: 2,                                                                                   // Only 2 unique CIDRs
			expectedFreeCount: map[string]int{
				"24": 254, // Two /24s are used
			},
			expectError: false,
		},
		{
			name:          "Nested duplicate CIDR",
			poolCIDR:      "10.0.0.0/16",
			allocations:   []string{"10.0.0.0/24", "10.0.0.0/25"}, // /25 is part of /24
			expectedCount: 2,                                      // Both are unique (though duplicated on the bitmap)
			expectedFreeCount: map[string]int{
				"24": 255, // One /24 is used
				"25": 510, // Adjusted to match the exact calculation result
			},
			expectError: false,
		},
		{
			name:          "invalid pool CIDR",
			poolCIDR:      "invalid-cidr",
			allocations:   []string{"10.0.0.0/24"},
			expectedCount: 0,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count, freeCount, err := CalculatePoolStatus(tt.poolCIDR, tt.allocations)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedCount, count)

			// Check specific sizes that were specified in the test case
			for size, expected := range tt.expectedFreeCount {
				actual, ok := freeCount[size]
				assert.True(t, ok, "Missing expected free count for size %s", size)
				assert.Equal(t, expected, actual, "Free count for size %s incorrect", size)
			}
		})
	}
}

func TestCalculatePoolStatusFullRange(t *testing.T) {
	// Create a smaller pool for detailed testing
	poolCIDR := "10.0.0.0/20" // 4096 IP addresses, 16 /24 blocks
	allocations := []string{
		"10.0.0.0/24",  // Mark first /24
		"10.0.15.0/24", // Mark last /24
	}

	count, freeCount, err := CalculatePoolStatus(poolCIDR, allocations)

	assert.NoError(t, err)
	assert.Equal(t, 2, count)

	// In a /20 pool, we should have:
	// - 1 /20 blocks (but marked as used)
	// - 2 /21 blocks (partial use)
	// - 4 /22 blocks (partial use)
	// - 8 /23 blocks (partial use)
	// - 16 /24 blocks (2 used, 14 free)
	assert.Equal(t, 0, freeCount["20"])  // The entire /20 is partially used
	assert.Equal(t, 0, freeCount["21"])  // Both /21s are partially used
	assert.Equal(t, 2, freeCount["22"])  // 2 of 4 /22s are free
	assert.Equal(t, 6, freeCount["23"])  // 6 of 8 /23s are free
	assert.Equal(t, 14, freeCount["24"]) // 14 of 16 /24s are free
}
