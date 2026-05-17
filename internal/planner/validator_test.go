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
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// ---------------------------------------------------------------------------
// GetGVK (pure function, no client)
// ---------------------------------------------------------------------------

func TestGetGVK(t *testing.T) {
	tests := []struct {
		kind        string
		wantGroup   string
		wantVersion string
		wantKind    string
	}{
		{"Deployment", "apps", "v1", "Deployment"},
		{"Service", "", "v1", "Service"},
		{"HorizontalPodAutoscaler", "autoscaling", "v2", "HorizontalPodAutoscaler"},
		{"Ingress", "networking.k8s.io", "v1", "Ingress"},
		{"ConfigMap", "", "v1", "ConfigMap"},
		{"Secret", "", "v1", "Secret"},
		{"PersistentVolumeClaim", "", "v1", "PersistentVolumeClaim"},
		{"NetworkPolicy", "networking.k8s.io", "v1", "NetworkPolicy"},
	}

	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			got := GetGVK(tc.kind)
			if got.Group != tc.wantGroup {
				t.Errorf("Group = %q, want %q", got.Group, tc.wantGroup)
			}
			if got.Version != tc.wantVersion {
				t.Errorf("Version = %q, want %q", got.Version, tc.wantVersion)
			}
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.wantKind)
			}
		})
	}

	t.Run("unknown kind returns fallback with kind preserved", func(t *testing.T) {
		got := GetGVK("UnknownResource")
		if got.Kind != "UnknownResource" {
			t.Errorf("Kind = %q, want %q", got.Kind, "UnknownResource")
		}
		if got.Version != "v1" {
			t.Errorf("Version = %q, want v1", got.Version)
		}
	})
}

// ---------------------------------------------------------------------------
// ValidateYAML – schema pass and fail cases
// ---------------------------------------------------------------------------

// buildFakeScheme builds a minimal scheme with core and apps types so the
// fake client can recognise Deployment and ConfigMap objects.
func buildFakeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme(corev1): %v", err)
	}
	if err := appsv1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme(appsv1): %v", err)
	}
	return s
}

func TestValidateYAML_Pass(t *testing.T) {
	scheme := buildFakeScheme(t)
	ctx := context.Background()

	tests := []struct {
		name        string
		yaml        string
		existingObj runtime.Object // if non-nil, pre-populate fake client
		wantKind    string
		wantName    string
		wantIsNew   bool
	}{
		{
			name: "new ConfigMap is recognised as new resource",
			yaml: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-config
  namespace: default
data:
  key: value
`,
			wantKind:  "ConfigMap",
			wantName:  "my-config",
			wantIsNew: true,
		},
		{
			name: "existing ConfigMap is recognised as update",
			yaml: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: existing-config
  namespace: default
data:
  key: updated-value
`,
			existingObj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "existing-config",
					Namespace: "default",
				},
			},
			wantKind:  "ConfigMap",
			wantName:  "existing-config",
			wantIsNew: false,
		},
		{
			name: "new Deployment is recognised as new resource",
			yaml: `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-deploy
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: my-deploy
  template:
    metadata:
      labels:
        app: my-deploy
    spec:
      containers:
      - name: app
        image: nginx:latest
`,
			wantKind:  "Deployment",
			wantName:  "my-deploy",
			wantIsNew: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var fakeClient fake.ClientBuilder
			builder := fakeClient.WithScheme(scheme)
			if tc.existingObj != nil {
				builder = builder.WithRuntimeObjects(tc.existingObj)
			}
			c := builder.Build()

			v := NewManifestValidator(c)
			got, err := v.ValidateYAML(ctx, tc.yaml)
			if err != nil {
				t.Fatalf("ValidateYAML() unexpected error: %v", err)
			}
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
			if got.IsNew != tc.wantIsNew {
				t.Errorf("IsNew = %v, want %v", got.IsNew, tc.wantIsNew)
			}
			if got.Object == nil {
				t.Error("Object should not be nil after successful validation")
			}
			if got.YAML != tc.yaml {
				t.Error("YAML should be preserved verbatim")
			}
		})
	}
}

