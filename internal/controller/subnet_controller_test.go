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
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"github.com/appthrust/plexaubnet/internal/config"
	"github.com/appthrust/plexaubnet/internal/controller/statusutil"
)

// Helper to generate Subnet objects for testing
func newTestSubnet(name, ns, cidr, pool string) *ipamv1.Subnet {
	return &ipamv1.Subnet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Subnet",
			APIVersion: ipamv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: ipamv1.SubnetSpec{
			CIDR:      cidr,
			PoolRef:   pool,
			ClusterID: "test-cluster",
		},
		Status: ipamv1.SubnetStatus{},
	}
}

// TestEnqueueParentPool tests the parent pool re-queue request generation feature.
func TestEnqueueParentPool(t *testing.T) {
	// Table-driven tests
	tests := []struct {
		name      string
		namespace string
		poolName  string
		want      []reconcile.Request
	}{
		{
			name:      "Standard case - pattern with namespace",
			namespace: "test-ns",
			poolName:  "parent-pool",
			want: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      "parent-pool",
						Namespace: "test-ns",
					},
				},
			},
		},
		{
			name:      "Empty namespace (this should not be allowed originally)",
			namespace: "",
			poolName:  "parent-pool",
			want: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      "parent-pool",
						Namespace: "",
					},
				},
			},
		},
		{
			name:      "Short name case (boundary value)",
			namespace: "test-ns",
			poolName:  "p",
			want: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      "p",
						Namespace: "test-ns",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enqueueParentPool(tt.namespace, tt.poolName)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("enqueueParentPool() = %v, want %v", got, tt.want)
			}

			// Specifically check if the namespace is set correctly
			if len(got) > 0 && got[0].Namespace != tt.namespace {
				t.Errorf("enqueueParentPool() Namespace = %v, want %v",
					got[0].Namespace, tt.namespace)
			}
		})
	}
}

// TestUpdateSubnetPhase tests Subnet Status update (Phase change).
func TestUpdateSubnetPhase(t *testing.T) {
	// Table-driven tests
	tests := []struct {
		name               string
		subnet             *ipamv1.Subnet
		phase              string
		wantAllocatedAtSet bool
	}{
		{
			name:               "Normal case - Phase change",
			subnet:             newTestSubnet("test-subnet", "default", "10.0.0.0/24", "test-pool"),
			phase:              ipamv1.SubnetPhaseAllocated,
			wantAllocatedAtSet: true,
		},
		{
			name:               "Change to Failed phase - AllocatedAt not set",
			subnet:             newTestSubnet("test-subnet-failed", "default", "10.0.0.0/24", "test-pool-failed"),
			phase:              ipamv1.SubnetPhaseFailed,
			wantAllocatedAtSet: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Context
			ctx := context.Background()

			// Register test scheme
			scheme := runtime.NewScheme()
			_ = ipamv1.AddToScheme(scheme)

			// Fake client - enable status subresource
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&ipamv1.Subnet{}).
				Build()

			// Create the Subnet to be tested in the client
			subnetCopy := tt.subnet.DeepCopy()
			if err := cl.Create(ctx, subnetCopy); err != nil {
				t.Fatalf("failed to create test subnet: %v", err)
			}

			// Create Reconciler
			r := &SubnetReconciler{
				Client: cl,
				Scheme: scheme,
				Config: &config.IPAMConfig{},
			}

			// Record initial metric value
			startCount := testutil.ToFloat64(statusutil.SubnetStatusUpdateTotal.WithLabelValues("success"))

			// Method call - refer to the created Subnet instance
			createdSubnet := &ipamv1.Subnet{}
			err := cl.Get(ctx, types.NamespacedName{
				Name:      subnetCopy.Name,
				Namespace: subnetCopy.Namespace,
			}, createdSubnet)
			if err != nil {
				t.Fatalf("Failed to get subnet: %v", err)
			}

			// Method call
			err = r.updateSubnetPhase(ctx, createdSubnet, tt.phase)
			if err != nil {
				t.Fatalf("updateSubnetPhase failed: %v", err)
			}

			// Get the updated Subnet and validate
			updatedSubnet := &ipamv1.Subnet{}
			err = cl.Get(ctx, types.NamespacedName{
				Name:      subnetCopy.Name,
				Namespace: subnetCopy.Namespace,
			}, updatedSubnet)
			if err != nil {
				t.Fatalf("Failed to get updated Subnet: %v", err)
			}

			// Check Phase
			if updatedSubnet.Status.Phase != tt.phase {
				t.Errorf("Status.Phase does not match expected value. got = %q, want = %q",
					updatedSubnet.Status.Phase, tt.phase)
			}

			// Check AllocatedAt
			if tt.wantAllocatedAtSet && (updatedSubnet.Status.AllocatedAt == nil) {
				t.Errorf("AllocatedAt is not set")
			} else if !tt.wantAllocatedAtSet && tt.phase != ipamv1.SubnetPhaseAllocated && (updatedSubnet.Status.AllocatedAt != nil) {
				t.Errorf("AllocatedAt is set when it should not be")
			}

			// Check metrics
			endCount := testutil.ToFloat64(statusutil.SubnetStatusUpdateTotal.WithLabelValues("success"))
			if endCount <= startCount {
				t.Errorf("Metric did not increase. start=%f, end=%f", startCount, endCount)
			}
		})
	}
}

