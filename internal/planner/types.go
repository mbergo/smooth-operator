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
	"github.com/mbergo/smooth-operator/internal/policy"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Plan represents a validated plan with manifests ready for application
type Plan struct {
	// Manifests are the validated Kubernetes resources to apply
	Manifests []ValidatedManifest

	// Diffs show the before/after changes
	Diffs []ManifestDiff

	// PolicyResults contain policy evaluation outcomes
	PolicyResults policy.PolicyEvaluationResult

	// RiskAssessment contains the overall risk assessment
	RiskAssessment RiskAssessment

	// Errors encountered during planning
	Errors []string

	// Warnings that don't block the plan
	Warnings []string
}

// ValidatedManifest represents a Kubernetes manifest that passed validation
type ValidatedManifest struct {
	// Kind is the resource kind (Deployment, Service, HPA, etc.)
	Kind string

	// Name is the resource name
	Name string

	// Namespace is the resource namespace
	Namespace string

	// YAML is the validated YAML content
	YAML string

	// Object is the parsed unstructured object
	Object *unstructured.Unstructured

	// IsNew indicates if this is a new resource (vs update)
	IsNew bool

	// DryRunResult contains the result of dry-run validation
	DryRunResult *DryRunResult
}

// ManifestDiff represents the difference between current and proposed state
type Diff struct {
	Kind      string
	Name      string
	Namespace string

	// Unified diff format
	UnifiedDiff string

	// Change type: create, update, delete
	ChangeType string

	// Number of lines added/removed
	LinesAdded   int
	LinesRemoved int
}

// ManifestDiff represents changes for a specific manifest
type ManifestDiff struct {
	// Resource identification
	Kind      string
	Name      string
	Namespace string

	// Change type
	ChangeType string // create, update, delete, no-change

	// Before state (empty if creating)
	Before string

	// After state (empty if deleting)
	After string

	// Unified diff (git-style)
	UnifiedDiff string

	// Summary of changes
	Summary string

	// Changed fields
	ChangedFields []string
}

// DryRunResult contains the result of a dry-run apply
type DryRunResult struct {
	Success bool
	Message string
	Errors  []string
}

// RiskAssessment evaluates the risk of applying changes
type RiskAssessment struct {
	// Overall risk level
	OverallRisk string // low, med, high

	// LLM confidence score (0.0 - 1.0)
	LLMConfidence float64

	// Risk factors
	RiskFactors []RiskFactor

	// Recommendation
	Recommendation string // approve, review-carefully, reject
}

// RiskFactor represents a specific risk consideration
type RiskFactor struct {
	Factor   string // e.g., "high-privilege-change", "production-namespace"
	Severity string // low, med, high
	Reason   string
}
