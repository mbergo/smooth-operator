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

package gitops

import (
	"strings"
	"testing"
	"time"

	"github.com/mbergo/smooth-operator/internal/executor"
	"github.com/mbergo/smooth-operator/internal/llm"
	"github.com/mbergo/smooth-operator/internal/planner"
	"github.com/mbergo/smooth-operator/internal/policy"
)

// buildMinimalInputs creates the minimum set of inputs needed by GenerateRationale
// so tests only need to override what they care about.
func buildMinimalInputs() (
	chatSession string,
	userPrompt string,
	llmResp *llm.LLMResponse,
	plan *planner.Plan,
	result *executor.ExecutionResult,
) {
	chatSession = "test-session-abc"
	userPrompt = "Scale the web deployment to 3 replicas"

	llmResp = &llm.LLMResponse{
		Confidence:  0.92,
		Risk:        "low",
		Explanation: "Scaling replicas is safe and non-destructive.",
		InferredNeeds: []llm.InferredNeed{
			{Type: "HPA", Reason: "Load may increase", Priority: "med"},
		},
	}

	plan = &planner.Plan{
		PolicyResults: policy.PolicyEvaluationResult{
			Passed:     true,
			Violations: nil,
		},
		RiskAssessment: planner.RiskAssessment{
			OverallRisk:    "low",
			Recommendation: "approve",
			RiskFactors:    nil,
		},
		Diffs: []planner.ManifestDiff{
			{
				Kind:        "Deployment",
				Name:        "web",
				ChangeType:  "update",
				Summary:     "Replicas: 1 -> 3",
				UnifiedDiff: "@@ -1 +1 @@\n-replicas: 1\n+replicas: 3\n",
			},
		},
	}

	result = &executor.ExecutionResult{
		Success: true,
		AppliedResources: []executor.AppliedResource{
			{
				Kind:      "Deployment",
				Name:      "web",
				Operation: "updated",
				AppliedAt: time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC),
			},
		},
		ExecutionTime: 2 * time.Second,
	}

	return chatSession, userPrompt, llmResp, plan, result
}

// ---------------------------------------------------------------------------
// Top-level structure
// ---------------------------------------------------------------------------

func TestGenerateRationale_TopLevelSections(t *testing.T) {
	t.Parallel()

	gen := NewRationaleGenerator()
	session, prompt, llmResp, plan, result := buildMinimalInputs()
	out := gen.GenerateRationale(session, prompt, llmResp, plan, result)

	sections := []string{
		"# Smooth Operator - Change Rationale",
		"## User Request",
		"## AI Analysis",
		"## Policy Validation",
		"## Risk Assessment",
		"## Applied Changes",
	}
	for _, s := range sections {
		if !strings.Contains(out, s) {
			t.Errorf("SMOOTH.md missing section %q", s)
		}
	}
}

// ---------------------------------------------------------------------------
// User prompt is quoted in output
// ---------------------------------------------------------------------------

