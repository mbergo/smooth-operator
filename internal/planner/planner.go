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

	"github.com/mbergo/smooth-operator/internal/llm"
	"github.com/mbergo/smooth-operator/internal/policy"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Planner orchestrates manifest validation, policy checks, and risk assessment
type Planner struct {
	validator     *ManifestValidator
	diffGenerator *DiffGenerator
	policyEngine  *policy.Engine
	riskAssessor  *RiskAssessor
}

// NewPlanner creates a new planner
func NewPlanner(client client.Client, riskOptions RiskAssessorOptions) *Planner {
	return &Planner{
		validator:     NewManifestValidator(client),
		diffGenerator: NewDiffGenerator(client),
		policyEngine:  policy.NewEngine(),
		riskAssessor:  NewRiskAssessor(riskOptions),
	}
}

// CreatePlan validates LLM output and creates an executable plan
func (p *Planner) CreatePlan(
	ctx context.Context,
	llmResponse *llm.LLMResponse,
	targetNamespace string,
	autoModeRequested bool,
) (*Plan, error) {
	log := log.FromContext(ctx)

	log.Info("Creating execution plan",
		"patches", len(llmResponse.Patches),
		"targetNamespace", targetNamespace,
		"autoMode", autoModeRequested,
	)

	plan := &Plan{
		Manifests: []ValidatedManifest{},
		Diffs:     []ManifestDiff{},
		Errors:    []string{},
		Warnings:  []string{},
	}

	// Step 1: Validate each manifest
	for i, patch := range llmResponse.Patches {
		log.V(1).Info("Validating manifest", "index", i, "kind", patch.Kind)

		validated, err := p.validator.ValidateYAML(ctx, patch.YAML)
		if err != nil {
			errMsg := fmt.Sprintf("Manifest %d (%s) validation failed: %v", i, patch.Kind, err)
			plan.Errors = append(plan.Errors, errMsg)
			log.Error(err, "Manifest validation failed", "index", i, "kind", patch.Kind)
			continue
		}

		// Step 2: Generate diff
		diff, err := p.diffGenerator.GenerateDiff(ctx, validated)
		if err != nil {
			errMsg := fmt.Sprintf("Diff generation failed for %s/%s: %v", validated.Kind, validated.Name, err)
			plan.Warnings = append(plan.Warnings, errMsg)
			log.Error(err, "Diff generation failed", "kind", validated.Kind, "name", validated.Name)
		} else {
			plan.Diffs = append(plan.Diffs, *diff)
		}

		// Step 3: Perform dry-run
		dryRunResult, err := p.validator.PerformDryRun(ctx, validated)
		if err != nil {
			errMsg := fmt.Sprintf("Dry-run error for %s/%s: %v", validated.Kind, validated.Name, err)
			plan.Warnings = append(plan.Warnings, errMsg)
		}
		validated.DryRunResult = dryRunResult

		if dryRunResult != nil && !dryRunResult.Success {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("Dry-run failed: %s", dryRunResult.Message))
		}

		// Step 4: Policy evaluation
		policyResult, err := p.policyEngine.EvaluateManifest(ctx, validated.Object, validated.Kind, validated.Name, validated.Namespace)
		if err != nil {
			errMsg := fmt.Sprintf("Policy evaluation failed for %s/%s: %v", validated.Kind, validated.Name, err)
			plan.Errors = append(plan.Errors, errMsg)
			log.Error(err, "Policy evaluation failed")
			continue
		}

		// Aggregate policy results
		if plan.PolicyResults.EvaluatedPolicies == 0 {
			plan.PolicyResults = *policyResult
		} else {
			plan.PolicyResults.Violations = append(plan.PolicyResults.Violations, policyResult.Violations...)
			plan.PolicyResults.EvaluatedPolicies += policyResult.EvaluatedPolicies
			if !policyResult.Passed {
				plan.PolicyResults.Passed = false
			}
		}

		plan.Manifests = append(plan.Manifests, *validated)
	}

	// Step 5: Risk assessment
	plan.RiskAssessment = *p.riskAssessor.AssessRisk(
		ctx,
		plan,
		llmResponse.Risk,
		llmResponse.Confidence,
		targetNamespace,
		autoModeRequested,
	)

	log.Info("Plan created",
		"manifests", len(plan.Manifests),
		"diffs", len(plan.Diffs),
		"policyViolations", len(plan.PolicyResults.Violations),
		"overallRisk", plan.RiskAssessment.OverallRisk,
		"recommendation", plan.RiskAssessment.Recommendation,
	)

	return plan, nil
}

// ShouldAutoApply checks if the plan can be auto-applied
func (p *Planner) ShouldAutoApply(plan *Plan, autoModeRequested bool) bool {
	// Never auto-apply if there are blocking policy violations
	if !plan.PolicyResults.Passed {
		return false
	}

	// Never auto-apply if there are validation errors
	if len(plan.Errors) > 0 {
		return false
	}

	// Defer to risk assessor
	return p.riskAssessor.ShouldAutoApply(&plan.RiskAssessment, autoModeRequested)
}