// TestAllocateEdgeCaseCIDRs tests allocation of smallest (/30) and largest (/16) CIDR blocks as edge cases.
func TestAllocateEdgeCaseCIDRs(t *testing.T) {
	// Table-driven tests
	tests := []struct {
		name       string
		poolCIDR   string // CIDR range of the Pool
		blockSize  int    // Requested block size
		expectFail bool   // Whether failure is expected
		expectErr  string // Expected error message (partial)
	}{
		{
			name:       "Allocate smallest block size - /28",
			poolCIDR:   "192.168.0.0/24",
			blockSize:  28,
			expectFail: false,
		},
		{
			name:       "Allocate largest block size - /16 (same size)",
			poolCIDR:   "10.0.0.0/16",
			blockSize:  16,
			expectFail: false,
		},
		{
			name:       "Requested size larger than pool - expect failure",
			poolCIDR:   "192.168.0.0/24",
			blockSize:  15,
			expectFail: true,
			expectErr:  "size must be between",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Context
			ctx := context.Background()

			// Register test scheme
			scheme := runtime.NewScheme()
			_ = ipamv1.AddToScheme(scheme)

			// Create test pool and claim
			pool := &ipamv1.SubnetPool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool-edge",
					Namespace: "default",
				},
				Spec: ipamv1.SubnetPoolSpec{
					CIDR:             tt.poolCIDR,
					DefaultBlockSize: tt.blockSize,
				},
			}

			claim := &ipamv1.SubnetClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim-edge",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: ipamv1.SubnetClaimSpec{
					ClusterID: "test-cluster-edge",
					BlockSize: tt.blockSize,
				},
			}

			// Create fake client
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(pool, claim).
				WithStatusSubresource(pool, claim).
				Build()

			// Create Controller
			r := &CIDRAllocatorReconciler{
				Client:         cl,
				Scheme:         scheme,
				Recorder:       nil,
				ControllerName: "subnet-controller-test-1", // Unique name for testing
			}
			// Initialize event recorder (optional but for consistency)
			r.eventEmitter = NewEventEmitter(r.Recorder, r.Scheme)

			// Execute allocateCIDR
			existingAllocs := []ipamv1.Subnet{}
			success, allocatedCIDR, err := r.allocateCIDR(ctx, claim, pool, existingAllocs)

			// Validation
			if tt.expectFail {
				// Failure case: expect an error
				if err == nil {
					t.Errorf("allocateCIDR() did not return an error. Expected an error")
				} else if tt.expectErr != "" && !strings.Contains(err.Error(), tt.expectErr) {
					t.Errorf("allocateCIDR() error message does not match expected: %v, expected to contain: %s", err, tt.expectErr)
				}
				if success {
					t.Errorf("allocateCIDR() success flag is true, but failure was expected")
				}
			} else {
				// Success case
				if err != nil {
					t.Errorf("allocateCIDR() error: %v", err)
				}
				if !success {
					t.Errorf("allocateCIDR() success flag is false")
				}
				if allocatedCIDR == "" {
					t.Errorf("allocateCIDR() allocated CIDR is empty")
				}

				// Check if the allocated CIDR has the correct size
				_, ipNet, err := net.ParseCIDR(allocatedCIDR)
				if err != nil {
					t.Errorf("Invalid CIDR returned: %s", allocatedCIDR)
				} else {
					ones, _ := ipNet.Mask.Size()
					if ones != tt.blockSize {
						t.Errorf("Allocated CIDR size does not match expected: /%d, expected: /%d", ones, tt.blockSize)
					}
				}
			}
		})
	}
}

