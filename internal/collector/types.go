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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterContext represents collected information about the cluster state
type ClusterContext struct {
	// TargetNamespace is the namespace being analyzed
	TargetNamespace string

	// ChatSessionName is the name of the ChatSession triggering collection
	ChatSessionName string

	// Deployments found in the target namespace
	Deployments []DeploymentInfo

	// Services found in the target namespace
	Services []ServiceInfo

	// Ingresses found in the target namespace
	Ingresses []IngressInfo

	// Pods found in the target namespace
	Pods []PodInfo

	// Events are recent events in the target namespace
	Events []EventInfo

	// CollectedAt is the timestamp when collection occurred
	CollectedAt metav1.Time

	// Errors encountered during collection
	Errors []string
}

// DeploymentInfo contains relevant information about a Deployment
type DeploymentInfo struct {
	Name              string
	Namespace         string
	Replicas          int32
	ReadyReplicas     int32
	AvailableReplicas int32
	Labels            map[string]string
	Annotations       map[string]string
	Containers        []ContainerInfo
	Conditions        []appsv1.DeploymentCondition
	CreationTimestamp metav1.Time

	// YAMLSnippet is a condensed YAML representation for LLM context
	YAMLSnippet string
}

// ContainerInfo contains information about a container in a pod
type ContainerInfo struct {
	Name  string
	Image string

	// Resource requests and limits
	RequestsCPU    string
	RequestsMemory string
	LimitsCPU      string
	LimitsMemory   string

	// Probes
	HasReadinessProbe bool
	HasLivenessProbe  bool
	HasStartupProbe   bool

	// Ports
	Ports []int32
}

// ServiceInfo contains relevant information about a Service
type ServiceInfo struct {
	Name              string
	Namespace         string
	Type              corev1.ServiceType
	ClusterIP         string
	ExternalIPs       []string
	LoadBalancerIP    string
	Ports             []corev1.ServicePort
	Selector          map[string]string
	Labels            map[string]string
	Annotations       map[string]string
	CreationTimestamp metav1.Time

	// YAMLSnippet is a condensed YAML representation
	YAMLSnippet string
}

// IngressInfo contains relevant information about an Ingress
type IngressInfo struct {
	Name              string
	Namespace         string
	IngressClassName  string
	Rules             []networkingv1.IngressRule
	TLS               []networkingv1.IngressTLS
	Labels            map[string]string
	Annotations       map[string]string
	CreationTimestamp metav1.Time

	// YAMLSnippet is a condensed YAML representation
	YAMLSnippet string
}

// PodInfo contains relevant information about a Pod
type PodInfo struct {
	Name              string
	Namespace         string
	Phase             corev1.PodPhase
	HostIP            string
	PodIP             string
	Labels            map[string]string
	Annotations       map[string]string
	Containers        []ContainerInfo
	RestartCount      int32
	CreationTimestamp metav1.Time

	// Conditions
	Conditions []corev1.PodCondition
}

// EventInfo contains information about a Kubernetes Event
type EventInfo struct {
	Type               string // Normal, Warning, Error
	Reason             string
	Message            string
	InvolvedObjectKind string
	InvolvedObjectName string
	Count              int32
	FirstTimestamp     metav1.Time
	LastTimestamp      metav1.Time
}

// CollectorOptions configures the collector behavior
type CollectorOptions struct {
	// IncludeEvents determines whether to collect events
	IncludeEvents bool

	// EventLookbackMinutes is how far back to look for events
	EventLookbackMinutes int

	// MaxEventsPerObject limits events collected per resource
	MaxEventsPerObject int

	// IncludeYAMLSnippets determines whether to generate YAML snippets
	IncludeYAMLSnippets bool

	// MaxPods limits the number of pods to collect (0 = unlimited)
	MaxPods int
}

// DefaultCollectorOptions returns sensible defaults
func DefaultCollectorOptions() CollectorOptions {
	return CollectorOptions{
		IncludeEvents:        true,
		EventLookbackMinutes: 10,
		MaxEventsPerObject:   5,
		IncludeYAMLSnippets:  true,
		MaxPods:              50,
	}
}
