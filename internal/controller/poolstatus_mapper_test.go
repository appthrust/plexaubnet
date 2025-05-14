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
	"sigs.k8s.io/controller-runtime/pkg/client"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

// TestPoolStatusReconciler_MapAllocToPool verifies that mapAllocToPool returns
// the expected ctrl.Request slice when the Subnet has a PoolRef (regardless of phase).
func TestPoolStatusReconciler_MapAllocToPool(t *testing.T) {
	reconciler := &PoolStatusReconciler{}

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
				Status: ipamv1.SubnetStatus{
					Phase: ipamv1.SubnetPhaseAllocated,
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
				Status: ipamv1.SubnetStatus{
					Phase: ipamv1.SubnetPhaseFailed,
				},
			},
			wantLen: 0,
		},
		{
			name:    "Object other than Subnet",
			object:  &ipamv1.SubnetPool{},
			wantLen: 0,
		},
		{
			name: "Subnet with PoolRef but Pending phase (still enqueues)",
			object: &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-subnet-pending",
					Namespace: "test-ns",
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef: "parent-pool",
					CIDR:    "10.0.1.0/24",
				},
				Status: ipamv1.SubnetStatus{
					Phase: ipamv1.SubnetPhasePending,
				},
			},
			wantLen:    1,
			wantParent: "parent-pool",
			wantNs:     "test-ns",
		},
		{
			name: "Subnet with empty Namespace (should not enqueue)",
			object: &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-subnet-empty-ns",
					Namespace: "", // intentionally empty
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef: "parent-pool",
					CIDR:    "10.0.2.0/24",
				},
				Status: ipamv1.SubnetStatus{
					Phase: ipamv1.SubnetPhaseAllocated,
				},
			},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reconciler.mapAllocToPool(context.Background(), tt.object)
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
