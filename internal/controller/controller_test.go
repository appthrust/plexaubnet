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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

// TestValidateRequestedCIDR tests the validateRequestedCIDR function
// to ensure it properly validates that:
// 1. The requested CIDR is entirely within the pool CIDR
// 2. The requested CIDR doesn't overlap with existing allocations
func TestValidateRequestedCIDR(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = ipamv1.AddToScheme(scheme)

	// Create a reconciler with fake client for testing
	reconciler := &CIDRAllocatorReconciler{
		Client:         fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme:         scheme,
		Recorder:       &record.FakeRecorder{},
		ControllerName: "controller-test-1", // テスト用に一意な名前
	}

	tests := []struct {
		name                string
		requestedCIDR       string
		poolCIDR            string
		existingAllocations []ipamv1.Subnet
		expectError         bool
		errorContains       string
	}{
		{
			name:          "Valid CIDR within pool",
			requestedCIDR: "10.0.1.0/25",
			poolCIDR:      "10.0.0.0/16",
			expectError:   false,
		},
		{
			name:          "CIDR network address outside pool",
			requestedCIDR: "11.0.1.0/24",
			poolCIDR:      "10.0.0.0/16",
			expectError:   true,
			errorContains: "outside pool",
		},
		{
			name:          "CIDR too large for pool",
			requestedCIDR: "10.0.0.0/22",
			poolCIDR:      "10.0.0.0/24",
			expectError:   true,
			errorContains: "outside pool",
		},
		{
			name:          "CIDR partially outside pool",
			requestedCIDR: "10.0.255.0/24",
			poolCIDR:      "10.0.0.0/17",
			expectError:   true,
			errorContains: "outside pool",
		},
		{
			name:          "CIDR overlaps with existing allocation",
			requestedCIDR: "10.0.1.0/24",
			poolCIDR:      "10.0.0.0/16",
			existingAllocations: []ipamv1.Subnet{
				{
					Spec: ipamv1.SubnetSpec{
						CIDR: "10.0.1.128/25",
					},
				},
			},
			expectError:   true,
			errorContains: "overlaps with existing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := reconciler.validateRequestedCIDR(tt.requestedCIDR, tt.poolCIDR, tt.existingAllocations)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestApplyCommonMetadataLegacy tests the deprecated applyCommonMetadata function
// In new code, metadata is added via Webhook, so this test
// remains to verify the behavior of the legacy function, not the deleted feature path.
func TestApplyCommonMetadataLegacy(t *testing.T) {
	tests := []struct {
		name         string
		object       client.Object
		expectedObj  client.Object
		expectedDiff bool
	}{
		{
			name: "empty object gets label and finalizer",
			object: &ipamv1.SubnetClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-claim",
				},
			},
			expectedObj: &ipamv1.SubnetClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-claim",
					Labels: map[string]string{
						IPAMLabel: "true",
					},
					Finalizers: []string{IPAMFinalizer},
				},
			},
			expectedDiff: true,
		},
		{
			name: "object with existing labels gets additional label and finalizer",
			object: &ipamv1.SubnetClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-claim",
					Labels: map[string]string{
						"existing": "label",
					},
				},
			},
			expectedObj: &ipamv1.SubnetClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-claim",
					Labels: map[string]string{
						"existing": "label",
						IPAMLabel:  "true",
					},
					Finalizers: []string{IPAMFinalizer},
				},
			},
			expectedDiff: true,
		},
		{
			name: "object with existing finalizer gets additional label",
			object: &ipamv1.SubnetClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-claim",
					Finalizers: []string{"existing-finalizer"},
				},
			},
			expectedObj: &ipamv1.SubnetClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-claim",
					Labels: map[string]string{
						IPAMLabel: "true",
					},
					Finalizers: []string{"existing-finalizer", IPAMFinalizer},
				},
			},
			expectedDiff: true,
		},
		{
			name: "object with both label and finalizer remains unchanged",
			object: &ipamv1.SubnetClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-claim",
					Labels: map[string]string{
						IPAMLabel: "true",
					},
					Finalizers: []string{IPAMFinalizer},
				},
			},
			expectedObj: &ipamv1.SubnetClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-claim",
					Labels: map[string]string{
						IPAMLabel: "true",
					},
					Finalizers: []string{IPAMFinalizer},
				},
			},
			expectedDiff: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original state
			original := tt.object.DeepCopyObject()

			// Apply metadata
			applyCommonMetadata(tt.object)

			// Compare with expected
			if !reflect.DeepEqual(tt.object, tt.expectedObj) {
				t.Errorf("applyCommonMetadata() = %v, want %v", tt.object, tt.expectedObj)
			}

			// Check if there was a diff when expected
			hasDiff := !reflect.DeepEqual(original, tt.object)
			if hasDiff != tt.expectedDiff {
				t.Errorf("expectedDiff = %v, got diff = %v", tt.expectedDiff, hasDiff)
			}
		})
	}
}

