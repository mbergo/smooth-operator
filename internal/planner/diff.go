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
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"
)

// DiffGenerator generates diffs between current and proposed manifests
type DiffGenerator struct {
	client client.Client
}

// NewDiffGenerator creates a new diff generator
func NewDiffGenerator(client client.Client) *DiffGenerator {
	return &DiffGenerator{
		client: client,
	}
}

// GenerateDiff creates a diff for a validated manifest
func (d *DiffGenerator) GenerateDiff(ctx context.Context, manifest *ValidatedManifest) (*ManifestDiff, error) {
	log := log.FromContext(ctx)

	diff := &ManifestDiff{
		Kind:          manifest.Kind,
		Name:          manifest.Name,
		Namespace:     manifest.Namespace,
		ChangedFields: []string{},
	}

	if manifest.IsNew {
		// New resource - show creation diff
		diff.ChangeType = "create"
		diff.Before = ""
		diff.After = manifest.YAML
		diff.Summary = fmt.Sprintf("Creating new %s: %s", manifest.Kind, manifest.Name)
		diff.UnifiedDiff = d.generateUnifiedDiff("", manifest.YAML, manifest.Kind, manifest.Name)

		log.Info("Generated diff for new resource",
			"kind", manifest.Kind,
			"name", manifest.Name,
		)
		return diff, nil
	}

	// Existing resource - fetch current state and compare
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(manifest.Object.GroupVersionKind())

	err := d.client.Get(ctx, client.ObjectKey{
		Namespace: manifest.Namespace,
		Name:      manifest.Name,
	}, existing)

	if err != nil {
		return nil, fmt.Errorf("failed to fetch existing resource for diff: %w", err)
	}

	// Convert existing to YAML
	existingYAML, err := yaml.Marshal(existing)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal existing resource: %w", err)
	}

	diff.ChangeType = "update"
	diff.Before = string(existingYAML)
	diff.After = manifest.YAML
	diff.UnifiedDiff = d.generateUnifiedDiff(string(existingYAML), manifest.YAML, manifest.Kind, manifest.Name)

	// Detect changed fields
	diff.ChangedFields = d.detectChangedFields(existing.Object, manifest.Object.Object)
	diff.Summary = fmt.Sprintf("Updating %s: %s (%d fields changed)",
		manifest.Kind, manifest.Name, len(diff.ChangedFields))

	log.Info("Generated diff for existing resource",
		"kind", manifest.Kind,
		"name", manifest.Name,
		"changedFields", len(diff.ChangedFields),
	)

	return diff, nil
}

// generateUnifiedDiff creates a git-style unified diff
func (d *DiffGenerator) generateUnifiedDiff(before, after, kind, name string) string {
	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(before),
		B:        difflib.SplitLines(after),
		FromFile: fmt.Sprintf("current/%s/%s", kind, name),
		ToFile:   fmt.Sprintf("proposed/%s/%s", kind, name),
		Context:  3,
	}

	result, err := difflib.GetUnifiedDiffString(diff)
	if err != nil {
		return fmt.Sprintf("Error generating diff: %v", err)
	}

	return result
}

// detectChangedFields compares two objects and returns changed field paths
func (d *DiffGenerator) detectChangedFields(before, after map[string]interface{}) []string {
	changed := []string{}

	// Compare spec (most common changes)
	beforeSpec, beforeHasSpec := before["spec"].(map[string]interface{})
	afterSpec, afterHasSpec := after["spec"].(map[string]interface{})

	if beforeHasSpec && afterHasSpec {
		changed = append(changed, d.compareMap("spec", beforeSpec, afterSpec)...)
	} else if !beforeHasSpec && afterHasSpec {
		changed = append(changed, "spec")
	}

	// Compare metadata (labels, annotations)
	beforeMeta, beforeHasMeta := before["metadata"].(map[string]interface{})
	afterMeta, afterHasMeta := after["metadata"].(map[string]interface{})

	if beforeHasMeta && afterHasMeta {
		changed = append(changed, d.compareMap("metadata", beforeMeta, afterMeta)...)
	}

	return changed
}

// compareMap recursively compares two maps
func (d *DiffGenerator) compareMap(prefix string, before, after map[string]interface{}) []string {
	changed := []string{}

	// Check all keys in after
	for key, afterValue := range after {
		beforeValue, exists := before[key]

		if !exists {
			changed = append(changed, fmt.Sprintf("%s.%s", prefix, key))
			continue
		}

		// If both are maps, recurse
		afterMap, afterIsMap := afterValue.(map[string]interface{})
		beforeMap, beforeIsMap := beforeValue.(map[string]interface{})

		if afterIsMap && beforeIsMap {
			changed = append(changed, d.compareMap(fmt.Sprintf("%s.%s", prefix, key), beforeMap, afterMap)...)
		} else if afterValue != beforeValue {
			changed = append(changed, fmt.Sprintf("%s.%s", prefix, key))
		}
	}

	// Check for removed keys
	for key := range before {
		if _, exists := after[key]; !exists {
			changed = append(changed, fmt.Sprintf("%s.%s (removed)", prefix, key))
		}
	}

	return changed
}

// FormatDiffForDisplay creates a human-readable diff summary
func (d *DiffGenerator) FormatDiffForDisplay(diff *ManifestDiff) string {
	var builder strings.Builder

	builder.WriteString(fmt.Sprintf("📝 %s: %s/%s\n", diff.ChangeType, diff.Kind, diff.Name))
	builder.WriteString(fmt.Sprintf("   %s\n", diff.Summary))

	if len(diff.ChangedFields) > 0 && len(diff.ChangedFields) <= 10 {
		builder.WriteString("\n   Changed fields:\n")
		for _, field := range diff.ChangedFields {
			builder.WriteString(fmt.Sprintf("   - %s\n", field))
		}
	} else if len(diff.ChangedFields) > 10 {
		builder.WriteString(fmt.Sprintf("\n   Changed fields: %d fields modified\n", len(diff.ChangedFields)))
	}

	return builder.String()
}
