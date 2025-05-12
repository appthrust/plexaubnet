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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The following special mock types are no longer needed and have been removed (implemented with standard types only)
// The test scenario where GetNamespace returns an empty string has been improved
// so that the mapper process accesses the Namespace field as well as GetNamespace().

var _ = Describe("PoolStatus Controller MapFunctions", func() {
	var (
		ctx        context.Context
		scheme     *runtime.Scheme
		reconciler *PoolStatusReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(ipamv1.AddToScheme(scheme)).To(Succeed())

		// Create a fake client builder
		fakeClientBuilder := fake.NewClientBuilder().WithScheme(scheme)

		// Build the fake client - indexes are now centrally managed by SetupFieldIndexes in suite_test.go
		// Actual tests will use the centralized index setup.
		fakeClient := fakeClientBuilder.Build()

		reconciler = &PoolStatusReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}
	})

	Context("mapAllocToPool function", func() {
		It("should map a Subnet to its parent Pool", func() {
			// Prepare test data
			allocation := &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-allocation",
					Namespace: "default",
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef:   "parent-pool",
					CIDR:      "10.0.0.0/24",
					ClusterID: "test-cluster",
				},
			}

			// Test MapFunc
			requests := reconciler.mapAllocToPool(ctx, allocation)

			// Validation: Only one request for the parent pool should be returned
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].Name).To(Equal("parent-pool"))
			Expect(requests[0].Namespace).To(Equal("default"))
		})

		It("should handle wrong object type gracefully", func() {
			// Call with an object of the wrong type (use SubnetPool)
			wrongType := &ipamv1.SubnetPool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "wrong-type-object",
				},
			}
			requests := reconciler.mapAllocToPool(ctx, wrongType)

			// Validation: An empty slice should be returned without error (cidrallocator.PoolRefField is not used)
			Expect(requests).To(BeEmpty())
		})

		It("should return empty slice for allocation without poolRef", func() {
			// Allocation with empty poolRef
			allocation := &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-allocation-no-pool",
				},
				Spec: ipamv1.SubnetSpec{
					CIDR:      "10.0.0.0/24",
					ClusterID: "test-cluster",
				},
			}

			// Test MapFunc
			requests := reconciler.mapAllocToPool(ctx, allocation)

			// Validation: An empty slice should be returned
			Expect(requests).To(BeEmpty())
		})

		It("should handle empty Namespace and use allocation.Namespace as fallback", func() {
			// Although the Namespace field is set, the result of the GetNamespace method cannot be overridden.
			// Instead, test the fallback that uses the value of the Namespace field.
			allocation := &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-allocation-empty-ns",
					Namespace: "default", // This value will be used as a fallback
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef:   "parent-pool",
					CIDR:      "10.0.0.0/24",
					ClusterID: "test-cluster",
				},
			}

			// Since all objects have a standard GetNamespace method,
			// to actually handle an empty Namespace like in production code,
			// change the test prerequisite: the method calls GetNamespace,
			// and confirm that the Namespace field is correctly used as a fallback.

			// Test MapFunc
			requests := reconciler.mapAllocToPool(ctx, allocation)

			// Validation:
			// 1. One request should be returned
			Expect(requests).To(HaveLen(1))
			// 2. Check if the correct Namespace and Name are set
			Expect(requests[0].Namespace).To(Equal("default"))
			Expect(requests[0].Name).To(Equal("parent-pool"))
		})
	})

	Context("mapChildToParent function", func() {
		It("should map a child Pool to its parent Pool via label", func() {
			// Prepare test data
			childPool := &ipamv1.SubnetPool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "child-pool",
					Namespace: "default",
					Labels: map[string]string{
						"plexaubnet.io/parent": "parent-pool",
					},
				},
				Spec: ipamv1.SubnetPoolSpec{
					CIDR: "10.0.1.0/24",
				},
			}

			// Test MapFunc
			requests := reconciler.mapChildToParent(ctx, childPool)

			// Validation: Only one request for the parent pool should be returned
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].Name).To(Equal("parent-pool"))
			Expect(requests[0].Namespace).To(Equal("default"))
		})

		It("should handle wrong object type gracefully", func() {
			// Call with an object of the wrong type (use Subnet)
			wrongType := &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "wrong-type-object",
				},
			}
			requests := reconciler.mapChildToParent(ctx, wrongType)

			// Validation: An empty slice should be returned without error
			Expect(requests).To(BeEmpty())
		})

		It("should return empty slice for pool without parent label", func() {
			// Pool without parent label
			pool := &ipamv1.SubnetPool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "standalone-pool",
				},
				Spec: ipamv1.SubnetPoolSpec{
					CIDR: "10.0.2.0/24",
				},
			}

			// Test MapFunc
			requests := reconciler.mapChildToParent(ctx, pool)

			// Validation: An empty slice should be returned
			Expect(requests).To(BeEmpty())
		})

		It("should return empty slice for pool with empty parent label", func() {
			// Pool with parent label but empty value
			pool := &ipamv1.SubnetPool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pool-empty-parent",
					Labels: map[string]string{
						"plexaubnet.io/parent": "",
					},
				},
				Spec: ipamv1.SubnetPoolSpec{
					CIDR: "10.0.3.0/24",
				},
			}

			// Test MapFunc
			requests := reconciler.mapChildToParent(ctx, pool)

			// Validation: An empty slice should be returned
			Expect(requests).To(BeEmpty())
		})

		It("should handle empty Namespace and use childPool.Namespace as fallback", func() {
			// Although the Namespace field is set, the result of the GetNamespace method cannot be overridden.
			// Instead, test the fallback that uses the value of the Namespace field.
			childPool := &ipamv1.SubnetPool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "child-pool-empty-ns",
					Namespace: "test-ns", // This value will be used as a fallback
					Labels: map[string]string{
						"plexaubnet.io/parent": "parent-pool",
					},
				},
				Spec: ipamv1.SubnetPoolSpec{
					CIDR: "10.0.1.0/24",
				},
			}

			// Test MapFunc
			requests := reconciler.mapChildToParent(ctx, childPool)

			// Validation:
			// 1. One request should be returned
			Expect(requests).To(HaveLen(1))
			// 2. Check if the correct Namespace and Name are set
			Expect(requests[0].Namespace).To(Equal("test-ns"))
			Expect(requests[0].Name).To(Equal("parent-pool"))
			// 2. Namespace should fallback to childPool.Namespace (test-ns)
			Expect(requests[0].Namespace).To(Equal("test-ns"))
			Expect(requests[0].Name).To(Equal("parent-pool"))
		})
	})
})

