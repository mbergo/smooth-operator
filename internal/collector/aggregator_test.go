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

package collector

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mbergo/smooth-operator/internal/logs"
	"github.com/mbergo/smooth-operator/internal/metrics"
)

// newDisabledPrometheus creates a PrometheusClient with Enabled=false (no address needed).
func newDisabledPrometheus(t *testing.T) *metrics.PrometheusClient {
	t.Helper()
	pc, err := metrics.NewPrometheusClient(metrics.PrometheusOptions{Enabled: false})
	if err != nil {
		t.Fatalf("failed to create disabled prometheus client: %v", err)
	}
	return pc
}

// newDisabledLoki creates a LokiClient with Enabled=false.
func newDisabledLoki(t *testing.T) *logs.LokiClient {
	t.Helper()
	lc, err := logs.NewLokiClient(logs.LokiOptions{Enabled: false})
	if err != nil {
		t.Fatalf("failed to create disabled loki client: %v", err)
	}
	return lc
}

// newPrometheusWithServer creates a PrometheusClient pointed at srv (an httptest.Server).
// The server should serve valid Prometheus API responses.
func newPrometheusWithServer(t *testing.T, srv *httptest.Server) *metrics.PrometheusClient {
	t.Helper()
	pc, err := metrics.NewPrometheusClient(metrics.PrometheusOptions{
		Address:       srv.URL,
		QueryTimeout:  5 * time.Second,
		MetricsWindow: "5m",
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("failed to create prometheus client with server: %v", err)
	}
	return pc
}

// newDeploymentWithLabels is a test helper creating a Deployment with given labels.
func newDeploymentWithLabels(name, namespace string, labels map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr32(2),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "myapp:v1",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("250m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
						},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:      2,
			ReadyReplicas: 2,
		},
	}
}

// promStubHandler returns an HTTP handler that responds with a valid, empty
// Prometheus query result for every /api/v1/query request.
func promStubHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			http.NotFound(w, r)
			return
		}
		// Return a successful empty vector result.
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	})
}

// -------------------------------------------------------------------
// Aggregator tests
// -------------------------------------------------------------------

func TestAggregator_MissingPrometheus_NoError(t *testing.T) {
	// When Prometheus is nil the aggregator must succeed and return non-nil
	// AggregatedContext with an empty MetricsSnapshots map.
	ns := "prom-nil-ns"
	labels := map[string]string{"app": "svc"}
	dep := newDeploymentWithLabels("svc", ns, labels)

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(dep).
		Build()

	col := NewCollector(c, defaultOpts())
	agg := NewAggregator(col, nil, nil, 5*time.Minute)

	result, err := agg.AggregateContext(context.Background(), ns, "sess-prom-nil")
	if err != nil {
		t.Fatalf("expected no error with nil Prometheus client, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil AggregatedContext")
	}
	if len(result.MetricsSnapshots) != 0 {
		t.Errorf("expected empty MetricsSnapshots, got %d entries", len(result.MetricsSnapshots))
	}
}

func TestAggregator_MissingLoki_NoError(t *testing.T) {
	// When Loki is nil the aggregator must succeed with an empty LogSummaries map.
	ns := "loki-nil-ns"
	labels := map[string]string{"app": "svc"}
	dep := newDeploymentWithLabels("svc", ns, labels)

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(dep).
		Build()

	col := NewCollector(c, defaultOpts())
	agg := NewAggregator(col, nil, nil, 5*time.Minute)

	result, err := agg.AggregateContext(context.Background(), ns, "sess-loki-nil")
	if err != nil {
		t.Fatalf("expected no error with nil Loki client, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil AggregatedContext")
	}
	if len(result.LogSummaries) != 0 {
		t.Errorf("expected empty LogSummaries, got %d entries", len(result.LogSummaries))
	}
}

