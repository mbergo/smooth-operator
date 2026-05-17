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
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// buildScheme returns a scheme with core Kubernetes types registered.
func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("failed to add clientgo scheme: %v", err)
	}
	return s
}

// ptr32 returns a pointer to an int32 value.
func ptr32(v int32) *int32 { return &v }

// newDeployment returns a minimal Deployment for testing.
func newDeployment(name, namespace string, labels map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr32(1),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:latest",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("200m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
							ReadinessProbe: &corev1.Probe{},
							LivenessProbe:  &corev1.Probe{},
						},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:          1,
			ReadyReplicas:     1,
			AvailableReplicas: 1,
		},
	}
}

// newPod returns a minimal Pod for testing. The phase parameter is exposed so
// future tests can exercise non-Running pods, even though current callers
// always pass corev1.PodRunning.
func newPod(name, namespace string, labels map[string]string, phase corev1.PodPhase) *corev1.Pod { //nolint:unparam // phase is parameterized for future use
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx:latest"},
			},
		},
		Status: corev1.PodStatus{
			Phase: phase,
		},
	}
}

// newService returns a minimal Service for testing.
func newService(name, namespace string, selector map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selector,
			Ports: []corev1.ServicePort{
				{Port: 80},
			},
		},
	}
}

// newIngress returns a minimal Ingress for testing.
func newIngress(name, namespace string) *networkingv1.Ingress {
	className := "nginx"
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &className,
			Rules: []networkingv1.IngressRule{
				{Host: "example.com"},
			},
		},
	}
}

// newEvent returns an Event whose LastTimestamp is within the lookback window.
func newEvent(name, namespace, kind, objName, reason, msg string) *corev1.Event {
	now := metav1.NewTime(time.Now())
	return &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		InvolvedObject: corev1.ObjectReference{
			Kind: kind,
			Name: objName,
		},
		Type:           corev1.EventTypeWarning,
		Reason:         reason,
		Message:        msg,
		Count:          1,
		FirstTimestamp: now,
		LastTimestamp:  now,
	}
}

// defaultOpts returns CollectorOptions suitable for tests.
func defaultOpts() CollectorOptions {
	return CollectorOptions{
		IncludeEvents:        true,
		EventLookbackMinutes: 60, // wide window so test events always pass the filter
		MaxEventsPerObject:   5,
		IncludeYAMLSnippets:  true,
		MaxPods:              50,
	}
}

// -------------------------------------------------------------------
// Tests
// -------------------------------------------------------------------

func TestCollectContext_DiscoverDeployments(t *testing.T) {
	labels := map[string]string{"app": "web"}
	dep := newDeployment("web-deploy", "test-ns", labels)

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(dep).
		Build()

	col := NewCollector(c, defaultOpts())
	ctx, err := col.CollectContext(context.Background(), "test-ns", "session-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctx.Deployments) != 1 {
		t.Fatalf("expected 1 deployment, got %d", len(ctx.Deployments))
	}
	if ctx.Deployments[0].Name != "web-deploy" {
		t.Errorf("expected deployment name 'web-deploy', got %q", ctx.Deployments[0].Name)
	}
}

func TestCollectContext_DiscoverPods(t *testing.T) {
	labels := map[string]string{"app": "worker"}
	pod := newPod("worker-0", "test-ns", labels, corev1.PodRunning)

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(pod).
		Build()

	col := NewCollector(c, defaultOpts())
	ctx, err := col.CollectContext(context.Background(), "test-ns", "session-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctx.Pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(ctx.Pods))
	}
	if ctx.Pods[0].Name != "worker-0" {
		t.Errorf("expected pod name 'worker-0', got %q", ctx.Pods[0].Name)
	}
	if ctx.Pods[0].Phase != corev1.PodRunning {
		t.Errorf("expected phase Running, got %q", ctx.Pods[0].Phase)
	}
}

func TestCollectContext_DiscoverServices(t *testing.T) {
	svc := newService("web-svc", "test-ns", map[string]string{"app": "web"})

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(svc).
		Build()

	col := NewCollector(c, defaultOpts())
	ctx, err := col.CollectContext(context.Background(), "test-ns", "session-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctx.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(ctx.Services))
	}
	if ctx.Services[0].Name != "web-svc" {
		t.Errorf("expected service name 'web-svc', got %q", ctx.Services[0].Name)
	}
}