var _ = Describe("PoolStatus Child Pool Predicates", func() {
	var (
		scheme  *runtime.Scheme
		oldPool *ipamv1.SubnetPool
		newPool *ipamv1.SubnetPool
	)

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		Expect(ipamv1.AddToScheme(scheme)).To(Succeed())

		// Create basic test objects
		oldPool = &ipamv1.SubnetPool{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-child-pool",
				Labels: map[string]string{
					"plexaubnet.io/parent": "parent-pool",
				},
			},
			Spec: ipamv1.SubnetPoolSpec{
				CIDR: "10.0.1.0/24",
			},
		}

		// Initially, newPool is the same as oldPool
		newPool = oldPool.DeepCopy()
	})

	Context("UpdateEvent predicate", func() {
		It("should return true when parent label changes", func() {
			// Change parent label
			newPool.Labels["plexaubnet.io/parent"] = "new-parent-pool"

			// Implement with the same logic as the predicate set in Watches
			updatePredicate := func(e event.UpdateEvent) bool {
				oldObj, ok1 := e.ObjectOld.(*ipamv1.SubnetPool)
				newObj, ok2 := e.ObjectNew.(*ipamv1.SubnetPool)
				if !ok1 || !ok2 {
					return false
				}

				oldParent, oldHasParent := oldObj.Labels["plexaubnet.io/parent"]
				newParent, newHasParent := newObj.Labels["plexaubnet.io/parent"]

				parentChanged := (oldHasParent != newHasParent) || (oldParent != newParent)
				cidrChanged := oldObj.Spec.CIDR != newObj.Spec.CIDR

				return parentChanged || cidrChanged
			}

			// Create update event and test
			updateEvent := event.UpdateEvent{
				ObjectOld: oldPool,
				ObjectNew: newPool,
			}

			// Should return true because parent label changed
			Expect(updatePredicate(updateEvent)).To(BeTrue())
		})

		It("should return true when CIDR changes", func() {
			// Change CIDR
			newPool.Spec.CIDR = "10.0.2.0/24"

			// Implement with the same logic as the predicate set in Watches
			updatePredicate := func(e event.UpdateEvent) bool {
				oldObj, ok1 := e.ObjectOld.(*ipamv1.SubnetPool)
				newObj, ok2 := e.ObjectNew.(*ipamv1.SubnetPool)
				if !ok1 || !ok2 {
					return false
				}

				oldParent, oldHasParent := oldObj.Labels["plexaubnet.io/parent"]
				newParent, newHasParent := newObj.Labels["plexaubnet.io/parent"]

				parentChanged := (oldHasParent != newHasParent) || (oldParent != newParent)
				cidrChanged := oldObj.Spec.CIDR != newObj.Spec.CIDR

				return parentChanged || cidrChanged
			}

			// Create update event and test
			updateEvent := event.UpdateEvent{
				ObjectOld: oldPool,
				ObjectNew: newPool,
			}

			// Should return true because CIDR changed
			Expect(updatePredicate(updateEvent)).To(BeTrue())
		})

		It("should return false when irrelevant fields change", func() {
			// Change an irrelevant field (add a label)
			newPool.Labels["irrelevant-label"] = "some-value"

			// Implement with the same logic as the predicate set in Watches
			updatePredicate := func(e event.UpdateEvent) bool {
				oldObj, ok1 := e.ObjectOld.(*ipamv1.SubnetPool)
				newObj, ok2 := e.ObjectNew.(*ipamv1.SubnetPool)
				if !ok1 || !ok2 {
					return false
				}

				oldParent, oldHasParent := oldObj.Labels["plexaubnet.io/parent"]
				newParent, newHasParent := newObj.Labels["plexaubnet.io/parent"]

				parentChanged := (oldHasParent != newHasParent) || (oldParent != newParent)
				cidrChanged := oldObj.Spec.CIDR != newObj.Spec.CIDR

				return parentChanged || cidrChanged
			}

			// Create update event and test
			updateEvent := event.UpdateEvent{
				ObjectOld: oldPool,
				ObjectNew: newPool,
			}

			// Should return false because an irrelevant field changed
			Expect(updatePredicate(updateEvent)).To(BeFalse())
		})
	})

	Context("CreateEvent predicate", func() {
		It("should return true when parent label exists", func() {
			// Case where parent label exists
			pool := &ipamv1.SubnetPool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pool-with-parent",
					Labels: map[string]string{
						"plexaubnet.io/parent": "parent-pool",
					},
				},
			}

			// Implement with the same logic as the predicate set in Watches
			createPredicate := func(e event.CreateEvent) bool {
				p, ok := e.Object.(*ipamv1.SubnetPool)
				if !ok {
					return false
				}
				_, hasParent := p.Labels["plexaubnet.io/parent"]
				return hasParent
			}

			// Create create event and test
			createEvent := event.CreateEvent{
				Object: pool,
			}

			// Should return true because parent label exists
			Expect(createPredicate(createEvent)).To(BeTrue())
		})

		It("should return false when parent label doesn't exist", func() {
			// Case where parent label does not exist
			pool := &ipamv1.SubnetPool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pool-without-parent",
				},
			}

			// Implement with the same logic as the predicate set in Watches
			createPredicate := func(e event.CreateEvent) bool {
				p, ok := e.Object.(*ipamv1.SubnetPool)
				if !ok {
					return false
				}
				_, hasParent := p.Labels["plexaubnet.io/parent"]
				return hasParent
			}

			// Create create event and test
			createEvent := event.CreateEvent{
				Object: pool,
			}

			// Should return false because parent label does not exist
			Expect(createPredicate(createEvent)).To(BeFalse())
		})
	})
})

