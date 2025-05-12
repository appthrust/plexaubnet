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
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// SubnetEvents returns a predicate that allows Create/Update/Delete events for Subnet resources.
// Status-only changes are excluded (by GenerationChangedPredicate).
func SubnetEvents() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Ignore status-only updates (Generation doesn't change)
			return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration()
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return true
		},
	}
}

// DeletionOnly returns a predicate that only allows deletion events.
func DeletionOnly() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}
}

// PoolStatusPred returns a predicate that passes Pool's own updates only when generation changes, while passing all external events (Create/Delete/Generic).
// This avoids infinite loops on Pool's status-only updates while ensuring events from other resources are processed.
func PoolStatusPred() predicate.Predicate {
	return predicate.Funcs{
		// Pool's Update passes only when generation changes
		UpdateFunc: func(e event.UpdateEvent) bool {
			return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration()
		},
		// All external events are allowed
		CreateFunc:  func(e event.CreateEvent) bool { return true },
		DeleteFunc:  func(e event.DeleteEvent) bool { return true },
		GenericFunc: func(e event.GenericEvent) bool { return true },
	}
}
