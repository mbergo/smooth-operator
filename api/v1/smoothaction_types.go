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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// InferredNeed represents a resource or configuration that the LLM determined is needed
type InferredNeed struct {
	// Type is the kind of need (e.g., HPA, Service-LB, Ingress, PVC, Probe, NetworkPolicy, PodSecurity)
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`

	// Reason explains why this need was inferred
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Reason string `json:"reason"`

	// Priority indicates the importance of this need
	// +kubebuilder:validation:Enum=low;med;high
	// +kubebuilder:default=med
	// +optional
	Priority string `json:"priority,omitempty"`
}

// Patch represents a suggested Kubernetes manifest or modification
type Patch struct {
	// Kind is the Kubernetes resource kind (e.g., Deployment, Service, HPA)
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`

	// YAML is the full YAML manifest for the resource
	// +kubebuilder:validation:Required
	YAML string `json:"yaml"`
}

// GeneratedArtifact represents an artifact created by the operator (Helm chart, dashboard, etc.)
type GeneratedArtifact struct {
	// Type is the kind of artifact
	// +kubebuilder:validation:Enum=helm-chart;kustomize;grafana-dashboard
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Path is the repository path where the artifact was committed
	// +kubebuilder:validation:Required
	Path string `json:"path"`
}

// ApprovalInfo tracks approval requirements and status for suggest mode
type ApprovalInfo struct {
	// Required indicates whether approval is needed before applying
	// +kubebuilder:default=true
	// +optional
	Required bool `json:"required,omitempty"`

	// ApprovedBy contains the identity of the user who approved (if approved)
	// +optional
	ApprovedBy string `json:"approvedBy,omitempty"`
}

// SmoothActionSpec defines the desired state of SmoothAction
type SmoothActionSpec struct {
	// ChatRef is the name of the ChatSession that triggered this action
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ChatRef string `json:"chatRef"`

	// Mode indicates whether this is a suggestion or automatic action
	// +kubebuilder:validation:Enum=suggest;auto
	// +kubebuilder:validation:Required
	Mode string `json:"mode"`

	// InferredNeeds contains the condensed plan of what the LLM determined is needed
	// +optional
	InferredNeeds []InferredNeed `json:"inferredNeeds,omitempty"`

	// Patches contains the suggested manifests or values diffs
	// +optional
	Patches []Patch `json:"patches,omitempty"`

	// GeneratedArtifacts lists the artifacts (charts, dashboards) created
	// +optional
	GeneratedArtifacts []GeneratedArtifact `json:"generatedArtifacts,omitempty"`

	// Approval tracks approval requirements and status for suggest mode
	// +optional
	Approval ApprovalInfo `json:"approval,omitempty"`
}

// GitInfo contains Git-related status information
type GitInfo struct {
	// Commit is the Git commit SHA
	// +optional
	Commit string `json:"commit,omitempty"`

	// Branch is the Git branch name
	// +optional
	Branch string `json:"branch,omitempty"`

	// PRURL is the URL of the pull request created
	// +optional
	PRURL string `json:"prURL,omitempty"`
}

// SmoothActionStatus defines the observed state of SmoothAction.
type SmoothActionStatus struct {
	// State represents the current state of the action
	// +kubebuilder:validation:Enum=Proposed;Applied;RolledBack;Declined;Error
	// +optional
	State string `json:"state,omitempty"`

	// Git contains Git-related information after commits
	// +optional
	Git GitInfo `json:"git,omitempty"`

	// AppliedAt is the timestamp when changes were applied (RFC3339)
	// +optional
	AppliedAt string `json:"appliedAt,omitempty"`

	// Errors contains any error messages encountered during processing
	// +optional
	Errors []string `json:"errors,omitempty"`

	// Conditions represent the current state of the SmoothAction resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// SmoothAction is the Schema for the smoothactions API
type SmoothAction struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of SmoothAction
	// +required
	Spec SmoothActionSpec `json:"spec"`

	// status defines the observed state of SmoothAction
	// +optional
	Status SmoothActionStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// SmoothActionList contains a list of SmoothAction
type SmoothActionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SmoothAction `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SmoothAction{}, &SmoothActionList{})
}
