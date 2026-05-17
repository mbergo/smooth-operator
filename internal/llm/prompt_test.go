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

package llm

import (
	"strings"
	"testing"

	"github.com/mbergo/smooth-operator/internal/collector"
	"github.com/mbergo/smooth-operator/internal/metrics"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// minimalAggregated builds a small but non-trivial AggregatedContext.
func minimalAggregated(ns string) *collector.AggregatedContext {
	return &collector.AggregatedContext{
		ClusterContext: &collector.ClusterContext{
			TargetNamespace: ns,
			Deployments: []collector.DeploymentInfo{
				{
					Name:          "api-server",
					Replicas:      3,
					ReadyReplicas: 3,
					Labels:        map[string]string{"app": "api-server"},
					Containers: []collector.ContainerInfo{
						{
							Name:              "api-server",
							Image:             "ghcr.io/org/api:v1",
							RequestsCPU:       "200m",
							RequestsMemory:    "256Mi",
							HasReadinessProbe: true,
							HasLivenessProbe:  true,
						},
					},
					YAMLSnippet: "name: api-server",
				},
			},
			Services: []collector.ServiceInfo{
				{Name: "api-svc", Type: "ClusterIP"},
			},
			Ingresses: []collector.IngressInfo{
				{Name: "api-ingress"},
			},
		},
		MetricsSnapshots: map[string]*metrics.MetricsSnapshot{},
		Summary: collector.ContextSummary{
			TextSummary: "Namespace contains 1 deployment(s).",
		},
	}
}

// ---------------------------------------------------------------------------
// Stage 1: Reasoner system prompt
// ---------------------------------------------------------------------------

func TestBuildReasonerSystem(t *testing.T) {
	pb := NewPromptBuilder()
	sys := pb.BuildReasonerSystem()

	checks := []struct {
		name    string
		snippet string
	}{
		{"smooth reasoner identity", "Smooth Reasoner"},
		{"output schema header", "OUTPUT SCHEMA"},
		{"no yaml rule", "Do NOT produce YAML"},
		{"confidence field", "confidence"},
		{"risk field", "risk"},
		{"inferredNeeds field", "inferredNeeds"},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(sys, tc.snippet) {
				t.Errorf("reasoner system prompt missing %q", tc.snippet)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Stage 1: Reasoner user prompt
// ---------------------------------------------------------------------------

func TestBuildReasonerUser_IncludesUserPrompt(t *testing.T) {
	pb := NewPromptBuilder()
	agg := minimalAggregated("staging")
	prompt := pb.BuildReasonerUser("scale up the api-server", agg)

	if !strings.Contains(prompt, "scale up the api-server") {
		t.Errorf("reasoner user prompt missing user prompt text; len=%d", len(prompt))
	}
	if !strings.Contains(prompt, "<user_request>") {
		t.Errorf("reasoner user prompt missing <user_request> delimiter")
	}
}

func TestBuildReasonerUser_IncludesPolicySummary(t *testing.T) {
	pb := NewPromptBuilder()
	agg := minimalAggregated("staging")
	prompt := pb.BuildReasonerUser("test", agg)

	if !strings.Contains(prompt, "POLICY CONSTRAINTS") {
		t.Errorf("reasoner user prompt missing POLICY CONSTRAINTS header")
	}
	// The default policy summary includes the image registry list.
	if !strings.Contains(prompt, "docker.io") {
		t.Errorf("reasoner user prompt missing default policy content (docker.io)")
	}
}

func TestBuildReasonerUser_IncludesNamespace(t *testing.T) {
	pb := NewPromptBuilder()
	agg := minimalAggregated("production")
	prompt := pb.BuildReasonerUser("review", agg)

	if !strings.Contains(prompt, "production") {
		t.Errorf("reasoner user prompt missing namespace 'production'")
	}
	if !strings.Contains(prompt, "<cluster_state>") {
		t.Errorf("reasoner user prompt missing <cluster_state> delimiter")
	}
}

func TestBuildReasonerUser_IncludesDeployments(t *testing.T) {
	pb := NewPromptBuilder()
	agg := minimalAggregated("test-ns")
	prompt := pb.BuildReasonerUser("check deployments", agg)

	if !strings.Contains(prompt, "api-server") {
		t.Errorf("reasoner user prompt missing deployment name 'api-server'")
	}
	if !strings.Contains(prompt, "DEPLOYMENTS") {
		t.Errorf("reasoner user prompt missing DEPLOYMENTS header")
	}
}

func TestBuildReasonerUser_Truncates5PlusDeployments(t *testing.T) {
	pb := NewPromptBuilder()
	agg := &collector.AggregatedContext{
		ClusterContext: &collector.ClusterContext{
			TargetNamespace: "big-ns",
		},
		MetricsSnapshots: map[string]*metrics.MetricsSnapshot{},
		Summary:          collector.ContextSummary{TextSummary: "many"},
	}
	for i := 0; i < 7; i++ {
		agg.ClusterContext.Deployments = append(agg.ClusterContext.Deployments,
			collector.DeploymentInfo{Name: strings.Repeat("d", i+1)})
	}

	prompt := pb.BuildReasonerUser("test", agg)

	if !strings.Contains(prompt, "more") {
		t.Errorf("reasoner user prompt should truncate >5 deployments with 'more' suffix")
	}
}

func TestBuildReasonerUser_MissingAggregatedNil(t *testing.T) {
	pb := NewPromptBuilder()
	// Must not panic when aggregated is nil.
	prompt := pb.BuildReasonerUser("test prompt", nil)

	if !strings.Contains(prompt, `available="false"`) {
		t.Errorf("nil aggregated should mark cluster_state available=false; got:\n%s", prompt)
	}
	// User prompt must still appear.
	if !strings.Contains(prompt, "test prompt") {
		t.Errorf("nil aggregated prompt missing user prompt text")
	}
}

func TestBuildReasonerUser_Metrics(t *testing.T) {
	pb := NewPromptBuilder()
	agg := &collector.AggregatedContext{
		ClusterContext: &collector.ClusterContext{
			TargetNamespace: "metrics-ns",
			Deployments: []collector.DeploymentInfo{
				{Name: "web", Replicas: 2, ReadyReplicas: 2},
			},
		},
		MetricsSnapshots: map[string]*metrics.MetricsSnapshot{
			"web": {
				CPUUsageAverage:    1.2,
				MemoryUsageAverage: 512 * 1024 * 1024,
				RequestsPerSecond:  99.9,
				ErrorRate:          0.1,
			},
		},
		Summary: collector.ContextSummary{TextSummary: "has metrics"},
	}

	prompt := pb.BuildReasonerUser("optimize", agg)

	if !strings.Contains(prompt, "METRICS") {
		t.Errorf("reasoner user prompt missing METRICS section when snapshots present")
	}
	if !strings.Contains(prompt, "web") {
		t.Errorf("reasoner user prompt missing deployment name 'web' in metrics section")
	}
}

func TestBuildReasonerUser_Gaps(t *testing.T) {
	pb := NewPromptBuilder()
	agg := &collector.AggregatedContext{
		ClusterContext: &collector.ClusterContext{
			TargetNamespace: "gap-ns",
			Deployments: []collector.DeploymentInfo{
				{Name: "worker", Replicas: 1, ReadyReplicas: 1},
			},
		},
		MetricsSnapshots: map[string]*metrics.MetricsSnapshot{},
		Summary: collector.ContextSummary{
			TextSummary:                 "gaps present",
			DeploymentsWithoutService:   []string{"worker"},
			DeploymentsWithoutProbes:    []string{"worker"},
			DeploymentsWithoutResources: []string{"worker"},
		},
	}

	prompt := pb.BuildReasonerUser("review", agg)

	if !strings.Contains(prompt, "DETECTED GAPS") {
		t.Errorf("reasoner user prompt missing DETECTED GAPS section")
	}
	if !strings.Contains(prompt, "worker") {
		t.Errorf("reasoner user prompt missing 'worker' in gaps section")
	}
}

func TestBuildReasonerUser_Perf(t *testing.T) {
	pb := NewPromptBuilder()
	agg := &collector.AggregatedContext{
		ClusterContext: &collector.ClusterContext{
			TargetNamespace: "perf-ns",
			Deployments: []collector.DeploymentInfo{
				{Name: "hot-svc", Replicas: 2, ReadyReplicas: 2},
			},
		},
		MetricsSnapshots: map[string]*metrics.MetricsSnapshot{},
		Summary: collector.ContextSummary{
			TextSummary:           "performance issues",
			HighCPUDeployments:    []string{"hot-svc"},
			HighMemoryDeployments: []string{"hot-svc"},
		},
	}

	prompt := pb.BuildReasonerUser("scale", agg)

	if !strings.Contains(prompt, "PERFORMANCE SIGNALS") {
		t.Errorf("reasoner user prompt missing PERFORMANCE SIGNALS section")
	}
	if !strings.Contains(prompt, "hot-svc") {
		t.Errorf("reasoner user prompt missing 'hot-svc' in performance section")
	}
}

// ---------------------------------------------------------------------------
// Stage 2: Generator system prompt
// ---------------------------------------------------------------------------

func TestBuildGeneratorSystem(t *testing.T) {
	pb := NewPromptBuilder()
	sys := pb.BuildGeneratorSystem()

	checks := []struct {
		name    string
		snippet string
	}{
		{"smooth generator identity", "Smooth Generator"},
		{"patches field", "patches"},
		{"kubectl apply rule", "kubectl apply"},
		{"yaml field", "yaml"},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(sys, tc.snippet) {
				t.Errorf("generator system prompt missing %q", tc.snippet)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Stage 2: Generator user prompt
// ---------------------------------------------------------------------------

func TestBuildGeneratorUser(t *testing.T) {
	pb := NewPromptBuilder()
	reasoner := &ReasonerOutput{
		InferredNeeds: []InferredNeed{
			{Type: "HPA", Reason: "high CPU", Priority: "high", Spec: "minReplicas=2"},
		},
		Confidence:  0.85,
		Risk:        "low",
		Explanation: "scale needed",
	}

	prompt := pb.BuildGeneratorUser("add HPA for api-server", "production", reasoner)

	checks := []struct {
		name    string
		snippet string
	}{
		{"user prompt present", "add HPA for api-server"},
		{"namespace present", "production"},
		{"reasoner json present", "inferredNeeds"},
		{"hpa type present", "HPA"},
		{"policy constraints present", "POLICY CONSTRAINTS"},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(prompt, tc.snippet) {
				t.Errorf("generator user prompt missing %q", tc.snippet)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// WithPolicySummary
// ---------------------------------------------------------------------------

func TestWithPolicySummary(t *testing.T) {
	pb := NewPromptBuilder()
	custom := "custom-policy: no-privesc"

	pbCopy := pb.WithPolicySummary(custom)

	// Original must be unmodified.
	if pb.PolicySummary == custom {
		t.Error("WithPolicySummary must return a copy; original was mutated")
	}
	if pbCopy.PolicySummary != custom {
		t.Errorf("copy PolicySummary = %q, want %q", pbCopy.PolicySummary, custom)
	}
	// The copy's prompts must include the custom policy.
	sys := pbCopy.BuildReasonerUser("test", nil)
	if !strings.Contains(sys, custom) {
		t.Errorf("BuildReasonerUser on copy does not include custom policy")
	}
}