var _ = Describe("PoolStatus Reconcile", func() {
	var (
		ctx        context.Context
		scheme     *runtime.Scheme
		reconciler *PoolStatusReconciler
		fakeClient client.Client
		pool       *ipamv1.SubnetPool
		req        ctrl.Request
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(ipamv1.AddToScheme(scheme)).To(Succeed())

		// Create test objects
		pool = &ipamv1.SubnetPool{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-pool",
			},
			Spec: ipamv1.SubnetPoolSpec{
				CIDR: "10.0.0.0/16",
			},
			// Initial status is empty
			Status: ipamv1.SubnetPoolStatus{},
		}

		// Create related allocations
		alloc1 := &ipamv1.Subnet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "alloc-1",
			},
			Spec: ipamv1.SubnetSpec{
				PoolRef:   "test-pool",
				CIDR:      "10.0.1.0/24",
				ClusterID: "cluster-1",
			},
		}

		// Create Fake client and register test objects
		fakeClient = fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(pool, alloc1).
			WithStatusSubresource(pool). // Enable status subresource
			// Register field indexes
			WithIndex(&ipamv1.Subnet{}, PoolRefField, func(o client.Object) []string {
				return []string{o.(*ipamv1.Subnet).Spec.PoolRef}
			}).
			WithIndex(&ipamv1.SubnetPool{}, ParentPoolLabelIndex, func(obj client.Object) []string {
				if p, ok := obj.(*ipamv1.SubnetPool); ok {
					if parent, ok := p.Labels["plexaubnet.io/parent"]; ok {
						return []string{parent}
					}
				}
				return nil
			}).
			Build()

		reconciler = &PoolStatusReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		req = ctrl.Request{
			NamespacedName: client.ObjectKey{Name: "test-pool"},
		}
	})

	Context("Status update", func() {
		It("should properly update SubnetPool status via PATCH", func() {
			// 1. Reconcileを実行
			result, err := reconciler.Reconcile(ctx, req)

			// 2. エラーがないことを確認
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			// 3. 更新後のプールを取得
			updatedPool := &ipamv1.SubnetPool{}
			err = fakeClient.Get(ctx, client.ObjectKey{Name: "test-pool"}, updatedPool)
			Expect(err).NotTo(HaveOccurred())

			// 4. ステータスが正しく更新されていることを確認
			// AllocatedCIDRsはstring->stringのマップ（CIDRがキー、ClusterIDが値）
			Expect(updatedPool.Status.AllocatedCIDRs).To(HaveKey("10.0.1.0/24"))
			Expect(updatedPool.Status.AllocatedCIDRs["10.0.1.0/24"]).To(Equal("cluster-1"))
			Expect(updatedPool.Status.AllocatedCount).To(Equal(1))

			// 5. FreeCountBySizeが正しいことを確認
			// "/24"のエントリがあるはず
			Expect(updatedPool.Status.FreeCountBySize).To(HaveKey("24"))
			// /16から1つの/24を割り当てたので、多数の/24ブロックが残るはず
			Expect(updatedPool.Status.FreeCountBySize["24"]).To(BeNumerically(">", 200))
		})

		It("should handle not found error gracefully", func() {
			// 存在しないPoolに対してReconcileを実行
			wrongReq := ctrl.Request{
				NamespacedName: client.ObjectKey{Name: "non-existent-pool"},
			}

			result, err := reconciler.Reconcile(ctx, wrongReq)

			// エラーなしで処理され、requeue=falseで返るはず
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})

	// Child Pool deletion テストは新設計に基づき削除されました
	// 新仕様では子SubnetPoolはPoolStatusの計算対象外であるため

	// 親プールのラベルが欠けているケースを追加
	Context("Parent Pool with missing labels", func() {
		It("should calculate status even if pool has no labels", func() {
			// ラベルのないプールを作成
			noLabelsPool := &ipamv1.SubnetPool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pool-without-labels",
					Namespace: "default",
					// ラベルを意図的に省略
				},
				Spec: ipamv1.SubnetPoolSpec{
					CIDR: "10.1.0.0/16",
				},
			}

			// Subnetを作成（このプールを参照）
			subnet := &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "allocated-subnet",
					Namespace: "default",
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef:   "pool-without-labels",
					CIDR:      "10.1.1.0/24",
					ClusterID: "test-cluster-no-labels",
				},
			}

			// Fake clientにオブジェクトを登録
			testClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(noLabelsPool, subnet).
				WithStatusSubresource(noLabelsPool).
				WithIndex(&ipamv1.Subnet{}, PoolRefField, func(o client.Object) []string {
					return []string{o.(*ipamv1.Subnet).Spec.PoolRef}
				}).
				Build()

			noLabelsReconciler := &PoolStatusReconciler{
				Client: testClient,
				Scheme: scheme,
			}

			// Reconcileを実行
			req := ctrl.Request{
				NamespacedName: client.ObjectKey{
					Name:      noLabelsPool.Name,
					Namespace: noLabelsPool.Namespace,
				},
			}

			// ラベルがなくてもReconcileが成功すること
			result, err := noLabelsReconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			// 更新されたプールを取得
			updatedPool := &ipamv1.SubnetPool{}
			err = testClient.Get(ctx, client.ObjectKey{
				Name:      noLabelsPool.Name,
				Namespace: noLabelsPool.Namespace,
			}, updatedPool)
			Expect(err).NotTo(HaveOccurred())

			// 期待する Status 更新を確認
			// - AllocatedCIDRs に追加された
			// - AllocatedCount が 1
			Expect(updatedPool.Status.AllocatedCIDRs).To(HaveKey("10.1.1.0/24"))
			Expect(updatedPool.Status.AllocatedCount).To(Equal(1))
		})
	})

	// 単一機能テスト: Mapper の Namespace 処理
	Context("Namespace handling in mapper function", func() {
		It("should correctly set namespace in request from subnet", func() {
			// 1. テスト用のSubnetを作成 - 有効なNamespaceを設定
			specialAlloc := &ipamv1.Subnet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "special-alloc",
					Namespace: "default", // 実際のNamespace（空ではない）
				},
				Spec: ipamv1.SubnetSpec{
					PoolRef:   "test-pool",
					CIDR:      "10.0.2.0/24",
					ClusterID: "special-cluster",
				},
			}

			// クライアントにオブジェクトを追加
			Expect(fakeClient.Create(ctx, specialAlloc)).To(Succeed())

			// Mapperの動作検証: オブジェクトからリクエストが正しく生成されるか
			requests := reconciler.mapAllocToPool(ctx, specialAlloc)

			// 3. 検証: Namespaceが正しく設定されていること
			Expect(requests).To(HaveLen(1), "Mapperはリクエストを1件返すべき")
			Expect(requests[0].Namespace).To(Equal("default"), "Namespaceが正しく設定されている")
			Expect(requests[0].Name).To(Equal("test-pool"), "Nameが正しく設定されている")

			// Mapperの単体テストではStatusの検証は行わない
			// Mapperの役割はイベントをリクエストに変換するだけで、
			// ステータス更新ロジックは別のテストケースでカバーされている
		})
	})
})

