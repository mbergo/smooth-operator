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

// makeAggregated returns a minimal AggregatedContext for prompt tests.
func makeAggregated(ns, userPrompt string) (*collector.AggregatedContext, string) {
	agg := &collector.AggregatedContext{
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
							Image:             "myrepo/api:v1.2.3",
							RequestsCPU:       "100m",
							RequestsMemory:    "128Mi",
							HasReadinessProbe: true,
							HasLivenessProbe:  true,
						},
					},
					YAMLSnippet: "name: api-server",
				},
				{
					Name:          "worker",
					Replicas:      2,
					ReadyReplicas: 1,
					Labels:        map[string]string{"app": "worker"},
					Containers: []collector.ContainerInfo{
						{
							Name: "worker",
						},
					},
				},
			},
			Services: []collector.ServiceInfo{
				{Name: "api-svc", Type: "ClusterIP"},
			},
			Ingresses: []collector.IngressInfo{
				{Name: "api-ingress"},
			},
		},
		MetricsSnapshots: map[string]*metrics.MetricsSnapshot{
			"api-server": {
				CPUUsageAverage:    0.8,
				MemoryUsageAverage: 256 * 1024 * 1024,
				RequestsPerSecond:  42.5,
				ErrorRate:          0.5,
			},
		},
		Summary: collector.ContextSummary{
			TotalDeployments:            2,
			TotalServices:               1,
			TotalIngresses:              1,
			DeploymentsWithoutService:   []string{"worker"},
			DeploymentsWithoutProbes:    []string{"worker"},
			DeploymentsWithoutResources: []string{"worker"},
			TextSummary:                 "Namespace contains 2 deployment(s).",
		},
	}
	return agg, userPrompt
}

func TestBuildSystemPrompt(t *testing.T) {
	pb := NewPromptBuilder()
	sys := pb.BuildSystemPrompt()

	checks := []struct {
		name    string
		snippet string
	}{
		{"kubernetes role", "Kubernetes"},
		{"json output instruction", "JSON"},
		{"no credentials rule", "credentials"},
		{"confidence mention", "confidence"},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(sys, tc.snippet) {
				t.Errorf("system prompt missing %q; got:\n%s", tc.snippet, sys)
			}
		})
	}
}

func TestBuildPrompt_UserPromptSection(t *testing.T) {
	pb := NewPromptBuilder()
	agg, userPrompt := makeAggregated("staging", "scale up the api-server")

	prompt := pb.BuildPrompt(userPrompt, agg)

	if !strings.Contains(prompt, "scale up the api-server") {
		t.Errorf("prompt missing user_prompt text; got length=%d", len(prompt))
	}
	if !strings.Contains(prompt, "USER REQUEST") {
		t.Errorf("prompt missing USER REQUEST label")
	}
}

func TestBuildPrompt_ClusterContext(t *testing.T) {
	pb := NewPromptBuilder()
	agg, up := makeAggregated("production", "add hpa")

	prompt := pb.BuildPrompt(up, agg)

	checks := []struct {
		name    string
		snippet string
	}{
		{"namespace", "production"},
		{"deployment name api-server", "api-server"},
		{"cluster state header", "CLUSTER STATE"},
		{"summary text", "Namespace contains"},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(prompt, tc.snippet) {
				t.Errorf("prompt missing cluster_context section %q", tc.snippet)
			}
		})
	}
}

func TestBuildPrompt_MetricsSnapshot(t *testing.T) {
	pb := NewPromptBuilder()
	agg, up := makeAggregated("prod", "optimise resources")

	prompt := pb.BuildPrompt(up, agg)

	if !strings.Contains(prompt, "METRICS") {
		t.Errorf("prompt missing METRICS section")
	}
	// api-server has metrics entries; verify at least the deployment name appears near metrics
	if !strings.Contains(prompt, "api-server") {
		t.Errorf("prompt missing api-server in metrics section")
	}
}

func TestBuildPrompt_PoliciesSummary_Gaps(t *testing.T) {
	pb := NewPromptBuilder()
	agg, up := makeAggregated("dev", "review")

	prompt := pb.BuildPrompt(up, agg)

	// The "policies summary" / gap analysis section must mention deployments without services
	if !strings.Contains(prompt, "worker") {
		t.Errorf("prompt missing worker in gaps section")
	}
	if !strings.Contains(prompt, "DETECTED GAPS") {
		t.Errorf("prompt missing DETECTED GAPS section")
	}
}

func TestBuildPrompt_MoreThanFiveDeployments_Truncated(t *testing.T) {
	pb := NewPromptBuilder()
	agg := &collector.AggregatedContext{
		ClusterContext: &collector.ClusterContext{
			TargetNamespace: "big",
		},
		MetricsSnapshots: map[string]*metrics.MetricsSnapshot{},
		Summary:          collector.ContextSummary{TextSummary: "many deployments"},
	}
	for i := range 7 {
		agg.ClusterContext.Deployments = append(agg.ClusterContext.Deployments,
			collector.DeploymentInfo{Name: strings.Repeat("x", i+1) + "-svc"})
	}

	prompt := pb.BuildPrompt("test", agg)

	// The builder caps at 5 and appends "... and N more"
	if !strings.Contains(prompt, "more") {
		t.Errorf("prompt did not truncate deployments list; expected 'more' suffix")
	}
}

func TestBuildPrompt_NoMetrics_SectionAbsent(t *testing.T) {
	pb := NewPromptBuilder()
	agg := &collector.AggregatedContext{
		ClusterContext: &collector.ClusterContext{
			TargetNamespace: "empty",
		},
		MetricsSnapshots: map[string]*metrics.MetricsSnapshot{},
		Summary:          collector.ContextSummary{TextSummary: "no metrics"},
	}

	prompt := pb.BuildPrompt("test", agg)

	if strings.Contains(prompt, "METRICS") {
		t.Errorf("prompt should not contain METRICS section when snapshots are absent")
	}
}

func TestBuildPrompt_TokenSizeCap(t *testing.T) {
	// Ensure extremely large deployments don't silently create prompts > 50 KB.
	// The builder caps at 5 deployments; each YAML snippet can still be large,
	// but the hard limit we assert here is a sanity check on runaway growth.
	const maxExpectedBytes = 50_000

	pb := NewPromptBuilder()
	agg := &collector.AggregatedContext{
		ClusterContext: &collector.ClusterContext{
			TargetNamespace: "big",
		},
		MetricsSnapshots: map[string]*metrics.MetricsSnapshot{},
		Summary:          collector.ContextSummary{TextSummary: strings.Repeat("x", 1000)},
	}
	// 5 deployments each with a large YAML snippet
	bigYAML := strings.Repeat("y: "+strings.Repeat("z", 200)+"\n", 10)
	for i := range 5 {
		agg.ClusterContext.Deployments = append(agg.ClusterContext.Deployments,
			collector.DeploymentInfo{
				Name:        strings.Repeat("d", i+1),
				YAMLSnippet: bigYAML,
			})
	}

	prompt := pb.BuildPrompt(strings.Repeat("u", 500), agg)

	if len(prompt) > maxExpectedBytes {
		t.Errorf("prompt exceeds expected size cap: got %d bytes, want <= %d", len(prompt), maxExpectedBytes)
	}
}