func TestValidateYAML_Fail(t *testing.T) {
	scheme := buildFakeScheme(t)
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	v := NewManifestValidator(c)

	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "empty string is invalid YAML",
			yaml:    "",
			wantErr: "invalid YAML",
		},
		{
			name:    "non-YAML garbage returns parse error",
			yaml:    "{{{{not yaml}}}}",
			wantErr: "invalid YAML",
		},
		{
			name: "YAML without kind field returns invalid YAML error",
			// The Unstructured decoder requires 'Kind' to be present.
			yaml: `
metadata:
  name: no-kind
  namespace: default
`,
			wantErr: "invalid YAML",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := v.ValidateYAML(ctx, tc.yaml)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				// error message should mention the cause
				return
			}
			// wantErr == "" means we expect success
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EnforceNamespace – namespace constraint enforcement
// ---------------------------------------------------------------------------

// makeManifest is a test helper that builds a minimal ValidatedManifest.
func makeManifest(kind, name, namespace string) *ValidatedManifest {
	return &ValidatedManifest{
		Kind:      kind,
		Name:      name,
		Namespace: namespace,
	}
}

// TestEnforceNamespace covers the core enforcement rules in isolation — no
// Kubernetes API calls are needed because EnforceNamespace is a pure function
// with respect to the client.
func TestEnforceNamespace(t *testing.T) {
	const target = "my-tenant"

	tests := []struct {
		name              string
		targetNamespace   string
		manifest          *ValidatedManifest
		allowCrossNS      bool   // passed via WithAllowCrossNamespace
		wantErrCount      int    // 0 means pass
		wantErrSubstring  string // checked against first error when wantErrCount > 0
	}{
		{
			name:            "matching namespace passes",
			targetNamespace: target,
			manifest:        makeManifest("ConfigMap", "my-config", target),
			wantErrCount:    0,
		},
		{
			name:             "non-matching namespace fails",
			targetNamespace:  target,
			manifest:         makeManifest("ConfigMap", "bad-config", "kube-system"),
			wantErrCount:     1,
			wantErrSubstring: "cross-namespace writes are not allowed",
		},
		{
			name:             "empty namespace fails",
			targetNamespace:  target,
			manifest:         makeManifest("ConfigMap", "no-ns", ""),
			wantErrCount:     1,
			wantErrSubstring: "empty metadata.namespace",
		},
		{
			name:             "ClusterRole is always rejected",
			targetNamespace:  target,
			manifest:         makeManifest("ClusterRole", "admin-role", ""),
			wantErrCount:     1,
			wantErrSubstring: "cluster-scoped resource",
		},
		{
			name:             "Namespace kind is always rejected",
			targetNamespace:  target,
			manifest:         makeManifest("Namespace", "evil-ns", ""),
			wantErrCount:     1,
			wantErrSubstring: "cluster-scoped resource",
		},
		{
			name:             "ClusterRoleBinding is always rejected",
			targetNamespace:  target,
			manifest:         makeManifest("ClusterRoleBinding", "crb", target),
			wantErrCount:     1,
			wantErrSubstring: "cluster-scoped resource",
		},
		{
			name:             "Node is always rejected",
			targetNamespace:  target,
			manifest:         makeManifest("Node", "worker-1", ""),
			wantErrCount:     1,
			wantErrSubstring: "cluster-scoped resource",
		},
		{
			name:             "PersistentVolume is always rejected",
			targetNamespace:  target,
			manifest:         makeManifest("PersistentVolume", "pv-data", ""),
			wantErrCount:     1,
			wantErrSubstring: "cluster-scoped resource",
		},
		{
			name:             "CustomResourceDefinition is always rejected",
			targetNamespace:  target,
			manifest:         makeManifest("CustomResourceDefinition", "my.crd.io", ""),
			wantErrCount:     1,
			wantErrSubstring: "cluster-scoped resource",
		},
		{
			name:            "AllowCrossNamespace bypasses check for mismatched namespace",
			targetNamespace: target,
			manifest:        makeManifest("ConfigMap", "cross", "other-tenant"),
			allowCrossNS:    true,
			wantErrCount:    0,
		},
		{
			name:            "AllowCrossNamespace bypasses check for empty namespace",
			targetNamespace: target,
			manifest:        makeManifest("Deployment", "cross-deploy", ""),
			allowCrossNS:    true,
			wantErrCount:    0,
		},
		{
			name:            "AllowCrossNamespace bypasses check for cluster-scoped kind",
			targetNamespace: target,
			manifest:        makeManifest("ClusterRole", "admin", ""),
			allowCrossNS:    true,
			wantErrCount:    0,
		},
		{
			name:            "Deployment with correct namespace passes",
			targetNamespace: "production",
			manifest:        makeManifest("Deployment", "api-server", "production"),
			wantErrCount:    0,
		},
		{
			name:             "Deployment targeting kube-system rejected",
			targetNamespace:  "staging",
			manifest:         makeManifest("Deployment", "kube-dns-override", "kube-system"),
			wantErrCount:     1,
			wantErrSubstring: "cross-namespace writes are not allowed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// We don't need a real client for EnforceNamespace; pass a nil-safe
			// fake built from an empty scheme.
			scheme := buildFakeScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).Build()

			opts := []ManifestValidatorOption{}
			if tc.allowCrossNS {
				opts = append(opts, WithAllowCrossNamespace(true))
			}
			v := NewManifestValidator(c, opts...)

			errs := v.EnforceNamespace(tc.targetNamespace, tc.manifest)

			if tc.wantErrCount == 0 {
				if len(errs) != 0 {
					t.Errorf("expected no errors, got %d: %v", len(errs), errs)
				}
				return
			}

			if len(errs) != tc.wantErrCount {
				t.Errorf("error count = %d, want %d; errors: %v", len(errs), tc.wantErrCount, errs)
				return
			}
			if tc.wantErrSubstring != "" && !strings.Contains(errs[0], tc.wantErrSubstring) {
				t.Errorf("error[0] = %q, want substring %q", errs[0], tc.wantErrSubstring)
			}
		})
	}
}

