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

// InferredNeed represents a resource that the model determined is needed.
type InferredNeed struct {
	Type     string `json:"type"`     // HPA, Service-LB, Ingress, Probe, etc.
	Reason   string `json:"reason"`   // Why this is needed
	Priority string `json:"priority"` // low, med, high
	Spec     string `json:"spec"`     // Suggested configuration (brief)
}

// PatchSuggestion represents a suggested Kubernetes manifest.
type PatchSuggestion struct {
	Kind string `json:"kind"` // Deployment, Service, HPA, etc.
	YAML string `json:"yaml"` // Full YAML manifest
}

// ReasonerOutput is the structured result of stage 1 (Reasoner).
// No YAML — pure inference + risk assessment.
type ReasonerOutput struct {
	InferredNeeds []InferredNeed `json:"inferredNeeds"`
	Confidence    float64        `json:"confidence"` // 0.0 - 1.0
	Risk          string         `json:"risk"`       // low, med, high
	Explanation   string         `json:"explanation"`
}

// GeneratorOutput is the structured result of stage 2 (Generator).
// Concrete K8s manifests for each inferred need.
type GeneratorOutput struct {
	Patches []PatchSuggestion `json:"patches"`
	Notes   string            `json:"notes,omitempty"`
}

// LLMResponse is the combined result of the Reasoner -> Generator chain.
type LLMResponse struct {
	InferredNeeds []InferredNeed    `json:"inferredNeeds"`
	Patches       []PatchSuggestion `json:"patches"`
	Confidence    float64           `json:"confidence"`
	Risk          string            `json:"risk"`
	Explanation   string            `json:"explanation"`
}

// PromptContext contains all context for building prompts.
type PromptContext struct {
	UserPrompt     string
	ClusterSummary string
	Deployments    []string
	Services       []string
	Ingresses      []string
	MetricsSummary string
	GapsSummary    string
	Namespace      string
	ChatSessionID  string
}
