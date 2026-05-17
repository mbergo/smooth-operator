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
	"strings"
	"testing"
)

// newTestDiffGenerator returns a DiffGenerator with a nil client,
// sufficient for all tests that only exercise pure-logic methods.
func newTestDiffGenerator() *DiffGenerator {
	return &DiffGenerator{client: nil}
}

// ---------------------------------------------------------------------------
// generateUnifiedDiff
// ---------------------------------------------------------------------------

func TestGenerateUnifiedDiff(t *testing.T) {
	d := newTestDiffGenerator()

	tests := []struct {
		name        string
		before      string
		after       string
		kind        string
		resourceName string
		wantInDiff  []string
		wantAbsent  []string
	}{
		{
			name:        "added field shows plus line",
			before:      "replicas: 1\n",
			after:       "replicas: 1\nnewField: value\n",
			kind:        "Deployment",
			resourceName: "myapp",
			wantInDiff:  []string{"+newField: value"},
		},
		{
			name:        "removed field shows minus line",
			before:      "replicas: 1\noldField: value\n",
			after:       "replicas: 1\n",
			kind:        "Deployment",
			resourceName: "myapp",
			wantInDiff:  []string{"-oldField: value"},
		},
		{
			name:        "modified field shows both minus and plus",
			before:      "replicas: 1\n",
			after:       "replicas: 3\n",
			kind:        "Deployment",
			resourceName: "myapp",
			wantInDiff:  []string{"-replicas: 1", "+replicas: 3"},
		},
		{
			name:        "identical content produces empty diff",
			before:      "replicas: 2\n",
			after:       "replicas: 2\n",
			kind:        "Deployment",
			resourceName: "myapp",
			wantInDiff:  nil, // empty string is fine
			wantAbsent:  []string{"-replicas", "+replicas"},
		},
		{
			name:        "diff header contains kind and name",
			before:      "",
			after:       "kind: Service\n",
			kind:        "Service",
			resourceName: "svc-frontend",
			wantInDiff:  []string{"Service", "svc-frontend"},
		},
		{
			name:        "create from empty before shows full content as additions",
			before:      "",
			after:       "apiVersion: apps/v1\nkind: Deployment\n",
			kind:        "Deployment",
			resourceName: "new-deploy",
			wantInDiff:  []string{"+apiVersion: apps/v1", "+kind: Deployment"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := d.generateUnifiedDiff(tc.before, tc.after, tc.kind, tc.resourceName)

			for _, want := range tc.wantInDiff {
				if !strings.Contains(result, want) {
					t.Errorf("expected diff to contain %q\ngot:\n%s", want, result)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(result, absent) {
					t.Errorf("expected diff NOT to contain %q\ngot:\n%s", absent, result)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// detectChangedFields
// ---------------------------------------------------------------------------

func TestDetectChangedFields(t *testing.T) {
	d := newTestDiffGenerator()

	tests := []struct {
		name          string
		before        map[string]interface{}
		after         map[string]interface{}
		wantFields    []string
		wantNotFields []string
	}{
		{
			name: "spec field added",
			before: map[string]interface{}{
				"spec": map[string]interface{}{"replicas": 1},
			},
			after: map[string]interface{}{
				"spec": map[string]interface{}{"replicas": 1, "newKey": "val"},
			},
			wantFields: []string{"spec.newKey"},
		},
		{
			name: "spec field removed",
			before: map[string]interface{}{
				"spec": map[string]interface{}{"replicas": 1, "oldKey": "bye"},
			},
			after: map[string]interface{}{
				"spec": map[string]interface{}{"replicas": 1},
			},
			wantFields: []string{"spec.oldKey (removed)"},
		},
		{
			name: "spec field modified",
			before: map[string]interface{}{
				"spec": map[string]interface{}{"replicas": 1},
			},
			after: map[string]interface{}{
				"spec": map[string]interface{}{"replicas": 3},
			},
			wantFields: []string{"spec.replicas"},
		},
		{
			name: "no spec change returns empty slice",
			before: map[string]interface{}{
				"spec": map[string]interface{}{"replicas": 2},
			},
			after: map[string]interface{}{
				"spec": map[string]interface{}{"replicas": 2},
			},
			wantFields:    []string{},
			wantNotFields: []string{"spec.replicas"},
		},
		{
			name: "metadata label added",
			before: map[string]interface{}{
				"metadata": map[string]interface{}{"labels": map[string]interface{}{"app": "foo"}},
			},
			after: map[string]interface{}{
				"metadata": map[string]interface{}{"labels": map[string]interface{}{"app": "foo"}, "env": "prod"},
			},
			wantFields: []string{"metadata.env"},
		},
		{
			name: "spec absent in before, present in after",
			before: map[string]interface{}{},
			after: map[string]interface{}{
				"spec": map[string]interface{}{"replicas": 1},
			},
			wantFields: []string{"spec"},
		},
		{
			name: "nested spec change detected",
			before: map[string]interface{}{
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"containers": "old",
					},
				},
			},
			after: map[string]interface{}{
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"containers": "new",
					},
				},
			},
			wantFields: []string{"spec.template.containers"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := d.detectChangedFields(tc.before, tc.after)

			gotSet := make(map[string]bool, len(got))
			for _, f := range got {
				gotSet[f] = true
			}

			for _, want := range tc.wantFields {
				if !gotSet[want] {
					t.Errorf("expected field %q in changed fields, got: %v", want, got)
				}
			}
			for _, notWant := range tc.wantNotFields {
				if gotSet[notWant] {
					t.Errorf("unexpected field %q in changed fields, got: %v", notWant, got)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FormatDiffForDisplay
// ---------------------------------------------------------------------------

func TestFormatDiffForDisplay(t *testing.T) {
	d := newTestDiffGenerator()

	tests := []struct {
		name    string
		diff    *ManifestDiff
		wantIn  []string
	}{
		{
			name: "create diff contains kind and name",
			diff: &ManifestDiff{
				Kind:          "Deployment",
				Name:          "myapp",
				ChangeType:    "create",
				Summary:       "Creating new Deployment: myapp",
				ChangedFields: []string{},
			},
			wantIn: []string{"create", "Deployment", "myapp", "Creating new"},
		},
		{
			name: "update diff with few changed fields lists each field",
			diff: &ManifestDiff{
				Kind:          "Service",
				Name:          "svc",
				ChangeType:    "update",
				Summary:       "Updating Service: svc (2 fields changed)",
				ChangedFields: []string{"spec.port", "spec.selector"},
			},
			wantIn: []string{"spec.port", "spec.selector", "Changed fields"},
		},
		{
			name: "update diff with more than 10 fields shows count instead",
			diff: &ManifestDiff{
				Kind:       "Deployment",
				Name:       "big",
				ChangeType: "update",
				Summary:    "Updating Deployment: big (11 fields changed)",
				ChangedFields: []string{
					"f1", "f2", "f3", "f4", "f5", "f6",
					"f7", "f8", "f9", "f10", "f11",
				},
			},
			wantIn: []string{"11 fields modified"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := d.FormatDiffForDisplay(tc.diff)
			for _, want := range tc.wantIn {
				if !strings.Contains(result, want) {
					t.Errorf("expected output to contain %q\ngot:\n%s", want, result)
				}
			}
		})
	}
}
