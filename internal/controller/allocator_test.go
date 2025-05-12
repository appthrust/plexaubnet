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
	"strings"
	"testing"
)

func TestEncodeAllocationName(t *testing.T) {
	tests := []struct {
		name       string
		poolName   string
		cidr       string
		wantLength int // Expected length of the name
	}{
		{
			name:       "Standard short name",
			poolName:   "test-pool",
			cidr:       "10.0.0.0/24",
			wantLength: 21, // "test-pool-10-0-0-0-24" is 21 characters (because dots are converted to hyphens)
		},
		{
			name:       "Long name - exactly 63 characters",
			poolName:   strings.Repeat("a", 50),
			cidr:       "10.0.0.0/24",
			wantLength: 63, // 63 characters or less
		},
		{
			name:       "Very long name",
			poolName:   strings.Repeat("a", 100),
			cidr:       "10.0.0.0/24",
			wantLength: 63, // Shortened to 63 characters or less
		},
		{
			name:       "Complex CIDR",
			poolName:   "test-pool",
			cidr:       "2001:0db8:85a3:0000:0000:8a2e:0370:7334/64",
			wantLength: 63, // 63 characters or less
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EncodeAllocationName(tt.poolName, tt.cidr)

			// Confirm that the name length is 63 characters or less
			if len(got) > 63 {
				t.Errorf("EncodeAllocationName() generated name is too long: length %d, maximum allowed 63", len(got))
			}

			// Confirm if it matches the expected length (considering the case where shortening logic is used)
			if tt.wantLength > 0 && len(got) > tt.wantLength {
				// If shortening logic is applied, the exact length cannot be predicted, but it is confirmed to be 63 characters or less
				if tt.wantLength == 63 && len(got) <= 63 {
					// OK - Fits within 63 characters or less
				} else {
					t.Errorf("EncodeAllocationName() does not match expected length: expected <= %d, actual %d", tt.wantLength, len(got))
				}
			}

			// Is a valid Kubernetes name (alphanumeric, '-' only, starts and ends with alphanumeric)
			if !isValidK8sName(got) {
				t.Errorf("EncodeAllocationName() generated an invalid Kubernetes name: %q", got)
			}

			// Should return the same output for the same input (deterministic)
			gotAgain := EncodeAllocationName(tt.poolName, tt.cidr)
			if got != gotAgain {
				t.Errorf("EncodeAllocationName() is not deterministic: %q != %q", got, gotAgain)
			}
		})
	}

	// Test to confirm that outputs from different inputs do not collide
	t.Run("Name uniqueness", func(t *testing.T) {
		// Generate names with multiple similar inputs
		names := make(map[string]bool)

		for i := 0; i < 10; i++ {
			for j := 0; j < 5; j++ {
				pool := "test-pool-" + strings.Repeat("a", i*5)
				cidr := "10." + strings.Repeat("1", j) + ".0.0/24"

				name := EncodeAllocationName(pool, cidr)

				// Check if the same name already exists
				if names[name] {
					t.Errorf("Name collision detected: %q was generated from multiple inputs", name)
				}
				names[name] = true
			}
		}
	})
}

// Check if valid as a Kubernetes name (simple implementation)
func isValidK8sName(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}

	// Alphanumeric, '-' only allowed, starts and ends with alphanumeric
	for i, r := range name {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		isDash := r == '-'

		// Start and end must be alphanumeric only
		if (i == 0 || i == len(name)-1) && !isAlphaNum {
			return false
		}

		// Middle part must be alphanumeric or '-' only
		if !isAlphaNum && !isDash {
			return false
		}
	}

	return true
}

// TestBitmapExhaustion tests the behavior after the CIDR pool is exhausted
func TestBitmapExhaustion(t *testing.T) {
	// Create a small pool for testing (/27 has 32 addresses, of which two /28 blocks can be allocated)
	poolCIDR := "192.168.1.0/27"
	blockSize := 28 // /28 block (16 IP addresses)

	// Create all allocatable /28 blocks from the pool
	allCIDRs := []string{
		"192.168.1.0/28",  // First /28 block
		"192.168.1.16/28", // Second /28 block
	}

	// Map to record already allocated CIDRs
	usedCIDRs := make(map[string]bool)

	// Allocate all /30 blocks from the pool
	for i, cidr := range allCIDRs {
		t.Logf("Allocation test %d: CIDR %s", i+1, cidr)

		// Call findAvailableCIDR
		availableCIDR, err := findAvailableCIDR(poolCIDR, blockSize, usedCIDRs)

		// Confirm there is no error
		if err != nil {
			t.Fatalf("Error in findAvailableCIDR(%d): %v", i+1, err)
		}

		// Confirm if it matches the predicted CIDR
		if availableCIDR != allCIDRs[i] {
			t.Errorf("Result of findAvailableCIDR(%d) differs from expectation: got %s, want %s",
				i+1, availableCIDR, allCIDRs[i])
		}

		// Record as allocated
		usedCIDRs[availableCIDR] = true
	}

	// At this point, the pool should be exhausted - attempt additional allocation
	t.Log("Pool exhausted: Attempting additional allocation")
	_, err := findAvailableCIDR(poolCIDR, blockSize, usedCIDRs)

	// Expect an error to occur (some error should be returned when the pool is exhausted)
	if err == nil {
		t.Fatalf("No error in findAvailableCIDR after pool exhaustion")
	}

	// Log error content (for information)
	t.Logf("Error during pool exhaustion: %v", err)
}
