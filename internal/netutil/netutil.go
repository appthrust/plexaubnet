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

// Package netutil provides network utility functions for CIDR and IP address operations.
// It is primarily used for Aquanaut's IPAM functionality to handle CIDR overlap detection
// and boundary calculations.
package netutil

import (
	"net"
)

// CIDROverlaps checks if two CIDRs overlap.
// This function properly detects cases where the network addresses don't contain
// each other, but the CIDR ranges still overlap.
//
// Parameters:
// - cidr1, cidr2: The two CIDRs to check for overlap
//
// Returns:
// - true if the CIDRs overlap, false otherwise
// - If either CIDR is invalid, returns false
// - If the CIDRs are from different address families (IPv4 vs IPv6), returns false
func CIDROverlaps(cidr1, cidr2 string) bool {
	// Parse both CIDRs
	_, ipNet1, err1 := net.ParseCIDR(cidr1)
	_, ipNet2, err2 := net.ParseCIDR(cidr2)

	// Return false for any parsing errors
	if err1 != nil || err2 != nil {
		return false
	}

	// Check address family compatibility
	// Get network size for both
	_, bits1 := ipNet1.Mask.Size()
	_, bits2 := ipNet2.Mask.Size()

	// IPv4 addresses have 32 bits, IPv6 addresses have 128 bits
	// If they don't match, they're from different families and can't overlap
	if bits1 != bits2 {
		return false
	}

	// Handle IPv4-mapped IPv6 addresses special case
	// Convert to 4-byte representation for IPv4 if needed
	ip1IsV4 := ipNet1.IP.To4() != nil
	ip2IsV4 := ipNet2.IP.To4() != nil

	// If one is IPv4 and one is IPv6 (even if IPv4-mapped), they don't overlap
	if ip1IsV4 != ip2IsV4 {
		return false
	}

	// Check if network addresses are contained in each other's network
	// This is the simpler case where one network wholly contains the other
	if ipNet1.Contains(ipNet2.IP) || ipNet2.Contains(ipNet1.IP) {
		return true
	}

	// Check for partial overlap - calculate the last IP of each network
	// This detects cases where network addresses differ but ranges overlap
	// Example: 10.0.0.0/23 and 10.0.1.0/24 don't contain each other's network address but do overlap
	lastIP1 := CalculateLastIP(ipNet1)
	lastIP2 := CalculateLastIP(ipNet2)

	// Check if either network contains the other's last IP
	return ipNet1.Contains(lastIP2) || ipNet2.Contains(lastIP1)
}

// CalculateLastIP calculates the last IP address in a network.
// This is used to check for partial CIDR overlaps and boundary conditions.
//
// Parameters:
// - network: The network to calculate the last IP for
//
// Returns:
// - The last IP address in the network
func CalculateLastIP(network *net.IPNet) net.IP {
	// Get the network mask length
	ones, bits := network.Mask.Size()

	// Calculate how many host bits we have
	hostBits := bits - ones

	// Create a copy of the IP address (network address)
	ip := make(net.IP, len(network.IP))
	copy(ip, network.IP)

	// Convert IP to 4-byte representation for IPv4 if needed
	if len(ip) == 16 && ip.To4() != nil {
		ip = ip.To4()
	}

	// For IPv4 (4 bytes) or IPv6 (16 bytes)
	size := len(ip)

	// Set all host bits to 1 to get the broadcast/last address
	// Starting with the least significant byte
	for i := size - 1; i >= 0; i-- {
		// How many bits in this byte are host bits?
		// If we have more than 8 host bits remaining, the entire byte is host bits
		byteHostBits := 8
		if hostBits < 8 {
			byteHostBits = hostBits
		}

		// If we have host bits in this byte, set them to 1
		if byteHostBits > 0 {
			// Create a mask like 00011111 for the host bits in this byte
			mask := byte((1 << byteHostBits) - 1)
			ip[i] |= mask
		}

		// Subtract the bits we've processed
		hostBits -= byteHostBits
		if hostBits <= 0 {
			break
		}
	}

	return ip
}
