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
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	_ "github.com/appthrust/plexaubnet/internal/testutils" // Import for envtest assets setup
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment
var ctx context.Context
var cancel context.CancelFunc

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CIDR Allocator Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	// Increase API Server request limits (for parallel testing)
	// Pass APIServer parameters via KUBEBUILDER_KUBE_APISERVER_FLAGS environment variable
	// (parameters are parsed internally by envtest)
	envErr := os.Setenv("KUBEBUILDER_ASSETS_VERBOSE", "1") // Also enable debug output
	Expect(envErr).NotTo(HaveOccurred(), "Failed to set KUBEBUILDER_ASSETS_VERBOSE")

	envErr = os.Setenv("KUBEBUILDER_KUBE_APISERVER_FLAGS",
		"--max-mutating-requests-inflight=800 "+ // Increased from default 200
			"--max-requests-inflight=1600 "+ // Increased from default 400
			"--watch-cache-sizes=*=1000") // Also increase Watch cache size
	Expect(envErr).NotTo(HaveOccurred(), "Failed to set KUBEBUILDER_KUBE_APISERVER_FLAGS")

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	// Increase client and API Server communication limits for performance improvement
	cfg.QPS = 200   // Significantly increased from default 5
	cfg.Burst = 400 // Significantly increased from default 10
	GinkgoWriter.Printf("Setting high QPS=%v, Burst=%v for parallel test\n", cfg.QPS, cfg.Burst)

	err = ipamv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// Setup field indexers for better filtering
	// Configuration to avoid port conflicts during parallel testing
	// Disable metrics server with the following setting to prevent port conflicts
	os.Setenv("KUBEBUILDER_CONTROLPLANE_START_PORT", "18200")

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme.Scheme,
		HealthProbeBindAddress: "0",   // Disable health check server
		LeaderElection:         false, // Also disable leader election
		Metrics: metricsserver.Options{
			BindAddress: "0", // Disable metrics server
		},
	})
	Expect(err).NotTo(HaveOccurred())

	// Use centralized index configuration (due to changes in Task 2)
	if err := SetupFieldIndexes(ctx, mgr, logf.Log.WithName("test-index-setup")); err != nil {
		GinkgoWriter.Printf("ERROR: Failed to setup field indexes: %v\n", err)
		Expect(err).NotTo(HaveOccurred(), "Failed to setup field indexes")
	} else {
		GinkgoWriter.Printf("Field indexes setup successful\n")
	}
	reconciler := &CIDRAllocatorReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		Recorder:       mgr.GetEventRecorderFor("subnet-controller"),
		ControllerName: "allocator-suite-test", // Avoid name collisions between tests
	}
	reconciler.eventEmitter = NewEventEmitter(reconciler.Recorder, reconciler.Scheme)

	// Register controller using a unique name for testing
	skipValidation := true
	err = ctrl.NewControllerManagedBy(mgr).
		Named("subnetclaim-test-controller-suite"). // Use a unique name for test execution
		WithOptions(controller.Options{
			// Skip name validation to avoid controller name conflicts
			SkipNameValidation: &skipValidation,
		}).
		For(&ipamv1.SubnetClaim{}).
		Owns(&ipamv1.Subnet{}).
		// GenerationChangedPredicate is not needed, so commented out
		Complete(reconciler)
	Expect(err).NotTo(HaveOccurred())

	// Add SubnetReconciler registration
	// Without this, Pool.Status update processing will not occur
	GinkgoWriter.Printf("Setting up SubnetReconciler for Pool Status updates\n")

	allocReconciler := &SubnetReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Config: nil, // Tests can run even if IPAMConfig is nil
	}

	// Call SetupWithManager to initialize (also registers field indexes)
	err = allocReconciler.SetupWithManager(mgr)

	// Log error details
	if err != nil {
		GinkgoWriter.Printf("ERROR: Failed to setup SubnetReconciler: %v\n", err)

		// If index conflict, register controller directly
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "indexer conflict") {
			GinkgoWriter.Printf("WARNING: Ignoring indexer conflict, trying alternative controller registration\n")

			// Register controller directly using controller manager
			// Add random element to name for shared test environment
			skipValidation := true
			_, err := ctrl.NewControllerManagedBy(mgr).
				Named("subnet-controller-fallback-suite").
				WithOptions(controller.Options{
					MaxConcurrentReconciles: MaxConcurrentReconciles,
					// Skip name validation to avoid controller name conflicts
					SkipNameValidation: &skipValidation,
				}).
				For(&ipamv1.Subnet{}).
				Build(allocReconciler)

			if err != nil {
				GinkgoWriter.Printf("CRITICAL: Failed to register fallback controller: %v\n", err)
				Expect(err).NotTo(HaveOccurred(), "Failed to register fallback controller")
			} else {
				GinkgoWriter.Printf("Successfully registered fallback SubnetReconciler\n")
			}
		} else {
			Expect(err).NotTo(HaveOccurred(), "Failed to setup SubnetReconciler")
		}
	}

	// Register Pool Status Reconciler
	poolStatusReconciler := &PoolStatusReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		ControllerName: "poolstatus-suite-test", // Explicitly set controller name to avoid duplication
	}

	err = poolStatusReconciler.SetupWithManager(mgr)
	if err != nil {
		GinkgoWriter.Printf("ERROR: Failed to setup PoolStatusReconciler: %v\n", err)

		// If index conflict, register controller directly
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "indexer conflict") {
			GinkgoWriter.Printf("WARNING: Ignoring indexer conflict for PoolStatusReconciler, trying alternative registration\n")

			// Register controller directly using controller manager
			// Add random element to name for shared test environment
			skipValidation := true
			builder := ctrl.NewControllerManagedBy(mgr).
				Named("poolstatus-controller-fallback-suite").
				WithOptions(controller.Options{
					// Skip name validation to avoid controller name conflicts
					SkipNameValidation: &skipValidation,
				}).
				For(&ipamv1.SubnetPool{})

			// Add necessary Watches
			builder = builder.Watches(
				&ipamv1.Subnet{},
				handler.EnqueueRequestsFromMapFunc(poolStatusReconciler.mapAllocToPool),
			)

			builder = builder.Watches(
				&ipamv1.SubnetPool{},
				handler.EnqueueRequestsFromMapFunc(poolStatusReconciler.mapChildToParent),
			)

			// Build controller
			err = builder.Complete(poolStatusReconciler)
			if err != nil {
				GinkgoWriter.Printf("CRITICAL: Failed to register fallback PoolStatusReconciler: %v\n", err)
				Expect(err).NotTo(HaveOccurred(), "Failed to register fallback PoolStatusReconciler")
			} else {
				GinkgoWriter.Printf("Successfully registered fallback PoolStatusReconciler\n")
			}
		} else {
			Expect(err).NotTo(HaveOccurred(), "Failed to setup PoolStatusReconciler")
		}
	}

	go func() {
		defer GinkgoRecover()
		err = mgr.Start(ctx)
		Expect(err).NotTo(HaveOccurred(), "Failed to run manager")
	}()
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
