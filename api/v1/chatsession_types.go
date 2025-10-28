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

// ChartRef defines a reference to a chart (image, diagram, metrics graph)
type ChartRef struct {
	// Name is a descriptive name for the chart
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// URL is an external URL to the chart resource (S3, HTTP, etc.)
	// +optional
	URL string `json:"url,omitempty"`

	// BlobBase64 is the base64-encoded content of the chart (for inline charts/images)
	// +optional
	BlobBase64 string `json:"blobBase64,omitempty"`
}

// SessionMetadata contains Git and other metadata for the chat session
type SessionMetadata struct {
	// GitRepo is the SSH or HTTPS URL of the Git repository
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	GitRepo string `json:"gitRepo"`

	// GitPath is the subdirectory path within the repo for the app/chart
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	GitPath string `json:"gitPath"`

	// Labels are arbitrary key-value pairs for categorization
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// ChatSessionSpec defines the desired state of ChatSession
type ChatSessionSpec struct {
	// User is the requester identity/email who initiated this chat session
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	User string `json:"user"`

	// TargetNamespace is the Kubernetes namespace where resources will be deployed/managed
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	TargetNamespace string `json:"targetNamespace"`

	// Prompt is the natural language instruction from the user
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Prompt string `json:"prompt"`

	// Charts are optional context charts/graphs/images attached by the user
	// +optional
	Charts []ChartRef `json:"charts,omitempty"`

	// Metadata contains Git repository information and other metadata
	// +kubebuilder:validation:Required
	Metadata SessionMetadata `json:"metadata"`

	// PreferAuto indicates whether the user wants automatic application of changes (vs. suggest mode)
	// +kubebuilder:default=false
	// +optional
	PreferAuto bool `json:"preferAuto,omitempty"`

	// CreatedByUI is a flag to indicate this CRD was created by the Chat UI
	// +kubebuilder:default=true
	// +optional
	CreatedByUI bool `json:"createdByUI,omitempty"`
}

// ChatSessionStatus defines the observed state of ChatSession.
type ChatSessionStatus struct {
	// State represents the current processing state of the chat session
	// +kubebuilder:validation:Enum=Pending;Processing;Blocked;Completed;Failed
	// +optional
	State string `json:"state,omitempty"`

	// Reason provides additional context about the current state
	// +optional
	Reason string `json:"reason,omitempty"`

	// LastUpdated is the timestamp of the last status update (RFC3339)
	// +optional
	LastUpdated string `json:"lastUpdated,omitempty"`

	// Conditions represent the current state of the ChatSession resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ChatSession is the Schema for the chatsessions API
type ChatSession struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of ChatSession
	// +required
	Spec ChatSessionSpec `json:"spec"`

	// status defines the observed state of ChatSession
	// +optional
	Status ChatSessionStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// ChatSessionList contains a list of ChatSession
type ChatSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ChatSession `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ChatSession{}, &ChatSessionList{})
}
