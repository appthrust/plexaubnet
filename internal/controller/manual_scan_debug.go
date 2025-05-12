//go:build debug
// +build debug

package cidrallocator

import (
	"context"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// manualSubnetScan is the implementation for debug builds
// Overrides the normal implementation in manual_scan_prod.go
// Lists all Subnets in the Namespace and manually verifies their association with the Pool
func manualSubnetScan(ctx context.Context, c client.Client, pool *ipamv1.SubnetPool) int {
	logger := log.FromContext(ctx).WithValues("debug", "manualScan", "pool", pool.Name)

	// Get all Subnets without field index (for debugging)
	allSubnets := &ipamv1.SubnetList{}
	if err := c.List(ctx, allSubnets, client.InNamespace(pool.Namespace)); err != nil {
		logger.Error(err, "DEBUG: Failed to list all Subnets in namespace")
		return 0
	}

	// Manual check of pool reference (for debugging)
	manualMatchCount := 0
	for _, subnet := range allSubnets.Items {
		if subnet.Spec.PoolRef == pool.Name {
			manualMatchCount++
			logger.V(5).Info("DEBUG: Manual match found",
				"subnet", subnet.Name,
				"cidr", subnet.Spec.CIDR,
				"poolRef", subnet.Spec.PoolRef)
		}
	}

	logger.V(1).Info("DEBUG: Manual pool reference check",
		"namespace", pool.Namespace,
		"poolName", pool.Name,
		"totalSubnets", len(allSubnets.Items),
		"manualMatches", manualMatchCount)

	return manualMatchCount
}
