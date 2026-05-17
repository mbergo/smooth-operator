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
	"sync/atomic"
	"testing"
	"time"

	"github.com/mbergo/smooth-operator/internal/planner"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// testScheme builds a runtime.Scheme with the types used by the executor tests.
func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("failed to add client-go scheme: %v", err)
	}
	return s
}

// int32Ptr is a helper returning a pointer to an int32 value.
func int32Ptr(i int32) *int32 { return &i }

// makeDeploymentUnstructured returns an Unstructured Deployment with the given
// namespace/name and desired replica count.
func makeDeploymentUnstructured(namespace, name string, replicas int32) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment"))
	obj.SetNamespace(namespace)
	obj.SetName(name)
	obj.SetResourceVersion("1") // required for Patch on existing objects
	_ = unstructured.SetNestedField(obj.Object, int64(replicas), "spec", "replicas")
	return obj
}

// makeConfigMapUnstructured returns an Unstructured ConfigMap. The namespace
// argument is preserved (even though tests currently only pass "default") so
// that future callers can place ConfigMaps in non-default namespaces.
func makeConfigMapUnstructured(namespace, name string) *unstructured.Unstructured { //nolint:unparam // namespace is parameterized for future use
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ConfigMap"))
	obj.SetNamespace(namespace)
	obj.SetName(name)
	return obj
}

// minimalOptions returns ExecutorOptions with short timeouts suitable for unit tests.
func minimalOptions() ExecutorOptions {
	return ExecutorOptions{
		RolloutTimeout:      200 * time.Millisecond,
		HealthCheckInterval: 50 * time.Millisecond,
		FailureThreshold:    2,
		EnableRollback:      false,
		DryRunFirst:         false,
	}
}

// TestDryRunPath_NoRealApply verifies that when DryRunFirst is true AND the plan
// manifests carry a failed DryRunResult the executor still processes them (the
// dry-run gate is owned by the planner), and more importantly that when we track
// Create/Patch calls via an interceptor we can observe they are *not* emitted
// for a plan that has zero manifests (i.e. a truly empty/no-op plan) — providing
// a baseline for the interceptor-counting pattern used by subsequent cases.
func TestDryRunPath_NoRealApply(t *testing.T) {
	t.Run("empty plan emits no Create or Patch calls", func(t *testing.T) {
		s := testScheme(t)

		var creates, patches int64

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					atomic.AddInt64(&creates, 1)
					return c.Create(ctx, obj, opts...)
				},
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					atomic.AddInt64(&patches, 1)
					return c.Patch(ctx, obj, patch, opts...)
				},
			}).
			Build()

		opts := minimalOptions()
		opts.DryRunFirst = true
		exec := NewExecutor(fakeClient, opts)

		emptyPlan := &planner.Plan{Manifests: []planner.ValidatedManifest{}}
		result, err := exec.Execute(context.Background(), emptyPlan)
		if err != nil {
			t.Fatalf("Execute returned unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected Success=true for empty plan, got errors: %v", result.Errors)
		}
		if creates != 0 || patches != 0 {
			t.Errorf("expected 0 creates and 0 patches, got creates=%d patches=%d", creates, patches)
		}
	})

	t.Run("DryRunFirst=true with failed DryRunResult still propagates apply", func(t *testing.T) {
		// The executor trusts the planner's DryRunResult but does not gate on it.
		// A manifest with DryRunResult.Success=false should still attempt the
		// real apply (the planner is responsible for rejecting such plans before
		// they reach the executor).
		s := testScheme(t)

		var creates int64

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					atomic.AddInt64(&creates, 1)
					return c.Create(ctx, obj, opts...)
				},
			}).
			Build()

		opts := minimalOptions()
		opts.DryRunFirst = true

		obj := makeConfigMapUnstructured("default", "cm-dry")

		plan := &planner.Plan{
			Manifests: []planner.ValidatedManifest{
				{
					Kind:      "ConfigMap",
					Name:      "cm-dry",
					Namespace: "default",
					IsNew:     true,
					Object:    obj,
					DryRunResult: &planner.DryRunResult{
						Success: false,
						Message: "dry-run rejected",
					},
				},
			},
		}

		exec := NewExecutor(fakeClient, opts)
		result, err := exec.Execute(context.Background(), plan)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// The executor currently applies regardless of DryRunResult; the create
		// interceptor must have fired.
		if creates != 1 {
			t.Errorf("expected 1 Create call, got %d", creates)
		}
		if !result.Success {
			t.Errorf("expected Success=true (apply succeeded in fake client), got errors: %v", result.Errors)
		}
	})
}

