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

package statusutil

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic" // PatchFunc内で使うため維持
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

// mockStatusClient is a mock for Client.Status()
type mockStatusClient struct {
	client.Client
	StatusWriter client.StatusWriter
	SubnetToGet  *ipamv1.Subnet                                                                                     // Subnet to return with Get (default behavior)
	GetErr       error                                                                                              // Error to return with Get (default behavior)
	GetFunc      func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error // Function to override Get behavior
}

func (c *mockStatusClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.GetFunc != nil {
		return c.GetFunc(ctx, key, obj, opts...)
	}
	// Default behavior if GetFunc is not set
	if c.GetErr != nil {
		return c.GetErr
	}
	if c.SubnetToGet != nil && key.Name == c.SubnetToGet.Name && key.Namespace == c.SubnetToGet.Namespace {
		subnetToReturn, ok := obj.(*ipamv1.Subnet)
		if !ok {
			return errors.New("mock Get: obj is not *ipamv1.Subnet")
		}
		c.SubnetToGet.DeepCopyInto(subnetToReturn)
		return nil
	}
	return apierrors.NewNotFound(schema.GroupResource{Group: ipamv1.GroupVersion.Group, Resource: "subnets"}, key.Name)
}

func (c *mockStatusClient) Status() client.StatusWriter {
	if c.StatusWriter == nil {
		panic("StatusWriter is nil in mockStatusClient")
	}
	return c.StatusWriter
}

// mockStatusWriter mocks status updates
type mockStatusWriter struct {
	PatchFunc func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error
}

func (m *mockStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return errors.New("Update is not implemented in mock - use Patch")
}

func (m *mockStatusWriter) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	return errors.New("Create is not implemented in mock - use Patch")
}

// Patch is the mock status update process
func (m *mockStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if m.PatchFunc != nil {
		return m.PatchFunc(ctx, obj, patch, opts...)
	}
	return errors.New("PatchFunc not set in mockStatusWriter")
}

// newTestSubnet generates a Subnet instance for testing
func newTestSubnet(name, ns, cidr, pool string) *ipamv1.Subnet {
	return &ipamv1.Subnet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Subnet",
			APIVersion: ipamv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			UID:       types.UID("subnet-uid-" + name),
		},
		Spec: ipamv1.SubnetSpec{
			CIDR:      cidr,
			PoolRef:   pool,
			ClusterID: "test-cluster",
		},
		Status: ipamv1.SubnetStatus{},
	}
}