func TestCollectContext_DiscoverIngresses(t *testing.T) {
	ing := newIngress("web-ing", "test-ns")

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(ing).
		Build()

	col := NewCollector(c, defaultOpts())
	ctx, err := col.CollectContext(context.Background(), "test-ns", "session-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctx.Ingresses) != 1 {
		t.Fatalf("expected 1 ingress, got %d", len(ctx.Ingresses))
	}
	if ctx.Ingresses[0].Name != "web-ing" {
		t.Errorf("expected ingress name 'web-ing', got %q", ctx.Ingresses[0].Name)
	}
	if ctx.Ingresses[0].IngressClassName != "nginx" {
		t.Errorf("expected ingressClassName 'nginx', got %q", ctx.Ingresses[0].IngressClassName)
	}
}

func TestCollectContext_IncludesEvents(t *testing.T) {
	evt := newEvent("evt-1", "test-ns", "Pod", "pod-0", "BackOff", "Back-off restarting failed container")

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(evt).
		Build()

	opts := defaultOpts()
	opts.IncludeEvents = true
	col := NewCollector(c, opts)

	ctx, err := col.CollectContext(context.Background(), "test-ns", "session-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctx.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(ctx.Events))
	}
	if ctx.Events[0].Reason != "BackOff" {
		t.Errorf("expected event reason 'BackOff', got %q", ctx.Events[0].Reason)
	}
}

func TestCollectContext_EventsDisabled(t *testing.T) {
	evt := newEvent("evt-1", "test-ns", "Pod", "pod-0", "BackOff", "container failed")

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(evt).
		Build()

	opts := defaultOpts()
	opts.IncludeEvents = false
	col := NewCollector(c, opts)

	ctx, err := col.CollectContext(context.Background(), "test-ns", "session-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctx.Events) != 0 {
		t.Errorf("expected 0 events when disabled, got %d", len(ctx.Events))
	}
}

func TestCollectContext_EmptyNamespace(t *testing.T) {
	// No objects at all — should succeed with empty slices, not error.
	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		Build()

	col := NewCollector(c, defaultOpts())
	ctx, err := col.CollectContext(context.Background(), "empty-ns", "session-7")
	if err != nil {
		t.Fatalf("expected no error for empty namespace, got: %v", err)
	}
	if ctx == nil {
		t.Fatal("expected non-nil ClusterContext")
	}
	if len(ctx.Deployments) != 0 {
		t.Errorf("expected 0 deployments, got %d", len(ctx.Deployments))
	}
	if len(ctx.Pods) != 0 {
		t.Errorf("expected 0 pods, got %d", len(ctx.Pods))
	}
	if len(ctx.Services) != 0 {
		t.Errorf("expected 0 services, got %d", len(ctx.Services))
	}
	if len(ctx.Ingresses) != 0 {
		t.Errorf("expected 0 ingresses, got %d", len(ctx.Ingresses))
	}
	if len(ctx.Errors) != 0 {
		t.Errorf("expected 0 collection errors, got %v", ctx.Errors)
	}
}

func TestCollectContext_NamespaceIsolation(t *testing.T) {
	// Objects in a different namespace must not appear.
	labels := map[string]string{"app": "other"}
	dep := newDeployment("other-deploy", "other-ns", labels)
	pod := newPod("other-pod", "other-ns", labels, corev1.PodRunning)
	svc := newService("other-svc", "other-ns", labels)

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(dep, pod, svc).
		Build()

	col := NewCollector(c, defaultOpts())
	ctx, err := col.CollectContext(context.Background(), "target-ns", "session-8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctx.Deployments) != 0 {
		t.Errorf("namespace isolation broken: got %d deployments from other-ns", len(ctx.Deployments))
	}
	if len(ctx.Pods) != 0 {
		t.Errorf("namespace isolation broken: got %d pods from other-ns", len(ctx.Pods))
	}
	if len(ctx.Services) != 0 {
		t.Errorf("namespace isolation broken: got %d services from other-ns", len(ctx.Services))
	}
}

func TestCollectContext_MaxPodsLimit(t *testing.T) {
	labels := map[string]string{"app": "app"}

	// Build a list of 15 pods as client.Object.
	pods := make([]corev1.Pod, 15)
	clientObjs := make([]any, 15)
	for i := 0; i < 15; i++ {
		pods[i] = *newPod(fmt.Sprintf("pod-%d", i), "test-ns", labels, corev1.PodRunning)
		clientObjs[i] = &pods[i]
	}

	builder := fake.NewClientBuilder().WithScheme(buildScheme(t))
	for i := range clientObjs {
		builder = builder.WithObjects(clientObjs[i].(*corev1.Pod))
	}

	opts := defaultOpts()
	opts.MaxPods = 5
	col := NewCollector(builder.Build(), opts)

	ctx, err := col.CollectContext(context.Background(), "test-ns", "session-9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctx.Pods) > opts.MaxPods {
		t.Errorf("expected at most %d pods (MaxPods), got %d", opts.MaxPods, len(ctx.Pods))
	}
}

