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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// GitClient handles Git operations
type GitClient struct {
	workDir string
}

// NewGitClient creates a new Git client
func NewGitClient() *GitClient {
	return &GitClient{
		workDir: "/tmp/smooth-operator-git",
	}
}

// CommitAndPush commits artifacts to Git and optionally creates a PR
func (g *GitClient) CommitAndPush(
	ctx context.Context,
	options GitOptions,
	chart *HelmChart,
	rationale string,
	chatSessionID string,
) (*GitCommitResult, error) {
	log := log.FromContext(ctx)

	result := &GitCommitResult{
		Success: false,
		Errors:  []string{},
	}

	log.Info("Starting Git operations",
		"repo", options.RepoURL,
		"mode", options.CommitMode,
	)

	// Create temporary directory for repo
	repoPath := filepath.Join(g.workDir, chatSessionID)
	os.RemoveAll(repoPath) // Clean up any previous attempts
	os.MkdirAll(repoPath, 0755)

	// Clone repository
	log.Info("Cloning repository", "url", options.RepoURL)
	repo, err := git.PlainClone(repoPath, false, &git.CloneOptions{
		URL:      options.RepoURL,
		Progress: nil,
	})
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("Clone failed: %v", err))
		return result, fmt.Errorf("failed to clone repo: %w", err)
	}

	// Create feature branch
	branchName := fmt.Sprintf("smooth/%s", chatSessionID)
	result.Branch = branchName

	log.Info("Creating feature branch", "branch", branchName)
	headRef, err := repo.Head()
	if err != nil {
		return result, fmt.Errorf("failed to get HEAD: %w", err)
	}

	ref := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/"+branchName), headRef.Hash())
	err = repo.Storer.SetReference(ref)
	if err != nil {
		return result, fmt.Errorf("failed to create branch: %w", err)
	}

	// Checkout branch
	worktree, err := repo.Worktree()
	if err != nil {
		return result, fmt.Errorf("failed to get worktree: %w", err)
	}

	err = worktree.Checkout(&git.CheckoutOptions{
		Branch: plumbing.ReferenceName("refs/heads/" + branchName),
	})
	if err != nil {
		return result, fmt.Errorf("failed to checkout branch: %w", err)
	}

	// Write Helm chart files
	chartPath := filepath.Join(repoPath, "charts", chart.Name)
	os.MkdirAll(chartPath, 0755)

	for filename, content := range chart.Files {
		filePath := filepath.Join(chartPath, filename)
		os.MkdirAll(filepath.Dir(filePath), 0755)
		err := os.WriteFile(filePath, []byte(content), 0644)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("Failed to write %s: %v", filename, err))
			continue
		}
	}

	// Write SMOOTH.md
	smoothPath := filepath.Join(chartPath, "SMOOTH.md")
	err = os.WriteFile(smoothPath, []byte(rationale), 0644)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to write SMOOTH.md: %v", err))
	}

	// Stage all changes
	log.Info("Staging changes")
	_, err = worktree.Add(".")
	if err != nil {
		return result, fmt.Errorf("failed to stage changes: %w", err)
	}

	// Commit
	log.Info("Creating commit", "message", options.CommitMessage)
	commitHash, err := worktree.Commit(options.CommitMessage, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Smooth Operator",
			Email: "smooth-operator@k8s.io",
			When:  time.Now(),
		},
	})
	if err != nil {
		return result, fmt.Errorf("failed to commit: %w", err)
	}

	result.CommitSHA = commitHash.String()
	log.Info("Commit created", "sha", result.CommitSHA[:7])

	// Push to remote
	log.Info("Pushing to remote", "branch", branchName)
	err = repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs: []config.RefSpec{
			config.RefSpec("refs/heads/" + branchName + ":refs/heads/" + branchName),
		},
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		result.Errors = append(result.Errors, fmt.Sprintf("Push failed: %v", err))
		result.Warnings = append(result.Warnings, "Changes committed locally but not pushed")
		// Continue anyway - local commit succeeded
	} else {
		log.Info("Pushed successfully")
	}

	// If PR mode, create pull request
	if options.CommitMode == "pr" {
		log.Info("Creating pull request")
		prURL, err := g.createPullRequest(ctx, options, branchName, chatSessionID)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("PR creation failed: %v", err))
		} else {
			result.PRURL = prURL
			log.Info("Pull request created", "url", prURL)
		}
	}

	result.Success = len(result.Errors) == 0

	log.Info("Git operations complete",
		"success", result.Success,
		"commitSHA", result.CommitSHA[:7],
		"branch", result.Branch,
		"prURL", result.PRURL,
	)

	return result, nil
}

// createPullRequest creates a PR on GitHub/GitLab
func (g *GitClient) createPullRequest(ctx context.Context, options GitOptions, branch, chatSessionID string) (string, error) {
	// TODO: Implement actual GitHub/GitLab API integration
	// For Phase 5 MVP, return a placeholder URL
	prURL := fmt.Sprintf("https://github.com/example/repo/pull/new/%s", branch)
	return prURL, nil
}
