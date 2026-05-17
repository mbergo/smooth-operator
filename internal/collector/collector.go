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
	"strings"
	"time"
	"unicode"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const maxAnnotationValueLen = 256

// sensitiveAnnotationPrefixes lists annotation key prefixes (and exact keys) that
// must never be forwarded to the Reasoner prompt because they routinely carry
// secrets, full prior manifests with inlined env-var values, or PEM material.
var sensitiveAnnotationPrefixes = []string{
	"kubectl.kubernetes.io/last-applied-configuration",
	"deployment.kubernetes.io/revision",
	"cert-manager.io/",
	"kubectl.kubernetes.io/restartedAt",
}

// scrubAnnotations returns a sanitized copy of the given annotation map.
// It drops any key whose name matches a sensitive prefix, drops any entry
// whose value contains a PEM block marker, and truncates remaining values
// to maxAnnotationValueLen characters.
func scrubAnnotations(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if isSensitiveAnnotationKey(k) {
			continue
		}
		if strings.Contains(v, "-----BEGIN") {
			continue
		}
		if len(v) > maxAnnotationValueLen {
			v = v[:maxAnnotationValueLen]
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isSensitiveAnnotationKey returns true when key matches any entry in
// sensitiveAnnotationPrefixes (exact match or prefix match for entries that
// end with "/").
func isSensitiveAnnotationKey(key string) bool {
	for _, prefix := range sensitiveAnnotationPrefixes {
		if strings.HasSuffix(prefix, "/") {
			if strings.HasPrefix(key, prefix) {
				return true
			}
		} else {
			if key == prefix {
				return true
			}
		}
	}
	return false
}

// scrubLabels returns a copy of the given label map with each value truncated
// to maxAnnotationValueLen characters. Labels are less likely to carry
// sensitive payloads than annotations, but we apply length-capping defensively.
func scrubLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if len(v) > maxAnnotationValueLen {
			v = v[:maxAnnotationValueLen]
		}
		out[k] = v
	}
	return out
}

// sanitizeEventMessage strips control characters (newlines, carriage returns,
// tabs, and other non-printable runes) from an event message and truncates the
// result to maxAnnotationValueLen characters. This limits the blast radius of
// prompt-injection payloads authored by hostile workloads.
func sanitizeEventMessage(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsControl(r) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	result := b.String()
	if len(result) > maxAnnotationValueLen {
		result = result[:maxAnnotationValueLen]
	}
	return result
}

// Collector collects Kubernetes resources and context
type Collector struct {
	client  client.Client
	options CollectorOptions
}

// NewCollector creates a new Collector instance
func NewCollector(client client.Client, options CollectorOptions) *Collector {
	return &Collector{
		client:  client,
		options: options,
	}
}

// CollectContext gathers all relevant cluster context for a given namespace
func (c *Collector) CollectContext(ctx context.Context, namespace, chatSessionName string) (*ClusterContext, error) {
	log := log.FromContext(ctx)
	log.Info("Starting cluster context collection",
		"namespace", namespace,
		"chatSession", chatSessionName,
	)

	clusterCtx := &ClusterContext{
		TargetNamespace: namespace,
		ChatSessionName: chatSessionName,
		CollectedAt:     metav1.Now(),
		Errors:          []string{},
	}

	// Collect Deployments
	deployments, err := c.collectDeployments(ctx, namespace)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to collect Deployments: %v", err)
		log.Error(err, "Deployment collection failed")
		clusterCtx.Errors = append(clusterCtx.Errors, errMsg)
	} else {
		clusterCtx.Deployments = deployments
		log.Info("Collected Deployments", "count", len(deployments))
	}

	// Collect Services
	services, err := c.collectServices(ctx, namespace)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to collect Services: %v", err)
		log.Error(err, "Service collection failed")
		clusterCtx.Errors = append(clusterCtx.Errors, errMsg)
	} else {
		clusterCtx.Services = services
		log.Info("Collected Services", "count", len(services))
	}

	// Collect Ingresses
	ingresses, err := c.collectIngresses(ctx, namespace)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to collect Ingresses: %v", err)
		log.Error(err, "Ingress collection failed")
		clusterCtx.Errors = append(clusterCtx.Errors, errMsg)
	} else {
		clusterCtx.Ingresses = ingresses
		log.Info("Collected Ingresses", "count", len(ingresses))
	}

	// Collect Pods
	pods, err := c.collectPods(ctx, namespace)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to collect Pods: %v", err)
		log.Error(err, "Pod collection failed")
		clusterCtx.Errors = append(clusterCtx.Errors, errMsg)
	} else {
		clusterCtx.Pods = pods
		log.Info("Collected Pods", "count", len(pods))
	}

	// Collect Events (if enabled)
	if c.options.IncludeEvents {
		events, err := c.collectEvents(ctx, namespace)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to collect Events: %v", err)
			log.Error(err, "Event collection failed")
			clusterCtx.Errors = append(clusterCtx.Errors, errMsg)
		} else {
			clusterCtx.Events = events
			log.Info("Collected Events", "count", len(events))
		}
	}

	log.Info("Cluster context collection complete",
		"deployments", len(clusterCtx.Deployments),
		"services", len(clusterCtx.Services),
		"ingresses", len(clusterCtx.Ingresses),
		"pods", len(clusterCtx.Pods),
		"events", len(clusterCtx.Events),
		"errors", len(clusterCtx.Errors),
	)

	return clusterCtx, nil
}

