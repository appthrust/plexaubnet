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

// SubnetPoolStrategy defines the allocation strategy for the subnet pool
// +kubebuilder:validation:Enum=Linear;Buddy
type SubnetPoolStrategy string

const (
	// StrategyLinear uses a linear first-fit allocation strategy
	StrategyLinear SubnetPoolStrategy = "Linear"
	// StrategyBuddy uses a buddy allocation strategy to minimize fragmentation
	StrategyBuddy SubnetPoolStrategy = "Buddy"
)

// SubnetPoolSpec defines the desired state of SubnetPool
type SubnetPoolSpec struct {
	// CIDR is the overall CIDR range for this pool
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Format=cidr
	CIDR string `json:"cidr"`

	// DefaultBlockSize is the default prefix length to use when a SubnetClaim doesn't specify a blockSize
	// +kubebuilder:validation:Minimum=16
	// +kubebuilder:validation:Maximum=28
	// +kubebuilder:default=24
	// +optional
	DefaultBlockSize int `json:"defaultBlockSize,omitempty"`

	// MinBlockSize is the minimum prefix length that can be requested from this pool
	// +kubebuilder:validation:Minimum=16
	// +kubebuilder:validation:Maximum=28
	// +optional
	MinBlockSize int `json:"minBlockSize,omitempty"`

	// MaxBlockSize is the maximum prefix length that can be requested from this pool
	// +kubebuilder:validation:Minimum=16
	// +kubebuilder:validation:Maximum=28
	// +optional
	MaxBlockSize int `json:"maxBlockSize,omitempty"`

	// Strategy defines the allocation strategy to use
	// +kubebuilder:default=Linear
	// +optional
	Strategy SubnetPoolStrategy `json:"strategy,omitempty"`
}

// SubnetPoolStatus defines the observed state of SubnetPool
type SubnetPoolStatus struct {
	// ObservedGeneration はコントローラが最後に処理した世代を記録
	// これにより、Spec変更のない状態更新では再計算を回避できる
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// AllocatedCount is the number of allocated subnet blocks
	// +optional
	AllocatedCount int `json:"allocatedCount,omitempty"`

	// FreeCountBySize is a map of prefix length to number of free blocks
	// +optional
	FreeCountBySize map[string]int `json:"freeCountBySize,omitempty"`

	// AllocatedCIDRs is a map of allocated CIDRs to cluster IDs
	// This serves as the source of truth for subnet allocation
	// +optional
	AllocatedCIDRs map[string]string `json:"allocatedCIDRs,omitempty"`

	// Conditions represents the latest available observations of the pool's state
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=subnetpool
// +kubebuilder:printcolumn:name="CIDR",type="string",JSONPath=".spec.cidr"
// +kubebuilder:printcolumn:name="Strategy",type="string",JSONPath=".spec.strategy"
// +kubebuilder:printcolumn:name="Allocated",type="integer",JSONPath=".status.allocatedCount"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SubnetPool is the Schema for the subnetpools API
type SubnetPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SubnetPoolSpec   `json:"spec,omitempty"`
	Status SubnetPoolStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SubnetPoolList contains a list of SubnetPool
type SubnetPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SubnetPool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SubnetPool{}, &SubnetPoolList{})
}
