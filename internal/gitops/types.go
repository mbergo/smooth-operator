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

import "time"

// HelmChart represents a generated Helm chart
type HelmChart struct {
	Name      string
	Namespace string
	Files     map[string]string // filename -> content
}

// SmoothDocument represents the SMOOTH.md rationale file
type SmoothDocument struct {
	ChatSessionName string
	Timestamp       time.Time
	UserPrompt      string
	InferredNeeds   []string
	AppliedChanges  []string
	PolicyResults   string
	RiskAssessment  string
	BeforeAfter     string
}

// GitCommitResult contains the result of a Git operation
type GitCommitResult struct {
	Success   bool
	CommitSHA string
	Branch    string
	PRURL     string
	Errors    []string
	Warnings  []string
}

// GitOptions configures Git operations
type GitOptions struct {
	RepoURL       string
	BaseBranch    string
	CommitMessage string
	CommitMode    string // "pr" or "direct"
	Reviewers     []string
}
