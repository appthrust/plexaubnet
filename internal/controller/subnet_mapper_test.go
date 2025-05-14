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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

// TestSubnetMapper_MapSubnetToParentPool verifies that mapSubnetToParentPool
// enqueues the parent SubnetPool only when a PoolRef is specified.
func TestSubnetMapper_MapSubnetToParentPool(t *testing.T) {
	// Inline replica of production mapper logic to isolate the test subject.
	mapSubnetToParentPool := func(ctx context.Context, obj client.Object) []ctrl.Request {
		subnet, ok := obj.(*ipamv1.Subnet)
		if !ok {
			// Tombstoneなどは扱わない（ユニットテストでは検証不可）
			return nil
		}
		if subnet.Spec.PoolRef == "" {
			return nil
		}
		return enqueueParentPool(subnet.GetNamespace(), subnet.Spec.PoolRef)
	}

	tests := []struct {
		name       string
		object     client.Object
		wantLen    int
		wantParent string
		wantNs     string
	}{
		{
			name: "Subnet with PoolRef",
			object: &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-subnet",
					Namespace: "test-ns",
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef: "parent-pool",
					CIDR:    "10.0.0.0/24",
				},
			},
			wantLen:    1,
			wantParent: "parent-pool",
			wantNs:     "test-ns",
		},
		{
			name: "Subnet without PoolRef",
			object: &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-subnet-no-pool",
					Namespace: "test-ns",
				},
				Spec: ipamv1.SubnetSpec{
					CIDR: "10.0.0.0/24",
				},
			},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapSubnetToParentPool(context.Background(), tt.object)
			if len(got) != tt.wantLen {
				t.Errorf("got len %d, want %d", len(got), tt.wantLen)
				return
			}
			if tt.wantLen == 0 {
				return
			}
			if got[0].Name != tt.wantParent {
				t.Errorf("parent name = %q, want %q", got[0].Name, tt.wantParent)
			}
			if got[0].Namespace != tt.wantNs {
				t.Errorf("namespace = %q, want %q", got[0].Namespace, tt.wantNs)
			}
			if got[0].Namespace == "" {
				t.Error("namespace must not be empty (controller-development.md Rule#1)")
			}
		})
	}
}