// TestPatchSubnetStatus tests the PatchSubnetStatus function
func TestPatchSubnetStatus(t *testing.T) {
	// Reset metrics
	SubnetStatusUpdateTotal.Reset()
	SubnetStatusRetryTotal.Reset()

	// Test cases
	tests := []struct {
		name string
		// Return Conflict in PatchFunc up to conflictUntil times
		// (0 means no Conflict, 1 means Conflict on the first Patch call)
		conflictUntil int
		// Error to return in PatchFunc or GetFunc (other than Conflict)
		injectErr error
		// Whether to inject an error on Get
		errorOnGet bool
		expSuccess bool
		// Expected number of retries (value used for SubnetStatusRetryTotal label)
		// In PatchSubnetStatus, it's labeled with retryCount - 1, so
		// if retried once (total 2 attempts), expRetries = 1
		expRetries int
		// Actual number of times Patch is called
		expPatchCalls int
	}{
		{
			name:          "Normal case - success on first attempt",
			conflictUntil: 0,
			injectErr:     nil,
			expSuccess:    true,
			expRetries:    0,
			expPatchCalls: 1,
		},
		{
			name:          "Conflict once -> success",
			conflictUntil: 1,
			injectErr:     nil,
			expSuccess:    true,
			expRetries:    1,
			expPatchCalls: 2,
		},
		{
			name:          "Conflict 5 times -> success (within retry limit)",
			conflictUntil: 5, // Success on the 6th attempt after 5 conflicts
			injectErr:     nil,
			expSuccess:    true,
			expRetries:    5, // Retried 5 times (retryCount - 1 becomes 0, 1, 2, 3, 4)
			expPatchCalls: 6, // Initial attempt + 5 retries
		},
		{
			name: "Conflict limit exceeded -> failure",
			// wait.Backoff{Steps: 5} means a maximum of 5 retries.
			// That is, initial attempt + 5 retries = 6 attempts in total.
			// If a Conflict occurs on the 6th attempt (when conflictUntil = 5),
			// no further retries will occur, and it will result in an error.
			conflictUntil: 5,                                                                                                                                                                     // If it continues to conflict 5 times, the 6th attempt will also conflict. This is the retry limit.
			injectErr:     apierrors.NewConflict(schema.GroupResource{Group: ipamv1.GroupVersion.Group, Resource: "subnets"}, "test-subnet", errors.New("simulated conflict after max retries")), // Error to return finally
			expSuccess:    false,                                                                                                                                                                 // The 6th attempt is also a Conflict, so it will eventually error out
			expRetries:    5,                                                                                                                                                                     // Retried 5 times
			expPatchCalls: 6,                                                                                                                                                                     // Patch will be attempted 6 times
		},
		{
			name:          "Error on Get -> failure",
			conflictUntil: 0, // Does not reach Patch
			injectErr:     apierrors.NewServiceUnavailable("get failed"),
			errorOnGet:    true,
			expSuccess:    false,
			expRetries:    0,
			expPatchCalls: 0, // Patch is not called
		},
		{
			name:          "Persistent error on Patch -> failure",
			conflictUntil: 0,
			injectErr:     errors.New("permanent patch error"),
			expSuccess:    false,
			expRetries:    0,
			expPatchCalls: 1, // Attempted once
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			subnet := newTestSubnet("test-subnet", "default", "10.0.0.0/24", "test-pool")
			var patchCallCount int32
			var getCallCount int32 // Count Get calls

			// Override mockStatusClient's Get method to count calls
			getOverridden := false

			statusWriter := &mockStatusWriter{
				PatchFunc: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					currentAttempt := atomic.AddInt32(&patchCallCount, 1)
					if int(currentAttempt) <= tt.conflictUntil {
						grp := schema.GroupResource{Group: ipamv1.GroupVersion.Group, Resource: "subnets"}
						return apierrors.NewConflict(grp, subnet.Name, errors.New("simulated conflict"))
					}
					if tt.injectErr != nil && !tt.errorOnGet { // Error during Patch
						return tt.injectErr
					}
					// updatedSubnet, ok := obj.(*ipamv1.Subnet) // Commented out as it's unused
					// if !ok {
					// 	return errors.New("obj is not *ipamv1.Subnet in mock Patch")
					// }
					// In an actual Patch, the API server performs the merge. Do nothing here.
					// Mutate is called within PatchSubnetStatus, and the result is Patched.
					return nil
				},
			}

			// Client combining mock and schema
			scheme := runtime.NewScheme()
			_ = ipamv1.AddToScheme(scheme)
			// Use mockClient instead of cl, and correct declaration location
			mockClient := &mockStatusClient{
				Client:       fake.NewClientBuilder().WithScheme(scheme).WithObjects(subnet.DeepCopy()).Build(),
				StatusWriter: statusWriter,
				SubnetToGet:  subnet.DeepCopy(),
			}
			if tt.errorOnGet && tt.injectErr != nil {
				mockClient.GetFunc = func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					atomic.AddInt32(&getCallCount, 1)
					return tt.injectErr // Return the error to inject
				}
				getOverridden = true
			} else {
				// Even if GetFunc is not set, allow counting calls in the default Get behavior.
				// However, in this test case, it's controlled by GetErr or SubnetToGet,
				// so GetFunc can usually remain nil if not errorOnGet.
				// If you only want to count, wrap the default behavior like this:
				// defaultGet := mockClient.Get // This would be a recursive call, so NO
				// By not setting GetFunc, the default Get of mockStatusClient is called.
			}

			startUpdateSuccessCount := testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("success"))
			startUpdateUnchangedCount := testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("unchanged"))
			startUpdateErrorCount := testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("error"))
			retryMetricsStart := make(map[string]float64)
			for i := 0; i <= 5; i++ {
				retryMetricsStart[strconv.Itoa(i)] = testutil.ToFloat64(SubnetStatusRetryTotal.WithLabelValues(strconv.Itoa(i)))
			}

			// Use mockClient instead of cl
			err := PatchSubnetStatus(ctx, mockClient, subnet, func(status *ipamv1.SubnetStatus) {
				status.Phase = ipamv1.SubnetPhaseAllocated
				now := metav1.NewTime(time.Now())
				status.AllocatedAt = &now
			})

			if tt.expSuccess {
				assert.NoError(t, err, "Should succeed")
				assert.Equal(t, float64(1), testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("success"))-startUpdateSuccessCount, "Success metric")
			} else {
				assert.Error(t, err, "Should fail")
				if tt.injectErr != nil {
					assert.True(t, errors.Is(err, tt.injectErr) || apierrors.IsConflict(err) || errors.Is(err, context.Canceled), "Error type mismatch. Expected %v or Conflict or Canceled, got %v", tt.injectErr, err)
				}
				assert.Equal(t, float64(1), testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("error"))-startUpdateErrorCount, "Error metric")
			}
			assert.Equal(t, float64(0), testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("unchanged"))-startUpdateUnchangedCount, "Unchanged metric")
			assert.Equal(t, int32(tt.expPatchCalls), patchCallCount, "Patch call count")

			if tt.expRetries > 0 {
				assert.Equal(t, float64(1), testutil.ToFloat64(SubnetStatusRetryTotal.WithLabelValues(strconv.Itoa(tt.expRetries)))-retryMetricsStart[strconv.Itoa(tt.expRetries)],
					"Retry metric for %d retries", tt.expRetries)
			}
			for i := 0; i <= 5; i++ {
				if i != tt.expRetries {
					assert.Equal(t, float64(0), testutil.ToFloat64(SubnetStatusRetryTotal.WithLabelValues(strconv.Itoa(i)))-retryMetricsStart[strconv.Itoa(i)],
						"Retry metric for %d retries should not change", i)
				}
			}
			if getOverridden { // Validate Get call count (only for Get error cases)
				if tt.errorOnGet && tt.expPatchCalls == 0 { // If Get errors and Patch is not reached
					assert.GreaterOrEqual(t, atomic.LoadInt32(&getCallCount), int32(1), "Get should be called at least once if errorOnGet is true and patch is not called")
				}
			}
		})
	}
}