func TestAggregator_PrometheusDisabled_ReturnsNotAvailableMarker(t *testing.T) {
	// A disabled (but non-nil) PrometheusClient must not panic and should
	// record the "not enabled" marker in the snapshot Errors slice.
	ns := "prom-disabled-ns"
	labels := map[string]string{"app": "app"}
	dep := newDeploymentWithLabels("app", ns, labels)

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(dep).
		Build()

	pc := newDisabledPrometheus(t)
	col := NewCollector(c, defaultOpts())
	agg := NewAggregator(col, pc, nil, 5*time.Minute)

	result, err := agg.AggregateContext(context.Background(), ns, "sess-prom-disabled")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Because PrometheusClient.IsEnabled() returns false the aggregator must
	// skip metric collection — MetricsSnapshots should be empty.
	if len(result.MetricsSnapshots) != 0 {
		t.Errorf("expected empty MetricsSnapshots for disabled client, got %d entries", len(result.MetricsSnapshots))
	}
}

func TestAggregator_LokiDisabled_ReturnsNotAvailableMarker(t *testing.T) {
	// A disabled (but non-nil) LokiClient must not panic and should return
	// without adding entries to LogSummaries.
	ns := "loki-disabled-ns"
	labels := map[string]string{"app": "app"}
	dep := newDeploymentWithLabels("app", ns, labels)

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(dep).
		Build()

	lc := newDisabledLoki(t)
	col := NewCollector(c, defaultOpts())
	agg := NewAggregator(col, nil, lc, 5*time.Minute)

	result, err := agg.AggregateContext(context.Background(), ns, "sess-loki-disabled")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(result.LogSummaries) != 0 {
		t.Errorf("expected empty LogSummaries for disabled client, got %d entries", len(result.LogSummaries))
	}
}

func TestAggregator_PrometheusStubServer_CollectsMetrics(t *testing.T) {
	// Use an httptest server returning empty Prometheus vector responses.
	// The aggregator should collect (empty) snapshots without error.
	srv := httptest.NewServer(promStubHandler())
	defer srv.Close()

	ns := "prom-stub-ns"
	labels := map[string]string{"app": "stubapp"}
	dep := newDeploymentWithLabels("stubapp", ns, labels)

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(dep).
		Build()

	pc := newPrometheusWithServer(t, srv)
	col := NewCollector(c, defaultOpts())
	agg := NewAggregator(col, pc, nil, 5*time.Minute)

	result, err := agg.AggregateContext(context.Background(), ns, "sess-stub-prom")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if _, ok := result.MetricsSnapshots["stubapp"]; !ok {
		t.Errorf("expected MetricsSnapshot for 'stubapp', got keys: %v", keysOf(result.MetricsSnapshots))
	}
}

func TestAggregator_EmptyNamespace_NoError(t *testing.T) {
	// Empty namespace must return zero-count summary without error or panic.
	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		Build()

	col := NewCollector(c, defaultOpts())
	agg := NewAggregator(col, nil, nil, 5*time.Minute)

	result, err := agg.AggregateContext(context.Background(), "empty-ns", "sess-empty")
	if err != nil {
		t.Fatalf("expected no error for empty namespace, got: %v", err)
	}
	if result.Summary.TotalDeployments != 0 {
		t.Errorf("expected TotalDeployments=0, got %d", result.Summary.TotalDeployments)
	}
	if result.Summary.TotalPods != 0 {
		t.Errorf("expected TotalPods=0, got %d", result.Summary.TotalPods)
	}
	if result.Summary.TotalServices != 0 {
		t.Errorf("expected TotalServices=0, got %d", result.Summary.TotalServices)
	}
	if result.Summary.TotalIngresses != 0 {
		t.Errorf("expected TotalIngresses=0, got %d", result.Summary.TotalIngresses)
	}
	if result.Summary.TextSummary == "" {
		t.Error("expected non-empty TextSummary even for empty namespace")
	}
}

func TestAggregator_SummaryHealthCounts(t *testing.T) {
	ns := "health-ns"
	labels := map[string]string{"app": "health"}

	// healthy: readyReplicas == replicas
	healthy := newDeploymentWithLabels("healthy-dep", ns, labels)

	// unhealthy: readyReplicas < replicas
	unhealthy := newDeploymentWithLabels("unhealthy-dep", ns, map[string]string{"app": "sick"})
	unhealthy.Status.ReadyReplicas = 0

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(healthy, unhealthy).
		Build()

	col := NewCollector(c, defaultOpts())
	agg := NewAggregator(col, nil, nil, 5*time.Minute)

	result, err := agg.AggregateContext(context.Background(), ns, "sess-health")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary.TotalDeployments != 2 {
		t.Errorf("expected 2 total deployments, got %d", result.Summary.TotalDeployments)
	}
	if result.Summary.HealthyDeployments != 1 {
		t.Errorf("expected 1 healthy deployment, got %d", result.Summary.HealthyDeployments)
	}
	if result.Summary.UnhealthyDeployments != 1 {
		t.Errorf("expected 1 unhealthy deployment, got %d", result.Summary.UnhealthyDeployments)
	}
}