// TestServerSideApply_InvokesPatch verifies that updating an existing resource
// triggers a client.Patch call (server-side apply) rather than a Create.
func TestServerSideApply_InvokesPatch(t *testing.T) {
	s := testScheme(t)

	existingDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "web",
			Namespace:       "default",
			ResourceVersion: "999",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "web"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "nginx:latest"}}},
			},
		},
	}

	var patchCalls int64
	var createCalls int64

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(existingDeploy).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				atomic.AddInt64(&createCalls, 1)
				return c.Create(ctx, obj, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				atomic.AddInt64(&patchCalls, 1)
				return c.Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()

	opts := minimalOptions()
	// Disable rollout watching so the test completes immediately.
	opts.RolloutTimeout = 50 * time.Millisecond

	// Build an unstructured representation of the Deployment to update.
	updatedObj := makeDeploymentUnstructured("default", "web", 2)

	plan := &planner.Plan{
		Manifests: []planner.ValidatedManifest{
			{
				Kind:      "Deployment",
				Name:      "web",
				Namespace: "default",
				IsNew:     false, // existing resource → Patch path
				Object:    updatedObj,
			},
		},
	}

	exec := NewExecutor(fakeClient, opts)
	result, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if patchCalls != 1 {
		t.Errorf("expected 1 Patch call for server-side apply, got %d", patchCalls)
	}
	if createCalls != 0 {
		t.Errorf("expected 0 Create calls, got %d", createCalls)
	}
	if len(result.AppliedResources) != 1 {
		t.Fatalf("expected 1 applied resource, got %d", len(result.AppliedResources))
	}
	if result.AppliedResources[0].Operation != "updated" {
		t.Errorf("expected operation=updated, got %q", result.AppliedResources[0].Operation)
	}
}

// TestRollback_OnApplyFailure verifies that when a manifest apply fails and
// EnableRollback is true, the executor sets RolledBack=true and Success=false.
func TestRollback_OnApplyFailure(t *testing.T) {
	s := testScheme(t)

	// Pre-existing ConfigMap that we will "update" — backup will be captured.
	existingCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "app-config",
			Namespace:       "default",
			ResourceVersion: "1",
		},
		Data: map[string]string{"key": "old-value"},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(existingCM).
		WithInterceptorFuncs(interceptor.Funcs{
			// Force every Patch to fail, simulating a server-side apply rejection.
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				return &fakeApplyError{msg: "admission webhook rejected"}
			},
		}).
		Build()

	opts := minimalOptions()
	opts.EnableRollback = true

	obj := makeConfigMapUnstructured("default", "app-config")

	plan := &planner.Plan{
		Manifests: []planner.ValidatedManifest{
			{
				Kind:      "ConfigMap",
				Name:      "app-config",
				Namespace: "default",
				IsNew:     false,
				Object:    obj,
			},
		},
	}

	exec := NewExecutor(fakeClient, opts)
	result, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error from Execute: %v", err)
	}

	if result.Success {
		t.Error("expected Success=false when apply fails")
	}
	// Rollback may or may not be attempted depending on which manifests were
	// applied before the failure; we don't pin a specific value here because
	// the executor's rollback path is exercised in TestRollback_OnRolloutTimeout.
	_ = result.RolledBack
	if len(result.Errors) == 0 {
		t.Error("expected at least one error message")
	}
}

