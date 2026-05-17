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
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// RunAsNonRootPolicy enforces that containers must run as non-root
type RunAsNonRootPolicy struct{}

func (p *RunAsNonRootPolicy) Name() string {
	return "run-as-non-root"
}

func (p *RunAsNonRootPolicy) Severity() string {
	return "blocking"
}

func (p *RunAsNonRootPolicy) Evaluate(ctx context.Context, obj *unstructured.Unstructured) *PolicyResult {
	if obj.GetKind() != "Deployment" && obj.GetKind() != "Pod" {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	// Check pod security context
	spec, found, _ := unstructured.NestedMap(obj.Object, "spec", "template", "spec")
	if !found {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	securityContext, found, _ := unstructured.NestedMap(spec, "securityContext")
	if !found {
		return &PolicyResult{
			Passed:       false,
			PolicyName:   p.Name(),
			Message:      "Pod must have securityContext with runAsNonRoot: true",
			Severity:     p.Severity(),
			SuggestedFix: "Add spec.template.spec.securityContext.runAsNonRoot: true",
		}
	}

	runAsNonRoot, found, _ := unstructured.NestedBool(securityContext, "runAsNonRoot")
	if !found || !runAsNonRoot {
		return &PolicyResult{
			Passed:       false,
			PolicyName:   p.Name(),
			Message:      "securityContext.runAsNonRoot must be set to true",
			Severity:     p.Severity(),
			SuggestedFix: "Set spec.template.spec.securityContext.runAsNonRoot: true",
		}
	}

	return &PolicyResult{Passed: true, PolicyName: p.Name()}
}

// ResourceLimitsPolicy ensures containers have resource limits.
//
// The zero value is valid and defaults to severity "warning" for backward
// compatibility. Use WithSeverity to promote to "blocking" in strict mode.
type ResourceLimitsPolicy struct {
	// severity overrides the default "warning" when non-empty.
	severity string
}

// WithSeverity returns the policy with its severity set to s. It returns the
// same pointer so callers can chain: new(ResourceLimitsPolicy).WithSeverity("blocking").
func (p *ResourceLimitsPolicy) WithSeverity(s string) *ResourceLimitsPolicy {
	p.severity = s
	return p
}

// setSeverity implements the internal severitySetter interface used by
// Engine.SetSeverity for runtime reconfiguration.
func (p *ResourceLimitsPolicy) setSeverity(s string) { p.severity = s }

func (p *ResourceLimitsPolicy) Name() string {
	return "resource-limits-required"
}

// Severity returns the configured severity, defaulting to "warning" when none
// has been set. This preserves backward-compatible behaviour for existing callers.
func (p *ResourceLimitsPolicy) Severity() string {
	if p.severity != "" {
		return p.severity
	}
	return "warning"
}

func (p *ResourceLimitsPolicy) Evaluate(ctx context.Context, obj *unstructured.Unstructured) *PolicyResult {
	if obj.GetKind() != "Deployment" {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	containers, found, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if !found || len(containers) == 0 {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	for i, container := range containers {
		containerMap, ok := container.(map[string]interface{})
		if !ok {
			continue
		}

		resources, found, _ := unstructured.NestedMap(containerMap, "resources")
		if !found {
			return &PolicyResult{
				Passed:       false,
				PolicyName:   p.Name(),
				Message:      "Container must have resource requests and limits defined",
				Severity:     p.Severity(),
				SuggestedFix: fmt.Sprintf("Add resources.requests and resources.limits to container %d", i),
			}
		}

		requests, hasRequests, _ := unstructured.NestedMap(resources, "requests")
		limits, hasLimits, _ := unstructured.NestedMap(resources, "limits")

		if !hasRequests || len(requests) == 0 {
			return &PolicyResult{
				Passed:       false,
				PolicyName:   p.Name(),
				Message:      "Container must have resource requests (cpu, memory)",
				Severity:     p.Severity(),
				SuggestedFix: "Add resources.requests.cpu and resources.requests.memory",
			}
		}

		if !hasLimits || len(limits) == 0 {
			return &PolicyResult{
				Passed:       false,
				PolicyName:   p.Name(),
				Message:      "Container must have resource limits (cpu, memory)",
				Severity:     p.Severity(),
				SuggestedFix: "Add resources.limits.cpu and resources.limits.memory",
			}
		}
	}

	return &PolicyResult{Passed: true, PolicyName: p.Name()}
}

// ReadinessProbePolicy ensures containers have readiness probes.
//
// The zero value defaults to severity "warning" for backward compatibility.
// Use WithSeverity to promote to "blocking" in strict mode.
type ReadinessProbePolicy struct {
	// severity overrides the default "warning" when non-empty.
	severity string
}

// WithSeverity returns the policy with its severity set to s.
func (p *ReadinessProbePolicy) WithSeverity(s string) *ReadinessProbePolicy {
	p.severity = s
	return p
}

// setSeverity implements the internal severitySetter interface used by
// Engine.SetSeverity for runtime reconfiguration.
func (p *ReadinessProbePolicy) setSeverity(s string) { p.severity = s }

func (p *ReadinessProbePolicy) Name() string {
	return "readiness-probe-required"
}

// Severity returns the configured severity, defaulting to "warning".
func (p *ReadinessProbePolicy) Severity() string {
	if p.severity != "" {
		return p.severity
	}
	return "warning"
}

func (p *ReadinessProbePolicy) Evaluate(ctx context.Context, obj *unstructured.Unstructured) *PolicyResult {
	if obj.GetKind() != "Deployment" {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	containers, found, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if !found || len(containers) == 0 {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	for i, container := range containers {
		containerMap, ok := container.(map[string]interface{})
		if !ok {
			continue
		}

		_, found, _ := unstructured.NestedMap(containerMap, "readinessProbe")
		if !found {
			return &PolicyResult{
				Passed:       false,
				PolicyName:   p.Name(),
				Message:      "Container must have a readinessProbe defined",
				Severity:     p.Severity(),
				SuggestedFix: fmt.Sprintf("Add readinessProbe to container %d", i),
			}
		}
	}

	return &PolicyResult{Passed: true, PolicyName: p.Name()}
}

// LivenessProbePolicy ensures containers have liveness probes.
//
// The zero value defaults to severity "warning" for backward compatibility.
// Use WithSeverity to promote to "blocking" in strict mode.
type LivenessProbePolicy struct {
	// severity overrides the default "warning" when non-empty.
	severity string
}

// WithSeverity returns the policy with its severity set to s.
func (p *LivenessProbePolicy) WithSeverity(s string) *LivenessProbePolicy {
	p.severity = s
	return p
}

// setSeverity implements the internal severitySetter interface used by
// Engine.SetSeverity for runtime reconfiguration.
func (p *LivenessProbePolicy) setSeverity(s string) { p.severity = s }

func (p *LivenessProbePolicy) Name() string {
	return "liveness-probe-required"
}

// Severity returns the configured severity, defaulting to "warning".
func (p *LivenessProbePolicy) Severity() string {
	if p.severity != "" {
		return p.severity
	}
	return "warning"
}

func (p *LivenessProbePolicy) Evaluate(ctx context.Context, obj *unstructured.Unstructured) *PolicyResult {
	if obj.GetKind() != "Deployment" {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	containers, found, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if !found || len(containers) == 0 {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	for i, container := range containers {
		containerMap, ok := container.(map[string]interface{})
		if !ok {
			continue
		}

		_, found, _ := unstructured.NestedMap(containerMap, "livenessProbe")
		if !found {
			return &PolicyResult{
				Passed:       false,
				PolicyName:   p.Name(),
				Message:      "Container must have a livenessProbe defined",
				Severity:     p.Severity(),
				SuggestedFix: fmt.Sprintf("Add livenessProbe to container %d", i),
			}
		}
	}

	return &PolicyResult{Passed: true, PolicyName: p.Name()}
}

// ImageRegistryPolicy ensures images come from allowed registries
type ImageRegistryPolicy struct{}

func (p *ImageRegistryPolicy) Name() string {
	return "allowed-image-registries"
}

func (p *ImageRegistryPolicy) Severity() string {
	return "blocking"
}

func (p *ImageRegistryPolicy) Evaluate(ctx context.Context, obj *unstructured.Unstructured) *PolicyResult {
	if obj.GetKind() != "Deployment" {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	allowedRegistries := []string{
		"docker.io",
		"gcr.io",
		"quay.io",
		"ghcr.io",
		"registry.k8s.io",
		"k8s.gcr.io",
	}

	containers, found, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if !found {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	for _, container := range containers {
		containerMap, ok := container.(map[string]interface{})
		if !ok {
			continue
		}

		image, found, _ := unstructured.NestedString(containerMap, "image")
		if !found {
			continue
		}

		allowed := false
		for _, registry := range allowedRegistries {
			if strings.HasPrefix(image, registry) || !strings.Contains(image, "/") {
				allowed = true
				break
			}
		}

		if !allowed {
			return &PolicyResult{
				Passed:       false,
				PolicyName:   p.Name(),
				Message:      fmt.Sprintf("Image registry not allowed: %s", image),
				Severity:     p.Severity(),
				SuggestedFix: fmt.Sprintf("Use image from allowed registries: %v", allowedRegistries),
			}
		}
	}

	return &PolicyResult{Passed: true, PolicyName: p.Name()}
}

// HostPathPolicy prevents use of hostPath volumes
type HostPathPolicy struct{}

func (p *HostPathPolicy) Name() string {
	return "no-host-path"
}

func (p *HostPathPolicy) Severity() string {
	return "blocking"
}

func (p *HostPathPolicy) Evaluate(ctx context.Context, obj *unstructured.Unstructured) *PolicyResult {
	if obj.GetKind() != "Deployment" && obj.GetKind() != "Pod" {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	volumes, found, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "volumes")
	if !found {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	for _, volume := range volumes {
		volumeMap, ok := volume.(map[string]interface{})
		if !ok {
			continue
		}

		_, found, _ := unstructured.NestedMap(volumeMap, "hostPath")
		if found {
			return &PolicyResult{
				Passed:       false,
				PolicyName:   p.Name(),
				Message:      "hostPath volumes are not allowed for security reasons",
				Severity:     p.Severity(),
				SuggestedFix: "Use PersistentVolumeClaim, ConfigMap, or Secret instead",
			}
		}
	}

	return &PolicyResult{Passed: true, PolicyName: p.Name()}
}

// LoadBalancerInternalAnnotationPolicy requires internal annotation on LoadBalancer Services
type LoadBalancerInternalAnnotationPolicy struct{}

func (p *LoadBalancerInternalAnnotationPolicy) Name() string {
	return "loadbalancer-internal-annotation"
}

func (p *LoadBalancerInternalAnnotationPolicy) Severity() string {
	return "blocking"
}

func (p *LoadBalancerInternalAnnotationPolicy) Evaluate(ctx context.Context, obj *unstructured.Unstructured) *PolicyResult {
	if obj.GetKind() != "Service" {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	serviceType, _, _ := unstructured.NestedString(obj.Object, "spec", "type")
	if serviceType != "LoadBalancer" {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	annotations := obj.GetAnnotations()
	if annotations != nil {
		if _, ok := annotations["service.beta.kubernetes.io/aws-load-balancer-internal"]; ok {
			return &PolicyResult{Passed: true, PolicyName: p.Name()}
		}
	}

	return &PolicyResult{
		Passed:       false,
		PolicyName:   p.Name(),
		Message:      "LoadBalancer Service must have annotation service.beta.kubernetes.io/aws-load-balancer-internal",
		Severity:     p.Severity(),
		SuggestedFix: `Add annotation: service.beta.kubernetes.io/aws-load-balancer-internal: "true"`,
	}
}

// KindAllowlistPolicy enforces that only explicitly permitted manifest Kinds
// are accepted. It is fail-closed: any Kind absent from the allowlist is
// rejected with a blocking violation.
//
// The baseline allowlist covers the Kinds that the Generator is expected to
// emit. A curated denylist provides descriptive, auditable error messages for
// high-risk Kinds that must never be auto-applied.
//
// Operators may widen the allowlist at startup with NewKindAllowlistPolicy;
// the default zero-value struct uses the baseline sets defined below.
type KindAllowlistPolicy struct {
	// allowed is the complete set of permitted Kinds. When nil the baseline
	// allowlist is used.
	allowed map[string]struct{}
	// denied maps an explicitly prohibited Kind to a human-readable reason.
	// When nil the baseline denylist is used.
	denied map[string]string
}

// baselineAllowed is the canonical set of Kinds the engine may accept.
var baselineAllowed = map[string]struct{}{
	"Deployment":              {},
	"Service":                 {},
	"HorizontalPodAutoscaler": {},
	"Ingress":                 {},
	"ConfigMap":               {},
	"NetworkPolicy":           {},
	"PersistentVolumeClaim":   {},
	"ServiceAccount":          {},
	"Role":                    {},
	"RoleBinding":             {},
}

// baselineDenied maps high-risk Kinds to a reason string that is surfaced in
// the violation message, making audit logs self-explanatory.
var baselineDenied = map[string]string{
	"ClusterRole":                   "cluster-scoped RBAC; grants privileges across all namespaces",
	"ClusterRoleBinding":            "cluster-scoped RBAC; grants privileges across all namespaces",
	"Namespace":                     "namespace management must be performed by operators, not generated manifests",
	"MutatingWebhookConfiguration":  "webhook configs can intercept and mutate arbitrary API traffic",
	"ValidatingWebhookConfiguration": "webhook configs can block or observe arbitrary API traffic",
	"CustomResourceDefinition":      "CRDs extend the API surface and require explicit operator approval",
	"APIService":                    "API aggregation layer changes require explicit operator approval",
	"PriorityClass":                 "priority classes affect cluster-wide scheduling and eviction behaviour",
	"StorageClass":                  "storage class changes affect cluster-wide data persistence behaviour",
}

// NewKindAllowlistPolicy constructs a KindAllowlistPolicy starting from the
// baseline sets and optionally extending them.
//
// extraAllowed is a list of additional Kind strings to permit (e.g. "CronJob").
// extraDenied is a list of Kind strings to add to the explicit denylist with
// a generic operator-supplied reason message ("operator-denied").
//
// Passing empty slices for both arguments yields the baseline behaviour.
func NewKindAllowlistPolicy(extraAllowed []string, extraDenied []string) *KindAllowlistPolicy {
	allowed := make(map[string]struct{}, len(baselineAllowed)+len(extraAllowed))
	for k := range baselineAllowed {
		allowed[k] = struct{}{}
	}
	for _, k := range extraAllowed {
		allowed[k] = struct{}{}
	}

	denied := make(map[string]string, len(baselineDenied)+len(extraDenied))
	for k, v := range baselineDenied {
		denied[k] = v
	}
	for _, k := range extraDenied {
		if _, exists := denied[k]; !exists {
			denied[k] = "operator-denied"
		}
	}

	return &KindAllowlistPolicy{allowed: allowed, denied: denied}
}

func (p *KindAllowlistPolicy) Name() string {
	return "kind-allowlist"
}

func (p *KindAllowlistPolicy) Severity() string {
	return "blocking"
}

// effectiveAllowed returns the allowlist in use, falling back to the baseline
// when the struct was zero-initialised (e.g. &KindAllowlistPolicy{}).
func (p *KindAllowlistPolicy) effectiveAllowed() map[string]struct{} {
	if p.allowed != nil {
		return p.allowed
	}
	return baselineAllowed
}

// effectiveDenied returns the denylist in use, falling back to the baseline.
func (p *KindAllowlistPolicy) effectiveDenied() map[string]string {
	if p.denied != nil {
		return p.denied
	}
	return baselineDenied
}

func (p *KindAllowlistPolicy) Evaluate(_ context.Context, obj *unstructured.Unstructured) *PolicyResult {
	kind := obj.GetKind()

	// Check explicit denylist first so the message is maximally informative.
	if reason, isDenied := p.effectiveDenied()[kind]; isDenied {
		return &PolicyResult{
			Passed:       false,
			PolicyName:   p.Name(),
			Message:      fmt.Sprintf("kind %q is explicitly denied: %s", kind, reason),
			Severity:     p.Severity(),
			SuggestedFix: "Remove this resource from the generated manifest or request operator approval",
		}
	}

	// Fail-closed: anything not on the allowlist is rejected.
	if _, ok := p.effectiveAllowed()[kind]; !ok {
		return &PolicyResult{
			Passed:       false,
			PolicyName:   p.Name(),
			Message:      fmt.Sprintf("kind %q is not on the allowlist", kind),
			Severity:     p.Severity(),
			SuggestedFix: "Use one of the permitted Kinds or request that the allowlist be extended",
		}
	}

	return &PolicyResult{Passed: true, PolicyName: p.Name()}
}

// PrivilegedContainerPolicy prevents privileged containers
type PrivilegedContainerPolicy struct{}

func (p *PrivilegedContainerPolicy) Name() string {
	return "no-privileged-containers"
}

func (p *PrivilegedContainerPolicy) Severity() string {
	return "blocking"
}

func (p *PrivilegedContainerPolicy) Evaluate(ctx context.Context, obj *unstructured.Unstructured) *PolicyResult {
	if obj.GetKind() != "Deployment" && obj.GetKind() != "Pod" {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	containers, found, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if !found {
		return &PolicyResult{Passed: true, PolicyName: p.Name()}
	}

	for _, container := range containers {
		containerMap, ok := container.(map[string]interface{})
		if !ok {
			continue
		}

		securityContext, found, _ := unstructured.NestedMap(containerMap, "securityContext")
		if !found {
			continue
		}

		privileged, found, _ := unstructured.NestedBool(securityContext, "privileged")
		if found && privileged {
			return &PolicyResult{
				Passed:       false,
				PolicyName:   p.Name(),
				Message:      "Privileged containers are not allowed",
				Severity:     p.Severity(),
				SuggestedFix: "Set securityContext.privileged: false or remove the field",
			}
		}
	}

	return &PolicyResult{Passed: true, PolicyName: p.Name()}
}
