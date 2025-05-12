//go:build race
// +build race

package cidrallocator

import (
	"context"
	"testing"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

// setupEnvTest sets up a controller-runtime envtest environment and registers the CRDs.
// This duplicate implementation is compiled only when `-race` is enabled (build tag `race`).
func setupEnvTest(t *testing.T) *envtest.Environment {
	t.Helper()
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{"../../config/crd/bases"},
	}

	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("Error starting test env: %v", err)
	}

	// Increase client and API Server communication limits for performance improvement
	cfg.QPS = 200   // uplift default 5
	cfg.Burst = 400 // uplift default 10

	if err := ipamv1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatalf("Error adding scheme: %v", err)
	}

	return testEnv
}

// stopEnvTest terminates the envtest environment.
func stopEnvTest(t *testing.T, testEnv *envtest.Environment) {
	t.Helper()
	if err := testEnv.Stop(); err != nil {
		t.Logf("Error stopping test env: %v", err)
	}
}

// createK8sClient constructs a controller-runtime client using the provided rest.Config.
func createK8sClient(cfg *rest.Config) (client.Client, error) {
	return client.New(cfg, client.Options{Scheme: scheme.Scheme})
}

// setupManager creates a controller-runtime manager for tests with metrics & probes disabled.
func setupManager(cfg *rest.Config) (ctrl.Manager, error) {
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0", // Disable metrics server
		},
		HealthProbeBindAddress: "0", // Disable health probe server
	})
	if err != nil {
		return nil, err
	}

	// Set up field indexes if necessary
	if err := SetupFieldIndexes(context.Background(), mgr, ctrl.Log.WithName("test-setup")); err != nil {
		return nil, err
	}

	return mgr, nil
}
