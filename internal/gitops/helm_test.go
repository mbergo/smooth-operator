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

package gitops

import (
	"strings"
	"testing"
	"time"

	"github.com/mbergo/smooth-operator/internal/executor"
)

// makeResource is a small helper to build an AppliedResource for tests.
func makeResource(kind, name string) executor.AppliedResource {
	return executor.AppliedResource{
		Kind:      kind,
		Name:      name,
		Namespace: "default",
		Operation: "created",
		AppliedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

// makeResult wraps a slice of AppliedResource into an ExecutionResult so that
// GenerateChart can be called without a real cluster.
func makeResult(resources ...executor.AppliedResource) *executor.ExecutionResult {
	return &executor.ExecutionResult{
		Success:          true,
		AppliedResources: resources,
	}
}

// ---------------------------------------------------------------------------
// Chart.yaml
// ---------------------------------------------------------------------------

func TestGenerateChartYAML(t *testing.T) {
	t.Parallel()

	gen := NewHelmChartGenerator()
	yaml := gen.generateChartYAML("my-app")

	required := []string{
		"apiVersion: v2",
		"name: my-app",
		"type: application",
		"smooth.k8s.io/generated",
	}
	for _, want := range required {
		if !strings.Contains(yaml, want) {
			t.Errorf("Chart.yaml missing %q\nGot:\n%s", want, yaml)
		}
	}
}

// ---------------------------------------------------------------------------
// values.yaml
// ---------------------------------------------------------------------------

func TestGenerateValuesYAML(t *testing.T) {
	t.Parallel()

	gen := NewHelmChartGenerator()
	result := makeResult(makeResource("Deployment", "app"))
	yaml := gen.generateValuesYAML(result)

	required := []string{
		"replicaCount:",
		"image:",
		"service:",
		"autoscaling:",
		"resources:",
	}
	for _, want := range required {
		if !strings.Contains(yaml, want) {
			t.Errorf("values.yaml missing %q\nGot:\n%s", want, yaml)
		}
	}
}

// ---------------------------------------------------------------------------
// Per-kind template rendering
// ---------------------------------------------------------------------------

func TestGenerateTemplate_Deployment(t *testing.T) {
	t.Parallel()

	gen := NewHelmChartGenerator()
	out := gen.generateTemplate(makeResource("Deployment", "web"))

	checks := []string{
		"kind: Deployment",
		"{{ .Values.replicaCount }}",
		"{{ .Values.image.repository }}",
		"containerPort",
		"imagePullPolicy",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("Deployment template missing %q\nGot:\n%s", c, out)
		}
	}
}

func TestGenerateTemplate_Service(t *testing.T) {
	t.Parallel()

	gen := NewHelmChartGenerator()
	out := gen.generateTemplate(makeResource("Service", "svc"))

	checks := []string{
		"kind: Service",
		"{{ .Values.service.type }}",
		"{{ .Values.service.port }}",
		"selector:",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("Service template missing %q\nGot:\n%s", c, out)
		}
	}
}

func TestGenerateTemplate_HPA(t *testing.T) {
	t.Parallel()

	gen := NewHelmChartGenerator()

	for _, kind := range []string{"HorizontalPodAutoscaler", "HPA"} {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			out := gen.generateTemplate(makeResource(kind, "hpa-test"))

			checks := []string{
				"kind: HorizontalPodAutoscaler",
				"{{ .Values.autoscaling.minReplicas }}",
				"{{ .Values.autoscaling.maxReplicas }}",
				"scaleTargetRef",
			}
			for _, c := range checks {
				if !strings.Contains(out, c) {
					t.Errorf("HPA template (%s) missing %q\nGot:\n%s", kind, c, out)
				}
			}
		})
	}
}

func TestGenerateTemplate_Ingress(t *testing.T) {
	t.Parallel()

	gen := NewHelmChartGenerator()
	out := gen.generateTemplate(makeResource("Ingress", "web-ingress"))

	checks := []string{
		"kind: Ingress",
		".Values.ingress.enabled",
		"ingressClassName",
		"pathType",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("Ingress template missing %q\nGot:\n%s", c, out)
		}
	}
}

func TestGenerateTemplate_ConfigMap(t *testing.T) {
	t.Parallel()

	gen := NewHelmChartGenerator()
	out := gen.generateTemplate(makeResource("ConfigMap", "app-config"))

	checks := []string{
		"kind: ConfigMap",
		".Values.configMap.enabled",
		".Values.configMap.data",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("ConfigMap template missing %q\nGot:\n%s", c, out)
		}
	}
}

func TestGenerateTemplate_UnknownKind(t *testing.T) {
	t.Parallel()

	gen := NewHelmChartGenerator()
	out := gen.generateTemplate(makeResource("CronJob", "my-job"))

	if !strings.Contains(out, "kind: CronJob") {
		t.Errorf("generic template should include 'kind: CronJob'\nGot:\n%s", out)
	}
	if !strings.Contains(out, `include "chart.fullname"`) {
		t.Errorf("generic template should reference chart.fullname helper\nGot:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// GenerateChart integration – full chart structure
// ---------------------------------------------------------------------------

func TestGenerateChart_FileKeys(t *testing.T) {
	t.Parallel()

	gen := NewHelmChartGenerator()
	result := makeResult(
		makeResource("Deployment", "web"),
		makeResource("Service", "svc"),
		makeResource("HorizontalPodAutoscaler", "hpa"),
	)

	chart, err := gen.GenerateChart("my-app", "production", result)
	if err != nil {
		t.Fatalf("GenerateChart returned error: %v", err)
	}
	if chart.Name != "my-app" {
		t.Errorf("chart name: want my-app, got %q", chart.Name)
	}
	if chart.Namespace != "production" {
		t.Errorf("chart namespace: want production, got %q", chart.Namespace)
	}

	required := []string{
		"Chart.yaml",
		"values.yaml",
		"templates/NOTES.txt",
		"templates/deployment.yaml",
		"templates/service.yaml",
		"templates/horizontalpodautoscaler.yaml",
	}
	for _, key := range required {
		if _, ok := chart.Files[key]; !ok {
			t.Errorf("chart missing expected file %q; have: %v", key, fileKeys(chart.Files))
		}
	}
}

func fileKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
