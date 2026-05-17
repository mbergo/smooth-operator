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
	"bytes"
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// clusterScopedKinds is the set of well-known Kubernetes kinds that are
// cluster-scoped (have no namespace). Manifests with these kinds are always
// rejected by the namespace enforcer because the operator only manages
// namespaced resources on behalf of a ChatSession's targetNamespace.
//
// This list is intentionally conservative. When a new cluster-scoped CRD is
// encountered it will NOT be in this set, so add it here if the operator must
// guard against it explicitly.
var clusterScopedKinds = map[string]struct{}{
	"ClusterRole":               {},
	"ClusterRoleBinding":        {},
	"Namespace":                 {},
	"Node":                      {},
	"PersistentVolume":          {},
	"StorageClass":              {},
	"IngressClass":              {},
	"PriorityClass":             {},
	"RuntimeClass":              {},
	"ValidatingWebhookConfiguration": {},
	"MutatingWebhookConfiguration":  {},
	"CustomResourceDefinition":  {},
	"APIService":                {},
	"CertificateSigningRequest": {},
	"FlowSchema":                {},
	"ClusterCIDR":               {},
}

// ManifestValidatorOption is a functional option for ManifestValidator.
type ManifestValidatorOption func(*ManifestValidator)

// WithAllowCrossNamespace enables or disables the cross-namespace guard.
//
// WARNING: Setting this to true allows manifests that target a namespace other
// than the ChatSession's targetNamespace to pass validation. Only use this in
// controlled multi-tenant setups where the operator's RBAC is scoped
// appropriately. The default is false (enforcement on), which is the safe
// default for single-tenant ChatSession deployments.
func WithAllowCrossNamespace(allow bool) ManifestValidatorOption {
	return func(v *ManifestValidator) {
		v.allowCrossNamespace = allow
	}
}

// ManifestValidator validates Kubernetes manifests
type ManifestValidator struct {
	client client.Client

	// allowCrossNamespace disables the namespace enforcement check when true.
	// See WithAllowCrossNamespace for risk documentation.
	allowCrossNamespace bool
}

// NewManifestValidator creates a new validator with optional functional options.
func NewManifestValidator(c client.Client, opts ...ManifestValidatorOption) *ManifestValidator {
	v := &ManifestValidator{
		client:              c,
		allowCrossNamespace: false,
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

// EnforceNamespace checks that manifest targets exactly targetNamespace and is
// not a cluster-scoped resource kind. It returns a (possibly empty) slice of
// human-readable error strings. An empty slice means the manifest passed.
//
// Rules enforced:
//  1. Cluster-scoped kinds (ClusterRole, Namespace, …) are always rejected —
//     the operator only emits namespaced resources.
//  2. metadata.namespace must equal targetNamespace exactly.
//  3. An empty metadata.namespace is rejected; the ChatSession always supplies
//     a concrete namespace so an empty value signals a malformed or injected
//     manifest.
//
// When ManifestValidator was created with WithAllowCrossNamespace(true) this
// method returns nil immediately. No auto-rewrite of the namespace is
// performed; the caller must surface the error and log it so the operator UI
// can present the rejection to the user.
func (v *ManifestValidator) EnforceNamespace(targetNamespace string, manifest *ValidatedManifest) []string {
	if v.allowCrossNamespace {
		return nil
	}

	var errs []string

	// Rule 1: cluster-scoped kinds are never allowed.
	normalised := strings.TrimSpace(manifest.Kind)
	if _, isClusterScoped := clusterScopedKinds[normalised]; isClusterScoped {
		errs = append(errs, fmt.Sprintf(
			"cluster-scoped resource %q (%s) is not allowed; the operator only manages namespaced resources",
			manifest.Name, manifest.Kind,
		))
		// Cluster-scoped resources have no namespace field by definition, so
		// there is nothing further to check — return early.
		return errs
	}

	// Rule 2 & 3: namespace must be non-empty and must match targetNamespace.
	ns := strings.TrimSpace(manifest.Namespace)
	switch {
	case ns == "":
		errs = append(errs, fmt.Sprintf(
			"manifest %q/%s has empty metadata.namespace; the operator requires an explicit namespace matching %q",
			manifest.Kind, manifest.Name, targetNamespace,
		))
	case ns != targetNamespace:
		errs = append(errs, fmt.Sprintf(
			"manifest %q/%s targets namespace %q but ChatSession targetNamespace is %q; cross-namespace writes are not allowed",
			manifest.Kind, manifest.Name, ns, targetNamespace,
		))
	}

	return errs
}

// ValidateYAML parses and validates a YAML manifest
func (v *ManifestValidator) ValidateYAML(ctx context.Context, yamlContent string) (*ValidatedManifest, error) {
	log := log.FromContext(ctx)

	// Parse YAML to unstructured object
	obj := &unstructured.Unstructured{}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(yamlContent)), 4096)
	if err := decoder.Decode(obj); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	// Extract basic metadata
	validated := &ValidatedManifest{
		Kind:      obj.GetKind(),
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		YAML:      yamlContent,
		Object:    obj,
	}

	// Check if resource exists
	gvk := obj.GroupVersionKind()
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(gvk)

	err := v.client.Get(ctx, client.ObjectKey{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}, existing)

	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Resource doesn't exist - this is a new creation
			validated.IsNew = true
			log.Info("Manifest validated (new resource)",
				"kind", validated.Kind,
				"name", validated.Name,
			)
		} else {
			return nil, fmt.Errorf("failed to check existing resource: %w", err)
		}
	} else {
		// Resource exists - this is an update
		validated.IsNew = false
		log.Info("Manifest validated (update existing)",
			"kind", validated.Kind,
			"name", validated.Name,
		)
	}

	return validated, nil
}