// TestEnforceNamespace_PreservesExistingValidatorBehavior verifies that adding
// namespace enforcement did not change the behaviour of ValidateYAML for valid
// and invalid inputs (regression guard for existing tests).
func TestEnforceNamespace_PreservesExistingValidatorBehavior(t *testing.T) {
	scheme := buildFakeScheme(t)
	ctx := context.Background()

	t.Run("ValidateYAML still rejects empty YAML after enforcement wiring", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		v := NewManifestValidator(c)
		_, err := v.ValidateYAML(ctx, "")
		if err == nil {
			t.Error("expected error for empty YAML, got nil")
		}
	})

	t.Run("ValidateYAML still rejects garbage YAML after enforcement wiring", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		v := NewManifestValidator(c)
		_, err := v.ValidateYAML(ctx, "{{{{not yaml}}}}")
		if err == nil {
			t.Error("expected error for malformed YAML, got nil")
		}
	})

	t.Run("ValidateYAML still accepts a valid ConfigMap manifest", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		v := NewManifestValidator(c)
		got, err := v.ValidateYAML(ctx, `apiVersion: v1
kind: ConfigMap
metadata:
  name: regression-check
  namespace: staging
data:
  ok: "true"
`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Kind != "ConfigMap" {
			t.Errorf("Kind = %q, want ConfigMap", got.Kind)
		}
		if got.Name != "regression-check" {
			t.Errorf("Name = %q, want regression-check", got.Name)
		}
	})
}
