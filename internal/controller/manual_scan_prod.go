//go:build !debug
// +build !debug

package cidrallocator

import (
	"context"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// manualSubnetScan is the implementation for production builds
// Always returns 0 without performing any Subnet retrieval processing
// In debug builds (-tags=debug), this function is overridden by the implementation in manual_scan_debug.go
func manualSubnetScan(_ context.Context, _ client.Client, _ *ipamv1.SubnetPool) int {
	// In production builds, do nothing and always return 0
	return 0
}
