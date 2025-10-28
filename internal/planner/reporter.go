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
	"fmt"
	"strings"

	"github.com/mbergo/smooth-operator/internal/policy"
)

// Reporter formats plan results for display
type Reporter struct{}

// NewReporter creates a new reporter
func NewReporter() *Reporter {
	return &Reporter{}
}

// FormatPlanSummary creates a human-readable plan summary
func (r *Reporter) FormatPlanSummary(plan *Plan) string {
	var builder strings.Builder

	builder.WriteString("📋 EXECUTION PLAN SUMMARY\n")
	builder.WriteString("═══════════════════════════════════════════════════════════\n\n")

	// Manifests section
	builder.WriteString(fmt.Sprintf("📦 Manifests: %d total\n", len(plan.Manifests)))
	for i, manifest := range plan.Manifests {
		changeType := "UPDATE"
		if manifest.IsNew {
			changeType = "CREATE"
		}
		builder.WriteString(fmt.Sprintf("  %d. [%s] %s/%s", i+1, changeType, manifest.Kind, manifest.Name))

		if manifest.DryRunResult != nil {
			if manifest.DryRunResult.Success {
				builder.WriteString(" ✅")
			} else {
				builder.WriteString(" ❌")
			}
		}
		builder.WriteString("\n")
	}
	builder.WriteString("\n")

	// Policy violations section
	if len(plan.PolicyResults.Violations) > 0 {
		builder.WriteString(fmt.Sprintf("🛡️  Policy Violations: %d found\n", len(plan.PolicyResults.Violations)))
		blockingCount := 0
		warningCount := 0

		for _, violation := range plan.PolicyResults.Violations {
			if violation.Severity == "blocking" {
				blockingCount++
			} else {
				warningCount++
			}
		}

		builder.WriteString(fmt.Sprintf("  - Blocking: %d\n", blockingCount))
		builder.WriteString(fmt.Sprintf("  - Warnings: %d\n\n", warningCount))

		for i, violation := range plan.PolicyResults.Violations {
			icon := "⚠️ "
			if violation.Severity == "blocking" {
				icon = "❌"
			}
			builder.WriteString(fmt.Sprintf("  %d. %s [%s] %s\n", i+1, icon, violation.Policy, violation.Message))
			if violation.SuggestedFix != "" {
				builder.WriteString(fmt.Sprintf("     💡 Fix: %s\n", violation.SuggestedFix))
			}
		}
		builder.WriteString("\n")
	} else {
		builder.WriteString("🛡️  Policy Check: ✅ All policies passed\n\n")
	}

	// Risk assessment section
	builder.WriteString(fmt.Sprintf("⚖️  Risk Assessment: %s\n", strings.ToUpper(plan.RiskAssessment.OverallRisk)))
	builder.WriteString(fmt.Sprintf("  - LLM Confidence: %.0f%%\n", plan.RiskAssessment.LLMConfidence*100))
	builder.WriteString(fmt.Sprintf("  - Recommendation: %s\n", plan.RiskAssessment.Recommendation))

	if len(plan.RiskAssessment.RiskFactors) > 0 {
		builder.WriteString("\n  Risk Factors:\n")
		for _, factor := range plan.RiskAssessment.RiskFactors {
			builder.WriteString(fmt.Sprintf("  - [%s] %s: %s\n", factor.Severity, factor.Factor, factor.Reason))
		}
	}
	builder.WriteString("\n")

	// Errors and warnings
	if len(plan.Errors) > 0 {
		builder.WriteString(fmt.Sprintf("❌ Errors: %d\n", len(plan.Errors)))
		for i, err := range plan.Errors {
			builder.WriteString(fmt.Sprintf("  %d. %s\n", i+1, err))
		}
		builder.WriteString("\n")
	}

	if len(plan.Warnings) > 0 {
		builder.WriteString(fmt.Sprintf("⚠️  Warnings: %d\n", len(plan.Warnings)))
		for i, warn := range plan.Warnings {
			builder.WriteString(fmt.Sprintf("  %d. %s\n", i+1, warn))
		}
		builder.WriteString("\n")
	}

	builder.WriteString("═══════════════════════════════════════════════════════════\n")

	return builder.String()
}