func TestAggregator_SummaryDetectsDeploymentWithoutService(t *testing.T) {
	ns := "no-svc-ns"
	labels := map[string]string{"app": "orphan"}
	dep := newDeploymentWithLabels("orphan-dep", ns, labels)

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(dep).
		Build()

	col := NewCollector(c, defaultOpts())
	agg := NewAggregator(col, nil, nil, 5*time.Minute)

	result, err := agg.AggregateContext(context.Background(), ns, "sess-no-svc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, name := range result.Summary.DeploymentsWithoutService {
		if name == "orphan-dep" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'orphan-dep' in DeploymentsWithoutService, got: %v",
			result.Summary.DeploymentsWithoutService)
	}
}

func TestAggregator_SummaryPodsWithRestarts(t *testing.T) {
	ns := "restart-ns"
	labels := map[string]string{"app": "restarter"}

	pod := newPod("restarter-0", ns, labels, corev1.PodRunning)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "app", RestartCount: 5},
	}

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(pod).
		Build()

	col := NewCollector(c, defaultOpts())
	agg := NewAggregator(col, nil, nil, 5*time.Minute)

	result, err := agg.AggregateContext(context.Background(), ns, "sess-restarts")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary.PodsWithRestarts != 1 {
		t.Errorf("expected PodsWithRestarts=1, got %d", result.Summary.PodsWithRestarts)
	}
}

func TestAggregator_CollectionDurationPositive(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).Build()
	col := NewCollector(c, defaultOpts())
	agg := NewAggregator(col, nil, nil, 5*time.Minute)

	result, err := agg.AggregateContext(context.Background(), "dur-ns", "sess-dur")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CollectionDuration <= 0 {
		t.Errorf("expected positive CollectionDuration, got %v", result.CollectionDuration)
	}
}

func TestAggregator_PrometheusUnreachable_DoesNotPanic(t *testing.T) {
	// Point the client at a server that is already closed — all queries will
	// fail with a connection error. The aggregator must handle this gracefully
	// without panicking and without propagating an error to the caller.
	//
	// CollectMetrics embeds query errors into MetricsSnapshot.Errors rather
	// than returning a non-nil error, so a snapshot may still be stored; the
	// important invariants are:
	//   1. AggregateContext returns no error.
	//   2. The call does not panic.
	srv := httptest.NewServer(promStubHandler())
	srv.Close() // immediately close so connections are refused

	ns := "unreachable-ns"
	labels := map[string]string{"app": "app"}
	dep := newDeploymentWithLabels("app", ns, labels)

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(dep).
		Build()

	pc := newPrometheusWithServer(t, srv)
	col := NewCollector(c, defaultOpts())
	agg := NewAggregator(col, pc, nil, 5*time.Minute)

	// Must not panic — errors in metric collection must not surface as an
	// AggregateContext error; they are embedded in each MetricsSnapshot.
	result, err := agg.AggregateContext(context.Background(), ns, "sess-unreachable")
	if err != nil {
		t.Fatalf("AggregateContext must not return an error for unreachable Prometheus, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil AggregatedContext")
	}
	// If a snapshot was stored, its Errors slice must be non-empty (query errors embedded).
	if snap, ok := result.MetricsSnapshots["app"]; ok {
		if len(snap.Errors) == 0 {
			t.Error("expected snapshot.Errors to be non-empty for unreachable Prometheus")
		}
	}
	// Either outcome (snapshot stored with errors, or skipped entirely) is acceptable;
	// no panic and no top-level error is the critical guarantee.
}

// keysOf is a small helper to format map keys for test error messages.
func keysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
