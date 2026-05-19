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

package executor

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ExecutionResult contains the outcome of applying manifests
type ExecutionResult struct {
	// Success indicates if all manifests were applied successfully
	Success bool

	// AppliedResources lists resources that were applied
	AppliedResources []AppliedResource

	// RolloutStatuses tracks rollout progress for Deployments
	RolloutStatuses []RolloutStatus

	// Errors encountered during execution
	Errors []string

	// Warnings (non-fatal issues)
	Warnings []string

	// RolledBack indicates if a rollback was performed
	RolledBack bool

	// ExecutionTime is the total time taken
	ExecutionTime time.Duration
}

// AppliedResource represents a successfully applied resource
type AppliedResource struct {
	Kind      string
	Name      string
	Namespace string
	Operation string // created, updated, unchanged
	AppliedAt time.Time
}

// RolloutStatus tracks the status of a Deployment rollout
type RolloutStatus struct {
	DeploymentName string
	Namespace      string

	// Rollout state
	State         string // progressing, complete, failed, timedout
	ReadyReplicas int32
	TotalReplicas int32

	// Timing
	StartedAt   time.Time
	CompletedAt time.Time

	// Conditions
	Conditions []string

	// Health check results
	HealthChecks []HealthCheckResult
}

// HealthCheckResult contains pod health information
type HealthCheckResult struct {
	PodName   string
	Ready     bool
	Restarts  int32
	Phase     string
	Message   string
	CheckedAt time.Time
}

// RollbackResult contains information about a rollback operation
type RollbackResult struct {
	Success         bool
	RolledBackCount int
	Errors          []string
	RollbackTime    time.Duration
}

// ExecutorOptions configures executor behavior
type ExecutorOptions struct {
	// RolloutTimeout is the max time to wait for a rollout
	RolloutTimeout time.Duration

	// HealthCheckInterval is how often to check pod health
	HealthCheckInterval time.Duration

	// FailureThreshold is the number of failed checks before rollback
	FailureThreshold int

	// EnableRollback determines if automatic rollback is enabled
	EnableRollback bool

	// DryRunFirst performs dry-run before actual apply
	DryRunFirst bool
}

// DefaultExecutorOptions returns sensible defaults
func DefaultExecutorOptions() ExecutorOptions {
	return ExecutorOptions{
		RolloutTimeout:      5 * time.Minute,
		HealthCheckInterval: 10 * time.Second,
		FailureThreshold:    3,
		EnableRollback:      true,
		DryRunFirst:         true,
	}
}

// BackupState stores the previous state for rollback
type BackupState struct {
	Resources map[string]*unstructured.Unstructured // Key: kind/namespace/name
	CreatedAt time.Time
}