// FormatPolicyViolations creates actionable policy violation messages
func (r *Reporter) FormatPolicyViolations(violations []policy.PolicyViolation) string {
	if len(violations) == 0 {
		return "✅ No policy violations - all checks passed!"
	}

	var builder strings.Builder

	builder.WriteString("🛡️  POLICY VIOLATIONS FOUND\n")
	builder.WriteString("═══════════════════════════════════════════════════════════\n\n")

	blocking := []policy.PolicyViolation{}
	warnings := []policy.PolicyViolation{}

	for _, v := range violations {
		if v.Severity == "blocking" {
			blocking = append(blocking, v)
		} else {
			warnings = append(warnings, v)
		}
	}

	if len(blocking) > 0 {
		builder.WriteString(fmt.Sprintf("❌ BLOCKING VIOLATIONS (%d) - Must be fixed:\n\n", len(blocking)))
		for i, v := range blocking {
			builder.WriteString(fmt.Sprintf("%d. Policy: %s\n", i+1, v.Policy))
			builder.WriteString(fmt.Sprintf("   Resource: %s/%s\n", v.Kind, v.Name))
			builder.WriteString(fmt.Sprintf("   Issue: %s\n", v.Message))
			if v.SuggestedFix != "" {
				builder.WriteString(fmt.Sprintf("   💡 How to fix: %s\n", v.SuggestedFix))
			}
			builder.WriteString("\n")
		}
	}

	if len(warnings) > 0 {
		builder.WriteString(fmt.Sprintf("⚠️  WARNINGS (%d) - Should be addressed:\n\n", len(warnings)))
		for i, v := range warnings {
			builder.WriteString(fmt.Sprintf("%d. Policy: %s\n", i+1, v.Policy))
			builder.WriteString(fmt.Sprintf("   Resource: %s/%s\n", v.Kind, v.Name))
			builder.WriteString(fmt.Sprintf("   Issue: %s\n", v.Message))
			if v.SuggestedFix != "" {
				builder.WriteString(fmt.Sprintf("   💡 Suggestion: %s\n", v.SuggestedFix))
			}
			builder.WriteString("\n")
		}
	}

	builder.WriteString("═══════════════════════════════════════════════════════════\n")

	return builder.String()
}

// FormatDiffs creates a readable diff summary
func (r *Reporter) FormatDiffs(diffs []ManifestDiff) string {
	if len(diffs) == 0 {
		return "No changes detected."
	}

	var builder strings.Builder

	builder.WriteString("📊 PROPOSED CHANGES\n")
	builder.WriteString("═══════════════════════════════════════════════════════════\n\n")

	for i, diff := range diffs {
		builder.WriteString(fmt.Sprintf("%d. ", i+1))

		switch diff.ChangeType {
		case "create":
			builder.WriteString(fmt.Sprintf("➕ CREATE %s/%s\n", diff.Kind, diff.Name))
		case "update":
			builder.WriteString(fmt.Sprintf("✏️  UPDATE %s/%s\n", diff.Kind, diff.Name))
		case "delete":
			builder.WriteString(fmt.Sprintf("🗑️  DELETE %s/%s\n", diff.Kind, diff.Name))
		}

		builder.WriteString(fmt.Sprintf("   %s\n", diff.Summary))

		if len(diff.ChangedFields) > 0 && len(diff.ChangedFields) <= 5 {
			builder.WriteString("   Fields: ")
			builder.WriteString(strings.Join(diff.ChangedFields, ", "))
			builder.WriteString("\n")
		}

		builder.WriteString("\n")
	}

	builder.WriteString("═══════════════════════════════════════════════════════════\n")

	return builder.String()
}