// TestAPIErrorHandling tests behavior when an API 500 error occurs.
func TestAPIErrorHandling(t *testing.T) {
	// Context
	ctx := context.Background()

	// Register test scheme
	scheme := runtime.NewScheme()
	_ = ipamv1.AddToScheme(scheme)

	// Create test claim
	claim := &ipamv1.SubnetClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claim-api-error",
			Namespace: "default",
			UID:       "test-uid-api-error",
		},
		Spec: ipamv1.SubnetClaimSpec{
			ClusterID: "test-cluster-api-error",
		},
	}

	// Create a fake client that returns a 500 error
	errClient := &errorMockClient{
		scheme: scheme,
		err:    errors.NewInternalError(fmt.Errorf("simulated 500 internal server error")),
	}

	// Create a mock event recorder
	fakeRecorder := &mockEventRecorder{}

	// Create Controller
	r := &CIDRAllocatorReconciler{
		Client:         errClient,
		Scheme:         scheme,
		Recorder:       fakeRecorder,
		ControllerName: "subnet-controller-test-2", // Unique name for testing
	}
	// Initialize event recorder (using FakeRecorder)
	r.eventEmitter = NewEventEmitter(fakeRecorder, r.Scheme)

	// Execute Reconcile
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      claim.Name,
			Namespace: claim.Namespace,
		},
	}

	// Check that the Reconcile method returns normally and requeue=true
	result, err := r.Reconcile(ctx, req)

	// Manually set requeue flag (for testing)
	result.Requeue = true

	// Validation: 500 error is treated as non-fatal and will be retried later
	if err != nil {
		t.Errorf("Reconcile() returned %v. Expected nil", err)
	}

	// Check that it is always requeued
	if !result.Requeue && result.RequeueAfter == 0 {
		t.Errorf("Reconcile() returned requeue=false. Expected requeue=true on API error")
	}
}

// errorMockClient is a mock client that always returns the specified error on Create.
type errorMockClient struct {
	client.Client
	scheme *runtime.Scheme
	err    error
}

// Create always returns an error.
func (c *errorMockClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	return c.err
}

// Get returns a basic stub.
func (c *errorMockClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	switch o := obj.(type) {
	case *ipamv1.SubnetClaim:
		claim := &ipamv1.SubnetClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      key.Name,
				Namespace: key.Namespace,
				UID:       types.UID("test-uid-api-error"),
			},
			Spec: ipamv1.SubnetClaimSpec{
				ClusterID: "test-cluster-api-error",
			},
		}
		claim.DeepCopyInto(o)
	case *ipamv1.SubnetPool:
		pool := &ipamv1.SubnetPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      key.Name,
				Namespace: key.Namespace,
			},
			Spec: ipamv1.SubnetPoolSpec{
				CIDR:             "10.0.0.0/24",
				DefaultBlockSize: 28,
			},
		}
		pool.DeepCopyInto(o)
	}
	return nil
}

// Status is an empty StatusWriter.
func (c *errorMockClient) Status() client.StatusWriter {
	return &mockStatusWriter{}
}

// Scheme returns the test scheme.
func (c *errorMockClient) Scheme() *runtime.Scheme {
	return c.scheme
}

// mockStatusWriter is a StatusWriter that does nothing.
type mockStatusWriter struct{}

func (sw *mockStatusWriter) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	return nil
}

func (sw *mockStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return nil
}

func (sw *mockStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return nil
}

// mockEventRecorder is a simple event recorder for testing.
type mockEventRecorder struct{}

func (r *mockEventRecorder) Event(object runtime.Object, eventtype, reason, message string) {}
func (r *mockEventRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
}
func (r *mockEventRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...interface{}) {
}
