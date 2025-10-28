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

package collector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mbergo/smooth-operator/internal/logs"
	"github.com/mbergo/smooth-operator/internal/metrics"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// AggregatedContext combines cluster context with observability data
type AggregatedContext struct {
	// Core cluster context
	ClusterContext *ClusterContext

	// Metrics from Prometheus
	MetricsSnapshots map[string]*metrics.MetricsSnapshot // Key: deployment name

	// Logs from Loki
	LogSummaries map[string]*logs.LogSummary // Key: deployment name

	// Aggregated summary for LLM
	Summary ContextSummary

	// Collection metadata
	CollectionDuration time.Duration
	CollectedAt        time.Time
}

// ContextSummary provides a high-level summary of the collected context
type ContextSummary struct {
	// Resource counts
	TotalDeployments int
	TotalServices    int
	TotalIngresses   int
	TotalPods        int
	TotalEvents      int

	// Health indicators
	HealthyDeployments   int
	UnhealthyDeployments int
	PodsWithRestarts     int

	// Resource gaps (missing components)
	DeploymentsWithoutService   []string
	DeploymentsWithoutProbes    []string
	DeploymentsWithoutResources []string
	DeploymentsWithoutHPA       []string

	// Performance indicators (from metrics)
	HighCPUDeployments    []string // >70% CPU usage
	HighMemoryDeployments []string // >80% memory usage
	HighErrorRates        []string // >5% error rate

	// Textual summary for LLM
	TextSummary string
}

// Aggregator combines data from multiple sources
type Aggregator struct {
	collector     *Collector
	metricsClient *metrics.PrometheusClient
	logsClient    *logs.LokiClient
	metricsWindow time.Duration
}

// NewAggregator creates a new context aggregator
func NewAggregator(
	collector *Collector,
	metricsClient *metrics.PrometheusClient,
	logsClient *logs.LokiClient,
	metricsWindow time.Duration,
) *Aggregator {
	return &Aggregator{
		collector:     collector,
		metricsClient: metricsClient,
		logsClient:    logsClient,
		metricsWindow: metricsWindow,
	}
}

// AggregateContext collects and aggregates all available context
func (a *Aggregator) AggregateContext(ctx context.Context, namespace, chatSessionName string) (*AggregatedContext, error) {
	log := log.FromContext(ctx)
	startTime := time.Now()

	log.Info("Starting context aggregation",
		"namespace", namespace,
		"chatSession", chatSessionName,
	)

	// Step 1: Collect cluster context
	clusterCtx, err := a.collector.CollectContext(ctx, namespace, chatSessionName)
	if err != nil {
		return nil, fmt.Errorf("failed to collect cluster context: %w", err)
	}

	aggregated := &AggregatedContext{
		ClusterContext:   clusterCtx,
		MetricsSnapshots: make(map[string]*metrics.MetricsSnapshot),
		LogSummaries:     make(map[string]*logs.LogSummary),
		CollectedAt:      time.Now(),
	}

	// Step 2: Collect metrics for each deployment (if Prometheus is enabled)
	if a.metricsClient != nil && a.metricsClient.IsEnabled() {
		for _, dep := range clusterCtx.Deployments {
			metricsSnapshot, err := a.metricsClient.CollectMetrics(ctx, namespace, dep.Name, a.metricsWindow)
			if err != nil {
				log.Error(err, "Failed to collect metrics for deployment", "deployment", dep.Name)
				// Continue with other deployments
				continue
			}
			aggregated.MetricsSnapshots[dep.Name] = metricsSnapshot
		}
	}

	// Step 3: Collect logs for each deployment (if Loki is enabled)
	if a.logsClient != nil && a.logsClient.IsEnabled() {
		for _, dep := range clusterCtx.Deployments {
			logSummary, err := a.logsClient.CollectLogs(ctx, namespace, dep.Name, a.metricsWindow)
			if err != nil {
				log.Error(err, "Failed to collect logs for deployment", "deployment", dep.Name)
				// Continue with other deployments
				continue
			}
			aggregated.LogSummaries[dep.Name] = logSummary
		}
	}

	// Step 4: Generate summary
	aggregated.Summary = a.generateSummary(aggregated)

	aggregated.CollectionDuration = time.Since(startTime)

	log.Info("Context aggregation complete",
		"duration", aggregated.CollectionDuration.String(),
		"deployments", aggregated.Summary.TotalDeployments,
		"metricsCollected", len(aggregated.MetricsSnapshots),
		"logsCollected", len(aggregated.LogSummaries),
	)

	return aggregated, nil
}

