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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// --------------------------------------------------------------------------
// RunAsNonRootPolicy
// --------------------------------------------------------------------------

func TestRunAsNonRootPolicy(t *testing.T) {
	ctx := context.Background()
	p := &RunAsNonRootPolicy{}

	tests := []struct {
		name       string
		obj        *unstructured.Unstructured
		wantPassed bool
	}{
		{
			name: "Deployment without securityContext is denied",
			obj: makeDeployment("no-sc", map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{"name": "app", "image": "nginx"},
						},
					},
				},
			}),
			wantPassed: false,
		},
		{
			name: "Deployment with runAsNonRoot false is denied",
			obj: makeDeployment("false-sc", map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"securityContext": map[string]interface{}{"runAsNonRoot": false},
						"containers": []interface{}{
							map[string]interface{}{"name": "app", "image": "nginx"},
						},
					},
				},
			}),
			wantPassed: false,
		},
		{
			name: "Deployment with runAsNonRoot true passes",
			obj: makeDeployment("good-sc", map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"securityContext": map[string]interface{}{"runAsNonRoot": true},
						"containers": []interface{}{
							map[string]interface{}{"name": "app", "image": "nginx"},
						},
					},
				},
			}),
			wantPassed: true,
		},
		{
			name: "non-Deployment kind is always allowed",
			obj: &unstructured.Unstructured{Object: map[string]interface{}{
				"kind":     "Service",
				"metadata": map[string]interface{}{"name": "svc"},
			}},
			wantPassed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := p.Evaluate(ctx, tc.obj)
			if result.Passed != tc.wantPassed {
				t.Errorf("Passed = %v, want %v; message: %q", result.Passed, tc.wantPassed, result.Message)
			}
			if !result.Passed && result.Severity != "blocking" {
				t.Errorf("expected blocking severity, got %q", result.Severity)
			}
		})
	}
}

// --------------------------------------------------------------------------
// LoadBalancerInternalAnnotationPolicy
// --------------------------------------------------------------------------

func TestLoadBalancerInternalAnnotationPolicy(t *testing.T) {
	ctx := context.Background()
	p := &LoadBalancerInternalAnnotationPolicy{}

	tests := []struct {
		name       string
		obj        *unstructured.Unstructured
		wantPassed bool
	}{
		{
			name:       "LoadBalancer without annotation is denied",
			obj:        makeService("no-ann", "LoadBalancer", map[string]string{}, nil),
			wantPassed: false,
		},
		{
			name: "LoadBalancer with internal annotation passes",
			obj: makeService("with-ann", "LoadBalancer", map[string]string{
				"service.beta.kubernetes.io/aws-load-balancer-internal": "true",
			}, nil),
			wantPassed: true,
		},
		{
			name: "LoadBalancer with internal annotation value 0.0.0.0/0 passes",
			obj: makeService("with-ann-cidr", "LoadBalancer", map[string]string{
				"service.beta.kubernetes.io/aws-load-balancer-internal": "0.0.0.0/0",
			}, nil),
			wantPassed: true,
		},
		{
			name:       "ClusterIP Service is not evaluated (passes)",
			obj:        makeService("cip", "ClusterIP", map[string]string{}, nil),
			wantPassed: true,
		},
		{
			name:       "NodePort Service is not evaluated (passes)",
			obj:        makeService("np", "NodePort", map[string]string{}, nil),
			wantPassed: true,
		},
		{
			name: "Non-Service kind is ignored (passes)",
			obj: &unstructured.Unstructured{Object: map[string]interface{}{
				"kind":     "Deployment",
				"metadata": map[string]interface{}{"name": "dep"},
			}},
			wantPassed: true,
		},
		{
			name: "LoadBalancer with unrelated annotation is denied",
			obj: makeService("wrong-ann", "LoadBalancer", map[string]string{
				"app": "myapp",
			}, nil),
			wantPassed: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := p.Evaluate(ctx, tc.obj)
			if result.Passed != tc.wantPassed {
				t.Errorf("Passed = %v, want %v; message: %q", result.Passed, tc.wantPassed, result.Message)
			}
			if !result.Passed && result.Severity != "blocking" {
				t.Errorf("expected blocking severity on denial, got %q", result.Severity)
			}
		})
	}
}

