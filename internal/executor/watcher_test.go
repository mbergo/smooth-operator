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
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// watcherOpts returns ExecutorOptions suitable for watcher tests — very short
// timeout and tick interval so tests complete in milliseconds.
func watcherOpts(timeout, interval time.Duration, threshold int) ExecutorOptions {
	return ExecutorOptions{
		RolloutTimeout:      timeout,
		HealthCheckInterval: interval,
		FailureThreshold:    threshold,
		EnableRollback:      false,
		DryRunFirst:         false,
	}
}

// buildDeployment is a typed-object builder for Deployment used by watcher tests.
func buildDeployment(namespace, name string, desired, ready, updated, available int32, labels map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			ResourceVersion: "1",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(desired),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "app:latest"}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:          desired,
			ReadyReplicas:     ready,
			UpdatedReplicas:   updated,
			AvailableReplicas: available,
		},
	}
}

// TestWatcher_DeploymentNotReady_TimedOut verifies that when a Deployment's
// ReadyReplicas never reaches the desired count, the watcher returns a signal
// with State="timedout" once the timeout elapses.
func TestWatcher_DeploymentNotReady_TimedOut(t *testing.T) {
	s := testScheme(t)

	labels := map[string]string{"app": "pending"}
	deploy := buildDeployment("default", "pending-deploy", 3, 0, 0, 0, labels)

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(deploy).
		WithStatusSubresource(&appsv1.Deployment{}).
		Build()

	opts := watcherOpts(200*time.Millisecond, 40*time.Millisecond, 3)
	watcher := NewRolloutWatcher(fakeClient, opts)

	applied := []AppliedResource{
		{Kind: "Deployment", Name: "pending-deploy", Namespace: "default"},
	}

	statuses, err := watcher.WatchRollouts(context.Background(), applied)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}

	got := statuses[0]
	if got.State != "timedout" {
		t.Errorf("expected state=timedout, got %q", got.State)
	}
	if got.DeploymentName != "pending-deploy" {
		t.Errorf("unexpected DeploymentName: %q", got.DeploymentName)
	}
}

// TestWatcher_DeploymentReady_Complete verifies that when a Deployment already
// has all replicas ready, the watcher returns State="complete" promptly.
func TestWatcher_DeploymentReady_Complete(t *testing.T) {
	s := testScheme(t)

	labels := map[string]string{"app": "ready"}
	deploy := buildDeployment("default", "ready-deploy", 2, 2, 2, 2, labels)

	// Add ready pods so the health check inside the watcher also passes.
	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ready-pod-1",
			Namespace: "default",
			Labels:    labels,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
	pod2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ready-pod-2",
			Namespace: "default",
			Labels:    labels,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(deploy, pod1, pod2).
		WithStatusSubresource(&appsv1.Deployment{}).
		Build()

	opts := watcherOpts(5*time.Second, 30*time.Millisecond, 3)
	watcher := NewRolloutWatcher(fakeClient, opts)

	applied := []AppliedResource{
		{Kind: "Deployment", Name: "ready-deploy", Namespace: "default"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	statuses, err := watcher.WatchRollouts(ctx, applied)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}

	got := statuses[0]
	if got.State != "complete" {
		t.Errorf("expected state=complete, got %q", got.State)
	}
}

// TestWatcher_NonDeploymentResource_Skipped verifies that resources other than
// Deployments are silently skipped — no status entry is created for them.
func TestWatcher_NonDeploymentResource_Skipped(t *testing.T) {
	s := testScheme(t)

	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	opts := watcherOpts(500*time.Millisecond, 50*time.Millisecond, 3)
	watcher := NewRolloutWatcher(fakeClient, opts)

	applied := []AppliedResource{
		{Kind: "ConfigMap", Name: "cfg", Namespace: "default"},
		{Kind: "Service", Name: "svc", Namespace: "default"},
	}

	statuses, err := watcher.WatchRollouts(context.Background(), applied)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(statuses) != 0 {
		t.Errorf("expected 0 statuses for non-Deployment resources, got %d", len(statuses))
	}
}

// TestWatcher_PodReadinessFails_ReachesFailureThreshold verifies that when pods
// are listed but none satisfy the readiness check, the watcher eventually
// transitions to State="failed" after exhausting the FailureThreshold while
// all replica counts report as equal (i.e. the health check is the bottleneck,
// not the replica count).
func TestWatcher_PodReadinessFails_ReachesFailureThreshold(t *testing.T) {
	s := testScheme(t)

	labels := map[string]string{"app": "crash"}

	// Deployment reports all replicas as ready so the replica-count gate passes
	// and the health check code path is exercised.
	deploy := buildDeployment("default", "crashloop-deploy", 1, 1, 1, 1, labels)

	// The single pod is Running but NOT Ready, simulating a failed readiness probe.
	unhealthyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "crash-pod",
			Namespace: "default",
			Labels:    labels,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				// PodReady is explicitly False.
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", RestartCount: 0},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(deploy, unhealthyPod).
		WithStatusSubresource(&appsv1.Deployment{}).
		Build()

	// Use a threshold of 2 so the test completes after two failed check cycles.
	opts := watcherOpts(5*time.Second, 40*time.Millisecond, 2)
	watcher := NewRolloutWatcher(fakeClient, opts)

	applied := []AppliedResource{
		{Kind: "Deployment", Name: "crashloop-deploy", Namespace: "default"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	statuses, err := watcher.WatchRollouts(ctx, applied)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}

	got := statuses[0]
	if got.State != "failed" {
		t.Errorf("expected state=failed when readiness probe fails consistently, got %q", got.State)
	}
}

// TestWatcher_EmptyApplied_ReturnsEmpty verifies that passing an empty applied
// slice returns an empty statuses slice without error.
func TestWatcher_EmptyApplied_ReturnsEmpty(t *testing.T) {
	s := testScheme(t)

	schemeForTest := s
	_ = clientgoscheme.AddToScheme(schemeForTest)

	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	opts := watcherOpts(500*time.Millisecond, 50*time.Millisecond, 3)
	watcher := NewRolloutWatcher(fakeClient, opts)

	statuses, err := watcher.WatchRollouts(context.Background(), []AppliedResource{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(statuses) != 0 {
		t.Errorf("expected 0 statuses, got %d", len(statuses))
	}
}
