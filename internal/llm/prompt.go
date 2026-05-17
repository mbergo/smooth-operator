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
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/mbergo/smooth-operator/internal/collector"
)

// Input size caps to defend against token-cost amplification + prompt injection
// via overlong tenant-controlled fields. See security audit (HIGH).
const (
	maxUserPromptLen     = 4096
	maxResourceNameLen   = 253
	maxYAMLSnippetLen    = 2048
	maxDeploymentsListed = 5
	maxServicesListed    = 10
	maxIngressesListed   = 10
	maxGapNamesListed    = 20
)

// sanitize strips control characters and truncates tenant-controlled strings
// before they land in either prompt. Newlines collapse to spaces so a
// malicious value cannot inject new structural lines into the prompt.
func sanitize(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			b.WriteByte(' ')
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= max {
			b.WriteString("...[truncated]")
			break
		}
	}
	return b.String()
}

// PromptBuilder constructs the two-stage prompt chain.
//
// Stage 1 (Reasoner): cluster YAML + metrics + policy summary -> ReasonerOutput
//   - System prompt is stable across all sessions (cacheable)
//   - Policy summary is stable per cluster (cacheable)
//   - User message carries the volatile per-session payload
//
// Stage 2 (Generator): inferredNeeds + policy schema -> GeneratorOutput
//   - System prompt is stable across all sessions (cacheable)
//   - User message carries the needs JSON produced by Stage 1
type PromptBuilder struct {
	// PolicySummary is the cluster-stable constraint summary fed to the Reasoner.
	// Inject via WithPolicySummary; defaults to the standard restricted-PSS rules.
	PolicySummary string
}

// NewPromptBuilder creates a builder with default policy text.
func NewPromptBuilder() *PromptBuilder {
	return &PromptBuilder{PolicySummary: defaultPolicySummary}
}

// WithPolicySummary returns a copy with a custom policy summary block.
func (pb *PromptBuilder) WithPolicySummary(s string) *PromptBuilder {
	cp := *pb
	cp.PolicySummary = s
	return &cp
}

// ----------------------------------------------------------------------------
// Stage 1: Reasoner
// ----------------------------------------------------------------------------

// BuildReasonerSystem returns the cacheable system prompt for stage 1.
// Stable across every reconcile; safe to mark with cache_control.
func (pb *PromptBuilder) BuildReasonerSystem() string {
	return `You are Smooth Reasoner, a Kubernetes operations expert.

YOUR ONLY JOB at this stage is to identify WHAT the workload needs.
Do NOT produce YAML. Do NOT write manifests. A separate Generator handles that.

Reason about the cluster state, metrics, and the user's prompt, then output a
structured JSON describing inferred needs, overall risk, and confidence.

OUTPUT SCHEMA (exact keys, no markdown, no prose around the JSON):
{
  "inferredNeeds": [
    {
      "type":     "HPA | Service-LB | Service-ClusterIP | Ingress | Probe | NetworkPolicy | PVC | PodSecurity | ResourceLimits | ConfigMap",
      "reason":   "one sentence justifying this need from the evidence",
      "priority": "low | med | high",
      "spec":     "short hint (e.g. minReplicas=2 maxReplicas=10 cpu=70%)"
    }
  ],
  "confidence": 0.0,
  "risk": "low | med | high",
  "explanation": "one paragraph summarising the reasoning"
}

RULES:
- Output ONLY the JSON object. No markdown fences. No commentary.
- Never invent secrets, tokens, or registry credentials.
- Skip changes that would violate the supplied policy summary.
- Set "risk" high if any change crosses namespace boundaries, exposes data
  externally without TLS, or modifies cluster-scoped resources.
- Set "confidence" honestly; below 0.5 means a human should review.
- If nothing is needed, return inferredNeeds=[] with confidence=1.0 and risk=low.`
}