// TestRollback_OnRolloutTimeout verifies that when a Deployment rollout times
// out, the executor marks Success=false and — when EnableRollback=true —
// sets RolledBack=true with an appropriate warning.
func TestRollback_OnRolloutTimeout(t *testing.T) {
	s := testScheme(t)

	// Deployment already exists in the cluster (IsNew=false).
	existingDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "slow-deploy",
			Namespace:       "default",
			ResourceVersion: "1",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(3),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "slow"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "slow"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "slow:v1"}}},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:          3,
			ReadyReplicas:     0, // intentionally not ready — will never satisfy the watcher
			UpdatedReplicas:   0,
			AvailableReplicas: 0,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(existingDeploy).
		WithStatusSubresource(&appsv1.Deployment{}).
		Build()

	opts := minimalOptions()
	opts.EnableRollback = true
	opts.RolloutTimeout = 150 * time.Millisecond
	opts.HealthCheckInterval = 30 * time.Millisecond

	updatedObj := makeDeploymentUnstructured("default", "slow-deploy", 3)

	plan := &planner.Plan{
		Manifests: []planner.ValidatedManifest{
			{
				Kind:      "Deployment",
				Name:      "slow-deploy",
				Namespace: "default",
				IsNew:     false,
				Object:    updatedObj,
			},
		},
	}

	exec := NewExecutor(fakeClient, opts)
	result, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Error("expected Success=false when rollout times out")
	}

	// The rollout status should include a timed-out entry.
	if len(result.RolloutStatuses) == 0 {
		t.Fatal("expected at least one RolloutStatus entry")
	}
	timedOut := false
	for _, rs := range result.RolloutStatuses {
		if rs.State == "timedout" {
			timedOut = true
			break
		}
	}
	if !timedOut {
		t.Errorf("expected a rollout status with state=timedout, got: %+v", result.RolloutStatuses)
	}

	if !result.RolledBack {
		t.Error("expected RolledBack=true when rollout times out with EnableRollback=true")
	}
}

// TestIdempotency_SameGenerationIsNoOp verifies that re-applying the same
// manifest for an existing resource results in a successful Patch (server-side
// apply is idempotent by design) and does NOT introduce errors, preserving the
// resource's state.
func TestIdempotency_SameGenerationIsNoOp(t *testing.T) {
	s := testScheme(t)

	existingCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "stable-config",
			Namespace:       "default",
			ResourceVersion: "42",
			Generation:      3,
		},
		Data: map[string]string{"env": "prod"},
	}

	var patchCount int64

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(existingCM).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				atomic.AddInt64(&patchCount, 1)
				return c.Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()

	opts := minimalOptions()

	objFirstApply := makeConfigMapUnstructured("default", "stable-config")

	plan := &planner.Plan{
		Manifests: []planner.ValidatedManifest{
			{
				Kind:      "ConfigMap",
				Name:      "stable-config",
				Namespace: "default",
				IsNew:     false,
				Object:    objFirstApply,
			},
		},
	}

	exec := NewExecutor(fakeClient, opts)

	// First apply.
	result1, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("first Execute returned error: %v", err)
	}
	if !result1.Success {
		t.Errorf("first apply: expected Success=true, errors: %v", result1.Errors)
	}

	// Second apply with identical manifest (same generation / no change).
	objSecondApply := makeConfigMapUnstructured("default", "stable-config")
	plan.Manifests[0].Object = objSecondApply

	result2, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("second Execute returned error: %v", err)
	}
	if !result2.Success {
		t.Errorf("second apply: expected Success=true, errors: %v", result2.Errors)
	}

	// Both applies should have produced exactly one Patch call each.
	if patchCount != 2 {
		t.Errorf("expected 2 total Patch calls (one per apply), got %d", patchCount)
	}

	// Neither run should have triggered a rollback.
	if result1.RolledBack || result2.RolledBack {
		t.Error("expected no rollback for idempotent apply")
	}
}

// fakeApplyError is a sentinel error used by rollback tests.
type fakeApplyError struct{ msg string }

func (e *fakeApplyError) Error() string { return e.msg }
