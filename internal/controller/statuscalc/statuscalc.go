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

// Package statuscalc provides utilities for calculating SubnetPool status metrics
package statuscalc

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

// defaultFreeCountKeyCap is the default initial capacity for the FreeCountBySize map
// Considers the maximum size range of IP address blocks (e.g., /16 to /24)
const defaultFreeCountKeyCap = 8

// estimateFreeCountCap calculates the estimated capacity for FreeCountBySize
// Returns an appropriate cap considering childCIDRs
func estimateFreeCountCap(blockSize int, childCIDRs []string) int {
	// Basically 2 entries for the corresponding block size ("24" and "/24" format)
	// If there are many child pools, expand capacity considering additional block sizes
	if len(childCIDRs) == 0 {
		return 2 // Basic 2 entries only
	}

	// Increase capacity according to the number of child pools (at least defaultFreeCountKeyCap)
	estimatedCap := 2 + len(childCIDRs)/10
	if estimatedCap < defaultFreeCountKeyCap {
		return defaultFreeCountKeyCap
	}
	return estimatedCap
}

// CalculateResult contains the results of SubnetPool status calculations
type CalculateResult struct {
	// AllocatedCount is the total number of allocations
	AllocatedCount int
	// FreeCountBySize maps block sizes to their free count
	// Uses both "24" and "/24" format keys for compatibility
	FreeCountBySize map[string]int
	// AllocatedCIDRs maps CIDRs to their cluster IDs
	AllocatedCIDRs map[string]string
}

// Calculate computes status metrics for a pool based on its CIDR, allocations, and child SubnetPools
// Returns:
// - AllocatedCount: Total number of allocations (including child SubnetPools)
// - FreeCountBySize: Map of blockSize -> free count
// - AllocatedCIDRs: Map of CIDR -> clusterID
// - error: Any error that occurred during calculation
func Calculate(poolCIDR string, allocations []*ipamv1.Subnet, childCIDRs []string) (*CalculateResult, error) {
	// Initialize result with allocation count including both Subnets and child SubnetPools
	totalAllocations := len(allocations) + len(childCIDRs)

	// Create a map with a pre-specified capacity (P4 optimization)
	allocSize := len(allocations) + len(childCIDRs)
	// Dynamically estimate the capacity of FreeCountBySize
	freeCountCap := estimateFreeCountCap(24, childCIDRs)

	result := &CalculateResult{
		AllocatedCount:  totalAllocations,
		FreeCountBySize: make(map[string]int, freeCountCap), // Initialize with dynamically estimated capacity
		AllocatedCIDRs:  make(map[string]string, allocSize),
	}

	// Use pointer array directly
	for _, alloc := range allocations {
		result.AllocatedCIDRs[alloc.Spec.CIDR] = alloc.Spec.ClusterID
	}

	// Add child pool CIDRs to the map
	// Using a special identifier for child pools: "child-pool"
	for _, childCIDR := range childCIDRs {
		result.AllocatedCIDRs[childCIDR] = "child-pool"
	}

	// Parse pool CIDR
	_, ipNet, err := net.ParseCIDR(poolCIDR)
	if err != nil {
		return result, fmt.Errorf("invalid pool CIDR format: %w", err)
	}

	// Get pool size and mask length
	ones, bits := ipNet.Mask.Size()

	// Default block size is /24 if not specified
	blockSize := 24

	// Calculate theoretical maximum allocations
	// (2^(blockSize - ones)) is the number of subnets we can create
	// But this doesn't account for alignment issues or other restrictions
	maxBlocks := 1 << (uint(blockSize) - uint(ones))
	if bits == 128 && ones < 64 {
		// For large IPv6 pools, cap at a reasonable number to avoid overflow
		maxBlocks = 1000000
	}

	// Calculate free count (total blocks minus allocated blocks)
	freeCount := maxBlocks - totalAllocations
	if freeCount < 0 {
		freeCount = 0
	}

	// Update free count by size map, using both old and new format keys
	// Old format: "/24"
	// New format: "24"
	legacyBlockSizeKey := fmt.Sprintf("/%d", blockSize)
	newBlockSizeKey := strconv.Itoa(blockSize)

	result.FreeCountBySize[legacyBlockSizeKey] = freeCount
	result.FreeCountBySize[newBlockSizeKey] = freeCount

	return result, nil
}

// NormalizeBlockSizeKey ensures a block size key is in the standard format
// Converts "/24" to "24" and ensures it's a valid integer
func NormalizeBlockSizeKey(key string) (string, error) {
	// Strip leading slash if present
	key = strings.TrimPrefix(key, "/")

	// Validate it's a valid integer
	_, err := strconv.Atoi(key)
	if err != nil {
		return "", fmt.Errorf("invalid block size key format: %w", err)
	}

	return key, nil
}
