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
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// SubnetStatusUpdateTotal counts Subnet status updates
	SubnetStatusUpdateTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aquanaut_ipam_subnet_status_update_total",
			Help: "Total number of Subnet status update attempts",
		},
		[]string{"result"},
	)

	// SubnetStatusUpdateLatency measures Subnet status update latency
	SubnetStatusUpdateLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "aquanaut_ipam_subnet_status_update_latency_seconds",
			Help:    "Latency of Subnet status update operations",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0},
		},
		[]string{"result"},
	)

	// SubnetStatusRetryTotal counts Subnet status update retries due to conflicts
	SubnetStatusRetryTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aquanaut_ipam_subnet_status_retry_total",
			Help: "Total number of Subnet status update retries due to conflicts",
		},
		[]string{"retries"},
	)
)

func init() {
	// Helper function for safe registration
	registerOrGet := func(c prometheus.Collector) {
		// Ignore duplicate registration errors (do not re-register metrics that are already registered)
		_ = metrics.Registry.Register(c) // Ignore already registered errors
	}

	// Register metrics safely
	registerOrGet(SubnetStatusUpdateTotal)
	registerOrGet(SubnetStatusUpdateLatency)
	registerOrGet(SubnetStatusRetryTotal)
}