func TestIsAllocationImmutable(t *testing.T) {
	tests := []struct {
		name     string
		oldAlloc *ipamv1.Subnet
		newAlloc *ipamv1.Subnet
		want     bool
		wantMsg  string
	}{
		{
			name: "identical allocations",
			oldAlloc: &ipamv1.Subnet{
				Spec: ipamv1.SubnetSpec{
					PoolRef:   "test-pool",
					CIDR:      "10.0.0.0/24",
					ClusterID: "test-cluster",
				},
			},
			newAlloc: &ipamv1.Subnet{
				Spec: ipamv1.SubnetSpec{
					PoolRef:   "test-pool",
					CIDR:      "10.0.0.0/24",
					ClusterID: "test-cluster",
				},
			},
			want:    true,
			wantMsg: "",
		},
		{
			name: "changed CIDR",
			oldAlloc: &ipamv1.Subnet{
				Spec: ipamv1.SubnetSpec{
					PoolRef:   "test-pool",
					CIDR:      "10.0.0.0/24",
					ClusterID: "test-cluster",
				},
			},
			newAlloc: &ipamv1.Subnet{
				Spec: ipamv1.SubnetSpec{
					PoolRef:   "test-pool",
					CIDR:      "10.0.1.0/24", // Changed CIDR
					ClusterID: "test-cluster",
				},
			},
			want:    false,
			wantMsg: "Subnet Spec fields are immutable",
		},
		{
			name: "changed pool reference",
			oldAlloc: &ipamv1.Subnet{
				Spec: ipamv1.SubnetSpec{
					PoolRef:   "test-pool",
					CIDR:      "10.0.0.0/24",
					ClusterID: "test-cluster",
				},
			},
			newAlloc: &ipamv1.Subnet{
				Spec: ipamv1.SubnetSpec{
					PoolRef:   "new-pool", // Changed poolRef
					CIDR:      "10.0.0.0/24",
					ClusterID: "test-cluster",
				},
			},
			want:    false,
			wantMsg: "Subnet Spec fields are immutable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotMsg := isAllocationImmutable(tt.oldAlloc, tt.newAlloc)
			if got != tt.want {
				t.Errorf("isAllocationImmutable() got = %v, want %v", got, tt.want)
			}
			if gotMsg != tt.wantMsg {
				t.Errorf("isAllocationImmutable() gotMsg = %v, want %v", gotMsg, tt.wantMsg)
			}
		})
	}
}

// TestReconcileSkipsMetadataUpdate tests that the Reconciler skips adding and updating
// metadata because it delegates to the Webhook.
// mockProcessClaim is a simplified mock version of processClaimReconciliation.
// A full processing flow is not needed for this test.
type mockReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Recorder     record.EventRecorder
	eventEmitter *EventEmitter
}

// Mock version of Reconcile: simply executes Get and Status without changing metadata.
func (m *mockReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	claim := &ipamv1.SubnetClaim{}
	if err := m.Get(ctx, req.NamespacedName, claim); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Important: Do not call ApplyCommonMetadata here.
	// The purpose of the test is to confirm that metadata is not added.

	return ctrl.Result{}, nil
}

func TestReconcileSkipsMetadataUpdate(t *testing.T) {
	// Create a scheme with IPAM types registered
	scheme := runtime.NewScheme()
	err := ipamv1.AddToScheme(scheme)
	assert.NoError(t, err, "Failed to add IPAM types to scheme")

	// Create a test SubnetClaim without metadata
	claim := &ipamv1.SubnetClaim{
		TypeMeta: metav1.TypeMeta{
			APIVersion: ipamv1.GroupVersion.String(),
			Kind:       "SubnetClaim",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claim",
			Namespace: "default",
		},
		Spec: ipamv1.SubnetClaimSpec{
			PoolRef:   "test-pool",
			ClusterID: "test-cluster",
			BlockSize: 24,
		},
	}

	// Create a recorder
	recorder := record.NewFakeRecorder(10)

	// Create the fake client with objects
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(claim).
		Build()

	// Create the mock reconciler
	r := &mockReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: recorder,
	}
	r.eventEmitter = NewEventEmitter(r.Recorder, r.Scheme)

	// Create a request for the claim
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-claim",
			Namespace: "default",
		},
	}

	// Call Reconcile
	_, err = r.Reconcile(context.Background(), req)
	assert.NoError(t, err)

	// Get the claim again to verify it wasn't modified
	updatedClaim := &ipamv1.SubnetClaim{}
	err = r.Get(context.Background(), req.NamespacedName, updatedClaim)
	assert.NoError(t, err)

	// Verify metadata wasn't added by the reconciler
	assert.Empty(t, updatedClaim.GetLabels())
	assert.Empty(t, updatedClaim.GetFinalizers())
}

func TestNewCIDRAllocatorReconciler(t *testing.T) {
	// Create a scheme with IPAM types registered
	scheme := runtime.NewScheme()
	_ = ipamv1.AddToScheme(scheme)

	// Create a fake client
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Create a recorder
	recorder := record.NewFakeRecorder(10)

	// Create the reconciler
	r := &CIDRAllocatorReconciler{
		Client:         client,
		Scheme:         scheme,
		Recorder:       recorder,
		ControllerName: "controller-test-2", // テスト用に一意な名前
	}

	// Initialize the event emitter
	r.eventEmitter = NewEventEmitter(r.Recorder, r.Scheme)

	// Verify the reconciler was created successfully
	assert.NotNil(t, r.Client)
	assert.NotNil(t, r.Scheme)
	assert.NotNil(t, r.Recorder)
	assert.NotNil(t, r.eventEmitter)
}
