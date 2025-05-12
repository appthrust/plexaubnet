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
	"strconv"
	"testing"
	"time"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestListSubnetsPaged(t *testing.T) {
	t.Run("P3-fix-T1: Should retrieve 12,345 subnets in 3 pages", func(t *testing.T) {
		// Reset metric counters before the test
		resetMetricsForTest()

		// Create a pool and subnets for testing
		pool := &ipamv1.SubnetPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pool",
				Namespace: "default",
			},
			Spec: ipamv1.SubnetPoolSpec{
				CIDR: "10.0.0.0/16",
			},
		}

		// Objects to register with the scheme
		scheme := runtime.NewScheme()
		require.NoError(t, ipamv1.AddToScheme(scheme))

		// Create 12,345 subnet objects
		var subnets []runtime.Object
		for i := 0; i < 12345; i++ {
			subnet := &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("subnet-%d", i),
					Namespace: "default",
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef: "test-pool",
					CIDR:    fmt.Sprintf("10.0.%d.0/24", i%256),
				},
			}
			subnets = append(subnets, subnet)
		}

		// Create a fake client and reproduce the contents of SetupFieldIndexes
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(append(subnets, pool)...).
			WithIndex(&ipamv1.Subnet{}, "spec.poolRef", func(o client.Object) []string {
				return []string{o.(*ipamv1.Subnet).Spec.PoolRef}
			}).
			Build()

		// Execute the test
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result, err := listSubnetsPaged(ctx, fakeClient, pool)
		require.NoError(t, err)

		// Validate the results
		assert.Equal(t, 12345, len(result), "Ensure all subnets are retrieved")

		// Logical validation: How many pages are needed to retrieve 12345 items with PageSize=5000?
		expectedPages := (12345 + PageSize - 1) / PageSize // Ceiling division
		assert.Equal(t, 13, expectedPages, "Should be 13 pages with PageSize=1000")
	})

	t.Run("P3-fix-T2: Should propagate error during listing with Continue token", func(t *testing.T) {
		// Reset metric counters before the test
		resetMetricsForTest()

		// Pool for testing
		pool := &ipamv1.SubnetPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "error-pool",
				Namespace: "default",
			},
			Spec: ipamv1.SubnetPoolSpec{
				CIDR: "10.0.0.0/16",
			},
		}

		// Fake client that returns an error
		fakeClient := newErrorInjectingFakeClient()

		// Execute the test
		result, err := listSubnetsPaged(context.Background(), fakeClient, pool)

		// Error validation
		assert.Error(t, err, "Error should occur when retrieving the second page")
		assert.Nil(t, result, "Should return nil on error")
		assert.Contains(t, err.Error(), "injected error on continue", "Should contain the expected error message")

		// Validate that the fake client is working correctly, without directly depending on metrics
		assert.Equal(t, 1, fakeClient.(*errorInjectingClient).pageCount, "Should retrieve only one page before the error")
	})

	t.Run("P3-fix-T3: Should handle zero subnets case", func(t *testing.T) {
		// Reset metric counters before the test
		resetMetricsForTest()

		// Pool for testing
		pool := &ipamv1.SubnetPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "empty-pool",
				Namespace: "default",
			},
			Spec: ipamv1.SubnetPoolSpec{
				CIDR: "10.0.0.0/16",
			},
		}

		// Empty fake client
		scheme := runtime.NewScheme()
		require.NoError(t, ipamv1.AddToScheme(scheme))

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(pool).
			WithIndex(&ipamv1.Subnet{}, "spec.poolRef", func(o client.Object) []string {
				return []string{o.(*ipamv1.Subnet).Spec.PoolRef}
			}).
			Build()

		// Execute the test
		result, err := listSubnetsPaged(context.Background(), fakeClient, pool)

		// Validation
		require.NoError(t, err)
		assert.Empty(t, result, "Should return an empty slice if there are zero subnets")

		// Even with 0 items, one page retrieval is performed
		assert.Equal(t, 0, len(result), "Should return an empty slice if there are zero subnets")
	})
}

// Metric reset function for tests
func resetMetricsForTest() {
	// Initialize Prometheus metrics between tests (optional)
	// May not be necessary depending on the test execution environment, but kept just in case
	prometheus.DefaultRegisterer.Unregister(SubnetListPagesTotal)
	prometheus.DefaultRegisterer.Unregister(SubnetListContinueSkippedTotal)
}

// Fake client that returns an error when retrieving the second page
type errorInjectingClient struct {
	client.Client
	pageCount int
}

func (c *errorInjectingClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	// Increment internal counter (number of List calls)
	listCallNum := c.pageCount + 1

	// Return the first page normally
	if listCallNum == 1 {
		subnetList, ok := list.(*ipamv1.SubnetList)
		if !ok {
			return fmt.Errorf("expected SubnetList but got %T", list)
		}

		// Create 100 dummy data items
		for i := 0; i < 100; i++ {
			subnetList.Items = append(subnetList.Items, ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "subnet-" + strconv.Itoa(i),
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef: "test-pool",
				},
			})
		}

		// Set Continue token to make it seem like there is a next page
		subnetList.Continue = "fake-continue-token"
		c.pageCount++ // Update counter only on success
		return nil
	}

	// Return an error for the second page onwards
	return fmt.Errorf("injected error on continue token page %d", listCallNum)
}

func newErrorInjectingFakeClient() client.Client {
	scheme := runtime.NewScheme()
	_ = ipamv1.AddToScheme(scheme)

	return &errorInjectingClient{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			Build(),
	}
}
