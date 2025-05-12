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

// Package cidrallocator implements paged list operations
package cidrallocator

import (
	"context"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// listSubnetsPaged retrieves all subnets for a given pool using pagination
// to limit memory usage per page to PageSize items.
//
// Parameters:
//   - ctx: Context for the operation
//   - c: Client interface for accessing the API server
//   - pool: The SubnetPool to fetch subnets for
//
// Returns:
//   - []ipamv1.Subnet: All subnets associated with the pool across all pages
//   - error: Any error encountered during listing
func listSubnetsPaged(ctx context.Context, c client.Client, pool *ipamv1.SubnetPool) ([]ipamv1.Subnet, error) {
	var (
		allSubnets  []ipamv1.Subnet
		baseOptions = []client.ListOption{
			client.MatchingFields{"spec.poolRef": pool.Name},
			client.InNamespace(pool.Namespace),
			client.Limit(PageSize),
		}
		continueOption client.ListOption
		pageCount      = 0
	)

	logger := log.FromContext(ctx).WithValues("func", "ListSubnetsPaged", "pool", pool.Name)
	logger.V(1).Info("Starting paged subnet list", "pageSize", PageSize)

	for {
		currentList := &ipamv1.SubnetList{}

		// Prepare the current set of options
		options := baseOptions
		if continueOption != nil {
			options = append(options, continueOption)
		}

		// Fetch data from the API server
		if err := c.List(ctx, currentList, options...); err != nil {
			logger.Error(err, "Failed to fetch subnet page", "page", pageCount+1)
			return nil, err
		}

		// Increment counter only on success
		pageCount++

		// Add retrieved items
		allSubnets = append(allSubnets, currentList.Items...)

		// Record metrics
		SubnetListPagesTotal.Inc()

		logger.V(1).Info("Fetched subnet page",
			"page", pageCount,
			"items", len(currentList.Items),
			"total", len(allSubnets),
			"continue", currentList.Continue != "",
		)

		// If there is no Continue token, exit
		if currentList.Continue == "" {
			SubnetListContinueSkippedTotal.Inc() // Record loop termination in metrics
			break
		}

		// Set Continue token for the next page
		continueOption = client.Continue(currentList.Continue)
	}

	logger.Info("Completed paged subnet list",
		"pages", pageCount,
		"totalItems", len(allSubnets),
	)

	return allSubnets, nil
}