// BuildReasonerUser returns the per-session user message for stage 1.
// Volatile (cluster state changes per reconcile); placed after cache breakpoint.
func (pb *PromptBuilder) BuildReasonerUser(
	userPrompt string,
	aggregated *collector.AggregatedContext,
) string {
	var b strings.Builder

	b.WriteString("POLICY CONSTRAINTS:\n")
	b.WriteString(pb.PolicySummary)
	b.WriteString("\n\n")

	// Tenant-controlled values are wrapped in XML-like delimiters so the model
	// can distinguish them from operator-authored content. This blunts prompt
	// injection: a value containing "RULES:" or "POLICY CONSTRAINTS:" is read
	// as user data, not as new structural sections.
	b.WriteString("<user_request>\n")
	b.WriteString(sanitize(userPrompt, maxUserPromptLen))
	b.WriteString("\n</user_request>\n\n")

	if aggregated == nil {
		b.WriteString("<cluster_state available=\"false\"/>\n")
		return b.String()
	}

	b.WriteString("<cluster_state>\n")
	b.WriteString(fmt.Sprintf("namespace=%s\n",
		sanitize(aggregated.ClusterContext.TargetNamespace, maxResourceNameLen)))
	if aggregated.Summary.TextSummary != "" {
		b.WriteString(aggregated.Summary.TextSummary)
		b.WriteString("\n")
	}

	if len(aggregated.ClusterContext.Deployments) > 0 {
		b.WriteString("\nDEPLOYMENTS:\n")
		for i, dep := range aggregated.ClusterContext.Deployments {
			if i >= maxDeploymentsListed {
				b.WriteString(fmt.Sprintf("... and %d more\n",
					len(aggregated.ClusterContext.Deployments)-maxDeploymentsListed))
				break
			}
			b.WriteString(fmt.Sprintf("\n%s (replicas: %d/%d):\n",
				sanitize(dep.Name, maxResourceNameLen),
				dep.ReadyReplicas, dep.Replicas))
			if dep.YAMLSnippet != "" {
				b.WriteString(sanitize(dep.YAMLSnippet, maxYAMLSnippetLen))
				b.WriteString("\n")
			}
		}
	}

	if len(aggregated.ClusterContext.Services) > 0 {
		b.WriteString("\nSERVICES:\n")
		for i, svc := range aggregated.ClusterContext.Services {
			if i >= maxServicesListed {
				b.WriteString(fmt.Sprintf("... and %d more\n",
					len(aggregated.ClusterContext.Services)-maxServicesListed))
				break
			}
			b.WriteString(fmt.Sprintf("- %s (type: %s)\n",
				sanitize(svc.Name, maxResourceNameLen),
				sanitize(string(svc.Type), 32)))
		}
	}

	if len(aggregated.ClusterContext.Ingresses) > 0 {
		b.WriteString("\nINGRESSES:\n")
		for i, ing := range aggregated.ClusterContext.Ingresses {
			if i >= maxIngressesListed {
				b.WriteString(fmt.Sprintf("... and %d more\n",
					len(aggregated.ClusterContext.Ingresses)-maxIngressesListed))
				break
			}
			b.WriteString(fmt.Sprintf("- %s (rules: %d)\n",
				sanitize(ing.Name, maxResourceNameLen),
				len(ing.Rules)))
		}
	}

	if len(aggregated.MetricsSnapshots) > 0 {
		b.WriteString("\nMETRICS (window):\n")
		for name, m := range aggregated.MetricsSnapshots {
			b.WriteString(fmt.Sprintf("- %s: cpu=%.2f mem=%.0fMB rps=%.1f err=%.1f%%\n",
				sanitize(name, maxResourceNameLen),
				m.CPUUsageAverage, m.MemoryUsageAverage/1024/1024,
				m.RequestsPerSecond, m.ErrorRate))
		}
	}

	if hasGaps(aggregated) {
		b.WriteString("\nDETECTED GAPS:\n")
		writeGapLine(&b, "without Service", aggregated.Summary.DeploymentsWithoutService)
		writeGapLine(&b, "without probes", aggregated.Summary.DeploymentsWithoutProbes)
		writeGapLine(&b, "without resources", aggregated.Summary.DeploymentsWithoutResources)
	}

	if hasPerf(aggregated) {
		b.WriteString("\nPERFORMANCE SIGNALS:\n")
		writeGapLine(&b, "high CPU (>70%)", aggregated.Summary.HighCPUDeployments)
		writeGapLine(&b, "high memory (>80%)", aggregated.Summary.HighMemoryDeployments)
		writeGapLine(&b, "high errors (>5%)", aggregated.Summary.HighErrorRates)
	}

	b.WriteString("</cluster_state>\n\n")
	b.WriteString("Return the ReasonerOutput JSON now.")
	return b.String()
}

// writeGapLine writes a sanitized, length-capped comma-joined list of names.
func writeGapLine(b *strings.Builder, label string, names []string) {
	if len(names) == 0 {
		return
	}
	cleaned := make([]string, 0, len(names))
	for i, n := range names {
		if i >= maxGapNamesListed {
			cleaned = append(cleaned, fmt.Sprintf("...(+%d more)", len(names)-maxGapNamesListed))
			break
		}
		cleaned = append(cleaned, sanitize(n, maxResourceNameLen))
	}
	b.WriteString(fmt.Sprintf("- %s: %s\n", label, strings.Join(cleaned, ", ")))
}

// ----------------------------------------------------------------------------
// Stage 2: Generator
// ----------------------------------------------------------------------------