// --------------------------------------------------------------------------
// ImageRegistryPolicy
// --------------------------------------------------------------------------

func TestImageRegistryPolicy(t *testing.T) {
	ctx := context.Background()
	p := &ImageRegistryPolicy{}

	baseSpec := func(image string) map[string]interface{} {
		return map[string]interface{}{
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{"name": "app", "image": image},
					},
				},
			},
		}
	}

	tests := []struct {
		name       string
		image      string
		wantPassed bool
	}{
		{"docker.io image passes", "docker.io/library/nginx:latest", true},
		{"gcr.io image passes", "gcr.io/myproject/app:v1", true},
		{"quay.io image passes", "quay.io/org/app:v1", true},
		{"ghcr.io image passes", "ghcr.io/owner/app:v1", true},
		{"registry.k8s.io image passes", "registry.k8s.io/pause:3.6", true},
		{"k8s.gcr.io image passes", "k8s.gcr.io/pause:3.6", true},
		{"bare image name passes (no registry prefix)", "nginx:latest", true},
		{"private registry is denied", "privateregistry.example.com/myapp:latest", false},
		{"unknown corporate registry is denied", "artifactory.corp.internal/myapp:v2", false},
		{"ECR registry is denied when no rule", "123456789.dkr.ecr.us-east-1.amazonaws.com/myapp:latest", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := p.Evaluate(ctx, makeDeployment("dep", baseSpec(tc.image)))
			if result.Passed != tc.wantPassed {
				t.Errorf("image %q: Passed = %v, want %v; message: %q", tc.image, result.Passed, tc.wantPassed, result.Message)
			}
		})
	}
}

// --------------------------------------------------------------------------
// HostPathPolicy
// --------------------------------------------------------------------------

func TestHostPathPolicy(t *testing.T) {
	ctx := context.Background()
	p := &HostPathPolicy{}

	tests := []struct {
		name       string
		obj        *unstructured.Unstructured
		wantPassed bool
	}{
		{
			name: "Deployment with hostPath volume is denied",
			obj: makeDeployment("with-hp", map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"volumes": []interface{}{
							map[string]interface{}{
								"name": "docker-sock",
								"hostPath": map[string]interface{}{
									"path": "/var/run/docker.sock",
								},
							},
						},
						"containers": []interface{}{
							map[string]interface{}{"name": "app", "image": "nginx"},
						},
					},
				},
			}),
			wantPassed: false,
		},
		{
			name: "Deployment with emptyDir volume passes",
			obj: makeDeployment("empty-dir", map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"volumes": []interface{}{
							map[string]interface{}{
								"name":     "tmp",
								"emptyDir": map[string]interface{}{},
							},
						},
						"containers": []interface{}{
							map[string]interface{}{"name": "app", "image": "nginx"},
						},
					},
				},
			}),
			wantPassed: true,
		},
		{
			name: "Deployment with no volumes passes",
			obj: makeDeployment("no-vols", map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{"name": "app", "image": "nginx"},
						},
					},
				},
			}),
			wantPassed: true,
		},
		{
			name: "non-Deployment kind always passes",
			obj: &unstructured.Unstructured{Object: map[string]interface{}{
				"kind":     "ConfigMap",
				"metadata": map[string]interface{}{"name": "cfg"},
			}},
			wantPassed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := p.Evaluate(ctx, tc.obj)
			if result.Passed != tc.wantPassed {
				t.Errorf("Passed = %v, want %v; message: %q", result.Passed, tc.wantPassed, result.Message)
			}
		})
	}
}

// --------------------------------------------------------------------------
// Well-formed manifest end-to-end via individual policies
// --------------------------------------------------------------------------

