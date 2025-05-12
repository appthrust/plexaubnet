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
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

// PatchSubnetStatus is a generic helper function to update the Status field of a Subnet using Patch
// Arbitrary Status updates are possible with a custom mutate function
// Enhance conflict resistance using client.Status().Patch and retry.OnError
//
// Arguments:
//   - ctx: Context (supports timeout/cancellation)
//   - c: Client interface
//   - sub: Subnet to be updated
//   - mutate: Callback function for Status update
//
// Example:
//
//	err := statusutil.PatchSubnetStatus(ctx, r.Client, subnet, func(status *ipamv1.SubnetStatus) {
//	    status.Phase = ipamv1.SubnetPhaseAllocated
//	    // Update other status fields
//	})
func PatchSubnetStatus(
	ctx context.Context,
	c client.Client,
	sub *ipamv1.Subnet,
	mutate func(*ipamv1.SubnetStatus),
) error {
	logger := log.FromContext(ctx)
	startTime := time.Now()
	var retryCount int
	var finalError error // Holds the final error of retry.OnError

	// Definition of maximum retry count (retry.OnError is initial + retry)
	const maxRetries = 5

	// Backoff settings - equivalent to client-go defaults, but Steps are explicitly adjusted
	backoff := wait.Backoff{
		Duration: 50 * time.Millisecond,
		Factor:   2,
		Steps:    maxRetries + 1, // Initial + max 5 retries = 6 attempts in total
		Jitter:   0.1,
	}

	// Apply patch with retries
	finalError = retry.OnError(backoff, apierrors.IsConflict, func() error {
		retryCount++ // Increment attempt count (starts from 1)

		// Get the latest version of the object
		currentSubnet := &ipamv1.Subnet{} // Changed variable name from updated to currentSubnet for clarity
		if err := c.Get(ctx, types.NamespacedName{
			Name:      sub.Name,
			Namespace: sub.Namespace,
		}, currentSubnet); err != nil {
			if apierrors.IsNotFound(err) {
				// If Subnet is already deleted, consider it a normal termination
				logger.V(1).Info("Subnet not found during status patch, already deleted",
					"ns", sub.Namespace, "name", sub.Name)
				durationSkipped := time.Since(startTime).Seconds()
				SubnetStatusUpdateTotal.WithLabelValues("skipped_deleted").Inc()
				SubnetStatusUpdateLatency.WithLabelValues("skipped_deleted").Observe(durationSkipped)
				return nil // Normal termination if already deleted
			}
			logger.Error(err, "Failed to get latest Subnet")
			return err // This error is not eligible for retry (if not IsConflict)
		}

		// Create a copy of the status before changes
		oldStatus := currentSubnet.Status.DeepCopy()

		// Perform DeepCopy before mutate and use it as the base for the patch
		baseForMerge := currentSubnet.DeepCopy()

		// Update status with callback function (directly modify Status of currentSubnet)
		mutate(&currentSubnet.Status)

		// If the resource is scheduled for deletion, skip patch after mutate
		if currentSubnet.GetDeletionTimestamp() != nil {
			logger.V(1).Info("Subnet marked for deletion, skipping status patch",
				"ns", currentSubnet.Namespace, "name", currentSubnet.Name)
			durationSkipped := time.Since(startTime).Seconds()
			SubnetStatusUpdateTotal.WithLabelValues("skipped_deleting").Inc()
			SubnetStatusUpdateLatency.WithLabelValues("skipped_deleting").Observe(durationSkipped)
			return nil
		}

		// Do not update if there are no changes to Status
		if equalSubnetStatus(*oldStatus, currentSubnet.Status) {
			// Record metrics for no change case here
			durationUnchanged := time.Since(startTime).Seconds() // Elapsed time at this point
			SubnetStatusUpdateTotal.WithLabelValues("unchanged").Inc()
			SubnetStatusUpdateLatency.WithLabelValues("unchanged").Observe(durationUnchanged)
			return nil // error == nil, terminate retry (no change)
		}

		// Update only Status using MergePatch
		if err := c.Status().Patch(ctx, currentSubnet, client.MergeFrom(baseForMerge)); err != nil {
			// NotFound can also occur during Patch (if deleted between Get and Patch)
			if apierrors.IsNotFound(err) {
				logger.V(1).Info("Subnet not found during status patch application",
					"ns", currentSubnet.Namespace, "name", currentSubnet.Name)
				durationSkipped := time.Since(startTime).Seconds()
				SubnetStatusUpdateTotal.WithLabelValues("skipped_deleted").Inc()
				SubnetStatusUpdateLatency.WithLabelValues("skipped_deleted").Observe(durationSkipped)
				return nil // Normal termination if already deleted
			}
			logger.Error(err, "Failed to patch Subnet status")
			return err // If err is Conflict, retry.OnError will retry
		}

		// Record metrics on patch success here
		durationSuccess := time.Since(startTime).Seconds() // Elapsed time at this point
		SubnetStatusUpdateTotal.WithLabelValues("success").Inc()
		SubnetStatusUpdateLatency.WithLabelValues("success").Observe(durationSuccess)

		logger.V(1).Info("Patched Subnet status",
			"ns", currentSubnet.Namespace,
			"name", currentSubnet.Name,
			"attempts", retryCount) // retryCount is the number of attempts
		return nil // error == nil, terminate retry (patch successful)
	})

	// Record retry count metrics (only if retries actually occurred)
	// retryCount is the total number of attempts. Retry count is attempts - 1.
	actualRetries := retryCount - 1
	if actualRetries > 0 {
		SubnetStatusRetryTotal.WithLabelValues(strconv.Itoa(actualRetries)).Inc()
	}

	// Record metrics on final error
	// If finalError is not nil, it is either a Conflict that exceeded the retry limit, or
	// a persistent error other than Conflict in Get or Patch.
	if finalError != nil {
		// "unchanged" and "success" are already recorded in the loop, so record only "error" here
		durationError := time.Since(startTime).Seconds()
		SubnetStatusUpdateTotal.WithLabelValues("error").Inc()
		SubnetStatusUpdateLatency.WithLabelValues("error").Observe(durationError)
	}

	return finalError
}

// equalSubnetStatus determines if two SubnetStatus are equivalent
// Detailed comparison logic can be adjusted according to application requirements
func equalSubnetStatus(old, new ipamv1.SubnetStatus) bool {
	// Phase comparison
	if old.Phase != new.Phase {
		return false
	}

	// AllocatedAt comparison
	if (old.AllocatedAt == nil && new.AllocatedAt != nil) ||
		(old.AllocatedAt != nil && new.AllocatedAt == nil) {
		return false
	}

	if old.AllocatedAt != nil && new.AllocatedAt != nil &&
		!old.AllocatedAt.Equal(new.AllocatedAt) {
		return false
	}

	// Add other fields that may need comparison in the future here

	return true
}
