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

// makeDeployment builds an unstructured Deployment with the given spec fields merged in.
func makeDeployment(name string, spec map[string]interface{}) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": "default",
			},
			"spec": spec,
		},
	}
	return obj
}

// makeService builds an unstructured Service.
func makeService(name string, serviceType string, annotations map[string]string, extraSpec map[string]interface{}) *unstructured.Unstructured {
	annotationsIface := map[string]interface{}{}
	for k, v := range annotations {
		annotationsIface[k] = v
	}

	spec := map[string]interface{}{
		"type": serviceType,
	}
	for k, v := range extraSpec {
		spec[k] = v
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata": map[string]interface{}{
				"name":        name,
				"namespace":   "default",
				"annotations": annotationsIface,
			},
			"spec": spec,
		},
	}
	return obj
}

// fullCompliantDeploymentSpec returns a spec that satisfies every blocking policy.
func fullCompliantDeploymentSpec() map[string]interface{} {
	return map[string]interface{}{
		"template": map[string]interface{}{
			"spec": map[string]interface{}{
				"securityContext": map[string]interface{}{
					"runAsNonRoot": true,
				},
				"volumes": []interface{}{},
				"containers": []interface{}{
					map[string]interface{}{
						"name":  "app",
						"image": "docker.io/library/nginx:latest",
						"resources": map[string]interface{}{
							"requests": map[string]interface{}{
								"cpu":    "100m",
								"memory": "128Mi",
							},
							"limits": map[string]interface{}{
								"cpu":    "500m",
								"memory": "512Mi",
							},
						},
						"readinessProbe": map[string]interface{}{
							"httpGet": map[string]interface{}{
								"path": "/ready",
								"port": int64(8080),
							},
						},
						"livenessProbe": map[string]interface{}{
							"httpGet": map[string]interface{}{
								"path": "/live",
								"port": int64(8080),
							},
						},
						"securityContext": map[string]interface{}{
							"privileged": false,
						},
					},
				},
			},
		},
	}
}