func TestWellFormedManifestPassesAllBlockingPolicies(t *testing.T) {
	ctx := context.Background()
	obj := makeDeployment("compliant", fullCompliantDeploymentSpec())

	blockingPolicies := []Policy{
		&RunAsNonRootPolicy{},
		&ImageRegistryPolicy{},
		&HostPathPolicy{},
		&PrivilegedContainerPolicy{},
	}

	for _, p := range blockingPolicies {
		t.Run(p.Name(), func(t *testing.T) {
			result := p.Evaluate(ctx, obj)
			if !result.Passed {
				t.Errorf("policy %q unexpectedly denied the manifest: %s", p.Name(), result.Message)
			}
		})
	}
}

// --------------------------------------------------------------------------
// AutoModeAllowed – risk threshold gate
// --------------------------------------------------------------------------

func TestAutoModeAllowed(t *testing.T) {
	const threshold = 0.7

	tests := []struct {
		name          string
		risk          string
		confidence    float64
		minConfidence float64
		wantAllowed   bool
	}{
		// Allowed cases: risk in {low, med} AND confidence >= 0.7
		{
			name:          "low risk high confidence is allowed",
			risk:          "low",
			confidence:    0.9,
			minConfidence: threshold,
			wantAllowed:   true,
		},
		{
			name:          "low risk exactly at threshold is allowed",
			risk:          "low",
			confidence:    0.7,
			minConfidence: threshold,
			wantAllowed:   true,
		},
		{
			name:          "med risk high confidence is allowed",
			risk:          "med",
			confidence:    0.85,
			minConfidence: threshold,
			wantAllowed:   true,
		},
		{
			name:          "med risk exactly at threshold is allowed",
			risk:          "med",
			confidence:    0.7,
			minConfidence: threshold,
			wantAllowed:   true,
		},
		// Denied by risk level
		{
			name:          "high risk high confidence is denied",
			risk:          "high",
			confidence:    0.95,
			minConfidence: threshold,
			wantAllowed:   false,
		},
		{
			name:          "high risk with confidence at threshold is denied",
			risk:          "high",
			confidence:    0.7,
			minConfidence: threshold,
			wantAllowed:   false,
		},
		{
			name:          "unknown risk string is denied",
			risk:          "unknown",
			confidence:    1.0,
			minConfidence: threshold,
			wantAllowed:   false,
		},
		// Denied by confidence
		{
			name:          "low risk below threshold is denied",
			risk:          "low",
			confidence:    0.69,
			minConfidence: threshold,
			wantAllowed:   false,
		},
		{
			name:          "med risk below threshold is denied",
			risk:          "med",
			confidence:    0.5,
			minConfidence: threshold,
			wantAllowed:   false,
		},
		{
			name:          "low risk zero confidence is denied",
			risk:          "low",
			confidence:    0.0,
			minConfidence: threshold,
			wantAllowed:   false,
		},
		// Default threshold (-1 means use DefaultMinConfidence)
		{
			name:          "default threshold used when minConfidence is negative",
			risk:          "low",
			confidence:    DefaultMinConfidence,
			minConfidence: -1,
			wantAllowed:   true,
		},
		{
			name:          "default threshold: med risk just below default is denied",
			risk:          "med",
			confidence:    DefaultMinConfidence - 0.01,
			minConfidence: -1,
			wantAllowed:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AutoModeAllowed(tc.risk, tc.confidence, tc.minConfidence)
			if got != tc.wantAllowed {
				t.Errorf("AutoModeAllowed(risk=%q, confidence=%.2f, min=%.2f) = %v, want %v",
					tc.risk, tc.confidence, tc.minConfidence, got, tc.wantAllowed)
			}
		})
	}
}

// --------------------------------------------------------------------------
// KindAllowlistPolicy
// --------------------------------------------------------------------------

// makeObjOfKind returns a minimal unstructured object whose Kind field is set.
func makeObjOfKind(kind string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       kind,
			"metadata": map[string]interface{}{
				"name":      "test-" + kind,
				"namespace": "default",
			},
		},
	}
}

