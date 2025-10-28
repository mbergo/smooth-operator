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
	"context"
	"fmt"
	"time"

	"github.com/mbergo/smooth-operator/internal/planner"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Executor safely applies Kubernetes manifests to the cluster
type Executor struct {
	client  client.Client
	options ExecutorOptions
	watcher *RolloutWatcher
}

// NewExecutor creates a new executor
func NewExecutor(client client.Client, options ExecutorOptions) *Executor {
	return &Executor{
		client:  client,
		options: options,
		watcher: NewRolloutWatcher(client, options),
	}
}

// Execute applies a validated plan to the cluster
func (e *Executor) Execute(ctx context.Context, plan *planner.Plan) (*ExecutionResult, error) {
	log := log.FromContext(ctx)
	startTime := time.Now()

	log.Info("Starting execution",
		"manifests", len(plan.Manifests),
		"dryRunFirst", e.options.DryRunFirst,
	)

	result := &ExecutionResult{
		Success:          true,
		AppliedResources: []AppliedResource{},
		RolloutStatuses:  []RolloutStatus{},
		Errors:           []string{},
		Warnings:         []string{},
		RolledBack:       false,
	}

	// Backup current state for potential rollback
	backup, err := e.backupCurrentState(ctx, plan)
	if err != nil {
		log.Error(err, "Failed to backup current state")
		result.Warnings = append(result.Warnings, fmt.Sprintf("Backup failed: %v", err))
	}

	// Apply each manifest
	for i, manifest := range plan.Manifests {
		log.Info("Applying manifest",
			"index", i+1,
			"kind", manifest.Kind,
			"name", manifest.Name,
			"isNew", manifest.IsNew,
		)

		applied, err := e.applyManifest(ctx, &manifest)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to apply %s/%s: %v", manifest.Kind, manifest.Name, err)
			result.Errors = append(result.Errors, errMsg)
			result.Success = false
			log.Error(err, "Manifest application failed")

			// Rollback if enabled
			if e.options.EnableRollback && backup != nil {
				log.Info("Triggering automatic rollback due to apply failure")
				rollbackResult := e.rollback(ctx, backup, result.AppliedResources)
				result.RolledBack = true
				result.Warnings = append(result.Warnings, fmt.Sprintf("Rolled back %d resources", rollbackResult.RolledBackCount))
			}

			break
		}

		result.AppliedResources = append(result.AppliedResources, *applied)
	}

	// If no errors, watch rollouts for Deployments
	if result.Success && len(result.AppliedResources) > 0 {
		log.Info("Watching rollouts for applied Deployments")
		
		rolloutResults, err := e.watcher.WatchRollouts(ctx, result.AppliedResources)
		if err != nil {
			log.Error(err, "Rollout watching failed")
			result.Warnings = append(result.Warnings, fmt.Sprintf("Rollout watch error: %v", err))
		}
		
		result.RolloutStatuses = rolloutResults

		// Check if any rollouts failed
		for _, rollout := range rolloutResults {
			if rollout.State == "failed" || rollout.State == "timedout" {
				result.Success = false
				result.Errors = append(result.Errors, 
					fmt.Sprintf("Rollout failed for %s: %s", rollout.DeploymentName, rollout.State))

				// Trigger rollback
				if e.options.EnableRollback && backup != nil {
					log.Info("Triggering automatic rollback due to rollout failure")
					rollbackResult := e.rollback(ctx, backup, result.AppliedResources)
					result.RolledBack = true
					result.Warnings = append(result.Warnings, 
						fmt.Sprintf("Rolled back %d resources due to rollout failure", rollbackResult.RolledBackCount))
				}
				break
			}
		}
	}

	result.ExecutionTime = time.Since(startTime)

	log.Info("Execution complete",
		"success", result.Success,
		"applied", len(result.AppliedResources),
		"errors", len(result.Errors),
		"rolledBack", result.RolledBack,
		"duration", result.ExecutionTime.String(),
	)

	return result, nil
}

