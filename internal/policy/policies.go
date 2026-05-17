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

// ResourceLimitsPolicy ensures containers have resource limits
type ResourceLimitsPolicy struct{}

func (p *ResourceLimitsPolicy) Name() string {
	return "resource-limits-required"
}

func (p *ResourceLimitsPolicy) Severity() string {
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

// ReadinessProbePolicy ensures containers have readiness probes
type ReadinessProbePolicy struct{}

func (p *ReadinessProbePolicy) Name() string {
	return "readiness-probe-required"
}

func (p *ReadinessProbePolicy) Severity() string {
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

// LivenessProbePolicy ensures containers have liveness probes
type LivenessProbePolicy struct{}

func (p *LivenessProbePolicy) Name() string {
	return "liveness-probe-required"
}

func (p *LivenessProbePolicy) Severity() string {
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