func TestKindAllowlistPolicy_AllowedKinds(t *testing.T) {
	ctx := context.Background()
	p := &KindAllowlistPolicy{}

	allowed := []string{
		"Deployment",
		"Service",
		"HorizontalPodAutoscaler",
		"Ingress",
		"ConfigMap",
		"NetworkPolicy",
		"PersistentVolumeClaim",
		"ServiceAccount",
		"Role",
		"RoleBinding",
	}

	for _, kind := range allowed {
		kind := kind // capture
		t.Run(kind+"_is_allowed", func(t *testing.T) {
			t.Parallel()
			result := p.Evaluate(ctx, makeObjOfKind(kind))
			if !result.Passed {
				t.Errorf("kind %q should be allowed but was denied: %s", kind, result.Message)
			}
		})
	}
}

func TestKindAllowlistPolicy_ExplicitlyDeniedKinds(t *testing.T) {
	ctx := context.Background()
	p := &KindAllowlistPolicy{}

	tests := []struct {
		kind            string
		wantMsgContains string
	}{
		{
			kind:            "ClusterRole",
			wantMsgContains: "explicitly denied",
		},
		{
			kind:            "ClusterRoleBinding",
			wantMsgContains: "explicitly denied",
		},
		{
			kind:            "Namespace",
			wantMsgContains: "explicitly denied",
		},
		{
			kind:            "MutatingWebhookConfiguration",
			wantMsgContains: "explicitly denied",
		},
		{
			kind:            "ValidatingWebhookConfiguration",
			wantMsgContains: "explicitly denied",
		},
		{
			kind:            "CustomResourceDefinition",
			wantMsgContains: "explicitly denied",
		},
		{
			kind:            "APIService",
			wantMsgContains: "explicitly denied",
		},
		{
			kind:            "PriorityClass",
			wantMsgContains: "explicitly denied",
		},
		{
			kind:            "StorageClass",
			wantMsgContains: "explicitly denied",
		},
	}

	for _, tc := range tests {
		tc := tc // capture
		t.Run(tc.kind+"_is_denied", func(t *testing.T) {
			t.Parallel()
			result := p.Evaluate(ctx, makeObjOfKind(tc.kind))
			if result.Passed {
				t.Errorf("kind %q should be blocked but was allowed", tc.kind)
			}
			if result.Severity != "blocking" {
				t.Errorf("kind %q: want severity blocking, got %q", tc.kind, result.Severity)
			}
			if !strings.Contains(result.Message, tc.wantMsgContains) {
				t.Errorf("kind %q: message %q does not contain %q", tc.kind, result.Message, tc.wantMsgContains)
			}
		})
	}
}

func TestKindAllowlistPolicy_UnknownKindIsFailClosed(t *testing.T) {
	ctx := context.Background()
	p := &KindAllowlistPolicy{}

	unknownKinds := []string{"Foo", "Bar", "CronJob", "DaemonSet", "StatefulSet", "Job"}

	for _, kind := range unknownKinds {
		kind := kind // capture
		t.Run(kind+"_is_denied_fail_closed", func(t *testing.T) {
			t.Parallel()
			result := p.Evaluate(ctx, makeObjOfKind(kind))
			if result.Passed {
				t.Errorf("unknown kind %q should be fail-closed (denied) but was allowed", kind)
			}
			if result.Severity != "blocking" {
				t.Errorf("unknown kind %q: want severity blocking, got %q", kind, result.Severity)
			}
			if !strings.Contains(result.Message, "not on the allowlist") {
				t.Errorf("unknown kind %q: message %q should contain \"not on the allowlist\"", kind, result.Message)
			}
		})
	}
}