func TestGenerateRationale_UserPromptQuoted(t *testing.T) {
	t.Parallel()

	gen := NewRationaleGenerator()
	session, _, llmResp, plan, result := buildMinimalInputs()
	prompt := "Deploy the canary build to staging"

	out := gen.GenerateRationale(session, prompt, llmResp, plan, result)
	if !strings.Contains(out, "> "+prompt) {
		t.Errorf("expected user prompt to appear as blockquote (> ...)\nGot:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// ChatSession name appears in document
// ---------------------------------------------------------------------------

func TestGenerateRationale_ChatSessionPresent(t *testing.T) {
	t.Parallel()

	gen := NewRationaleGenerator()
	session := "unique-session-xyz-999"
	_, prompt, llmResp, plan, result := buildMinimalInputs()

	out := gen.GenerateRationale(session, prompt, llmResp, plan, result)
	if !strings.Contains(out, session) {
		t.Errorf("expected chat session %q to appear in SMOOTH.md\nGot:\n%s", session, out)
	}
}

// ---------------------------------------------------------------------------
// Policy pass / fail rendering
// ---------------------------------------------------------------------------

func TestGenerateRationale_PolicyPassed(t *testing.T) {
	t.Parallel()

	gen := NewRationaleGenerator()
	session, prompt, llmResp, plan, result := buildMinimalInputs()
	plan.PolicyResults.Passed = true

	out := gen.GenerateRationale(session, prompt, llmResp, plan, result)
	if !strings.Contains(out, "All policies passed") {
		t.Errorf("expected 'All policies passed' marker\nGot:\n%s", out)
	}
}

func TestGenerateRationale_PolicyFailed(t *testing.T) {
	t.Parallel()

	gen := NewRationaleGenerator()
	session, prompt, llmResp, plan, result := buildMinimalInputs()
	plan.PolicyResults.Passed = false
	plan.PolicyResults.Violations = []policy.PolicyViolation{
		{
			Policy:       "no-latest-tag",
			Severity:     "high",
			Message:      "Image tag must not be 'latest'",
			SuggestedFix: "Pin to a specific digest",
		},
	}

	out := gen.GenerateRationale(session, prompt, llmResp, plan, result)
	if !strings.Contains(out, "no-latest-tag") {
		t.Errorf("expected policy name 'no-latest-tag' in output\nGot:\n%s", out)
	}
	if !strings.Contains(out, "Pin to a specific digest") {
		t.Errorf("expected suggested fix in output\nGot:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Applied changes section
// ---------------------------------------------------------------------------

func TestGenerateRationale_AppliedChanges_Success(t *testing.T) {
	t.Parallel()

	gen := NewRationaleGenerator()
	session, prompt, llmResp, plan, result := buildMinimalInputs()
	result.Success = true

	out := gen.GenerateRationale(session, prompt, llmResp, plan, result)
	if !strings.Contains(out, "Successfully applied") {
		t.Errorf("expected 'Successfully applied' marker\nGot:\n%s", out)
	}
	if !strings.Contains(out, "Deployment") {
		t.Errorf("expected resource kind 'Deployment' in applied changes\nGot:\n%s", out)
	}
}

func TestGenerateRationale_AppliedChanges_Failure(t *testing.T) {
	t.Parallel()

	gen := NewRationaleGenerator()
	session, prompt, llmResp, plan, result := buildMinimalInputs()
	result.Success = false
	result.Errors = []string{"timeout waiting for rollout", "pod CrashLoopBackOff"}
	result.RolledBack = true

	out := gen.GenerateRationale(session, prompt, llmResp, plan, result)
	if !strings.Contains(out, "Execution failed") {
		t.Errorf("expected 'Execution failed' marker\nGot:\n%s", out)
	}
	if !strings.Contains(out, "timeout waiting for rollout") {
		t.Errorf("expected error message in output\nGot:\n%s", out)
	}
	if !strings.Contains(out, "rollback") {
		t.Errorf("expected rollback marker in output\nGot:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Diff section
// ---------------------------------------------------------------------------

func TestGenerateRationale_DiffSection(t *testing.T) {
	t.Parallel()

	gen := NewRationaleGenerator()
	session, prompt, llmResp, plan, result := buildMinimalInputs()

	out := gen.GenerateRationale(session, prompt, llmResp, plan, result)

	if !strings.Contains(out, "## Changes Details") {
		t.Errorf("expected '## Changes Details' section\nGot:\n%s", out)
	}
	if !strings.Contains(out, "```diff") {
		t.Errorf("expected fenced diff block\nGot:\n%s", out)
	}
	if !strings.Contains(out, "replicas: 1") {
		t.Errorf("expected diff content in output\nGot:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Inferred needs section
// ---------------------------------------------------------------------------

func TestGenerateRationale_InferredNeeds(t *testing.T) {
	t.Parallel()

	gen := NewRationaleGenerator()
	session, prompt, llmResp, plan, result := buildMinimalInputs()
	llmResp.InferredNeeds = []llm.InferredNeed{
		{Type: "HPA", Reason: "Autoscale under load", Priority: "high"},
		{Type: "Ingress", Reason: "Expose externally", Priority: "med"},
	}

	out := gen.GenerateRationale(session, prompt, llmResp, plan, result)

	if !strings.Contains(out, "### Inferred Needs") {
		t.Errorf("expected '### Inferred Needs' section\nGot:\n%s", out)
	}
	if !strings.Contains(out, "HPA") {
		t.Errorf("expected 'HPA' in inferred needs\nGot:\n%s", out)
	}
	if !strings.Contains(out, "Ingress") {
		t.Errorf("expected 'Ingress' in inferred needs\nGot:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Footer presence
// ---------------------------------------------------------------------------

func TestGenerateRationale_Footer(t *testing.T) {
	t.Parallel()

	gen := NewRationaleGenerator()
	session, prompt, llmResp, plan, result := buildMinimalInputs()

	out := gen.GenerateRationale(session, prompt, llmResp, plan, result)
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "For questions or issues, refer to the ChatSession CRD in Kubernetes*") {
		// Accept any trailing newlines; just check the last meaningful line.
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		last := lines[len(lines)-1]
		if !strings.Contains(last, "ChatSession CRD") {
			t.Errorf("expected footer with 'ChatSession CRD' reference, last line: %q", last)
		}
	}
}