// PerformDryRun performs a server-side dry-run of the manifest
func (v *ManifestValidator) PerformDryRun(ctx context.Context, manifest *ValidatedManifest) (*DryRunResult, error) {
	log := log.FromContext(ctx)

	log.Info("Performing dry-run validation",
		"kind", manifest.Kind,
		"name", manifest.Name,
	)

	result := &DryRunResult{
		Success: false,
		Errors:  []string{},
	}

	// Attempt dry-run create or update
	obj := manifest.Object.DeepCopy()

	var err error
	if manifest.IsNew {
		err = v.client.Create(ctx, obj, client.DryRunAll)
	} else {
		err = v.client.Update(ctx, obj, client.DryRunAll)
	}

	if err != nil {
		result.Success = false
		result.Message = fmt.Sprintf("Dry-run failed: %v", err)
		result.Errors = append(result.Errors, err.Error())
		log.Error(err, "Dry-run validation failed",
			"kind", manifest.Kind,
			"name", manifest.Name,
		)
		return result, nil
	}

	result.Success = true
	result.Message = "Dry-run passed successfully"
	log.Info("Dry-run validation passed",
		"kind", manifest.Kind,
		"name", manifest.Name,
	)

	return result, nil
}

// GetGVK returns the GroupVersionKind for a resource type
func GetGVK(kind string) schema.GroupVersionKind {
	// Common Kubernetes resource mappings
	kindMap := map[string]schema.GroupVersionKind{
		"Deployment": {
			Group:   "apps",
			Version: "v1",
			Kind:    "Deployment",
		},
		"Service": {
			Group:   "",
			Version: "v1",
			Kind:    "Service",
		},
		"HorizontalPodAutoscaler": {
			Group:   "autoscaling",
			Version: "v2",
			Kind:    "HorizontalPodAutoscaler",
		},
		"Ingress": {
			Group:   "networking.k8s.io",
			Version: "v1",
			Kind:    "Ingress",
		},
		"ConfigMap": {
			Group:   "",
			Version: "v1",
			Kind:    "ConfigMap",
		},
		"Secret": {
			Group:   "",
			Version: "v1",
			Kind:    "Secret",
		},
		"PersistentVolumeClaim": {
			Group:   "",
			Version: "v1",
			Kind:    "PersistentVolumeClaim",
		},
		"NetworkPolicy": {
			Group:   "networking.k8s.io",
			Version: "v1",
			Kind:    "NetworkPolicy",
		},
	}

	if gvk, ok := kindMap[kind]; ok {
		return gvk
	}

	// Default fallback
	return schema.GroupVersionKind{
		Group:   "",
		Version: "v1",
		Kind:    kind,
	}
}