func TestNewKindAllowlistPolicy_ExtraAllowedUnlocksKind(t *testing.T) {
	ctx := context.Background()

	extraKind := "CronJob"
	p := NewKindAllowlistPolicy([]string{extraKind}, nil)

	t.Run("extra_allowed_kind_passes", func(t *testing.T) {
		result := p.Evaluate(ctx, makeObjOfKind(extraKind))
		if !result.Passed {
			t.Errorf("kind %q added via extraAllowed should pass, got: %s", extraKind, result.Message)
		}
	})

	t.Run("baseline_allowed_kinds_still_pass", func(t *testing.T) {
		result := p.Evaluate(ctx, makeObjOfKind("Deployment"))
		if !result.Passed {
			t.Errorf("baseline kind Deployment should still pass after extraAllowed extension: %s", result.Message)
		}
	})

	t.Run("baseline_denied_kinds_still_blocked", func(t *testing.T) {
		result := p.Evaluate(ctx, makeObjOfKind("ClusterRole"))
		if result.Passed {
			t.Errorf("baseline denied kind ClusterRole should still be blocked after extraAllowed extension")
		}
	})

	t.Run("unknown_kinds_still_fail_closed", func(t *testing.T) {
		result := p.Evaluate(ctx, makeObjOfKind("Foo"))
		if result.Passed {
			t.Errorf("unknown kind Foo should still be fail-closed even after extraAllowed extension")
		}
	})
}

func TestNewKindAllowlistPolicy_ExtraDenied(t *testing.T) {
	ctx := context.Background()

	// Add "DaemonSet" to the denylist (it is not in baseline allowed, so it would
	// normally produce a "not on the allowlist" message; with extraDenied it
	// should instead produce the "explicitly denied" message).
	p := NewKindAllowlistPolicy(nil, []string{"DaemonSet"})

	result := p.Evaluate(ctx, makeObjOfKind("DaemonSet"))
	if result.Passed {
		t.Fatalf("DaemonSet added via extraDenied should be blocked")
	}
	if !strings.Contains(result.Message, "explicitly denied") {
		t.Errorf("DaemonSet in extraDenied: message %q should contain \"explicitly denied\"", result.Message)
	}
}

// --------------------------------------------------------------------------
// Engine integration: KindAllowlistPolicy blocks high-risk Kinds end-to-end
// --------------------------------------------------------------------------

func TestEngine_KindAllowlistBlocksDeniedKinds(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()

	deniedKinds := []string{
		"ClusterRole",
		"ClusterRoleBinding",
		"Namespace",
		"MutatingWebhookConfiguration",
		"ValidatingWebhookConfiguration",
		"CustomResourceDefinition",
	}

	for _, kind := range deniedKinds {
		kind := kind // capture
		t.Run("engine_blocks_"+kind, func(t *testing.T) {
			t.Parallel()
			obj := makeObjOfKind(kind)
			result, err := engine.EvaluateManifest(ctx, obj, kind, "test-"+kind, "default")
			if err != nil {
				t.Fatalf("EvaluateManifest error: %v", err)
			}
			if result.Passed {
				t.Errorf("engine should have blocked kind %q but result.Passed=true", kind)
			}

			// Confirm the blocking violation comes from the allowlist policy.
			found := false
			for _, v := range result.Violations {
				if v.Policy == "kind-allowlist" && v.Severity == "blocking" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected a blocking violation from policy \"kind-allowlist\" for kind %q; got: %+v", kind, result.Violations)
			}
		})
	}
}

// --------------------------------------------------------------------------
// Configurable severity – default "warning", WithSeverity, NewStrictEngine,
// Engine.SetSeverity round-trip
// --------------------------------------------------------------------------

// TestConfigurablePolicies_DefaultSeverityIsWarning verifies that the zero
// value of each configurable policy reports "warning", preserving backward
// compatibility for callers that already construct these structs directly.
func TestConfigurablePolicies_DefaultSeverityIsWarning(t *testing.T) {
	policies := []Policy{
		&ResourceLimitsPolicy{},
		&ReadinessProbePolicy{},
		&LivenessProbePolicy{},
	}
	for _, p := range policies {
		p := p // capture
		t.Run(p.Name(), func(t *testing.T) {
			if got := p.Severity(); got != "warning" {
				t.Errorf("zero-value %T.Severity() = %q, want \"warning\"", p, got)
			}
		})
	}
}

