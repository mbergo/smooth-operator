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

// NewEngine creates a new policy engine with default policies
func NewEngine() *Engine {
	return &Engine{
		policies: []Policy{
			&RunAsNonRootPolicy{},
			&ResourceLimitsPolicy{},
			&ReadinessProbePolicy{},
			&LivenessProbePolicy{},
			&ImageRegistryPolicy{},
			&HostPathPolicy{},
			&PrivilegedContainerPolicy{},
			&LoadBalancerInternalAnnotationPolicy{},
		},
	}
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
