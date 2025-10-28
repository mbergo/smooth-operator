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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// RolloutWatcher monitors Deployment rollouts and pod health
type RolloutWatcher struct {
	client  client.Client
	options ExecutorOptions
}

// NewRolloutWatcher creates a new rollout watcher
func NewRolloutWatcher(client client.Client, options ExecutorOptions) *RolloutWatcher {
	return &RolloutWatcher{
		client:  client,
		options: options,
	}
}

// WatchRollouts monitors rollout progress for all applied Deployments
func (w *RolloutWatcher) WatchRollouts(ctx context.Context, applied []AppliedResource) ([]RolloutStatus, error) {
	log := log.FromContext(ctx)

	results := []RolloutStatus{}

	// Find all Deployments in applied resources
	for _, resource := range applied {
		if resource.Kind != "Deployment" {
			continue
		}

		log.Info("Watching rollout", "deployment", resource.Name, "namespace", resource.Namespace)

		status, err := w.watchDeploymentRollout(ctx, resource.Namespace, resource.Name)
		if err != nil {
			log.Error(err, "Failed to watch rollout", "deployment", resource.Name)
			status = &RolloutStatus{
				DeploymentName: resource.Name,
				Namespace:      resource.Namespace,
				State:          "failed",
				StartedAt:      time.Now(),
				CompletedAt:    time.Now(),
				Conditions:     []string{fmt.Sprintf("Watch error: %v", err)},
			}
		}

		results = append(results, *status)
	}

	return results, nil
}

// watchDeploymentRollout watches a single Deployment until complete or timeout
func (w *RolloutWatcher) watchDeploymentRollout(ctx context.Context, namespace, name string) (*RolloutStatus, error) {
	log := log.FromContext(ctx)

	status := &RolloutStatus{
		DeploymentName: name,
		Namespace:      namespace,
		State:          "progressing",
		StartedAt:      time.Now(),
		Conditions:     []string{},
		HealthChecks:   []HealthCheckResult{},
	}

	timeout := time.After(w.options.RolloutTimeout)
	ticker := time.NewTicker(w.options.HealthCheckInterval)
	defer ticker.Stop()

	failedChecks := 0

	for {
		select {
		case <-timeout:
			// Timeout reached
			status.State = "timedout"
			status.CompletedAt = time.Now()
			log.Info("Rollout timed out",
				"deployment", name,
				"timeout", w.options.RolloutTimeout.String(),
			)
			return status, nil

		case <-ticker.C:
			// Check deployment status
			deployment := &appsv1.Deployment{}
			err := w.client.Get(ctx, client.ObjectKey{
				Namespace: namespace,
				Name:      name,
			}, deployment)

			if err != nil {
				return nil, fmt.Errorf("failed to get deployment: %w", err)
			}

			// Update status
			status.ReadyReplicas = deployment.Status.ReadyReplicas
			status.TotalReplicas = *deployment.Spec.Replicas

			// Check conditions
			for _, condition := range deployment.Status.Conditions {
				status.Conditions = append(status.Conditions, 
					fmt.Sprintf("%s: %s (%s)", condition.Type, condition.Status, condition.Message))
			}

			// Check if rollout is complete
			if deployment.Status.ReadyReplicas == *deployment.Spec.Replicas &&
				deployment.Status.UpdatedReplicas == *deployment.Spec.Replicas &&
				deployment.Status.AvailableReplicas == *deployment.Spec.Replicas {

				// Perform health checks on pods
				healthChecksPassed, healthChecks := w.checkPodHealth(ctx, namespace, deployment)
				status.HealthChecks = healthChecks

				if healthChecksPassed {
					status.State = "complete"
					status.CompletedAt = time.Now()
					log.Info("Rollout complete",
						"deployment", name,
						"duration", time.Since(status.StartedAt).String(),
					)
					return status, nil
				}

				// Health checks failed
				failedChecks++
				log.Info("Health checks failed",
					"deployment", name,
					"failedChecks", failedChecks,
					"threshold", w.options.FailureThreshold,
				)

				if failedChecks >= w.options.FailureThreshold {
					status.State = "failed"
					status.CompletedAt = time.Now()
					log.Info("Rollout failed (health check threshold exceeded)", "deployment", name)
					return status, nil
				}
			}

			log.V(1).Info("Rollout in progress",
				"deployment", name,
				"ready", deployment.Status.ReadyReplicas,
				"desired", *deployment.Spec.Replicas,
			)
		}
	}
}

// checkPodHealth verifies that pods are healthy and ready
func (w *RolloutWatcher) checkPodHealth(ctx context.Context, namespace string, deployment *appsv1.Deployment) (bool, []HealthCheckResult) {
	log := log.FromContext(ctx)

	// Get pods for this deployment
	podList := &corev1.PodList{}
	err := w.client.List(ctx, podList, &client.ListOptions{
		Namespace: namespace,
	}, client.MatchingLabels(deployment.Spec.Selector.MatchLabels))

	if err != nil {
		log.Error(err, "Failed to list pods for health check")
		return false, []HealthCheckResult{}
	}

	healthChecks := []HealthCheckResult{}
	allHealthy := true

	for _, pod := range podList.Items {
		ready := false
		message := "Pod not ready"

		// Check pod conditions
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = true
				message = "Pod ready"
				break
			}
		}

		// Count restarts
		restarts := int32(0)
		for _, containerStatus := range pod.Status.ContainerStatuses {
			restarts += containerStatus.RestartCount
		}

		// Check for excessive restarts
		if restarts > 3 {
			ready = false
			message = fmt.Sprintf("Pod has restarted %d times", restarts)
			allHealthy = false
		}

		// Check pod phase
		if pod.Status.Phase != corev1.PodRunning {
			ready = false
			message = fmt.Sprintf("Pod phase: %s", pod.Status.Phase)
			allHealthy = false
		}

		healthCheck := HealthCheckResult{
			PodName:   pod.Name,
			Ready:     ready,
			Restarts:  restarts,
			Phase:     string(pod.Status.Phase),
			Message:   message,
			CheckedAt: time.Now(),
		}

		healthChecks = append(healthChecks, healthCheck)

		if !ready {
			allHealthy = false
		}
	}

	log.Info("Health check complete",
		"deployment", deployment.Name,
		"totalPods", len(podList.Items),
		"healthy", allHealthy,
	)

	return allHealthy, healthChecks
}

