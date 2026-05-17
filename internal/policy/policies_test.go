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
			name: "LoadBalancer with internal annotation value 0.0.0.0/0 is rejected (non-truthy)",
			obj: makeService("with-ann-cidr", "LoadBalancer", map[string]string{
				"service.beta.kubernetes.io/aws-load-balancer-internal": "0.0.0.0/0",
			}, nil),
			wantPassed: false,
		},
		{
			name: "LoadBalancer with internal annotation set to false is rejected",
			obj: makeService("with-ann-false", "LoadBalancer", map[string]string{
				"service.beta.kubernetes.io/aws-load-balancer-internal": "false",
			}, nil),
			wantPassed: false,
		},
		{
			name: "LoadBalancer with Azure internal annotation passes",
			obj: makeService("azure-ann", "LoadBalancer", map[string]string{
				"service.beta.kubernetes.io/azure-load-balancer-internal": "true",
			}, nil),
			wantPassed: true,
		},
		{
			name: "LoadBalancer with GKE internal load-balancer-type passes",
			obj: makeService("gke-ann", "LoadBalancer", map[string]string{
				"networking.gke.io/load-balancer-type": "Internal",
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