// BuildGeneratorSystem returns the cacheable system prompt for stage 2.
// Stable across every session; safe to mark with cache_control.
func (pb *PromptBuilder) BuildGeneratorSystem() string {
	return `You are Smooth Generator, a Kubernetes manifest author.

Stage 1 (Reasoner) has already decided WHAT is needed. Your job: produce the
exact Kubernetes manifests that satisfy each inferred need.

OUTPUT SCHEMA (exact keys, no markdown, no prose):
{
  "patches": [
    {"kind": "Deployment|Service|HorizontalPodAutoscaler|Ingress|ConfigMap|NetworkPolicy|PersistentVolumeClaim",
     "yaml": "complete YAML document as a string"}
  ],
  "notes": "optional one-paragraph caveats"
}

RULES:
- Output ONLY the JSON object. No markdown fences. No commentary.
- Each YAML must be self-contained and apply cleanly with kubectl apply -f -.
- Always include apiVersion, kind, metadata.name, metadata.namespace.
- Use the same namespace, labels, and selectors as the existing workload.
- Never inline secrets; reference Secrets by name via valueFrom.secretKeyRef.
- Respect the policy summary the Reasoner used:
  * runAsNonRoot: true; readOnlyRootFilesystem; drop all capabilities
  * LoadBalancer Services include the internal annotation unless explicitly external
  * Resource requests + limits on every container
  * Liveness + readiness probes on every container
  * Image registry must match the allowlist
- One patch per inferred need. Skip needs you cannot satisfy safely and explain
  in "notes".
- If the inferredNeeds array is empty, return patches=[] and notes="no changes needed".`
}

// BuildGeneratorUser returns the per-session user message for stage 2.
// Carries the Reasoner output as JSON plus the original prompt and namespace.
//
// SECURITY: the Reasoner's free-text Explanation is intentionally stripped
// before serialisation — it is model narrative, not authoritative cluster
// fact, and feeding it back risks compounding hallucinations. The Generator
// receives only the structured InferredNeeds + Risk + Confidence.
func (pb *PromptBuilder) BuildGeneratorUser(
	userPrompt string,
	namespace string,
	reasoner *ReasonerOutput,
) string {
	// Forward only structured fields; drop Explanation.
	forwarded := struct {
		InferredNeeds []InferredNeed `json:"inferredNeeds"`
		Confidence    float64        `json:"confidence"`
		Risk          string         `json:"risk"`
	}{
		InferredNeeds: reasoner.InferredNeeds,
		Confidence:    reasoner.Confidence,
		Risk:          reasoner.Risk,
	}
	needsJSON, err := json.MarshalIndent(forwarded, "", "  ")
	if err != nil {
		needsJSON = []byte(`{"inferredNeeds":[],"confidence":0,"risk":"high"}`)
	}

	return fmt.Sprintf(`<user_request>
%s
</user_request>

<target_namespace>%s</target_namespace>

<reasoner_output>
%s
</reasoner_output>

POLICY CONSTRAINTS:
%s

Produce the GeneratorOutput JSON now. Every manifest's metadata.namespace MUST equal the target_namespace value above.`,
		sanitize(userPrompt, maxUserPromptLen),
		sanitize(namespace, maxResourceNameLen),
		string(needsJSON),
		pb.PolicySummary,
	)
}

// ----------------------------------------------------------------------------
// Defaults + helpers
// ----------------------------------------------------------------------------

const defaultPolicySummary = `- Allowed image registries: docker.io/library, ghcr.io, gcr.io, k8s.gcr.io, quay.io, registry.k8s.io
- Pod security: runAsNonRoot=true, allowPrivilegeEscalation=false, privileged=false (container-level securityContext), drop=[ALL], readOnlyRootFilesystem=true, seccompProfile=RuntimeDefault
- LoadBalancer Services: must carry service.beta.kubernetes.io/aws-load-balancer-internal annotation unless external exposure is explicitly requested
- Resource limits and requests required on every container (blocking policy)
- Liveness and readiness probes recommended on every container (warning policy)
- No hostPath volumes (blocking)
- No hostNetwork, no hostPID
- NetworkPolicy default-deny preferred for new namespaces
- TLS required on every Ingress; cert-manager annotations preferred
- Allowed Kinds: Deployment, Service, HorizontalPodAutoscaler, Ingress, ConfigMap, NetworkPolicy, PersistentVolumeClaim — NEVER ClusterRole, ClusterRoleBinding, Namespace, MutatingWebhookConfiguration, ValidatingWebhookConfiguration, CustomResourceDefinition`

func hasGaps(a *collector.AggregatedContext) bool {
	return len(a.Summary.DeploymentsWithoutService) > 0 ||
		len(a.Summary.DeploymentsWithoutProbes) > 0 ||
		len(a.Summary.DeploymentsWithoutResources) > 0
}

func hasPerf(a *collector.AggregatedContext) bool {
	return len(a.Summary.HighCPUDeployments) > 0 ||
		len(a.Summary.HighMemoryDeployments) > 0 ||
		len(a.Summary.HighErrorRates) > 0
}
