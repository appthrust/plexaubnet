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

package config

import (
	"encoding/json"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

// IPAMConfig defines the configuration for the IPAM component
type IPAMConfig struct {
	// UpdatePoolStatus includes settings for pool status updates
	// Note: The old updatePoolStatus function has been removed, but this configuration is
	// still kept as it is used for retry strategies in PoolStatusReconciler for status updates.
	// Also refer to docs/parent-pool-requeue-strategy.md.
	UpdatePoolStatus UpdatePoolStatusConfig `json:"updatePoolStatus,omitempty"`

	// EnablePoolClaim is a flag to enable the PoolClaim feature
	// +optional
	EnablePoolClaim bool `json:"enablePoolClaim,omitempty"`
}

// UpdatePoolStatusConfig defines settings for pool status updates
type UpdatePoolStatusConfig struct {
	// Backoff includes retry backoff settings
	Backoff BackoffConfig `json:"backoff,omitempty"`
	// AsyncTimeoutSec specifies the timeout (in seconds) for asynchronous update operations
	AsyncTimeoutSec int `json:"asyncTimeoutSec,omitempty"`
}

// BackoffConfig defines the backoff settings
type BackoffConfig struct {
	// Steps is the maximum number of retries
	Steps int `json:"steps,omitempty"`
	// InitialMs is the initial wait time (in milliseconds)
	InitialMs int `json:"initialMs,omitempty"`
	// Factor is the factor for increasing wait time
	Factor float64 `json:"factor,omitempty"`
	// Jitter is the factor for random variation
	Jitter float64 `json:"jitter,omitempty"`
}

// ToWaitBackoff converts BackoffConfig to k8s.io/apimachinery/pkg/util/wait.Backoff
func (c *BackoffConfig) ToWaitBackoff() wait.Backoff {
	return wait.Backoff{
		Steps:    c.Steps,
		Duration: time.Duration(c.InitialMs) * time.Millisecond,
		Factor:   c.Factor,
		Jitter:   c.Jitter,
	}
}

// DefaultIPAMConfig returns the default configuration
func DefaultIPAMConfig() *IPAMConfig {
	return &IPAMConfig{
		UpdatePoolStatus: UpdatePoolStatusConfig{
			Backoff: BackoffConfig{
				Steps:     10,
				InitialMs: 10,
				Factor:    2.0,
				Jitter:    0.2,
			},
			AsyncTimeoutSec: 3,
		},
		EnablePoolClaim: false,
	}
}

// LoadFromFile loads configuration from a file
func LoadFromFile(path string) (*IPAMConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	config := DefaultIPAMConfig()
	if err := json.Unmarshal(data, config); err != nil {
		return nil, err
	}

	return config, nil
}
