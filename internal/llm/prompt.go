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
	"fmt"
	"strings"

	"github.com/mbergo/smooth-operator/internal/collector"
)

// PromptBuilder constructs LLM prompts from aggregated context
type PromptBuilder struct{}

// NewPromptBuilder creates a new prompt builder
func NewPromptBuilder() *PromptBuilder {
	return &PromptBuilder{}
}

// BuildPrompt creates a structured prompt for the LLM
func (pb *PromptBuilder) BuildPrompt(
	userPrompt string,
	aggregated *collector.AggregatedContext,
) string {
	var prompt strings.Builder

	// System instructions
	prompt.WriteString("You are Smooth Planner, a Kubernetes expert AI.\n\n")
	prompt.WriteString("Your job: Analyze the cluster state and suggest improvements.\n\n")
	prompt.WriteString("RULES:\n")
	prompt.WriteString("1. Output ONLY valid JSON matching the schema\n")
	prompt.WriteString("2. Never invent secrets or credentials\n")
	prompt.WriteString("3. Prefer minimal, safe manifests\n")
	prompt.WriteString("4. Justify every change with clear reasoning\n")
	prompt.WriteString("5. Set confidence (0.0-1.0) and risk (low/med/high)\n\n")

	// Expected JSON schema
	prompt.WriteString("EXPECTED JSON SCHEMA:\n")
	prompt.WriteString("```json\n")
	prompt.WriteString(`{
  "inferredNeeds": [
    {
      "type": "HPA|Service-LB|Ingress|Probe|NetworkPolicy|PVC",
      "reason": "Why this is needed",
      "priority": "low|med|high",
      "spec": "Brief configuration suggestion"
    }
  ],
  "patches": [
    {
      "kind": "Deployment|Service|HPA|Ingress",
      "yaml": "Full YAML manifest"
    }
  ],
  "confidence": 0.85,
  "risk": "low|med|high",
  "explanation": "Overall reasoning"
}
`)
	prompt.WriteString("```\n\n")

	// User's request
	prompt.WriteString("═══════════════════════════════════════════════════════════\n")
	prompt.WriteString("USER REQUEST:\n")
	prompt.WriteString(fmt.Sprintf("%s\n", userPrompt))
	prompt.WriteString("═══════════════════════════════════════════════════════════\n\n")

	// Cluster context summary
	prompt.WriteString("CLUSTER STATE:\n")
	prompt.WriteString(fmt.Sprintf("Namespace: %s\n", aggregated.ClusterContext.TargetNamespace))
	prompt.WriteString(aggregated.Summary.TextSummary)
	prompt.WriteString("\n")

	// Deployments
	if len(aggregated.ClusterContext.Deployments) > 0 {
		prompt.WriteString("\nDEPLOYMENTS:\n")
		for i, dep := range aggregated.ClusterContext.Deployments {
			if i >= 5 {
				prompt.WriteString(fmt.Sprintf("... and %d more\n", len(aggregated.ClusterContext.Deployments)-5))
				break
			}
			prompt.WriteString(fmt.Sprintf("\n%s (replicas: %d/%d):\n",
				dep.Name, dep.ReadyReplicas, dep.Replicas))
			if dep.YAMLSnippet != "" {
				prompt.WriteString(dep.YAMLSnippet)
				prompt.WriteString("\n")
			}
		}
	}

	// Services
	if len(aggregated.ClusterContext.Services) > 0 {
		prompt.WriteString("\nSERVICES:\n")
		for _, svc := range aggregated.ClusterContext.Services {
			prompt.WriteString(fmt.Sprintf("- %s (type: %s)\n", svc.Name, svc.Type))
		}
		prompt.WriteString("\n")
	}

	// Ingresses
	if len(aggregated.ClusterContext.Ingresses) > 0 {
		prompt.WriteString("\nINGRESSES:\n")
		for _, ing := range aggregated.ClusterContext.Ingresses {
			prompt.WriteString(fmt.Sprintf("- %s (rules: %d)\n", ing.Name, len(ing.Rules)))
		}
		prompt.WriteString("\n")
	}

	// Metrics (if available)
	if len(aggregated.MetricsSnapshots) > 0 {
		prompt.WriteString("\nMETRICS:\n")
		for name, metrics := range aggregated.MetricsSnapshots {
			prompt.WriteString(fmt.Sprintf("- %s: CPU=%.2f, Memory=%.0fMB, RPS=%.1f, Errors=%.1f%%\n",
				name,
				metrics.CPUUsageAverage,
				metrics.MemoryUsageAverage/1024/1024,
				metrics.RequestsPerSecond,
				metrics.ErrorRate))
		}
		prompt.WriteString("\n")
	}

	// Resource gaps
	if len(aggregated.Summary.DeploymentsWithoutService) > 0 ||
		len(aggregated.Summary.DeploymentsWithoutProbes) > 0 ||
		len(aggregated.Summary.DeploymentsWithoutResources) > 0 {
		prompt.WriteString("\nDETECTED GAPS:\n")
		if len(aggregated.Summary.DeploymentsWithoutService) > 0 {
			prompt.WriteString(fmt.Sprintf("- Deployments without Services: %s\n",
				strings.Join(aggregated.Summary.DeploymentsWithoutService, ", ")))
		}
		if len(aggregated.Summary.DeploymentsWithoutProbes) > 0 {
			prompt.WriteString(fmt.Sprintf("- Deployments without probes: %s\n",
				strings.Join(aggregated.Summary.DeploymentsWithoutProbes, ", ")))
		}
		if len(aggregated.Summary.DeploymentsWithoutResources) > 0 {
			prompt.WriteString(fmt.Sprintf("- Deployments without resource requests: %s\n",
				strings.Join(aggregated.Summary.DeploymentsWithoutResources, ", ")))
		}
		prompt.WriteString("\n")
	}

	// Performance issues
	if len(aggregated.Summary.HighCPUDeployments) > 0 ||
		len(aggregated.Summary.HighMemoryDeployments) > 0 ||
		len(aggregated.Summary.HighErrorRates) > 0 {
		prompt.WriteString("\nPERFORMANCE ISSUES:\n")
		if len(aggregated.Summary.HighCPUDeployments) > 0 {
			prompt.WriteString(fmt.Sprintf("- High CPU (>70%%): %s\n",
				strings.Join(aggregated.Summary.HighCPUDeployments, ", ")))
		}
		if len(aggregated.Summary.HighMemoryDeployments) > 0 {
			prompt.WriteString(fmt.Sprintf("- High Memory (>80%%): %s\n",
				strings.Join(aggregated.Summary.HighMemoryDeployments, ", ")))
		}
		if len(aggregated.Summary.HighErrorRates) > 0 {
			prompt.WriteString(fmt.Sprintf("- High Errors (>5%%): %s\n",
				strings.Join(aggregated.Summary.HighErrorRates, ", ")))
		}
		prompt.WriteString("\n")
	}

	prompt.WriteString("═══════════════════════════════════════════════════════════\n")
	prompt.WriteString("Based on the above, provide your JSON response with inferred needs and patches.\n")
	prompt.WriteString("Remember: Output ONLY the JSON object, no extra text.\n")

	return prompt.String()
}

// BuildSystemPrompt creates the system message for OpenAI
func (pb *PromptBuilder) BuildSystemPrompt() string {
	return `You are Smooth Planner, an expert Kubernetes operations AI assistant.

Your responsibilities:
1. Analyze Kubernetes cluster state and metrics
2. Identify missing or suboptimal configurations
3. Suggest safe, production-ready improvements
4. Generate valid Kubernetes YAML manifests

Guidelines:
- Always output valid JSON matching the expected schema
- Never invent secrets, passwords, or credentials
- Prefer conservative, safe changes
- Provide clear explanations for all suggestions
- Set realistic confidence scores (0.0 to 1.0)
- Mark high-risk changes explicitly

Output format: JSON only, no markdown, no extra text.`
}