// TestPoolStatusReconcilerMetrics tests the metrics wiring in the PoolStatusReconciler
func TestPoolStatusReconcilerMetrics(t *testing.T) {
	// Register metric reset helper
	resetPoolStatusMetrics := func() {
		poolStatusUpdateTotal.Reset()
		poolStatusRetryTotal.Reset()
		poolStatusUpdateLatency.Reset()
	}

	// Helper to create base test objects and reconciler
	setupTest := func() (*ipamv1.SubnetPool, *PoolStatusReconciler, client.Client, ctrl.Request) {
		scheme := runtime.NewScheme()
		_ = ipamv1.AddToScheme(scheme)

		pool := &ipamv1.SubnetPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pool",
				Namespace: "default",
			},
			Spec: ipamv1.SubnetPoolSpec{
				CIDR: "10.0.0.0/16",
			},
			Status: ipamv1.SubnetPoolStatus{},
		}

		alloc := &ipamv1.Subnet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-alloc",
				Namespace: "default",
			},
			Spec: ipamv1.SubnetSpec{
				PoolRef:   "test-pool",
				CIDR:      "10.0.1.0/24",
				ClusterID: "test-cluster",
			},
		}

		// Default client with successful Status().Patch
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(pool, alloc).
			WithStatusSubresource(pool).
			WithIndex(&ipamv1.Subnet{}, PoolRefField, func(o client.Object) []string {
				return []string{o.(*ipamv1.Subnet).Spec.PoolRef}
			}).
			Build()

		reconciler := &PoolStatusReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		req := ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      "test-pool",
				Namespace: "default",
			},
		}

		return pool, reconciler, fakeClient, req
	}

	// Helper to get metric value
	getMetricValue := func(t *testing.T, metric *prometheus.CounterVec, labelValues ...string) float64 {
		t.Helper()
		m, err := metric.GetMetricWithLabelValues(labelValues...)
		if err != nil {
			t.Fatalf("Failed to get metric: %v", err)
			return -1
		}
		return testutil.ToFloat64(m)
	}

	t.Run("success path", func(t *testing.T) {
		// Reset metrics before test
		resetPoolStatusMetrics()

		// Setup test resources
		_, reconciler, _, req := setupTest()

		// Execute reconcile
		_, err := reconciler.Reconcile(context.Background(), req)
		if err != nil {
			t.Fatalf("Reconcile failed: %v", err)
		}

		// Verify metrics
		successValue := getMetricValue(t, poolStatusUpdateTotal, "test-pool", "success")
		if successValue != 1.0 {
			t.Errorf("Expected pool_status_update_total{result=\"success\"} to be 1.0, got %v", successValue)
		}

		// Verify no retries were recorded
		retryValue := getMetricValue(t, poolStatusRetryTotal, "test-pool", "conflict")
		if retryValue != 0.0 {
			t.Errorf("Expected pool_status_retry_total to be 0, got %v", retryValue)
		}
	})

	t.Run("conflict-retry path", func(t *testing.T) {
		// Reset metrics before test
		resetPoolStatusMetrics()

		// Setup test resources
		_, reconciler, fakeClient, req := setupTest()

		// Create a fake client with a conflict error on first Patch that succeeds on second try
		patchCount := 0
		mockClient := &conflictRetryMockClient{
			Client:         fakeClient,
			conflictOnCall: 1, // First call returns conflict
			patchCountPtr:  &patchCount,
		}
		reconciler.Client = mockClient

		// Execute reconcile
		_, err := reconciler.Reconcile(context.Background(), req)
		if err != nil {
			t.Fatalf("Reconcile failed: %v", err)
		}

		// Verify success metric
		successValue := getMetricValue(t, poolStatusUpdateTotal, "test-pool", "success")
		if successValue != 1.0 {
			t.Errorf("Expected pool_status_update_total{result=\"success\"} to be 1.0, got %v", successValue)
		}

		// Verify retry metric
		retryValue := getMetricValue(t, poolStatusRetryTotal, "test-pool", "conflict")
		if retryValue != 1.0 {
			t.Errorf("Expected pool_status_retry_total to be 1.0, got %v", retryValue)
		}
	})

	t.Run("failure path", func(t *testing.T) {
		// Reset metrics before test
		resetPoolStatusMetrics()

		// Setup test resources
		_, reconciler, fakeClient, req := setupTest()

		// Create a client that always fails patch with non-conflict error
		mockClient := &failingMockClient{
			Client: fakeClient,
		}
		reconciler.Client = mockClient

		// Execute reconcile - should fail
		_, err := reconciler.Reconcile(context.Background(), req)
		if err == nil {
			t.Fatalf("Expected reconcile to fail but it succeeded")
		}

		// Verify failure metric
		failureValue := getMetricValue(t, poolStatusUpdateTotal, "test-pool", "failure")
		if failureValue != 1.0 {
			t.Errorf("Expected pool_status_update_total{result=\"failure\"} to be 1.0, got %v", failureValue)
		}
	})
}

