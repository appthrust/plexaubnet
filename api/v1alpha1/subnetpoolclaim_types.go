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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SubnetPoolClaimPhase defines the phase of a subnet pool claim
type SubnetPoolClaimPhase string

const (
	// PoolClaimPending indicates the claim is waiting for allocation
	PoolClaimPending SubnetPoolClaimPhase = "Pending"
	// PoolClaimBound indicates the claim has been successfully bound to a pool
	PoolClaimBound SubnetPoolClaimPhase = "Bound"
	// PoolClaimError indicates an error occurred during allocation
	PoolClaimError SubnetPoolClaimPhase = "Error"
)

// SubnetPoolClaimSpec defines the desired state of SubnetPoolClaim
type SubnetPoolClaimSpec struct {
	// ParentPoolRef is the name of the parent SubnetPool to allocate from
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ParentPoolRef string `json:"parentPoolRef"`

	// DesiredBlockSize is the desired prefix length for the allocated subnet pool
	// +kubebuilder:validation:Minimum=16
	// +kubebuilder:validation:Maximum=28
	// +kubebuilder:validation:Required
	DesiredBlockSize int `json:"desiredBlockSize"`
}

// SubnetPoolClaimStatus defines the observed state of SubnetPoolClaim
type SubnetPoolClaimStatus struct {
	// ObservedGeneration is the generation observed by the controller
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase indicates the current phase of the claim
	// +optional
	Phase SubnetPoolClaimPhase `json:"phase,omitempty"`

	// BoundPoolName is the name of the SubnetPool that was created for this claim
	// +optional
	BoundPoolName string `json:"boundPoolName,omitempty"`

	// Message provides additional information about the current phase
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions represents the latest available observations of the claim's state
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=subnetpc
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Parent",type="string",JSONPath=".spec.parentPoolRef"
// +kubebuilder:printcolumn:name="BoundPool",type="string",JSONPath=".status.boundPoolName"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SubnetPoolClaim is the Schema for the subnetpoolclaims API
type SubnetPoolClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SubnetPoolClaimSpec   `json:"spec,omitempty"`
	Status SubnetPoolClaimStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SubnetPoolClaimList contains a list of SubnetPoolClaim
type SubnetPoolClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SubnetPoolClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SubnetPoolClaim{}, &SubnetPoolClaimList{})
}