// generateSummary creates a high-level summary from aggregated data
func (a *Aggregator) generateSummary(aggregated *AggregatedContext) ContextSummary {
	summary := ContextSummary{
		TotalDeployments:            len(aggregated.ClusterContext.Deployments),
		TotalServices:               len(aggregated.ClusterContext.Services),
		TotalIngresses:              len(aggregated.ClusterContext.Ingresses),
		TotalPods:                   len(aggregated.ClusterContext.Pods),
		TotalEvents:                 len(aggregated.ClusterContext.Events),
		DeploymentsWithoutService:   []string{},
		DeploymentsWithoutProbes:    []string{},
		DeploymentsWithoutResources: []string{},
		DeploymentsWithoutHPA:       []string{},
		HighCPUDeployments:          []string{},
		HighMemoryDeployments:       []string{},
		HighErrorRates:              []string{},
	}

	// Analyze deployments
	for _, dep := range aggregated.ClusterContext.Deployments {
		// Check health
		if dep.ReadyReplicas == dep.Replicas {
			summary.HealthyDeployments++
		} else {
			summary.UnhealthyDeployments++
		}

		// Check for missing Service
		if !a.hasMatchingService(dep, aggregated.ClusterContext.Services) {
			summary.DeploymentsWithoutService = append(summary.DeploymentsWithoutService, dep.Name)
		}

		// Check for missing probes
		missingProbes := false
		for _, container := range dep.Containers {
			if !container.HasReadinessProbe || !container.HasLivenessProbe {
				missingProbes = true
				break
			}
		}
		if missingProbes {
			summary.DeploymentsWithoutProbes = append(summary.DeploymentsWithoutProbes, dep.Name)
		}

		// Check for missing resource requests/limits
		missingResources := false
		for _, container := range dep.Containers {
			if container.RequestsCPU == "" || container.RequestsMemory == "" {
				missingResources = true
				break
			}
		}
		if missingResources {
			summary.DeploymentsWithoutResources = append(summary.DeploymentsWithoutResources, dep.Name)
		}

		// Analyze metrics if available
		if metricsSnap, ok := aggregated.MetricsSnapshots[dep.Name]; ok {
			// High CPU check (>70%)
			if metricsSnap.CPURequestedTotal > 0 {
				cpuPercent := (metricsSnap.CPUUsageAverage / metricsSnap.CPURequestedTotal) * 100
				if cpuPercent > 70 {
					summary.HighCPUDeployments = append(summary.HighCPUDeployments, dep.Name)
				}
			}

			// High memory check (>80%)
			if metricsSnap.MemoryRequestedTotal > 0 {
				memPercent := (metricsSnap.MemoryUsageAverage / metricsSnap.MemoryRequestedTotal) * 100
				if memPercent > 80 {
					summary.HighMemoryDeployments = append(summary.HighMemoryDeployments, dep.Name)
				}
			}

			// High error rate check (>5%)
			if metricsSnap.ErrorRate > 5.0 {
				summary.HighErrorRates = append(summary.HighErrorRates, dep.Name)
			}
		}
	}

	// Count pods with restarts
	for _, pod := range aggregated.ClusterContext.Pods {
		if pod.RestartCount > 0 {
			summary.PodsWithRestarts++
		}
	}

	// Generate textual summary
	summary.TextSummary = a.generateTextSummary(summary)

	return summary
}

// hasMatchingService checks if a deployment has a matching Service
func (a *Aggregator) hasMatchingService(dep DeploymentInfo, services []ServiceInfo) bool {
	for _, svc := range services {
		// Check if service selector matches deployment labels
		if a.selectorsMatch(svc.Selector, dep.Labels) {
			return true
		}
	}
	return false
}

// selectorsMatch checks if a selector matches labels
func (a *Aggregator) selectorsMatch(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

// generateTextSummary creates a human-readable summary
func (a *Aggregator) generateTextSummary(summary ContextSummary) string {
	var builder strings.Builder

	builder.WriteString(fmt.Sprintf("Namespace contains %d deployment(s), %d service(s), %d ingress(es), %d pod(s).\n",
		summary.TotalDeployments, summary.TotalServices, summary.TotalIngresses, summary.TotalPods))

	builder.WriteString(fmt.Sprintf("Health status: %d healthy, %d unhealthy deployments.\n",
		summary.HealthyDeployments, summary.UnhealthyDeployments))

	// Resource gaps
	if len(summary.DeploymentsWithoutService) > 0 {
		builder.WriteString(fmt.Sprintf("⚠️  Deployments without Services: %s\n",
			strings.Join(summary.DeploymentsWithoutService, ", ")))
	}
	if len(summary.DeploymentsWithoutProbes) > 0 {
		builder.WriteString(fmt.Sprintf("⚠️  Deployments missing health probes: %s\n",
			strings.Join(summary.DeploymentsWithoutProbes, ", ")))
	}
	if len(summary.DeploymentsWithoutResources) > 0 {
		builder.WriteString(fmt.Sprintf("⚠️  Deployments without resource requests: %s\n",
			strings.Join(summary.DeploymentsWithoutResources, ", ")))
	}

	// Performance issues
	if len(summary.HighCPUDeployments) > 0 {
		builder.WriteString(fmt.Sprintf("🔥 High CPU usage (>70%%): %s\n",
			strings.Join(summary.HighCPUDeployments, ", ")))
	}
	if len(summary.HighMemoryDeployments) > 0 {
		builder.WriteString(fmt.Sprintf("🔥 High memory usage (>80%%): %s\n",
			strings.Join(summary.HighMemoryDeployments, ", ")))
	}
	if len(summary.HighErrorRates) > 0 {
		builder.WriteString(fmt.Sprintf("❌ High error rates (>5%%): %s\n",
			strings.Join(summary.HighErrorRates, ", ")))
	}

	if summary.PodsWithRestarts > 0 {
		builder.WriteString(fmt.Sprintf("🔄 %d pod(s) have restarted.\n", summary.PodsWithRestarts))
	}

	return builder.String()
}