// applyManifest applies a single manifest using server-side apply
func (e *Executor) applyManifest(ctx context.Context, manifest *planner.ValidatedManifest) (*AppliedResource, error) {
	log := log.FromContext(ctx)

	obj := manifest.Object.DeepCopy()

	var operation string
	var err error

	if manifest.IsNew {
		// Create new resource
		operation = "created"
		err = e.client.Create(ctx, obj)
	} else {
		// Update existing resource using server-side apply
		operation = "updated"
		err = e.client.Patch(ctx, obj, client.Apply, 
			client.ForceOwnership,
			client.FieldOwner("smooth-operator"))
	}

	if err != nil {
		return nil, fmt.Errorf("failed to %s %s/%s: %w", operation, manifest.Kind, manifest.Name, err)
	}

	applied := &AppliedResource{
		Kind:      manifest.Kind,
		Name:      manifest.Name,
		Namespace: manifest.Namespace,
		Operation: operation,
		AppliedAt: time.Now(),
	}

	log.Info("Manifest applied successfully",
		"kind", manifest.Kind,
		"name", manifest.Name,
		"operation", operation,
	)

	return applied, nil
}

// backupCurrentState captures current state for potential rollback
func (e *Executor) backupCurrentState(ctx context.Context, plan *planner.Plan) (*BackupState, error) {
	log := log.FromContext(ctx)

	backup := &BackupState{
		Resources: make(map[string]*unstructured.Unstructured),
		CreatedAt: time.Now(),
	}

	for _, manifest := range plan.Manifests {
		if manifest.IsNew {
			// No backup needed for new resources
			continue
		}

		// Fetch current state
		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(manifest.Object.GroupVersionKind())

		err := e.client.Get(ctx, client.ObjectKey{
			Namespace: manifest.Namespace,
			Name:      manifest.Name,
		}, existing)

		if err != nil {
			log.Error(err, "Failed to backup resource", "kind", manifest.Kind, "name", manifest.Name)
			continue
		}

		key := fmt.Sprintf("%s/%s/%s", manifest.Kind, manifest.Namespace, manifest.Name)
		backup.Resources[key] = existing.DeepCopy()
	}

	log.Info("Backup created", "resources", len(backup.Resources))
	return backup, nil
}

// rollback restores previous state
func (e *Executor) rollback(ctx context.Context, backup *BackupState, applied []AppliedResource) *RollbackResult {
	log := log.FromContext(ctx)
	startTime := time.Now()

	log.Info("Performing rollback", "backupResources", len(backup.Resources), "appliedResources", len(applied))

	result := &RollbackResult{
		Success:         true,
		RolledBackCount: 0,
		Errors:          []string{},
	}

	for _, appliedResource := range applied {
		key := fmt.Sprintf("%s/%s/%s", appliedResource.Kind, appliedResource.Namespace, appliedResource.Name)

		if appliedResource.Operation == "created" {
			// Delete newly created resources
			obj := &unstructured.Unstructured{}
			obj.SetKind(appliedResource.Kind)
			obj.SetNamespace(appliedResource.Namespace)
			obj.SetName(appliedResource.Name)

			err := e.client.Delete(ctx, obj)
			if err != nil {
				errMsg := fmt.Sprintf("Failed to delete %s: %v", key, err)
				result.Errors = append(result.Errors, errMsg)
				result.Success = false
				log.Error(err, "Rollback delete failed", "resource", key)
			} else {
				result.RolledBackCount++
				log.Info("Rolled back (deleted)", "resource", key)
			}
		} else if appliedResource.Operation == "updated" {
			// Restore previous state
			if previousState, exists := backup.Resources[key]; exists {
				err := e.client.Update(ctx, previousState)
				if err != nil {
					errMsg := fmt.Sprintf("Failed to restore %s: %v", key, err)
					result.Errors = append(result.Errors, errMsg)
					result.Success = false
					log.Error(err, "Rollback restore failed", "resource", key)
				} else {
					result.RolledBackCount++
					log.Info("Rolled back (restored)", "resource", key)
				}
			}
		}
	}

	result.RollbackTime = time.Since(startTime)

	log.Info("Rollback complete",
		"success", result.Success,
		"rolledBack", result.RolledBackCount,
		"errors", len(result.Errors),
		"duration", result.RollbackTime.String(),
	)

	return result
}

