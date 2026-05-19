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

// InferredNeed represents a resource that the LLM determined is needed
type InferredNeed struct {
	Type     string `json:"type"`     // HPA, Service-LB, Ingress, Probe, etc.
	Reason   string `json:"reason"`   // Why this is needed
	Priority string `json:"priority"` // low, med, high
	Spec     string `json:"spec"`     // Suggested configuration (YAML)
}

// PatchSuggestion represents a suggested Kubernetes manifest
type PatchSuggestion struct {
	Kind string `json:"kind"` // Deployment, Service, HPA, etc.
	YAML string `json:"yaml"` // Full YAML manifest
}

// LLMResponse represents the structured response from the LLM
type LLMResponse struct {
	InferredNeeds []InferredNeed    `json:"inferredNeeds"`
	Patches       []PatchSuggestion `json:"patches"`
	Confidence    float64           `json:"confidence"` // 0.0 - 1.0
	Risk          string            `json:"risk"`       // low, med, high
	Explanation   string            `json:"explanation"`
}

// PromptContext contains all context for building LLM prompts
type PromptContext struct {
	// User's natural language request
	UserPrompt string

	// Cluster context summary
	ClusterSummary string

	// Deployment details (YAML snippets)
	Deployments []string

	// Service details
	Services []string

	// Ingress details
	Ingresses []string

	// Metrics snapshot (if available)
	MetricsSummary string

	// Detected gaps
	GapsSummary string

	// Target namespace
	Namespace string

	// Chat session ID
	ChatSessionID string
}