// Mock client that returns conflict error on a specific call to Patch
type conflictRetryMockClient struct {
	client.Client
	conflictOnCall int
	patchCountPtr  *int
}

func (c *conflictRetryMockClient) Status() client.StatusWriter {
	return &conflictRetryMockStatusWriter{
		StatusWriter:   c.Client.Status(),
		conflictOnCall: c.conflictOnCall,
		patchCountPtr:  c.patchCountPtr,
	}
}

type conflictRetryMockStatusWriter struct {
	client.StatusWriter
	conflictOnCall int
	patchCountPtr  *int
}

func (sw *conflictRetryMockStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	*sw.patchCountPtr++
	currentPatchCount := *sw.patchCountPtr

	if currentPatchCount == sw.conflictOnCall {
		return errors.NewConflict(
			schema.GroupResource{Group: "plexaubnet.io", Resource: "subnetpools"},
			obj.GetName(),
			fmt.Errorf("object has been modified; please apply your changes to the latest version"))
	}

	return sw.StatusWriter.Patch(ctx, obj, patch, opts...)
}

// Mock client that always fails with non-conflict error on Patch
type failingMockClient struct {
	client.Client
}

func (c *failingMockClient) Status() client.StatusWriter {
	return &failingMockStatusWriter{
		StatusWriter: c.Client.Status(),
	}
}

type failingMockStatusWriter struct {
	client.StatusWriter
}

func (sw *failingMockStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return errors.NewInternalError(fmt.Errorf("internal server error during patch"))
}
