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

// SubnetStatus defines the observed state of Subnet
type SubnetStatus struct {
	// Phase represents the current state of the Subnet
	// +optional
	Phase string `json:"phase,omitempty"`

	// AllocatedAt is the timestamp when the CIDR was successfully allocated
	// +optional
	AllocatedAt *metav1.Time `json:"allocatedAt,omitempty"`
}

// Subnet Phase constants
const (
	// SubnetPhasePending indicates the Subnet request has been accepted but not yet reflected in the Pool
	SubnetPhasePending = "Pending"

	// SubnetPhaseAllocated indicates the CIDR has been successfully allocated
	SubnetPhaseAllocated = "Allocated"

	// SubnetPhaseFailed indicates the allocation failed or there is an inconsistency
	SubnetPhaseFailed = "Failed"
)

// ClaimReference contains information to identify the claim that led to this allocation
type ClaimReference struct {
	// Name is the name of the SubnetClaim that requested this allocation
	// +optional
	Name string `json:"name,omitempty"`

	// UID is the UID of the SubnetClaim that requested this allocation
	// +optional
	UID string `json:"uid,omitempty"`
}

// SubnetSpec defines the desired state of Subnet
type SubnetSpec struct {
	// PoolRef is the name of the SubnetPool this allocation belongs to
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	PoolRef string `json:"poolRef"`

	// CIDR is the allocated CIDR block
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Format=cidr
	CIDR string `json:"cidr"`

	// ClusterID is the unique identifier for the cluster
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z0-9.-]{1,63}$"
	ClusterID string `json:"clusterID"`

	// ClaimRef references the SubnetClaim that led to this allocation
	// +optional
	ClaimRef ClaimReference `json:"claimRef,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=subnet
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="CIDR",type="string",JSONPath=".spec.cidr"
// +kubebuilder:printcolumn:name="ClusterID",type="string",JSONPath=".spec.clusterID"
// +kubebuilder:printcolumn:name="Pool",type="string",JSONPath=".spec.poolRef"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Subnet is the Schema for the subnets API
// Subnet objects are immutable and should not be updated after creation
type Subnet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SubnetSpec   `json:"spec,omitempty"`
	Status SubnetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SubnetList contains a list of Subnet
type SubnetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Subnet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Subnet{}, &SubnetList{})
}