// collectDeployments retrieves all Deployments in the namespace
func (c *Collector) collectDeployments(ctx context.Context, namespace string) ([]DeploymentInfo, error) {
	deploymentList := &appsv1.DeploymentList{}
	if err := c.client.List(ctx, deploymentList, &client.ListOptions{
		Namespace: namespace,
	}); err != nil {
		return nil, err
	}

	result := make([]DeploymentInfo, 0, len(deploymentList.Items))
	for _, dep := range deploymentList.Items {
		info := DeploymentInfo{
			Name:              dep.Name,
			Namespace:         dep.Namespace,
			Replicas:          *dep.Spec.Replicas,
			ReadyReplicas:     dep.Status.ReadyReplicas,
			AvailableReplicas: dep.Status.AvailableReplicas,
			Labels:            scrubLabels(dep.Labels),
			Annotations:       scrubAnnotations(dep.Annotations),
			Conditions:        dep.Status.Conditions,
			CreationTimestamp: dep.CreationTimestamp,
		}

		// Extract container information
		for _, container := range dep.Spec.Template.Spec.Containers {
			containerInfo := ContainerInfo{
				Name:              container.Name,
				Image:             container.Image,
				HasReadinessProbe: container.ReadinessProbe != nil,
				HasLivenessProbe:  container.LivenessProbe != nil,
				HasStartupProbe:   container.StartupProbe != nil,
			}

			// Extract resource requests/limits
			if container.Resources.Requests != nil {
				if cpu := container.Resources.Requests.Cpu(); cpu != nil {
					containerInfo.RequestsCPU = cpu.String()
				}
				if mem := container.Resources.Requests.Memory(); mem != nil {
					containerInfo.RequestsMemory = mem.String()
				}
			}
			if container.Resources.Limits != nil {
				if cpu := container.Resources.Limits.Cpu(); cpu != nil {
					containerInfo.LimitsCPU = cpu.String()
				}
				if mem := container.Resources.Limits.Memory(); mem != nil {
					containerInfo.LimitsMemory = mem.String()
				}
			}

			// Extract ports
			for _, port := range container.Ports {
				containerInfo.Ports = append(containerInfo.Ports, port.ContainerPort)
			}

			info.Containers = append(info.Containers, containerInfo)
		}

		// Generate YAML snippet if enabled
		if c.options.IncludeYAMLSnippets {
			info.YAMLSnippet = c.generateDeploymentYAML(info)
		}

		result = append(result, info)
	}

	return result, nil
}

// collectServices retrieves all Services in the namespace
func (c *Collector) collectServices(ctx context.Context, namespace string) ([]ServiceInfo, error) {
	serviceList := &corev1.ServiceList{}
	if err := c.client.List(ctx, serviceList, &client.ListOptions{
		Namespace: namespace,
	}); err != nil {
		return nil, err
	}

	result := make([]ServiceInfo, 0, len(serviceList.Items))
	for _, svc := range serviceList.Items {
		info := ServiceInfo{
			Name:              svc.Name,
			Namespace:         svc.Namespace,
			Type:              svc.Spec.Type,
			ClusterIP:         svc.Spec.ClusterIP,
			ExternalIPs:       svc.Spec.ExternalIPs,
			Ports:             svc.Spec.Ports,
			Selector:          svc.Spec.Selector,
			Labels:            scrubLabels(svc.Labels),
			Annotations:       scrubAnnotations(svc.Annotations),
			CreationTimestamp: svc.CreationTimestamp,
		}

		if len(svc.Status.LoadBalancer.Ingress) > 0 {
			info.LoadBalancerIP = svc.Status.LoadBalancer.Ingress[0].IP
		}

		if c.options.IncludeYAMLSnippets {
			info.YAMLSnippet = c.generateServiceYAML(info)
		}

		result = append(result, info)
	}

	return result, nil
}

// collectIngresses retrieves all Ingresses in the namespace
func (c *Collector) collectIngresses(ctx context.Context, namespace string) ([]IngressInfo, error) {
	ingressList := &networkingv1.IngressList{}
	if err := c.client.List(ctx, ingressList, &client.ListOptions{
		Namespace: namespace,
	}); err != nil {
		return nil, err
	}

	result := make([]IngressInfo, 0, len(ingressList.Items))
	for _, ing := range ingressList.Items {
		info := IngressInfo{
			Name:              ing.Name,
			Namespace:         ing.Namespace,
			Rules:             ing.Spec.Rules,
			TLS:               ing.Spec.TLS,
			Labels:            scrubLabels(ing.Labels),
			Annotations:       scrubAnnotations(ing.Annotations),
			CreationTimestamp: ing.CreationTimestamp,
		}

		if ing.Spec.IngressClassName != nil {
			info.IngressClassName = *ing.Spec.IngressClassName
		}

		if c.options.IncludeYAMLSnippets {
			info.YAMLSnippet = c.generateIngressYAML(info)
		}

		result = append(result, info)
	}

	return result, nil
}

