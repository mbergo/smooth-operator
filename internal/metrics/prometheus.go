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

package metrics

import (
	"context"
	"fmt"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// PrometheusClient handles queries to Prometheus for metrics collection
type PrometheusClient struct {
	api     promv1.API
	enabled bool
}

// MetricsSnapshot contains aggregated metrics for a namespace/deployment
type MetricsSnapshot struct {
	Namespace  string
	Deployment string

	// CPU metrics
	CPUUsageAverage   float64 // Average CPU usage over window
	CPUUsageCurrent   float64 // Current CPU usage
	CPURequestedTotal float64 // Total CPU requested

	// Memory metrics
	MemoryUsageAverage   float64 // Average memory usage (bytes)
	MemoryUsageCurrent   float64 // Current memory usage (bytes)
	MemoryRequestedTotal float64 // Total memory requested (bytes)

	// Request metrics
	RequestsPerSecond float64 // Average RPS over window
	ErrorRate         float64 // Percentage of errors
	P95Latency        float64 // P95 response time (ms)

	// Time window
	WindowStart time.Time
	WindowEnd   time.Time

	// Errors encountered during collection
	Errors []string
}

// PrometheusOptions configures the Prometheus client
type PrometheusOptions struct {
	// Address is the Prometheus server address (e.g., "http://prometheus:9090")
	Address string

	// QueryTimeout is the timeout for individual queries
	QueryTimeout time.Duration

	// MetricsWindow is the time window for metric queries (e.g., "5m", "10m")
	MetricsWindow string

	// Enabled determines if Prometheus collection is active
	Enabled bool
}

// DefaultPrometheusOptions returns sensible defaults
func DefaultPrometheusOptions() PrometheusOptions {
	return PrometheusOptions{
		Address:       "http://prometheus-k8s.monitoring.svc:9090",
		QueryTimeout:  30 * time.Second,
		MetricsWindow: "5m",
		Enabled:       false, // Disabled by default for Phase 1
	}
}

// NewPrometheusClient creates a new Prometheus client
func NewPrometheusClient(options PrometheusOptions) (*PrometheusClient, error) {
	if !options.Enabled {
		return &PrometheusClient{
			enabled: false,
		}, nil
	}

	client, err := promapi.NewClient(promapi.Config{
		Address: options.Address,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Prometheus client: %w", err)
	}

	return &PrometheusClient{
		api:     promv1.NewAPI(client),
		enabled: true,
	}, nil
}

// CollectMetrics gathers Prometheus metrics for a namespace/deployment
func (p *PrometheusClient) CollectMetrics(ctx context.Context, namespace, deployment string, window time.Duration) (*MetricsSnapshot, error) {
	if !p.enabled {
		return &MetricsSnapshot{
			Namespace:   namespace,
			Deployment:  deployment,
			WindowEnd:   time.Now(),
			WindowStart: time.Now().Add(-window),
			Errors:      []string{"Prometheus client is not enabled"},
		}, nil
	}

	log := log.FromContext(ctx)
	log.Info("Collecting Prometheus metrics",
		"namespace", namespace,
		"deployment", deployment,
		"window", window.String(),
	)

	snapshot := &MetricsSnapshot{
		Namespace:   namespace,
		Deployment:  deployment,
		WindowEnd:   time.Now(),
		WindowStart: time.Now().Add(-window),
		Errors:      []string{},
	}

	// Collect CPU metrics
	if err := p.collectCPUMetrics(ctx, snapshot); err != nil {
		snapshot.Errors = append(snapshot.Errors, fmt.Sprintf("CPU metrics: %v", err))
	}

	// Collect Memory metrics
	if err := p.collectMemoryMetrics(ctx, snapshot); err != nil {
		snapshot.Errors = append(snapshot.Errors, fmt.Sprintf("Memory metrics: %v", err))
	}

	// Collect Request metrics
	if err := p.collectRequestMetrics(ctx, snapshot); err != nil {
		snapshot.Errors = append(snapshot.Errors, fmt.Sprintf("Request metrics: %v", err))
	}

	log.Info("Prometheus metrics collected",
		"cpuAvg", snapshot.CPUUsageAverage,
		"memAvg", snapshot.MemoryUsageAverage,
		"rps", snapshot.RequestsPerSecond,
		"errors", len(snapshot.Errors),
	)

	return snapshot, nil
}

// collectCPUMetrics queries CPU usage metrics
func (p *PrometheusClient) collectCPUMetrics(ctx context.Context, snapshot *MetricsSnapshot) error {
	// Query: Average CPU usage over window
	// rate(container_cpu_usage_seconds_total{namespace="...",pod=~"deployment-.*"}[5m])
	query := fmt.Sprintf(
		`avg(rate(container_cpu_usage_seconds_total{namespace="%s",pod=~"%s-.*"}[%s]))`,
		snapshot.Namespace,
		snapshot.Deployment,
		"5m",
	)

	result, warnings, err := p.api.Query(ctx, query, snapshot.WindowEnd)
	if err != nil {
		return fmt.Errorf("CPU usage query failed: %w", err)
	}
	if len(warnings) > 0 {
		snapshot.Errors = append(snapshot.Errors, fmt.Sprintf("CPU warnings: %v", warnings))
	}

	if vector, ok := result.(model.Vector); ok && len(vector) > 0 {
		snapshot.CPUUsageAverage = float64(vector[0].Value)
		snapshot.CPUUsageCurrent = float64(vector[0].Value)
	}

	// Query: Total CPU requested
	queryRequests := fmt.Sprintf(
		`sum(kube_pod_container_resource_requests{namespace="%s",pod=~"%s-.*",resource="cpu"})`,
		snapshot.Namespace,
		snapshot.Deployment,
	)

	resultReq, _, err := p.api.Query(ctx, queryRequests, snapshot.WindowEnd)
	if err == nil {
		if vector, ok := resultReq.(model.Vector); ok && len(vector) > 0 {
			snapshot.CPURequestedTotal = float64(vector[0].Value)
		}
	}

	return nil
}

// collectMemoryMetrics queries memory usage metrics
func (p *PrometheusClient) collectMemoryMetrics(ctx context.Context, snapshot *MetricsSnapshot) error {
	// Query: Average memory usage over window
	query := fmt.Sprintf(
		`avg(container_memory_usage_bytes{namespace="%s",pod=~"%s-.*"})`,
		snapshot.Namespace,
		snapshot.Deployment,
	)

	result, warnings, err := p.api.Query(ctx, query, snapshot.WindowEnd)
	if err != nil {
		return fmt.Errorf("memory usage query failed: %w", err)
	}
	if len(warnings) > 0 {
		snapshot.Errors = append(snapshot.Errors, fmt.Sprintf("Memory warnings: %v", warnings))
	}

	if vector, ok := result.(model.Vector); ok && len(vector) > 0 {
		snapshot.MemoryUsageAverage = float64(vector[0].Value)
		snapshot.MemoryUsageCurrent = float64(vector[0].Value)
	}

	// Query: Total memory requested
	queryRequests := fmt.Sprintf(
		`sum(kube_pod_container_resource_requests{namespace="%s",pod=~"%s-.*",resource="memory"})`,
		snapshot.Namespace,
		snapshot.Deployment,
	)

	resultReq, _, err := p.api.Query(ctx, queryRequests, snapshot.WindowEnd)
	if err == nil {
		if vector, ok := resultReq.(model.Vector); ok && len(vector) > 0 {
			snapshot.MemoryRequestedTotal = float64(vector[0].Value)
		}
	}

	return nil
}

// collectRequestMetrics queries HTTP request metrics (if available)
func (p *PrometheusClient) collectRequestMetrics(ctx context.Context, snapshot *MetricsSnapshot) error {
	// Query: Requests per second (using common HTTP metrics)
	// This assumes standard Prometheus HTTP metrics are available
	query := fmt.Sprintf(
		`sum(rate(http_requests_total{namespace="%s",deployment="%s"}[5m]))`,
		snapshot.Namespace,
		snapshot.Deployment,
	)

	result, warnings, err := p.api.Query(ctx, query, snapshot.WindowEnd)
	if err != nil {
		// RPS metrics might not be available - this is OK, not all apps expose them
		return nil
	}
	if len(warnings) > 0 {
		snapshot.Errors = append(snapshot.Errors, fmt.Sprintf("Request warnings: %v", warnings))
	}

	if vector, ok := result.(model.Vector); ok && len(vector) > 0 {
		snapshot.RequestsPerSecond = float64(vector[0].Value)
	}

	// Query: Error rate
	queryErrors := fmt.Sprintf(
		`sum(rate(http_requests_total{namespace="%s",deployment="%s",status=~"5.."}[5m])) /`+
			`sum(rate(http_requests_total{namespace="%s",deployment="%s"}[5m]))`,
		snapshot.Namespace, snapshot.Deployment,
		snapshot.Namespace, snapshot.Deployment,
	)

	resultErr, _, err := p.api.Query(ctx, queryErrors, snapshot.WindowEnd)
	if err == nil {
		if vector, ok := resultErr.(model.Vector); ok && len(vector) > 0 {
			snapshot.ErrorRate = float64(vector[0].Value) * 100 // Convert to percentage
		}
	}

	// Query: P95 latency (if histogram metrics available)
	queryP95 := fmt.Sprintf(
		`histogram_quantile(0.95, sum(rate(http_request_duration_seconds_bucket{`+
			`namespace="%s",deployment="%s"}[5m])) by (le))`,
		snapshot.Namespace,
		snapshot.Deployment,
	)

	resultP95, _, err := p.api.Query(ctx, queryP95, snapshot.WindowEnd)
	if err == nil {
		if vector, ok := resultP95.(model.Vector); ok && len(vector) > 0 {
			snapshot.P95Latency = float64(vector[0].Value) * 1000 // Convert to ms
		}
	}

	return nil
}

// IsEnabled returns whether the Prometheus client is enabled
func (p *PrometheusClient) IsEnabled() bool {
	return p.enabled
}

// HealthCheck performs a basic health check against Prometheus
func (p *PrometheusClient) HealthCheck(ctx context.Context) error {
	if !p.enabled {
		return fmt.Errorf("prometheus client is not enabled")
	}

	_, _, err := p.api.Query(ctx, "up", time.Now())
	if err != nil {
		return fmt.Errorf("prometheus health check failed: %w", err)
	}

	return nil
}
