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

// Package cidrallocator - centralized index registration for CIDR allocation
package cidrallocator

import (
	"context"
	"strings"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// PoolRefField is the field index for spec.poolRef
	PoolRefField = "spec.poolRef"
	// ParentPoolLabelIndex is the field index for metadata.labels[plexaubnet.io/parent]
	ParentPoolLabelIndex = "metadata.labels[plexaubnet.io/parent]"
	// PageSize is the default page size for List operations to limit memory usage
	PageSize = 1000
)

// SetupFieldIndexes registers all FieldIndexers once.
// Safe to call exactly one time during manager startup.
func SetupFieldIndexes(ctx context.Context, mgr ctrl.Manager, log logr.Logger) error {
	idx := mgr.GetFieldIndexer()

	// Common error handler
	handle := func(err error, name string) error {
		if err == nil {
			return nil
		}
		if strings.Contains(err.Error(), "already exists") ||
			strings.Contains(err.Error(), "indexer conflict") {
			log.V(1).Info("Index already registered, reusing", "index", name)
			return nil
		}
		log.Error(err, "Cannot register field index", "index", name)
		return err
	}

	// Register Subnet -> spec.poolRef
	if err := handle(idx.IndexField(ctx,
		&ipamv1.Subnet{}, PoolRefField,
		func(o client.Object) []string {
			return []string{o.(*ipamv1.Subnet).Spec.PoolRef}
		}), PoolRefField); err != nil {
		return err
	}

	// Register SubnetClaim -> spec.poolRef
	if err := handle(idx.IndexField(ctx,
		&ipamv1.SubnetClaim{}, PoolRefField,
		func(o client.Object) []string {
			return []string{o.(*ipamv1.SubnetClaim).Spec.PoolRef}
		}), PoolRefField+"(SubnetClaim)"); err != nil {
		return err
	}

	// Register SubnetPool -> metadata.labels[plexaubnet.io/parent]
	if err := handle(idx.IndexField(ctx,
		&ipamv1.SubnetPool{}, ParentPoolLabelIndex,
		func(o client.Object) []string {
			if p, ok := o.(*ipamv1.SubnetPool); ok {
				if parent, ok := p.Labels["plexaubnet.io/parent"]; ok {
					return []string{parent}
				}
			}
			return nil
		}), ParentPoolLabelIndex); err != nil {
		return err
	}

	return nil
}
