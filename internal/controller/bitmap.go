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
	"fmt"
	"math"
	"math/big"
	"net"
	"strconv"
	"strings"
)

// CIDRMap provides CIDR allocation tracking with bitmap-based implementation
type CIDRMap struct {
	// baseIP is the first IP in the CIDR range
	baseIP net.IP
	// prefixLen is the prefix length of the pool
	prefixLen int
	// bits is a bitmap representing allocated blocks
	bits *big.Int
	// maxSize is the maximum prefix length supported (e.g., /28)
	maxSize int
	// minSize is the minimum prefix length supported (e.g., /16)
	minSize int
}

// NewCIDRMap creates a CIDRMap from a CIDR string
func NewCIDRMap(cidr string) (*CIDRMap, error) {
	// Parse CIDR
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR format: %w", err)
	}

	// Check for IPv4
	if ipNet.IP.To4() == nil {
		return nil, fmt.Errorf("IPv6 not supported in this version")
	}

	// Get prefix length
	prefixLen, _ := ipNet.Mask.Size()
	if prefixLen > 28 {
		return nil, fmt.Errorf("prefix length must be at most /28")
	}
	if prefixLen < 12 {
		return nil, fmt.Errorf("prefix length must be at least /12")
	}

	// Calculate capacity for bitmap (not directly used but useful for validation)
	_ = int(math.Pow(2, float64(32-prefixLen)))

	return &CIDRMap{
		baseIP:    ipNet.IP.To4(),
		prefixLen: prefixLen,
		bits:      big.NewInt(0),
		maxSize:   28,
		minSize:   16,
	}, nil
}

// MarkAllocated marks a CIDR as allocated
func (c *CIDRMap) MarkAllocated(cidr string) error {
	// Parse CIDR
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid CIDR format: %w", err)
	}

	// Check if CIDR is within the pool range
	if !c.contains(ipNet) {
		return fmt.Errorf("CIDR %s is outside the pool range", cidr)
	}

	// Calculate the bit range to mark
	startIndex, size := c.getCIDRBitRange(ipNet)

	// Check if already allocated
	for i := int64(0); i < size; i++ {
		if c.bits.Bit(int(startIndex+i)) == 1 {
			return fmt.Errorf("CIDR %s or portion of it is already allocated", cidr)
		}
	}

	// Mark as allocated
	for i := int64(0); i < size; i++ {
		c.bits.SetBit(c.bits, int(startIndex+i), 1)
	}

	return nil
}

// AllocateNextAvailable finds and allocates the next available CIDR block of given size
func (c *CIDRMap) AllocateNextAvailable(size int) (string, error) {
	// Validate size
	if size < c.minSize || size > c.maxSize {
		return "", fmt.Errorf("size must be between %d and %d", c.minSize, c.maxSize)
	}

	// Calculate block size (in bits)
	blockSize := int64(math.Pow(2, float64(32-size)))

	// Total number of blocks possible
	totalBlocks := int64(math.Pow(2, float64(size-c.prefixLen)))

	// Find first available block
	for i := int64(0); i < totalBlocks; i++ {
		startBit := i * blockSize

		// Check if block is free
		free := true
		for j := int64(0); j < blockSize; j++ {
			if c.bits.Bit(int(startBit+j)) == 1 {
				free = false
				break
			}
		}

		if free {
			// Calculate the IP
			offset := startBit
			ip := make(net.IP, 4)
			copy(ip, c.baseIP)

			// Add offset to base IP
			for j := 3; j >= 0; j-- {
				ip[j] += byte(offset % 256)
				offset /= 256
			}

			// Create CIDR string
			cidr := fmt.Sprintf("%s/%d", ip.String(), size)

			// Mark as allocated
			if err := c.MarkAllocated(cidr); err != nil {
				return "", err
			}

			return cidr, nil
		}
	}

	return "", fmt.Errorf("no available CIDR blocks of size /%d", size)
}

// GetFreeBlockCount returns the number of free blocks for each size
func (c *CIDRMap) GetFreeBlockCount() map[string]int {
	result := make(map[string]int)

	for size := c.minSize; size <= c.maxSize; size++ {
		blockSize := int64(math.Pow(2, float64(32-size)))
		totalBlocks := int64(math.Pow(2, float64(size-c.prefixLen)))

		freeCount := 0
		for i := int64(0); i < totalBlocks; i++ {
			startBit := i * blockSize

			// Check if block is free
			free := true
			for j := int64(0); j < blockSize; j++ {
				if c.bits.Bit(int(startBit+j)) == 1 {
					free = false
					break
				}
			}

			if free {
				freeCount++
			}
		}

		result[strconv.Itoa(size)] = freeCount
	}

	return result
}

// CalculatePoolStatus calculates comprehensive pool statistics in one pass
// This helps avoid double-counting issues and improves accuracy of Pool.Status
func CalculatePoolStatus(poolCIDR string, allocations []string) (int, map[string]int, error) {
	// Create a clean CIDR bitmap
	cidrMap, err := NewCIDRMap(poolCIDR)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to create CIDR map: %w", err)
	}

	// Track unique CIDRs to prevent double-counting
	uniqueCIDRs := make(map[string]struct{})

	// Mark all allocations
	for _, cidr := range allocations {
		// Skip marking if we've already seen this exact CIDR
		if _, exists := uniqueCIDRs[cidr]; exists {
			continue
		}

		// Add to our unique set regardless of whether MarkAllocated succeeds
		uniqueCIDRs[cidr] = struct{}{}

		// Attempt to mark in bitmap (for accurate free count calculation)
		err := cidrMap.MarkAllocated(cidr)
		// Ignore "already allocated" errors - they might be from overlapping CIDRs
		// Just continue since we've already added this to our unique set
		if err != nil && !strings.Contains(err.Error(), "already allocated") {
			// If error is not about already allocated (e.g. outside pool),
			// don't count this CIDR but still continue processing
			delete(uniqueCIDRs, cidr)
		}
	}

	// Calculate free blocks
	freeCountBySize := cidrMap.GetFreeBlockCount()

	// Use the unique CIDR count as the validCount
	validCount := len(uniqueCIDRs)

	return validCount, freeCountBySize, nil
}

// contains checks if the given CIDR is contained within the pool
func (c *CIDRMap) contains(ipNet *net.IPNet) bool {
	// Create a pool network
	poolMask := net.CIDRMask(c.prefixLen, 32)
	poolNet := &net.IPNet{
		IP:   c.baseIP,
		Mask: poolMask,
	}

	// Check if the first IP of ipNet is contained in poolNet
	return poolNet.Contains(ipNet.IP)
}

// getCIDRBitRange calculates the bit range for a CIDR
func (c *CIDRMap) getCIDRBitRange(ipNet *net.IPNet) (int64, int64) {
	targetPrefix, _ := ipNet.Mask.Size()

	// Calculate offset from base IP
	offset := int64(0)
	for i := 0; i < 4; i++ {
		offset = offset*256 + int64(ipNet.IP[i]-c.baseIP[i])
	}

	// Calculate the size of the block
	size := int64(math.Pow(2, float64(32-targetPrefix)))

	return offset, size
}

// ParseCIDRSize extracts the prefix length from a CIDR string
func ParseCIDRSize(cidr string) (int, error) {
	parts := strings.Split(cidr, "/")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid CIDR format: %s", cidr)
	}

	prefixLen, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid prefix length: %w", err)
	}

	return prefixLen, nil
}
