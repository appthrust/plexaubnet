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
	"context"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// PoolGaugeEventHandler is a Predicate handler that monitors SubnetPool create/delete events
// to register/unregister static gauges.
type PoolGaugeEventHandler struct {
	// Empty implementation
}

// Create registers static gauges when a SubnetPool is created.
func (p *PoolGaugeEventHandler) Create(e event.CreateEvent) bool {
	logger := log.Log.WithValues("handler", "PoolGaugeEventHandler.Create")

	pool, ok := e.Object.(*ipamv1.SubnetPool)
	if !ok {
		logger.V(1).Info("Not a SubnetPool, ignoring")
		return false // Do not continue
	}

	logger.Info("SubnetPool created, registering static gauges",
		"name", pool.Name,
		"namespace", pool.Namespace)

	// Create static gauges (executed independently of normal controller processing)
	RegisterPoolGauges(pool.Name)

	// Whether to pass the event to the controller's Reconcile - return true if necessary.
	// Basically false if only gauge registration is the purpose.
	return false
}

// Delete unregisters static gauges when a SubnetPool is deleted.
func (p *PoolGaugeEventHandler) Delete(e event.DeleteEvent) bool {
	logger := log.Log.WithValues("handler", "PoolGaugeEventHandler.Delete")

	pool, ok := e.Object.(*ipamv1.SubnetPool)
	if !ok {
		logger.V(1).Info("Not a SubnetPool, ignoring")
		return false // Do not continue
	}

	logger.Info("SubnetPool deleted, unregistering static gauges",
		"name", pool.Name,
		"namespace", pool.Namespace)

	// Unregister static gauges
	UnregisterPoolGauges(pool.Name)

	// Whether to pass the event to the controller's Reconcile
	return false
}

// Update ignores Update events.
func (p *PoolGaugeEventHandler) Update(e event.UpdateEvent) bool {
	// Ignore as gauge update is not needed on Update events
	return false
}

// Generic ignores Generic events.
func (p *PoolGaugeEventHandler) Generic(e event.GenericEvent) bool {
	// Ignore Generic events
	return false
}

// NewPoolGaugePredicate creates a new PoolGaugeEventHandler.
func NewPoolGaugePredicate() predicate.Predicate {
	return &PoolGaugeEventHandler{}
}

// RegisterAllPoolGauges registers static gauges for all existing SubnetPools.
// Used when batch registration is needed, such as at controller startup.
func RegisterAllPoolGauges(ctx context.Context, c client.Client) error {
	logger := log.FromContext(ctx).WithValues("func", "RegisterAllPoolGauges")

	// List all SubnetPools
	var pools ipamv1.SubnetPoolList
	if err := c.List(ctx, &pools); err != nil {
		// If the cache is not yet started, just log the error and continue
		logger.Error(err, "Failed to list SubnetPools")
		return nil // Continue without returning an error (will be registered individually later when Reconcile is executed)
	}

	// Register static gauges for each Pool
	for i := range pools.Items {
		pool := &pools.Items[i]
		logger.V(1).Info("Registering static gauges for existing pool",
			"name", pool.Name,
			"namespace", pool.Namespace)
		RegisterPoolGauges(pool.Name)
	}

	logger.Info("Registered static gauges for all existing pools",
		"count", len(pools.Items))

	return nil
}
