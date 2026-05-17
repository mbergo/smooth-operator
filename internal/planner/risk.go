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
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Risk level identifiers shared across planner risk-assessment logic.
const (
	riskLow          = "low"
	riskMed          = "med"
	riskHigh         = "high"
	severityBlocking = "blocking"
)

// RiskAssessor evaluates risk of applying changes
type RiskAssessor struct {
	confidenceThreshold float64
}

// RiskAssessorOptions configures risk assessment
type RiskAssessorOptions struct {
	// MinConfidence is the minimum LLM confidence for auto-apply
	MinConfidence float64

	// HighRiskNamespaces are namespaces that require extra caution
	HighRiskNamespaces []string

	// AllowAutoInProduction determines if auto mode works in prod
	AllowAutoInProduction bool
}

// DefaultRiskAssessorOptions returns sensible defaults
func DefaultRiskAssessorOptions() RiskAssessorOptions {
	return RiskAssessorOptions{
		MinConfidence:         0.7, // 70% confidence minimum
		HighRiskNamespaces:    []string{"production", "prod", "default", "kube-system"},
		AllowAutoInProduction: false,
	}
}

// NewRiskAssessor creates a new risk assessor
func NewRiskAssessor(options RiskAssessorOptions) *RiskAssessor {
	return &RiskAssessor{
		confidenceThreshold: options.MinConfidence,
	}
}

// AssessRisk evaluates the overall risk of a plan
func (r *RiskAssessor) AssessRisk(
	ctx context.Context,
	plan *Plan,
	llmRisk string,
	llmConfidence float64,
	targetNamespace string,
	autoModeRequested bool,
) *RiskAssessment {
	logger := log.FromContext(ctx)

	assessment := &RiskAssessment{
		OverallRisk:   riskLow,
		LLMConfidence: llmConfidence,
		RiskFactors:   []RiskFactor{},
	}

	// Factor 1: LLM-reported risk
	switch llmRisk {
	case riskHigh:
		assessment.RiskFactors = append(assessment.RiskFactors, RiskFactor{
			Factor:   "llm-high-risk",
			Severity: riskHigh,
			Reason:   "LLM classified these changes as high risk",
		})
		assessment.OverallRisk = riskHigh
	case riskMed:
		assessment.RiskFactors = append(assessment.RiskFactors, RiskFactor{
			Factor:   "llm-medium-risk",
			Severity: riskMed,
			Reason:   "LLM classified these changes as medium risk",
		})
		if assessment.OverallRisk == riskLow {
			assessment.OverallRisk = riskMed
		}
	}

	// Factor 2: Low confidence
	if llmConfidence < r.confidenceThreshold {
		assessment.RiskFactors = append(assessment.RiskFactors, RiskFactor{
			Factor:   "low-confidence",
			Severity: riskHigh,
			Reason:   fmt.Sprintf("LLM confidence %.0f%% is below threshold %.0f%%", llmConfidence*100, r.confidenceThreshold*100),
		})
		assessment.OverallRisk = riskHigh
	}

	// Factor 3: Policy violations
	if !plan.PolicyResults.Passed {
		blockingViolations := 0
		for _, violation := range plan.PolicyResults.Violations {
			if violation.Severity == severityBlocking {
				blockingViolations++
			}
		}
		if blockingViolations > 0 {
			assessment.RiskFactors = append(assessment.RiskFactors, RiskFactor{
				Factor:   "policy-violations",
				Severity: riskHigh,
				Reason:   fmt.Sprintf("%d blocking policy violations", blockingViolations),
			})
			assessment.OverallRisk = riskHigh
		}
	}

	// Factor 4: High-risk namespace
	highRiskNamespaces := []string{"production", "prod", "default", "kube-system"}
	for _, riskNs := range highRiskNamespaces {
		if strings.Contains(strings.ToLower(targetNamespace), riskNs) {
			assessment.RiskFactors = append(assessment.RiskFactors, RiskFactor{
				Factor:   "high-risk-namespace",
				Severity: riskMed,
				Reason:   fmt.Sprintf("Namespace '%s' is considered high-risk", targetNamespace),
			})
			if assessment.OverallRisk == riskLow {
				assessment.OverallRisk = riskMed
			}
			break
		}
	}

	// Factor 5: Number of changes
	if len(plan.Manifests) > 5 {
		assessment.RiskFactors = append(assessment.RiskFactors, RiskFactor{
			Factor:   "many-changes",
			Severity: riskMed,
			Reason:   fmt.Sprintf("Large number of changes (%d manifests)", len(plan.Manifests)),
		})
		if assessment.OverallRisk == riskLow {
			assessment.OverallRisk = riskMed
		}
	}

	// Determine recommendation
	assessment.Recommendation = r.determineRecommendation(assessment, autoModeRequested)

	logger.Info("Risk assessment complete",
		"overallRisk", assessment.OverallRisk,
		"llmConfidence", llmConfidence,
		"riskFactors", len(assessment.RiskFactors),
		"recommendation", assessment.Recommendation,
	)

	return assessment
}

// determineRecommendation decides what action to recommend
func (r *RiskAssessor) determineRecommendation(assessment *RiskAssessment, autoModeRequested bool) string {
	// High risk = always require review
	if assessment.OverallRisk == riskHigh {
		return "review-carefully"
	}

	// Policy violations = reject
	hasBlockingViolations := false
	for _, factor := range assessment.RiskFactors {
		if factor.Factor == "policy-violations" {
			hasBlockingViolations = true
			break
		}
	}
	if hasBlockingViolations {
		return "reject"
	}

	// Medium risk = review if auto requested, otherwise approve
	if assessment.OverallRisk == riskMed {
		if autoModeRequested {
			return "review-carefully"
		}
		return "approve"
	}

	// Low risk = approve
	return "approve"
}

// ShouldAutoApply determines if auto-apply is safe
func (r *RiskAssessor) ShouldAutoApply(assessment *RiskAssessment, autoModeRequested bool) bool {
	if !autoModeRequested {
		return false
	}

	if assessment.Recommendation == "reject" {
		return false
	}

	if assessment.OverallRisk == riskHigh {
		return false
	}

	// Only auto-apply low-risk changes with high confidence
	return assessment.OverallRisk == riskLow && assessment.LLMConfidence >= r.confidenceThreshold
}