// TestConfigurablePolicies_WithSeverityFlipsToBlocking verifies that
// WithSeverity("blocking") causes Severity() to return "blocking" and that
// a failing evaluation emits a result with severity "blocking".
func TestConfigurablePolicies_WithSeverityFlipsToBlocking(t *testing.T) {
	ctx := context.Background()

	// A Deployment that omits resource limits, readiness probe, and liveness probe.
	noLimitsDeployment := makeDeployment("no-limits", map[string]interface{}{
		"template": map[string]interface{}{
			"spec": map[string]interface{}{
				"containers": []interface{}{
					map[string]interface{}{"name": "app", "image": "docker.io/library/nginx:latest"},
				},
			},
		},
	})

	tests := []struct {
		name   string
		policy Policy
	}{
		{
			name:   "ResourceLimitsPolicy",
			policy: new(ResourceLimitsPolicy).WithSeverity("blocking"),
		},
		{
			name:   "ReadinessProbePolicy",
			policy: new(ReadinessProbePolicy).WithSeverity("blocking"),
		},
		{
			name:   "LivenessProbePolicy",
			policy: new(LivenessProbePolicy).WithSeverity("blocking"),
		},
	}

	for _, tc := range tests {
		tc := tc // capture
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.Severity(); got != "blocking" {
				t.Errorf("WithSeverity(\"blocking\"): Severity() = %q, want \"blocking\"", got)
			}

			result := tc.policy.Evaluate(ctx, noLimitsDeployment)
			if result.Passed {
				t.Fatal("expected policy to fail on deployment without limits/probes, but it passed")
			}
			if result.Severity != "blocking" {
				t.Errorf("PolicyResult.Severity = %q, want \"blocking\"", result.Severity)
			}
		})
	}
}

// TestNewStrictEngine_BlocksMissingLimits verifies that NewStrictEngine
// promotes the three configurable policies to "blocking", so a Deployment
// missing resource limits yields Passed=false from EvaluateManifest.
func TestNewStrictEngine_BlocksMissingLimits(t *testing.T) {
	ctx := context.Background()
	engine := NewStrictEngine()

	noLimitsDeployment := makeDeployment("no-limits-strict", map[string]interface{}{
		"template": map[string]interface{}{
			"spec": map[string]interface{}{
				"securityContext": map[string]interface{}{"runAsNonRoot": true},
				"containers": []interface{}{
					map[string]interface{}{
						"name":  "app",
						"image": "docker.io/library/nginx:latest",
						// intentionally omit resources, readinessProbe, livenessProbe
					},
				},
			},
		},
	})

	result, err := engine.EvaluateManifest(ctx, noLimitsDeployment, "Deployment", "no-limits-strict", "default")
	if err != nil {
		t.Fatalf("EvaluateManifest returned unexpected error: %v", err)
	}

	if result.Passed {
		t.Error("NewStrictEngine: EvaluateManifest should have failed for a deployment missing resource limits, but Passed=true")
	}

	found := false
	for _, v := range result.Violations {
		if v.Policy == "resource-limits-required" && v.Severity == "blocking" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a blocking violation from \"resource-limits-required\"; got violations: %+v", result.Violations)
	}
}

// TestNewStrictEngine_CompliantDeploymentPasses confirms that a fully
// compliant deployment still passes the strict engine, ruling out false
// positives introduced by the severity promotion.
func TestNewStrictEngine_CompliantDeploymentPasses(t *testing.T) {
	ctx := context.Background()
	engine := NewStrictEngine()

	result, err := engine.EvaluateManifest(
		ctx,
		makeDeployment("compliant-strict", fullCompliantDeploymentSpec()),
		"Deployment", "compliant-strict", "default",
	)
	if err != nil {
		t.Fatalf("EvaluateManifest returned unexpected error: %v", err)
	}
	if !result.Passed {
		t.Errorf("NewStrictEngine: compliant deployment should pass; violations: %+v", result.Violations)
	}
}