// TestPatchSubnetStatusWithCancel tests behavior on context cancellation
func TestPatchSubnetStatusWithCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	subnet := newTestSubnet("test-context-cancel", "default", "10.0.0.0/24", "test-pool")
	var patchCalledCount int32 // Renamed patchCalled to patchCalledCount for consistency

	statusWriter := &mockStatusWriter{
		PatchFunc: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			atomic.AddInt32(&patchCalledCount, 1) // Use patchCalledCount
			return ctx.Err()
		},
	}
	mockClientForCancel := &mockStatusClient{ // Renamed cl to mockClientForCancel
		Client:       fake.NewClientBuilder().Build(),
		StatusWriter: statusWriter,
		SubnetToGet:  subnet.DeepCopy(),
	}

	err := PatchSubnetStatus(ctx, mockClientForCancel, subnet, func(status *ipamv1.SubnetStatus) { // Use mockClientForCancel
		status.Phase = ipamv1.SubnetPhaseAllocated
	})

	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "Error should be context.Canceled. Got: %v", err)
	// Due to PatchSubnetStatus implementation, Get is called first. If Get returns context.Canceled, Patch is not called.
	// If Get succeeds, Patch is called and returns context.Canceled.
	// Here, Get is mocked to succeed, so Patch should be called once.
	assert.Equal(t, int32(1), atomic.LoadInt32(&patchCalledCount), "Patch should be called once even if context is canceled, if Get succeeds") // Use patchCalledCount
}

// TestPatchSubnetStatusUnchanged tests the case where status is not changed
func TestPatchSubnetStatusUnchanged(t *testing.T) {
	ctx := context.Background()
	subnet := newTestSubnet("test-unchanged", "default", "10.0.0.0/24", "test-pool")
	subnet.Status.Phase = ipamv1.SubnetPhaseAllocated

	scheme := runtime.NewScheme()
	_ = ipamv1.AddToScheme(scheme)
	var patchCalled atomic.Bool

	statusWriter := &mockStatusWriter{
		PatchFunc: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patchCalled.Store(true)
			return nil
		},
	}
	cl := &mockStatusClient{
		Client:       fake.NewClientBuilder().WithScheme(scheme).WithObjects(subnet.DeepCopy()).Build(),
		StatusWriter: statusWriter,
		SubnetToGet:  subnet.DeepCopy(),
	}
	startUnchangedCount := testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("unchanged"))
	startSuccessCount := testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("success"))
	startErrorCount := testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("error"))

	err := PatchSubnetStatus(ctx, cl, subnet, func(status *ipamv1.SubnetStatus) {
		status.Phase = ipamv1.SubnetPhaseAllocated // No change
	})

	assert.NoError(t, err, "Error should be nil for unchanged status")
	assert.False(t, patchCalled.Load(), "Patch should not be called if status is unchanged")
	assert.Equal(t, float64(1), testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("unchanged"))-startUnchangedCount, "Unchanged metric should increment by 1")
	assert.Equal(t, float64(0), testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("success"))-startSuccessCount, "Success metric should not change for unchanged status")
	assert.Equal(t, float64(0), testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("error"))-startErrorCount, "Error metric should not change for unchanged status")
}