func TestEngine_EvaluateManifest(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()

	tests := []struct {
		name           string
		obj            *unstructured.Unstructured
		kind           string
		resourceName   string
		namespace      string
		wantPassed     bool
		wantViolations []string // policy names that must appear in violations
	}{
		{
			name: "deployment without runAsNonRoot is denied",
			obj: makeDeployment("no-nonroot", map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "app",
								"image": "docker.io/library/nginx:latest",
							},
						},
					},
				},
			}),
			kind:         "Deployment",
			resourceName: "no-nonroot",
			namespace:    "default",
			wantPassed:   false,
			wantViolations: []string{
				"run-as-non-root",
			},
		},
		{
			name: "deployment with runAsNonRoot false is denied",
			obj: makeDeployment("nonroot-false", map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"securityContext": map[string]interface{}{
							"runAsNonRoot": false,
						},
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "app",
								"image": "docker.io/library/nginx:latest",
							},
						},
					},
				},
			}),
			kind:         "Deployment",
			resourceName: "nonroot-false",
			namespace:    "default",
			wantPassed:   false,
			wantViolations: []string{
				"run-as-non-root",
			},
		},
		{
			name: "LoadBalancer Service without internal annotation is denied",
			obj: makeService("public-lb", "LoadBalancer",
				map[string]string{},
				map[string]interface{}{
					"ports": []interface{}{
						map[string]interface{}{
							"port":     int64(80),
							"protocol": "TCP",
						},
					},
				},
			),
			kind:         "Service",
			resourceName: "public-lb",
			namespace:    "default",
			wantPassed:   false,
			wantViolations: []string{
				"loadbalancer-internal-annotation",
			},
		},
		{
			name: "LoadBalancer Service with internal annotation passes policy",
			obj: makeService("internal-lb", "LoadBalancer",
				map[string]string{
					"service.beta.kubernetes.io/aws-load-balancer-internal": "true",
				},
				nil,
			),
			kind:         "Service",
			resourceName: "internal-lb",
			namespace:    "default",
			wantPassed:   true,
			wantViolations: []string{},
		},
		{
			name: "ClusterIP Service passes LoadBalancer annotation policy",
			obj: makeService("clusterip-svc", "ClusterIP",
				map[string]string{},
				nil,
			),
			kind:         "Service",
			resourceName: "clusterip-svc",
			namespace:    "default",
			wantPassed:   true,
			wantViolations: []string{},
		},
		{
			name: "deployment with disallowed image registry is denied",
			obj: makeDeployment("bad-registry", map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"securityContext": map[string]interface{}{
							"runAsNonRoot": true,
						},
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "app",
								"image": "privateregistry.example.com/myapp:latest",
							},
						},
					},
				},
			}),
			kind:         "Deployment",
			resourceName: "bad-registry",
			namespace:    "default",
			wantPassed:   false,
			wantViolations: []string{
				"allowed-image-registries",
			},
		},
		{
			name: "deployment with gcr.io image passes image registry policy",
			obj: makeDeployment("gcr-image", func() map[string]interface{} {
				spec := fullCompliantDeploymentSpec()
				containers := spec["template"].(map[string]interface{})["spec"].(map[string]interface{})["containers"].([]interface{})
				containers[0].(map[string]interface{})["image"] = "gcr.io/myproject/myapp:v1"
				return spec
			}()),
			kind:         "Deployment",
			resourceName: "gcr-image",
			namespace:    "default",
			wantPassed:   true,
			wantViolations: []string{},
		},
		{
			name: "deployment with hostPath volume is denied",
			obj: makeDeployment("hostpath-vol", map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"securityContext": map[string]interface{}{
							"runAsNonRoot": true,
						},
						"volumes": []interface{}{
							map[string]interface{}{
								"name": "host-vol",
								"hostPath": map[string]interface{}{
									"path": "/var/run/docker.sock",
								},
							},
						},
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "app",
								"image": "docker.io/library/nginx:latest",
							},
						},
					},
				},
			}),
			kind:         "Deployment",
			resourceName: "hostpath-vol",
			namespace:    "default",
			wantPassed:   false,
			wantViolations: []string{
				"no-host-path",
			},
		},
		{
			name:         "well-formed compliant deployment passes all blocking policies",
			obj:          makeDeployment("compliant", fullCompliantDeploymentSpec()),
			kind:         "Deployment",
			resourceName: "compliant",
			namespace:    "default",
			wantPassed:   true,
			wantViolations: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := engine.EvaluateManifest(ctx, tc.obj, tc.kind, tc.resourceName, tc.namespace)
			if err != nil {
				t.Fatalf("EvaluateManifest returned unexpected error: %v", err)
			}

			if result.Passed != tc.wantPassed {
				t.Errorf("Passed = %v, want %v; violations: %+v", result.Passed, tc.wantPassed, result.Violations)
			}

			// Verify that every expected violation policy name is present.
			for _, wantPolicy := range tc.wantViolations {
				found := false
				for _, v := range result.Violations {
					if v.Policy == wantPolicy {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected violation from policy %q not found; got violations: %+v", wantPolicy, result.Violations)
				}
			}

			// Verify no unexpected blocking violations caused a failure when we expect it to pass.
			if tc.wantPassed && len(result.Violations) > 0 {
				for _, v := range result.Violations {
					if v.Severity == "blocking" {
						t.Errorf("unexpected blocking violation from policy %q: %s", v.Policy, v.Message)
					}
				}
			}
		})
	}
}

func TestEngine_AddPolicy(t *testing.T) {
	ctx := context.Background()
	engine := &Engine{}

	called := false
	engine.AddPolicy(&stubPolicy{
		name:     "stub",
		severity: "blocking",
		evalFunc: func(_ context.Context, _ *unstructured.Unstructured) *PolicyResult {
			called = true
			return &PolicyResult{Passed: true, PolicyName: "stub"}
		},
	})

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"kind": "Pod",
			"metadata": map[string]interface{}{
				"name": "test",
			},
		},
	}

	if _, err := engine.EvaluateManifest(ctx, obj, "Pod", "test", "default"); err != nil {
		t.Fatalf("EvaluateManifest error: %v", err)
	}
	if !called {
		t.Error("custom policy Evaluate was not called")
	}
}

func TestEngine_EvaluatedPoliciesCount(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"kind": "ConfigMap",
			"metadata": map[string]interface{}{
				"name": "cfg",
			},
		},
	}

	result, err := engine.EvaluateManifest(ctx, obj, "ConfigMap", "cfg", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.EvaluatedPolicies != len(engine.policies) {
		t.Errorf("EvaluatedPolicies = %d, want %d", result.EvaluatedPolicies, len(engine.policies))
	}
}

// stubPolicy is a test-only Policy implementation.
type stubPolicy struct {
	name     string
	severity string
	evalFunc func(ctx context.Context, obj *unstructured.Unstructured) *PolicyResult
}

func (s *stubPolicy) Name() string     { return s.name }
func (s *stubPolicy) Severity() string { return s.severity }
func (s *stubPolicy) Evaluate(ctx context.Context, obj *unstructured.Unstructured) *PolicyResult {
	return s.evalFunc(ctx, obj)
}
