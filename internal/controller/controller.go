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
	"time"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
	"github.com/appthrust/plexaubnet/internal/config"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Constants for common metadata and configuration
const (
	// IPAMLabel is the label added to all IPAM resources
	IPAMLabel = "plexaubnet.io/ipam"

	// IPAMFinalizer is the finalizer added to all IPAM resources
	IPAMFinalizer = "ipam.plexaubnet.io/finalizer"

	// MaxConcurrentReconciles is the maximum number of concurrent reconciles
	MaxConcurrentReconciles = 1
)

// CIDRAllocatorReconciler reconciles IPAM resources
type CIDRAllocatorReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Recorder       record.EventRecorder
	eventEmitter   *EventEmitter
	Config         *config.IPAMConfig
	ControllerName string // For avoiding controller name duplication (mainly for testing)
}

// applyCommonMetadata adds common labels and finalizers to IPAM resources
// DEPRECATED: This method is kept for compatibility.
// New code should use the metadata attachment mechanism of Webhook.
// Reference: Use ApplyCommonMetadata in aquanaut/internal/webhook/ipam/common.go
func applyCommonMetadata(obj client.Object) {
	// Add the common IPAM label
	if obj.GetLabels() == nil {
		obj.SetLabels(map[string]string{})
	}

	labels := obj.GetLabels()
	if _, ok := labels[IPAMLabel]; !ok {
		labels[IPAMLabel] = "true"
		obj.SetLabels(labels)
	}

	// Add the finalizer if not present
	if !controllerutil.ContainsFinalizer(obj, IPAMFinalizer) {
		controllerutil.AddFinalizer(obj, IPAMFinalizer)
	}
}

// isAllocationImmutable checks if a Subnet update attempts to modify immutable fields
func isAllocationImmutable(oldAlloc, newAlloc *ipamv1.Subnet) (bool, string) {
	if !reflect.DeepEqual(oldAlloc.Spec, newAlloc.Spec) {
		return false, "Subnet Spec fields are immutable"
	}
	return true, ""
}

//+kubebuilder:rbac:groups=plexaubnet.io,resources=subnetpools,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=plexaubnet.io,resources=subnetpools/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=plexaubnet.io,resources=subnetpools/finalizers,verbs=update
//+kubebuilder:rbac:groups=plexaubnet.io,resources=subnetclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=plexaubnet.io,resources=subnetclaims/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=plexaubnet.io,resources=subnetclaims/finalizers,verbs=update
//+kubebuilder:rbac:groups=plexaubnet.io,resources=subnets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=plexaubnet.io,resources=subnets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=plexaubnet.io,resources=subnets/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile handles SubnetClaim reconciliation
func (r *CIDRAllocatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("subnetclaim", req.NamespacedName)
	logger.Info("Starting SubnetClaim reconciliation")

	// Fetch the SubnetClaim
	claim := &ipamv1.SubnetClaim{}
	if err := r.Get(ctx, req.NamespacedName, claim); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Allocation-First design: Process exclusively with unique constraints of Subnet resources.
	// Label lock is no longer needed.
	return r.processClaimReconciliation(ctx, claim)
}

// NewCIDRAllocatorReconciler creates a new reconciler
func NewCIDRAllocatorReconciler(client client.Client, scheme *runtime.Scheme, recorder record.EventRecorder, ipamConfig *config.IPAMConfig, controllerName ...string) *CIDRAllocatorReconciler {
	name := ""
	if len(controllerName) > 0 {
		name = controllerName[0]
	}

	r := &CIDRAllocatorReconciler{
		Client:         client,
		Scheme:         scheme,
		Recorder:       recorder,
		Config:         ipamConfig,
		ControllerName: name,
	}
	r.eventEmitter = NewEventEmitter(recorder, scheme)
	return r
}

// SetupWithManager sets up the controller with the Manager.
func (r *CIDRAllocatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Initialize the event emitter
	r.eventEmitter = NewEventEmitter(r.Recorder, r.Scheme)

	// Set controller name (to avoid name duplication between tests)
	name := r.ControllerName
	if name == "" {
		name = "subnetclaim" // Default name
	}

	// In Allocation-First design, resource lock filtering is no longer needed.

	return ctrl.NewControllerManagedBy(mgr).
		Named(name). // Explicitly set controller name
		For(&ipamv1.SubnetClaim{}).
		Owns(&ipamv1.Subnet{}).
		// Add generation change filter: Do not trigger readjustment only by Status update
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: MaxConcurrentReconciles,
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](
				// Set wait time until first retry to 5 seconds
				time.Second*5,
				// Set maximum retry interval to 5 minutes
				time.Minute*5,
			),
		}).
		Complete(r)
}

// RegisterControllers registers all controllers with the manager
func RegisterControllers(mgr ctrl.Manager, ipamConfig *config.IPAMConfig, recorder record.EventRecorder) error {
	logger := log.Log.WithName("controller-registration")

	// Register the CIDRAllocator controller
	allocator := NewCIDRAllocatorReconciler(mgr.GetClient(), mgr.GetScheme(), recorder, ipamConfig)
	if err := allocator.SetupWithManager(mgr); err != nil {
		logger.Error(err, "Failed to setup CIDRAllocator controller")
		return err
	}

	// Register the Subnet controller
	allocationReconciler := NewSubnetReconciler(mgr.GetClient(), mgr.GetScheme(), ipamConfig)
	if err := allocationReconciler.SetupWithManager(mgr); err != nil {
		logger.Error(err, "Failed to setup Subnet controller")
		return err
	}

	// Register the SubnetPoolClaim controller if enabled
	if ipamConfig.EnablePoolClaim {
		logger.Info("Setting up SubnetPoolClaim controller (feature enabled)")
		poolClaimController := NewSubnetPoolClaimReconciler(mgr.GetClient(), mgr.GetScheme(), ipamConfig)
		if err := poolClaimController.SetupWithManager(mgr); err != nil {
			logger.Error(err, "Failed to setup SubnetPoolClaim controller")
			return err
		}
	} else {
		logger.Info("SubnetPoolClaim controller is disabled", "enablePoolClaim", ipamConfig.EnablePoolClaim)
	}

	// Register the PoolStatus controller
	// TODO: Make this configurable via feature flag similar to EnablePoolClaim
	logger.Info("Setting up PoolStatus controller")
	poolStatusReconciler := &PoolStatusReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}
	if err := poolStatusReconciler.SetupWithManager(mgr); err != nil {
		logger.Error(err, "Failed to setup PoolStatus controller")
		return err
	}

	return nil
}
