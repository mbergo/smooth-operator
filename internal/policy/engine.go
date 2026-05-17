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

package policy

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// PolicyViolation represents a policy check failure
type PolicyViolation struct {
	// Policy name or identifier
	Policy string

	// Resource that violated the policy
	Kind      string
	Name      string
	Namespace string

	// Violation details
	Message  string
	Severity string // blocking, warning

	// Suggested fix
	SuggestedFix string
}

// PolicyEvaluationResult contains policy check outcomes
type PolicyEvaluationResult struct {
	// Passed indicates if all policies passed
	Passed bool

	// Violations contains any policy violations
	Violations []PolicyViolation

	// EvaluatedPolicies is the count of policies checked
	EvaluatedPolicies int
}

// Engine evaluates policies against manifests
type Engine struct {
	policies []Policy
}

// Policy represents a validation rule
type Policy interface {
	// Name returns the policy name
	Name() string

	// Evaluate checks if the manifest passes the policy
	Evaluate(ctx context.Context, obj *unstructured.Unstructured) *PolicyResult

	// Severity returns blocking or warning
	Severity() string
}

// PolicyResult contains the outcome of a policy evaluation
type PolicyResult struct {
	Passed       bool
	PolicyName   string
	Message      string
	Severity     string
	SuggestedFix string
}

// NewEngine creates a new policy engine with default policies.
//
// ResourceLimitsPolicy, LivenessProbePolicy, and ReadinessProbePolicy default
// to severity "warning" so that existing callers are not disrupted. Use
// NewStrictEngine or Engine.SetSeverity to promote individual policies to
// "blocking" for production deployments.
func NewEngine() *Engine {
	return &Engine{
		policies: []Policy{
			// Kind allowlist must be evaluated first so that high-risk Kinds are
			// rejected before any other policy attempts to inspect their fields.
			&KindAllowlistPolicy{},
			&RunAsNonRootPolicy{},
			&ResourceLimitsPolicy{},   // severity: warning (default)
			&ReadinessProbePolicy{},   // severity: warning (default)
			&LivenessProbePolicy{},    // severity: warning (default)
			&ImageRegistryPolicy{},
			&HostPathPolicy{},
			&PrivilegedContainerPolicy{},
			&LoadBalancerInternalAnnotationPolicy{},
		},
	}
}

// NewStrictEngine creates a policy engine with ResourceLimitsPolicy,
// LivenessProbePolicy, and ReadinessProbePolicy promoted to "blocking".
//
// Strict mode is recommended for production clusters: it ensures the prompt's
// enforcement guarantees match the engine's actual gating behaviour, closing
// the gap identified in the security audit.
func NewStrictEngine() *Engine {
	return &Engine{
		policies: []Policy{
			// Kind allowlist must be evaluated first.
			&KindAllowlistPolicy{},
			&RunAsNonRootPolicy{},
			new(ResourceLimitsPolicy).WithSeverity("blocking"),
			new(ReadinessProbePolicy).WithSeverity("blocking"),
			new(LivenessProbePolicy).WithSeverity("blocking"),
			&ImageRegistryPolicy{},
			&HostPathPolicy{},
			&PrivilegedContainerPolicy{},
			&LoadBalancerInternalAnnotationPolicy{},
		},
	}
}

// severitySetter is a private interface satisfied by policies whose severity
// can be reconfigured at runtime (ResourceLimitsPolicy, ReadinessProbePolicy,
// LivenessProbePolicy). It is intentionally not exported; callers should use
// Engine.SetSeverity instead.
type severitySetter interface {
	// setSeverity updates the policy's severity in-place.
	setSeverity(s string)
}

// SetSeverity updates the severity of the named policy in the engine's
// registered policy list. It returns true when the policy was found and
// updated, false when no policy with that name is registered.
//
// Only policies that support runtime severity mutation (ResourceLimitsPolicy,
// ReadinessProbePolicy, LivenessProbePolicy) respond to this call; all others
// are silently skipped so that a single call to SetSeverity("run-as-non-root",
// "warning") is safe even though RunAsNonRootPolicy has a fixed severity.
func (e *Engine) SetSeverity(policyName, severity string) bool {
	severity = strings.ToLower(strings.TrimSpace(severity))
	if severity != "blocking" && severity != "warning" {
		return false
	}
	for _, p := range e.policies {
		if p.Name() != policyName {
			continue
		}
		if ss, ok := p.(severitySetter); ok {
			ss.setSeverity(severity)
			return true
		}
		// Policy found but does not support runtime mutation — still report
		// found=true so callers can distinguish "unknown policy" from "policy
		// with fixed severity".
		return true
	}
	return false
}

// EvaluateManifest runs all policies against a manifest
func (e *Engine) EvaluateManifest(ctx context.Context, obj *unstructured.Unstructured, kind, name, namespace string) (*PolicyEvaluationResult, error) {
	log := log.FromContext(ctx)

	log.Info("Evaluating policies",
		"kind", kind,
		"name", name,
		"policyCount", len(e.policies),
	)

	result := &PolicyEvaluationResult{
		Passed:            true,
		Violations:        []PolicyViolation{},
		EvaluatedPolicies: len(e.policies),
	}

	for _, policy := range e.policies {
		policyResult := policy.Evaluate(ctx, obj)

		if !policyResult.Passed {
			violation := PolicyViolation{
				Policy:       policyResult.PolicyName,
				Kind:         kind,
				Name:         name,
				Namespace:    namespace,
				Message:      policyResult.Message,
				Severity:     policyResult.Severity,
				SuggestedFix: policyResult.SuggestedFix,
			}

			result.Violations = append(result.Violations, violation)

			if policyResult.Severity == "blocking" {
				result.Passed = false
			}

			log.Info("Policy violation detected",
				"policy", policyResult.PolicyName,
				"severity", policyResult.Severity,
				"resource", fmt.Sprintf("%s/%s", kind, name),
			)
		}
	}

	if result.Passed {
		log.Info("All policies passed",
			"kind", kind,
			"name", name,
		)
	} else {
		log.Info("Policy violations found",
			"kind", kind,
			"name", name,
			"blockingViolations", len(result.Violations),
		)
	}

	return result, nil
}

// AddPolicy adds a custom policy to the engine
func (e *Engine) AddPolicy(policy Policy) {
	e.policies = append(e.policies, policy)
}

// AutoModeAllowed reports whether automatic application is permitted given a risk
// level and confidence score. Auto-mode is allowed only when risk is "low" or
// "med" AND confidence is at or above the minimum threshold (0.7 by default).
//
// The minConfidence parameter is typically DefaultMinConfidence (0.7). Pass a
// negative value to use the default.
func AutoModeAllowed(risk string, confidence float64, minConfidence float64) bool {
	if minConfidence < 0 {
		minConfidence = DefaultMinConfidence
	}
	if confidence < minConfidence {
		return false
	}
	return risk == "low" || risk == "med"
}

// DefaultMinConfidence is the minimum LLM confidence score required for auto-mode.
const DefaultMinConfidence = 0.7