// TestEngine_SetSeverity_RoundTrip verifies the runtime mutation path used by
// the operator.strictPolicies knob: start with a default engine (warnings),
// call SetSeverity to promote to blocking, confirm the change, then demote
// back and confirm again.
func TestEngine_SetSeverity_RoundTrip(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()

	noLimitsDeployment := makeDeployment("no-limits-rt", map[string]interface{}{
		"template": map[string]interface{}{
			"spec": map[string]interface{}{
				"securityContext": map[string]interface{}{"runAsNonRoot": true},
				"containers": []interface{}{
					map[string]interface{}{
						"name":  "app",
						"image": "docker.io/library/nginx:latest",
					},
				},
			},
		},
	})

	// Phase 1: default engine must not block on missing resource limits.
	resultDefault, err := engine.EvaluateManifest(ctx, noLimitsDeployment, "Deployment", "no-limits-rt", "default")
	if err != nil {
		t.Fatalf("phase 1: unexpected error: %v", err)
	}
	for _, v := range resultDefault.Violations {
		if v.Policy == "resource-limits-required" && v.Severity == "blocking" {
			t.Errorf("phase 1: default engine emitted blocking violation from resource-limits-required")
		}
	}

	// Phase 2: promote resource-limits-required to blocking.
	if ok := engine.SetSeverity("resource-limits-required", "blocking"); !ok {
		t.Fatal("SetSeverity returned false for a registered policy")
	}

	resultStrict, err := engine.EvaluateManifest(ctx, noLimitsDeployment, "Deployment", "no-limits-rt", "default")
	if err != nil {
		t.Fatalf("phase 2: unexpected error: %v", err)
	}
	if resultStrict.Passed {
		t.Error("phase 2: engine should have blocked after SetSeverity(blocking)")
	}
	foundBlocking := false
	for _, v := range resultStrict.Violations {
		if v.Policy == "resource-limits-required" && v.Severity == "blocking" {
			foundBlocking = true
		}
	}
	if !foundBlocking {
		t.Errorf("phase 2: expected blocking violation from resource-limits-required; got %+v", resultStrict.Violations)
	}

	// Phase 3: demote back to warning – engine must pass again.
	if ok := engine.SetSeverity("resource-limits-required", "warning"); !ok {
		t.Fatal("SetSeverity returned false on demotion")
	}

	resultDemoted, err := engine.EvaluateManifest(ctx, noLimitsDeployment, "Deployment", "no-limits-rt", "default")
	if err != nil {
		t.Fatalf("phase 3: unexpected error: %v", err)
	}
	if !resultDemoted.Passed {
		t.Errorf("phase 3: engine should pass after demotion to warning; violations: %+v", resultDemoted.Violations)
	}
}

// TestEngine_SetSeverity_UnknownPolicyReturnsFalse confirms that calling
// SetSeverity with an unregistered policy name returns false.
func TestEngine_SetSeverity_UnknownPolicyReturnsFalse(t *testing.T) {
	engine := NewEngine()
	if ok := engine.SetSeverity("nonexistent-policy", "blocking"); ok {
		t.Error("SetSeverity should return false for an unregistered policy name")
	}
}

// TestNewEngine_DefaultEngineWarningPoliciesDoNotBlock verifies that
// NewEngine's three configurable policies emit warnings (not blocking) so
// that existing callers whose manifests lack probes/limits are not disrupted.
func TestNewEngine_DefaultEngineWarningPoliciesDoNotBlock(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()

	// Satisfies all blocking policies but omits limits and probes.
	noProbesDeployment := makeDeployment("no-probes", map[string]interface{}{
		"template": map[string]interface{}{
			"spec": map[string]interface{}{
				"securityContext": map[string]interface{}{"runAsNonRoot": true},
				"containers": []interface{}{
					map[string]interface{}{
						"name":  "app",
						"image": "docker.io/library/nginx:latest",
					},
				},
			},
		},
	})

	result, err := engine.EvaluateManifest(ctx, noProbesDeployment, "Deployment", "no-probes", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, v := range result.Violations {
		if v.Severity == "blocking" {
			t.Errorf("default engine should not block on missing probes/limits; blocking violation from %q: %s", v.Policy, v.Message)
		}
	}

	if !result.Passed {
		t.Errorf("default engine must return Passed=true when only warning-level policies fire; violations: %+v", result.Violations)
	}
}
