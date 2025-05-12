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

package netutil

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCIDROverlaps(t *testing.T) {
	tests := []struct {
		name  string
		cidr1 string
		cidr2 string
		want  bool
	}{
		// IPv4 cases (migration of existing test cases)
		{
			name:  "exact same cidr",
			cidr1: "10.0.0.0/24",
			cidr2: "10.0.0.0/24",
			want:  true,
		},
		{
			name:  "first contains second",
			cidr1: "10.0.0.0/16",
			cidr2: "10.0.1.0/24",
			want:  true,
		},
		{
			name:  "second contains first",
			cidr1: "10.0.1.0/24",
			cidr2: "10.0.0.0/16",
			want:  true,
		},
		{
			name:  "non-overlapping cidrs",
			cidr1: "10.0.0.0/24",
			cidr2: "10.0.1.0/24",
			want:  false,
		},
		{
			name:  "partial overlap case - larger network covers part of smaller network",
			cidr1: "10.0.0.0/23", // 10.0.0.0-10.0.1.255
			cidr2: "10.0.1.0/24", // 10.0.1.0-10.0.1.255
			want:  true,
		},
		{
			name:  "partial overlap case - smaller network is contained within larger network",
			cidr1: "10.0.1.0/24", // 10.0.1.0-10.0.1.255
			cidr2: "10.0.0.0/23", // 10.0.0.0-10.0.1.255
			want:  true,
		},
		{
			name:  "completely different networks",
			cidr1: "10.0.0.0/16",
			cidr2: "192.168.0.0/16",
			want:  false,
		},
		{
			name:  "invalid first cidr",
			cidr1: "invalid-cidr",
			cidr2: "10.0.0.0/24",
			want:  false,
		},
		{
			name:  "invalid second cidr",
			cidr1: "10.0.0.0/24",
			cidr2: "invalid-cidr",
			want:  false,
		},

		// IPv6 test cases (newly added)
		{
			name:  "IPv6 exact same cidr",
			cidr1: "2001:db8::/64",
			cidr2: "2001:db8::/64",
			want:  true,
		},
		{
			name:  "IPv6 first contains second",
			cidr1: "2001:db8::/48",
			cidr2: "2001:db8:0:1::/64",
			want:  true,
		},
		{
			name:  "IPv6 second contains first",
			cidr1: "2001:db8:0:1::/64",
			cidr2: "2001:db8::/48",
			want:  true,
		},
		{
			name:  "IPv6 non-overlapping cidrs",
			cidr1: "2001:db8:0:1::/64",
			cidr2: "2001:db8:0:2::/64",
			want:  false,
		},
		{
			name:  "IPv6 partial overlap",
			cidr1: "2001:db8::/63",     // 2001:db8:0:0:: - 2001:db8:0:1::ffff:ffff:ffff:ffff
			cidr2: "2001:db8:0:1::/64", // 2001:db8:0:1:: - 2001:db8:0:1:ffff:ffff:ffff:ffff
			want:  true,
		},
		{
			name:  "IPv6 boundary case - /126 network",
			cidr1: "2001:db8::/126",  // Just 4 addresses
			cidr2: "2001:db8::4/126", // Next 4 addresses
			want:  false,
		},
		{
			name:  "IPv6 boundary case - single address /128",
			cidr1: "2001:db8::1/128", // Single address
			cidr2: "2001:db8::2/128", // Adjacent single address
			want:  false,
		},
		{
			name:  "IPv6 mixed with IPv4-mapped",
			cidr1: "::ffff:10.0.0.0/120", // IPv4-mapped in IPv6
			cidr2: "10.0.0.0/24",         // Same range in IPv4
			want:  false,                 // Different address families don't overlap
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CIDROverlaps(tt.cidr1, tt.cidr2)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCalculateLastIP(t *testing.T) {
	tests := []struct {
		name  string
		cidr  string
		want  string
		valid bool
	}{
		// IPv4 test cases
		{
			name:  "IPv4 /24 network",
			cidr:  "192.168.1.0/24",
			want:  "192.168.1.255",
			valid: true,
		},
		{
			name:  "IPv4 /16 network",
			cidr:  "10.0.0.0/16",
			want:  "10.0.255.255",
			valid: true,
		},
		{
			name:  "IPv4 /31 network (point-to-point)",
			cidr:  "192.168.1.0/31",
			want:  "192.168.1.1",
			valid: true,
		},
		{
			name:  "IPv4 /32 network (single host)",
			cidr:  "192.168.1.1/32",
			want:  "192.168.1.1",
			valid: true,
		},

		// IPv6 test cases
		{
			name:  "IPv6 /64 network",
			cidr:  "2001:db8::/64",
			want:  "2001:db8::ffff:ffff:ffff:ffff",
			valid: true,
		},
		{
			name:  "IPv6 /112 network",
			cidr:  "2001:db8::/112",
			want:  "2001:db8::ffff",
			valid: true,
		},
		{
			name:  "IPv6 /126 network",
			cidr:  "2001:db8::/126",
			want:  "2001:db8::3",
			valid: true,
		},
		{
			name:  "IPv6 /127 network (point-to-point)",
			cidr:  "2001:db8::/127",
			want:  "2001:db8::1",
			valid: true,
		},
		{
			name:  "IPv6 /128 network (single host)",
			cidr:  "2001:db8::1/128",
			want:  "2001:db8::1",
			valid: true,
		},
		{
			name:  "Invalid CIDR",
			cidr:  "invalid-cidr",
			want:  "",
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ipNet, err := net.ParseCIDR(tt.cidr)
			if !tt.valid {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)

			got := CalculateLastIP(ipNet)
			wantIP := net.ParseIP(tt.want)
			assert.Equal(t, wantIP.String(), got.String(), "Expected %s, got %s", wantIP, got)
		})
	}
}
