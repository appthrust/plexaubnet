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

// SubnetClaimPhase defines the phase of a subnet claim
type SubnetClaimPhase string

const (
	// ClaimPending indicates the claim is waiting for allocation
	ClaimPending SubnetClaimPhase = "Pending"
	// ClaimBound indicates the claim has been successfully allocated
	ClaimBound SubnetClaimPhase = "Bound"
	// ClaimError indicates an error occurred during allocation
	ClaimError SubnetClaimPhase = "Error"
)

// SubnetClaimSpec defines the desired state of SubnetClaim
// +kubebuilder:validation:XValidation:rule="(has(self.blockSize) || has(self.requestedCIDR))",message="either blockSize or requestedCIDR must be set"
// +kubebuilder:validation:XValidation:rule="!(has(self.blockSize) && has(self.requestedCIDR))",message="blockSize and requestedCIDR are mutually exclusive"
type SubnetClaimSpec struct {
	// PoolRef is the name of the SubnetPool to allocate from
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	PoolRef string `json:"poolRef"`

	// ClusterID is the unique identifier for the cluster
	// Used as the idempotency key for allocation
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z0-9.-]{1,63}$"
	ClusterID string `json:"clusterID"`

	// BlockSize is the desired prefix length for the allocated subnet
	// +kubebuilder:validation:Minimum=16
	// +kubebuilder:validation:Maximum=28
	// +optional
	BlockSize int `json:"blockSize,omitempty"`

	// RequestedCIDR is a specific CIDR that is being requested
	// If provided, the allocator will try to allocate this exact CIDR
	// +kubebuilder:validation:Format=cidr
	// +optional
	RequestedCIDR string `json:"requestedCIDR,omitempty"`
}

// SubnetClaimStatus defines the observed state of SubnetClaim
type SubnetClaimStatus struct {
	// ObservedGeneration はコントローラが最後に処理した世代を記録
	// これにより、Spec変更のない状態更新では再処理を回避できる
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase indicates the current phase of the claim
	// +optional
	Phase SubnetClaimPhase `json:"phase,omitempty"`

	// AllocatedCIDR is the CIDR that was allocated for this claim
	// +optional
	AllocatedCIDR string `json:"allocatedCIDR,omitempty"`

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
// +kubebuilder:resource:shortName=subnetclaim
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="ClusterID",type="string",JSONPath=".spec.clusterID"
// +kubebuilder:printcolumn:name="Pool",type="string",JSONPath=".spec.poolRef"
// +kubebuilder:printcolumn:name="Allocated",type="string",JSONPath=".status.allocatedCIDR"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SubnetClaim is the Schema for the subnetclaims API
type SubnetClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SubnetClaimSpec   `json:"spec,omitempty"`
	Status SubnetClaimStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SubnetClaimList contains a list of SubnetClaim
type SubnetClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SubnetClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SubnetClaim{}, &SubnetClaimList{})
}
