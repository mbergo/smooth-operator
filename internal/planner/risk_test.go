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

	"github.com/mbergo/smooth-operator/internal/policy"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newAssessor(minConfidence float64) *RiskAssessor {
	return NewRiskAssessor(RiskAssessorOptions{
		MinConfidence:         minConfidence,
		HighRiskNamespaces:    []string{"production", "prod", "default", "kube-system"},
		AllowAutoInProduction: false,
	})
}

func emptyPlan() *Plan {
	return &Plan{
		Manifests: []ValidatedManifest{},
		Diffs:     []ManifestDiff{},
		PolicyResults: policy.PolicyEvaluationResult{
			Passed:     true,
			Violations: []policy.PolicyViolation{},
		},
		Errors:   []string{},
		Warnings: []string{},
	}
}

func planWithBlockingViolation() *Plan {
	p := emptyPlan()
	p.PolicyResults.Passed = false
	p.PolicyResults.Violations = []policy.PolicyViolation{
		{Policy: "no-root", Severity: "blocking", Message: "running as root"},
	}
	return p
}

func planWithManyManifests(n int) *Plan {
	p := emptyPlan()
	for i := 0; i < n; i++ {
		p.Manifests = append(p.Manifests, ValidatedManifest{Kind: "Deployment", Name: "app"})
	}
	return p
}

// ---------------------------------------------------------------------------
// AssessRisk – overall risk level boundaries
// ---------------------------------------------------------------------------

