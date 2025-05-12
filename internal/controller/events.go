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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

// Event reasons
const (
	// EventReasonAllocated is the reason for allocation success events
	EventReasonAllocated = "SubnetAllocated"

	// EventReasonPoolExhausted is the reason for pool exhaustion events
	EventReasonPoolExhausted = "SubnetPoolExhausted"

	// EventReasonConflict is the reason for allocation conflict events
	EventReasonConflict = "AllocationConflict"

	// EventReasonValidationFailed is the reason for validation failure events
	EventReasonValidationFailed = "ValidationFailed"
)

// EventEmitter provides methods to emit standardized Kubernetes events
type EventEmitter struct {
	recorder record.EventRecorder
	scheme   *runtime.Scheme
}

// NewEventEmitter creates a new EventEmitter
func NewEventEmitter(recorder record.EventRecorder, scheme *runtime.Scheme) *EventEmitter {
	return &EventEmitter{
		recorder: recorder,
		scheme:   scheme,
	}
}

// EmitAllocationSuccess emits an event for a successful allocation
func (e *EventEmitter) EmitAllocationSuccess(claim *ipamv1.SubnetClaim, cidr string) {
	msg := fmt.Sprintf("Successfully allocated Subnet %s", cidr)
	e.recorder.Event(claim, corev1.EventTypeNormal, EventReasonAllocated, msg)
}

// EmitAllocationConflict emits an event for an allocation conflict
func (e *EventEmitter) EmitAllocationConflict(claim *ipamv1.SubnetClaim, details string) {
	msg := fmt.Sprintf("Allocation conflict: %s", details)
	e.recorder.Event(claim, corev1.EventTypeWarning, EventReasonConflict, msg)
}

// EmitPoolExhausted emits an event for a pool exhaustion
func (e *EventEmitter) EmitPoolExhausted(claim *ipamv1.SubnetClaim, poolName string) {
	msg := fmt.Sprintf("Pool %s is exhausted for requested size", poolName)
	e.recorder.Event(claim, corev1.EventTypeWarning, EventReasonPoolExhausted, msg)
}

// EmitValidationFailed emits an event for validation failure
func (e *EventEmitter) EmitValidationFailed(claim *ipamv1.SubnetClaim, details string) {
	msg := fmt.Sprintf("Validation failed: %s", details)
	e.recorder.Event(claim, corev1.EventTypeWarning, EventReasonValidationFailed, msg)
}

// EmitPoolEvent emits an event associated with a pool
func (e *EventEmitter) EmitPoolEvent(pool *ipamv1.SubnetPool, eventType string, reason string, msg string) {
	e.recorder.Event(pool, eventType, reason, msg)
}

// RegisterEventHandlers updates the reconciler with event handling capabilities
func (r *CIDRAllocatorReconciler) RegisterEventHandlers() {
	r.eventEmitter = NewEventEmitter(r.Recorder, r.Scheme)
}