func TestCollectContext_DeploymentContainerFields(t *testing.T) {
	labels := map[string]string{"app": "full"}
	dep := newDeployment("full-deploy", "test-ns", labels)

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(dep).
		Build()

	col := NewCollector(c, defaultOpts())
	ctx, err := col.CollectContext(context.Background(), "test-ns", "session-10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctx.Deployments) != 1 {
		t.Fatalf("expected 1 deployment, got %d", len(ctx.Deployments))
	}

	di := ctx.Deployments[0]
	if len(di.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(di.Containers))
	}
	ci := di.Containers[0]
	if ci.RequestsCPU == "" {
		t.Error("expected RequestsCPU to be populated")
	}
	if ci.RequestsMemory == "" {
		t.Error("expected RequestsMemory to be populated")
	}
	if !ci.HasReadinessProbe {
		t.Error("expected HasReadinessProbe to be true")
	}
	if !ci.HasLivenessProbe {
		t.Error("expected HasLivenessProbe to be true")
	}
}

func TestCollectContext_PodRestartCount(t *testing.T) {
	labels := map[string]string{"app": "crasher"}
	pod := newPod("crasher-0", "test-ns", labels, corev1.PodRunning)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "app", RestartCount: 3},
	}

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(pod).
		Build()

	col := NewCollector(c, defaultOpts())
	ctx, err := col.CollectContext(context.Background(), "test-ns", "session-11")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctx.Pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(ctx.Pods))
	}
	if ctx.Pods[0].RestartCount != 3 {
		t.Errorf("expected RestartCount 3, got %d", ctx.Pods[0].RestartCount)
	}
}

func TestCollectContext_MultipleResourceTypes(t *testing.T) {
	ns := "multi-ns"
	appLabels := map[string]string{"app": "multi"}

	dep := newDeployment("multi-dep", ns, appLabels)
	pod := newPod("multi-pod", ns, appLabels, corev1.PodRunning)
	svc := newService("multi-svc", ns, appLabels)
	ing := newIngress("multi-ing", ns)

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(dep, pod, svc, ing).
		Build()

	col := NewCollector(c, defaultOpts())
	ctx, err := col.CollectContext(context.Background(), ns, "session-12")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctx.Deployments) != 1 {
		t.Errorf("expected 1 deployment, got %d", len(ctx.Deployments))
	}
	if len(ctx.Pods) != 1 {
		t.Errorf("expected 1 pod, got %d", len(ctx.Pods))
	}
	if len(ctx.Services) != 1 {
		t.Errorf("expected 1 service, got %d", len(ctx.Services))
	}
	if len(ctx.Ingresses) != 1 {
		t.Errorf("expected 1 ingress, got %d", len(ctx.Ingresses))
	}
	if ctx.TargetNamespace != ns {
		t.Errorf("expected TargetNamespace %q, got %q", ns, ctx.TargetNamespace)
	}
}

// -------------------------------------------------------------------
// Context builder tests (context.go)
// -------------------------------------------------------------------

func TestWithChatSessionID_RoundTrip(t *testing.T) {
	ctx := context.Background()
	ctx = WithChatSessionID(ctx, "sess-abc")
	if got := GetChatSessionID(ctx); got != "sess-abc" {
		t.Errorf("expected 'sess-abc', got %q", got)
	}
}

func TestWithNamespace_RoundTrip(t *testing.T) {
	ctx := context.Background()
	ctx = WithNamespace(ctx, "my-namespace")
	if got := GetNamespace(ctx); got != "my-namespace" {
		t.Errorf("expected 'my-namespace', got %q", got)
	}
}

func TestGetChatSessionID_MissingKey(t *testing.T) {
	ctx := context.Background()
	if got := GetChatSessionID(ctx); got != "" {
		t.Errorf("expected empty string for missing key, got %q", got)
	}
}

func TestGetNamespace_MissingKey(t *testing.T) {
	ctx := context.Background()
	if got := GetNamespace(ctx); got != "" {
		t.Errorf("expected empty string for missing key, got %q", got)
	}
}

func TestCollectContext_ChatSessionInResult(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		Build()

	col := NewCollector(c, defaultOpts())
	ctx, err := col.CollectContext(context.Background(), "ns", "my-session")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.ChatSessionName != "my-session" {
		t.Errorf("expected ChatSessionName 'my-session', got %q", ctx.ChatSessionName)
	}
	if ctx.TargetNamespace != "ns" {
		t.Errorf("expected TargetNamespace 'ns', got %q", ctx.TargetNamespace)
	}
}