func TestAssessRisk_OverallRisk(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name            string
		llmRisk         string
		llmConfidence   float64
		minConfidence   float64
		targetNamespace string
		plan            *Plan
		wantRisk        string
	}{
		{
			name:            "low risk: low llm risk, high confidence, safe namespace",
			llmRisk:         "low",
			llmConfidence:   0.9,
			minConfidence:   0.7,
			targetNamespace: "staging",
			plan:            emptyPlan(),
			wantRisk:        "low",
		},
		{
			name:            "med risk: llm reports med",
			llmRisk:         "med",
			llmConfidence:   0.9,
			minConfidence:   0.7,
			targetNamespace: "staging",
			plan:            emptyPlan(),
			wantRisk:        "med",
		},
		{
			name:            "high risk: llm reports high",
			llmRisk:         "high",
			llmConfidence:   0.9,
			minConfidence:   0.7,
			targetNamespace: "staging",
			plan:            emptyPlan(),
			wantRisk:        "high",
		},
		{
			name:            "high risk: confidence below threshold",
			llmRisk:         "low",
			llmConfidence:   0.5,
			minConfidence:   0.7,
			targetNamespace: "staging",
			plan:            emptyPlan(),
			wantRisk:        "high",
		},
		{
			name:            "high risk: confidence exactly at threshold boundary (equal = passes)",
			llmRisk:         "low",
			llmConfidence:   0.7,
			minConfidence:   0.7,
			targetNamespace: "staging",
			plan:            emptyPlan(),
			wantRisk:        "low",
		},
		{
			name:            "high risk: confidence just below threshold",
			llmRisk:         "low",
			llmConfidence:   0.699,
			minConfidence:   0.7,
			targetNamespace: "staging",
			plan:            emptyPlan(),
			wantRisk:        "high",
		},
		{
			name:            "med risk: high-risk namespace elevates low to med",
			llmRisk:         "low",
			llmConfidence:   0.9,
			minConfidence:   0.7,
			targetNamespace: "production",
			plan:            emptyPlan(),
			wantRisk:        "med",
		},
		{
			name:            "med risk: default namespace elevates to med",
			llmRisk:         "low",
			llmConfidence:   0.9,
			minConfidence:   0.7,
			targetNamespace: "default",
			plan:            emptyPlan(),
			wantRisk:        "med",
		},
		{
			name:            "high risk: blocking policy violation",
			llmRisk:         "low",
			llmConfidence:   0.9,
			minConfidence:   0.7,
			targetNamespace: "staging",
			plan:            planWithBlockingViolation(),
			wantRisk:        "high",
		},
		{
			name:            "med risk: many manifests (>5) elevates low to med",
			llmRisk:         "low",
			llmConfidence:   0.9,
			minConfidence:   0.7,
			targetNamespace: "staging",
			plan:            planWithManyManifests(6),
			wantRisk:        "med",
		},
		{
			name:            "low risk: exactly 5 manifests does not elevate",
			llmRisk:         "low",
			llmConfidence:   0.9,
			minConfidence:   0.7,
			targetNamespace: "staging",
			plan:            planWithManyManifests(5),
			wantRisk:        "low",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newAssessor(tc.minConfidence)
			got := r.AssessRisk(ctx, tc.plan, tc.llmRisk, tc.llmConfidence, tc.targetNamespace, false)
			if got.OverallRisk != tc.wantRisk {
				t.Errorf("OverallRisk = %q, want %q", got.OverallRisk, tc.wantRisk)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AssessRisk – recommendation
// ---------------------------------------------------------------------------

func TestAssessRisk_Recommendation(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name              string
		llmRisk           string
		llmConfidence     float64
		targetNamespace   string
		plan              *Plan
		autoModeRequested bool
		wantRecommend     string
	}{
		{
			name:              "low risk no auto -> approve",
			llmRisk:           "low",
			llmConfidence:     0.9,
			targetNamespace:   "staging",
			plan:              emptyPlan(),
			autoModeRequested: false,
			wantRecommend:     "approve",
		},
		{
			name:              "low risk with auto -> approve",
			llmRisk:           "low",
			llmConfidence:     0.9,
			targetNamespace:   "staging",
			plan:              emptyPlan(),
			autoModeRequested: true,
			wantRecommend:     "approve",
		},
		{
			name:              "med risk no auto -> approve",
			llmRisk:           "med",
			llmConfidence:     0.9,
			targetNamespace:   "staging",
			plan:              emptyPlan(),
			autoModeRequested: false,
			wantRecommend:     "approve",
		},
		{
			name:              "med risk with auto -> review-carefully",
			llmRisk:           "med",
			llmConfidence:     0.9,
			targetNamespace:   "staging",
			plan:              emptyPlan(),
			autoModeRequested: true,
			wantRecommend:     "review-carefully",
		},
		{
			name:              "high risk -> review-carefully",
			llmRisk:           "high",
			llmConfidence:     0.9,
			targetNamespace:   "staging",
			plan:              emptyPlan(),
			autoModeRequested: false,
			wantRecommend:     "review-carefully",
		},
		{
			name:              "blocking policy violation -> reject (via review-carefully path since high risk wins first)",
			llmRisk:           "low",
			llmConfidence:     0.9,
			targetNamespace:   "staging",
			plan:              planWithBlockingViolation(),
			autoModeRequested: false,
			// policy-violations factor makes risk "high", so recommendation is review-carefully
			// (high risk branch runs before policy-violations branch in determineRecommendation)
			wantRecommend: "review-carefully",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newAssessor(0.7)
			got := r.AssessRisk(ctx, tc.plan, tc.llmRisk, tc.llmConfidence, tc.targetNamespace, tc.autoModeRequested)
			if got.Recommendation != tc.wantRecommend {
				t.Errorf("Recommendation = %q, want %q (OverallRisk=%s, factors=%v)",
					got.Recommendation, tc.wantRecommend, got.OverallRisk, got.RiskFactors)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ShouldAutoApply
// ---------------------------------------------------------------------------

func TestShouldAutoApply(t *testing.T) {
	tests := []struct {
		name              string
		assessment        *RiskAssessment
		autoModeRequested bool
		minConfidence     float64
		want              bool
	}{
		{
			name: "auto not requested -> false",
			assessment: &RiskAssessment{
				OverallRisk:    "low",
				LLMConfidence:  0.9,
				Recommendation: "approve",
			},
			autoModeRequested: false,
			minConfidence:     0.7,
			want:              false,
		},
		{
			name: "reject recommendation -> false",
			assessment: &RiskAssessment{
				OverallRisk:    "low",
				LLMConfidence:  0.9,
				Recommendation: "reject",
			},
			autoModeRequested: true,
			minConfidence:     0.7,
			want:              false,
		},
		{
			name: "high risk -> false",
			assessment: &RiskAssessment{
				OverallRisk:    "high",
				LLMConfidence:  0.9,
				Recommendation: "review-carefully",
			},
			autoModeRequested: true,
			minConfidence:     0.7,
			want:              false,
		},
		{
			name: "med risk -> false",
			assessment: &RiskAssessment{
				OverallRisk:    "med",
				LLMConfidence:  0.9,
				Recommendation: "approve",
			},
			autoModeRequested: true,
			minConfidence:     0.7,
			want:              false,
		},
		{
			name: "low risk, high confidence, auto requested -> true",
			assessment: &RiskAssessment{
				OverallRisk:    "low",
				LLMConfidence:  0.9,
				Recommendation: "approve",
			},
			autoModeRequested: true,
			minConfidence:     0.7,
			want:              true,
		},
		{
			name: "low risk but confidence below threshold -> false",
			assessment: &RiskAssessment{
				OverallRisk:    "low",
				LLMConfidence:  0.5,
				Recommendation: "approve",
			},
			autoModeRequested: true,
			minConfidence:     0.7,
			want:              false,
		},
		{
			name: "low risk confidence exactly at threshold -> true",
			assessment: &RiskAssessment{
				OverallRisk:    "low",
				LLMConfidence:  0.7,
				Recommendation: "approve",
			},
			autoModeRequested: true,
			minConfidence:     0.7,
			want:              true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newAssessor(tc.minConfidence)
			got := r.ShouldAutoApply(tc.assessment, tc.autoModeRequested)
			if got != tc.want {
				t.Errorf("ShouldAutoApply = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DefaultRiskAssessorOptions
// ---------------------------------------------------------------------------

func TestDefaultRiskAssessorOptions(t *testing.T) {
	opts := DefaultRiskAssessorOptions()
	if opts.MinConfidence != 0.7 {
		t.Errorf("MinConfidence = %v, want 0.7", opts.MinConfidence)
	}
	if opts.AllowAutoInProduction {
		t.Error("AllowAutoInProduction should be false by default")
	}
	if len(opts.HighRiskNamespaces) == 0 {
		t.Error("HighRiskNamespaces should not be empty")
	}
}