// TestPatchSubnetStatusWithNotFound tests the case where Get returns NotFound
func TestPatchSubnetStatusWithNotFound(t *testing.T) {
	ctx := context.Background()
	subnet := newTestSubnet("test-notfound", "default", "10.0.0.0/24", "test-pool")

	// Reset metrics
	SubnetStatusUpdateTotal.Reset()

	// Mock setup
	var patchCalled atomic.Bool
	var mutateCalled atomic.Bool

	statusWriter := &mockStatusWriter{
		PatchFunc: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patchCalled.Store(true)
			return nil
		},
	}

	// mockClient that returns NotFound error
	cl := &mockStatusClient{
		Client:       fake.NewClientBuilder().Build(),
		StatusWriter: statusWriter,
		GetErr:       apierrors.NewNotFound(schema.GroupResource{Group: ipamv1.GroupVersion.Group, Resource: "subnets"}, subnet.Name),
	}

	startSkippedCount := testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("skipped_deleted"))

	// Execute test
	err := PatchSubnetStatus(ctx, cl, subnet, func(status *ipamv1.SubnetStatus) {
		mutateCalled.Store(true)
		status.Phase = ipamv1.SubnetPhaseAllocated
	})

	// Verification
	assert.NoError(t, err, "Should not return error on NotFound")
	assert.False(t, patchCalled.Load(), "Patch should not be called on NotFound")
	assert.False(t, mutateCalled.Load(), "mutate function should not be called on NotFound")
	assert.Equal(t, float64(1), testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("skipped_deleted"))-startSkippedCount,
		"skipped_deleted metric should increment")
}

// TestPatchSubnetStatusWithDeletionTimestamp tests resources marked for deletion
func TestPatchSubnetStatusWithDeletionTimestamp(t *testing.T) {
	ctx := context.Background()
	subnet := newTestSubnet("test-deleting", "default", "10.0.0.0/24", "test-pool")

	// Set deletion timestamp
	now := metav1.Now()
	subnet.DeletionTimestamp = &now

	// Reset metrics
	SubnetStatusUpdateTotal.Reset()

	// Mock setup
	var patchCalled atomic.Bool
	var mutateCalled atomic.Bool

	statusWriter := &mockStatusWriter{
		PatchFunc: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patchCalled.Store(true)
			return nil
		},
	}

	cl := &mockStatusClient{
		Client:       fake.NewClientBuilder().Build(),
		StatusWriter: statusWriter,
		SubnetToGet:  subnet.DeepCopy(),
	}

	startSkippedCount := testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("skipped_deleting"))

	// Execute test
	err := PatchSubnetStatus(ctx, cl, subnet, func(status *ipamv1.SubnetStatus) {
		mutateCalled.Store(true)
		status.Phase = ipamv1.SubnetPhaseAllocated
	})

	// Verification
	assert.NoError(t, err, "Should not return error for resources marked for deletion")
	assert.False(t, patchCalled.Load(), "Patch should not be called for resources marked for deletion")
	assert.True(t, mutateCalled.Load(), "mutate function should still be called for resources marked for deletion")
	assert.Equal(t, float64(1), testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("skipped_deleting"))-startSkippedCount,
		"skipped_deleting metric should increment")
}

// TestPatchSubnetStatusWithPatchNotFound tests when a NotFound error is returned during Patch
func TestPatchSubnetStatusWithPatchNotFound(t *testing.T) {
	ctx := context.Background()
	subnet := newTestSubnet("test-patch-notfound", "default", "10.0.0.0/24", "test-pool")

	// Reset metrics
	SubnetStatusUpdateTotal.Reset()

	// Mock setup
	var patchCalled atomic.Bool
	var mutateCalled atomic.Bool

	statusWriter := &mockStatusWriter{
		PatchFunc: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patchCalled.Store(true)
			// Return NotFound during Patch
			return apierrors.NewNotFound(schema.GroupResource{Group: ipamv1.GroupVersion.Group, Resource: "subnets"}, subnet.Name)
		},
	}

	cl := &mockStatusClient{
		Client:       fake.NewClientBuilder().Build(),
		StatusWriter: statusWriter,
		SubnetToGet:  subnet.DeepCopy(),
	}

	startSkippedCount := testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("skipped_deleted"))

	// Execute test
	err := PatchSubnetStatus(ctx, cl, subnet, func(status *ipamv1.SubnetStatus) {
		mutateCalled.Store(true)
		status.Phase = ipamv1.SubnetPhaseAllocated
	})

	// Verification
	assert.NoError(t, err, "NotFound during Patch should not return an error")
	assert.True(t, patchCalled.Load(), "Patch should be called")
	assert.True(t, mutateCalled.Load(), "mutate function should be called")
	assert.Equal(t, float64(1), testutil.ToFloat64(SubnetStatusUpdateTotal.WithLabelValues("skipped_deleted"))-startSkippedCount,
		"skipped_deleted metric should increment")
}
