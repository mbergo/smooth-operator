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

package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// InferenceRequestsTotal counts total LLM inference requests
	InferenceRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "smooth_inference_requests_total",
			Help: "Total number of LLM inference requests",
		},
		[]string{"model", "status"},
	)

	// InferenceLatencySeconds measures LLM inference latency
	InferenceLatencySeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "smooth_inference_latency_seconds",
			Help:    "LLM inference latency in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"model"},
	)

	// ActionsAppliedTotal counts applied SmoothActions
	ActionsAppliedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "smooth_actions_applied_total",
			Help: "Total number of SmoothActions applied",
		},
		[]string{"mode", "namespace"},
	)

	// RollbacksTotal counts automatic rollbacks
	RollbacksTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "smooth_rollbacks_total",
			Help: "Total number of automatic rollbacks",
		},
		[]string{"reason", "namespace"},
	)

	// PolicyBlocksTotal counts policy violations
	PolicyBlocksTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "smooth_policy_blocks_total",
			Help: "Total number of policy blocks",
		},
		[]string{"policy", "severity"},
	)

	// ChatSessionsTotal counts ChatSession CRDs
	ChatSessionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "smooth_chatsessions_total",
			Help: "Total number of ChatSessions processed",
		},
		[]string{"state", "namespace"},
	)

	// ExecutionDurationSeconds measures end-to-end execution time
	ExecutionDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "smooth_execution_duration_seconds",
			Help:    "Execution duration in seconds",
			Buckets: []float64{1, 5, 10, 30, 60, 120, 300, 600},
		},
		[]string{"mode"},
	)
)

func init() {
	// Register custom metrics with controller-runtime metrics registry
	metrics.Registry.MustRegister(
		InferenceRequestsTotal,
		InferenceLatencySeconds,
		ActionsAppliedTotal,
		RollbacksTotal,
		PolicyBlocksTotal,
		ChatSessionsTotal,
		ExecutionDurationSeconds,
	)
}
