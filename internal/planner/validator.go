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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ManifestValidator validates Kubernetes manifests
type ManifestValidator struct {
	client client.Client
}

// NewManifestValidator creates a new validator
func NewManifestValidator(c client.Client) *ManifestValidator {
	return &ManifestValidator{
		client: c,
	}
}

// ValidateYAML parses and validates a YAML manifest
func (v *ManifestValidator) ValidateYAML(ctx context.Context, yamlContent string) (*ValidatedManifest, error) {
	logger := log.FromContext(ctx)

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
			logger.Info("Manifest validated (new resource)",
				"kind", validated.Kind,
				"name", validated.Name,
			)
		} else {
			return nil, fmt.Errorf("failed to check existing resource: %w", err)
		}
	} else {
		// Resource exists - this is an update
		validated.IsNew = false
		logger.Info("Manifest validated (update existing)",
			"kind", validated.Kind,
			"name", validated.Name,
		)
	}

	return validated, nil
}

// PerformDryRun performs a server-side dry-run of the manifest
func (v *ManifestValidator) PerformDryRun(ctx context.Context, manifest *ValidatedManifest) (*DryRunResult, error) {
	logger := log.FromContext(ctx)

	logger.Info("Performing dry-run validation",
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
		logger.Error(err, "Dry-run validation failed",
			"kind", manifest.Kind,
			"name", manifest.Name,
		)
		return result, nil
	}

	result.Success = true
	result.Message = "Dry-run passed successfully"
	logger.Info("Dry-run validation passed",
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
