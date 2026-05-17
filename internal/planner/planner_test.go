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

package planner

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mbergo/smooth-operator/internal/llm"
	"github.com/mbergo/smooth-operator/internal/policy"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func buildPlannerScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return s
}

func newTestPlanner(t *testing.T) *Planner {
	t.Helper()
	scheme := buildPlannerScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	return NewPlanner(c, DefaultRiskAssessorOptions())
}

// ---------------------------------------------------------------------------
// ShouldAutoApply – pure logic, no I/O
// ---------------------------------------------------------------------------

func TestShouldAutoApply_Planner(t *testing.T) {
	p := newTestPlanner(t)

	tests := []struct {
		name              string
		plan              *Plan
		autoModeRequested bool
		want              bool
	}{
		{
			name: "policy violation blocks auto-apply",
			plan: &Plan{
				PolicyResults: policy.PolicyEvaluationResult{
					Passed:     false,
					Violations: []policy.PolicyViolation{{Severity: "blocking"}},
				},
				RiskAssessment: RiskAssessment{
					OverallRisk:    "low",
					LLMConfidence:  0.9,
					Recommendation: "approve",
				},
				Errors: []string{},
			},
			autoModeRequested: true,
			want:              false,
		},
		{
			name: "validation errors block auto-apply",
			plan: &Plan{
				PolicyResults: policy.PolicyEvaluationResult{Passed: true},
				RiskAssessment: RiskAssessment{
					OverallRisk:    "low",
					LLMConfidence:  0.9,
					Recommendation: "approve",
				},
				Errors: []string{"manifest 0 invalid"},
			},
			autoModeRequested: true,
			want:              false,
		},
		{
			name: "auto not requested blocks auto-apply even on clean low-risk plan",
			plan: &Plan{
				PolicyResults: policy.PolicyEvaluationResult{Passed: true},
				RiskAssessment: RiskAssessment{
					OverallRisk:    "low",
					LLMConfidence:  0.9,
					Recommendation: "approve",
				},
				Errors: []string{},
			},
			autoModeRequested: false,
			want:              false,
		},
		{
			name: "happy path: low risk, high confidence, auto requested, no errors",
			plan: &Plan{
				PolicyResults: policy.PolicyEvaluationResult{Passed: true},
				RiskAssessment: RiskAssessment{
					OverallRisk:    "low",
					LLMConfidence:  0.9,
					Recommendation: "approve",
				},
				Errors: []string{},
			},
			autoModeRequested: true,
			want:              true,
		},
		{
			name: "high risk blocks auto-apply",
			plan: &Plan{
				PolicyResults: policy.PolicyEvaluationResult{Passed: true},
				RiskAssessment: RiskAssessment{
					OverallRisk:    "high",
					LLMConfidence:  0.9,
					Recommendation: "review-carefully",
				},
				Errors: []string{},
			},
			autoModeRequested: true,
			want:              false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := p.ShouldAutoApply(tc.plan, tc.autoModeRequested)
			if got != tc.want {
				t.Errorf("ShouldAutoApply = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CreatePlan – happy path with new (non-existing) resources
// ---------------------------------------------------------------------------

// configMapYAML returns a minimal ConfigMap manifest string.
func configMapYAML(name, ns string) string {
	return `apiVersion: v1
kind: ConfigMap
metadata:
  name: ` + name + `
  namespace: ` + ns + `
data:
  foo: bar
`
}

func TestCreatePlan_HappyPath_NewResources(t *testing.T) {
	scheme := buildPlannerScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	p := NewPlanner(c, DefaultRiskAssessorOptions())

	ctx := context.Background()
	resp := &llm.LLMResponse{
		Patches: []llm.PatchSuggestion{
			{Kind: "ConfigMap", YAML: configMapYAML("app-config", "staging")},
		},
		Confidence:  0.9,
		Risk:        "low",
		Explanation: "adding a configmap",
	}

	plan, err := p.CreatePlan(ctx, resp, "staging", false)
	if err != nil {
		t.Fatalf("CreatePlan() unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("CreatePlan() returned nil plan")
	}

	if len(plan.Errors) > 0 {
		t.Errorf("expected no errors, got: %v", plan.Errors)
	}
	if len(plan.Manifests) != 1 {
		t.Errorf("expected 1 manifest, got %d", len(plan.Manifests))
	}
	if plan.Manifests[0].Kind != "ConfigMap" {
		t.Errorf("Manifest Kind = %q, want ConfigMap", plan.Manifests[0].Kind)
	}
	if !plan.Manifests[0].IsNew {
		t.Error("ConfigMap should be marked as new (not pre-existing in fake cluster)")
	}
	if len(plan.Diffs) != 1 {
		t.Errorf("expected 1 diff, got %d", len(plan.Diffs))
	}
	if plan.Diffs[0].ChangeType != "create" {
		t.Errorf("Diff ChangeType = %q, want create", plan.Diffs[0].ChangeType)
	}
	if plan.RiskAssessment.OverallRisk == "" {
		t.Error("RiskAssessment.OverallRisk should not be empty")
	}
}

// ---------------------------------------------------------------------------
// CreatePlan – missing fields in patches
// ---------------------------------------------------------------------------

func TestCreatePlan_InvalidPatch(t *testing.T) {
	scheme := buildPlannerScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	p := NewPlanner(c, DefaultRiskAssessorOptions())
	ctx := context.Background()

	tests := []struct {
		name          string
		patches       []llm.PatchSuggestion
		wantErrors    bool
		wantManifests int
	}{
		{
			name: "empty YAML produces validation error",
			patches: []llm.PatchSuggestion{
				{Kind: "ConfigMap", YAML: ""},
			},
			wantErrors:    true,
			wantManifests: 0,
		},
		{
			name: "invalid YAML produces validation error",
			patches: []llm.PatchSuggestion{
				{Kind: "Deployment", YAML: "{{invalid}}"},
			},
			wantErrors:    true,
			wantManifests: 0,
		},
		{
			name:          "empty patches list produces empty plan without error",
			patches:       []llm.PatchSuggestion{},
			wantErrors:    false,
			wantManifests: 0,
		},
		{
			name: "one valid and one invalid patch: valid one is included",
			patches: []llm.PatchSuggestion{
				{Kind: "ConfigMap", YAML: configMapYAML("good", "staging")},
				{Kind: "Bad", YAML: "{{bad yaml"},
			},
			wantErrors:    true,
			wantManifests: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := &llm.LLMResponse{
				Patches:    tc.patches,
				Confidence: 0.8,
				Risk:       "low",
			}
			plan, err := p.CreatePlan(ctx, resp, "staging", false)
			if err != nil {
				t.Fatalf("CreatePlan() returned unexpected error: %v", err)
			}
			if tc.wantErrors && len(plan.Errors) == 0 {
				t.Error("expected plan.Errors to be non-empty")
			}
			if !tc.wantErrors && len(plan.Errors) > 0 {
				t.Errorf("expected no errors, got: %v", plan.Errors)
			}
			if len(plan.Manifests) != tc.wantManifests {
				t.Errorf("Manifests count = %d, want %d", len(plan.Manifests), tc.wantManifests)
			}
		})
	}
}