// collectPods retrieves all Pods in the namespace
func (c *Collector) collectPods(ctx context.Context, namespace string) ([]PodInfo, error) {
	podList := &corev1.PodList{}
	if err := c.client.List(ctx, podList, &client.ListOptions{
		Namespace: namespace,
	}); err != nil {
		return nil, err
	}

	// Apply max pods limit if set
	items := podList.Items
	if c.options.MaxPods > 0 && len(items) > c.options.MaxPods {
		items = items[:c.options.MaxPods]
	}

	result := make([]PodInfo, 0, len(items))
	for _, pod := range items {
		info := PodInfo{
			Name:              pod.Name,
			Namespace:         pod.Namespace,
			Phase:             pod.Status.Phase,
			HostIP:            pod.Status.HostIP,
			PodIP:             pod.Status.PodIP,
			Labels:            scrubLabels(pod.Labels),
			Annotations:       scrubAnnotations(pod.Annotations),
			Conditions:        pod.Status.Conditions,
			CreationTimestamp: pod.CreationTimestamp,
		}

		// Count total restarts
		for _, containerStatus := range pod.Status.ContainerStatuses {
			info.RestartCount += containerStatus.RestartCount
		}

		// Extract container information
		for _, container := range pod.Spec.Containers {
			containerInfo := ContainerInfo{
				Name:              container.Name,
				Image:             container.Image,
				HasReadinessProbe: container.ReadinessProbe != nil,
				HasLivenessProbe:  container.LivenessProbe != nil,
				HasStartupProbe:   container.StartupProbe != nil,
			}

			for _, port := range container.Ports {
				containerInfo.Ports = append(containerInfo.Ports, port.ContainerPort)
			}

			info.Containers = append(info.Containers, containerInfo)
		}

		result = append(result, info)
	}

	return result, nil
}

// collectEvents retrieves recent Events in the namespace
func (c *Collector) collectEvents(ctx context.Context, namespace string) ([]EventInfo, error) {
	eventList := &corev1.EventList{}
	if err := c.client.List(ctx, eventList, &client.ListOptions{
		Namespace:     namespace,
		LabelSelector: labels.Everything(),
	}); err != nil {
		return nil, err
	}

	// Filter events by lookback time
	lookbackTime := time.Now().Add(-time.Duration(c.options.EventLookbackMinutes) * time.Minute)
	result := make([]EventInfo, 0)

	for _, event := range eventList.Items {
		if event.LastTimestamp.Time.Before(lookbackTime) {
			continue
		}

		info := EventInfo{
			Type:               event.Type,
			Reason:             event.Reason,
			Message:            sanitizeEventMessage(event.Message),
			InvolvedObjectKind: event.InvolvedObject.Kind,
			InvolvedObjectName: event.InvolvedObject.Name,
			Count:              event.Count,
			FirstTimestamp:     event.FirstTimestamp,
			LastTimestamp:      event.LastTimestamp,
		}

		result = append(result, info)
	}

	return result, nil
}

// generateDeploymentYAML creates a condensed YAML snippet for LLM context
func (c *Collector) generateDeploymentYAML(info DeploymentInfo) string {
	yaml := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
spec:
  replicas: %d
  template:
    spec:
      containers:`, info.Name, info.Namespace, info.Replicas)

	for _, container := range info.Containers {
		yaml += fmt.Sprintf(`
      - name: %s
        image: %s
        ports: %v`, container.Name, container.Image, container.Ports)

		if container.RequestsCPU != "" || container.RequestsMemory != "" {
			yaml += `
        resources:`
			if container.RequestsCPU != "" || container.RequestsMemory != "" {
				yaml += `
          requests:`
				if container.RequestsCPU != "" {
					yaml += fmt.Sprintf(`
            cpu: %s`, container.RequestsCPU)
				}
				if container.RequestsMemory != "" {
					yaml += fmt.Sprintf(`
            memory: %s`, container.RequestsMemory)
				}
			}
		}

		yaml += fmt.Sprintf(`
        # Probes: readiness=%t, liveness=%t, startup=%t`,
			container.HasReadinessProbe, container.HasLivenessProbe, container.HasStartupProbe)
	}

	return yaml
}

// generateServiceYAML creates a condensed YAML snippet for Services
func (c *Collector) generateServiceYAML(info ServiceInfo) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
spec:
  type: %s
  selector: %v
  ports: %v`,
		info.Name, info.Namespace, info.Type, info.Selector, info.Ports)
}

// generateIngressYAML creates a condensed YAML snippet for Ingresses
func (c *Collector) generateIngressYAML(info IngressInfo) string {
	yaml := fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: %s
  namespace: %s`, info.Name, info.Namespace)

	if info.IngressClassName != "" {
		yaml += fmt.Sprintf(`
spec:
  ingressClassName: %s
  rules: [%d rules]
  tls: [%d TLS configs]`,
			info.IngressClassName, len(info.Rules), len(info.TLS))
	}

	return yaml
}
