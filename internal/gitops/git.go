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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

	// httpClient is used for API calls; defaults to http.DefaultClient.
	// Tests can inject a custom client pointing at an httptest.Server.
	httpClient *http.Client

	// githubAPIBase is the base URL for the GitHub REST API.
	// Defaults to "https://api.github.com". Override in tests.
	githubAPIBase string

	// gitlabAPIBase is the base URL for the GitLab REST API.
	// Defaults to "https://gitlab.com/api/v4". Override in tests.
	gitlabAPIBase string
}

// NewGitClient creates a new Git client
func NewGitClient() *GitClient {
	return &GitClient{
		workDir:       "/tmp/smooth-operator-git",
		httpClient:    http.DefaultClient,
		githubAPIBase: "https://api.github.com",
		gitlabAPIBase: "https://gitlab.com/api/v4",
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

// repoInfo holds parsed repository owner/name and host.
type repoInfo struct {
	host  string // e.g. "github.com" or "gitlab.com"
	owner string
	name  string
}

// parseRemoteURL extracts host, owner, and repo name from SSH or HTTPS remote URLs.
//
// Supported forms:
//
//	HTTPS: https://github.com/owner/repo.git        https://github.com/owner/repo
//	       https://gitlab.com/group/sub/repo.git     (GitLab subgroups)
//	SSH:   git@github.com:owner/repo.git             ssh://git@github.com/owner/repo.git
//	       git@gitlab.com:group/subgroup/repo.git    (GitLab subgroups)
//
// For paths with subgroups (e.g. group/subgroup/repo), owner is set to the first
// path segment and name captures the remainder (subgroup/repo), preserving slashes.
func parseRemoteURL(rawURL string) (repoInfo, error) {
	rawURL = strings.TrimSpace(rawURL)

	// SSH SCP-like form: git@host:path/to/repo[.git]
	// Only matches when the URL does NOT contain "://", so that HTTPS/ssh:// URLs
	// are handled by the scheme-stripping path below.
	// The path after the colon may contain multiple slashes (GitLab subgroups).
	scpRe := regexp.MustCompile(`^(?:[^@]+@)?([^:/]+):(.+?)(?:\.git)?$`)
	if !strings.Contains(rawURL, "://") {
		if m := scpRe.FindStringSubmatch(rawURL); m != nil {
			fullPath := m[2]
			slashIdx := strings.Index(fullPath, "/")
			if slashIdx < 0 {
				return repoInfo{}, fmt.Errorf("cannot parse remote URL %q: no slash in repo path", rawURL)
			}
			return repoInfo{host: m[1], owner: fullPath[:slashIdx], name: fullPath[slashIdx+1:]}, nil
		}
	}

	// HTTPS / ssh:// forms – strip the scheme and leading slashes
	cleaned := rawURL
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://"} {
		if strings.HasPrefix(cleaned, scheme) {
			cleaned = strings.TrimPrefix(cleaned, scheme)
			break
		}
	}
	// Strip optional user info (git@)
	if at := strings.Index(cleaned, "@"); at != -1 {
		cleaned = cleaned[at+1:]
	}
	// parts[0]=host, parts[1]=owner, parts[2]=rest-of-path (may include subgroups)
	parts := strings.SplitN(cleaned, "/", 3)
	if len(parts) < 3 {
		return repoInfo{}, fmt.Errorf("cannot parse remote URL %q: expected host/owner/repo", rawURL)
	}
	host := parts[0]
	owner := parts[1]
	repoName := strings.TrimSuffix(parts[2], ".git")
	return repoInfo{host: host, owner: owner, name: repoName}, nil
}

// PRResult holds the URL and number of a newly created pull/merge request.
type PRResult struct {
	URL    string
	Number int
}

// createPullRequest creates a PR (GitHub) or MR (GitLab) via the REST API.
// If GITHUB_TOKEN (or GITLAB_TOKEN for GitLab) is unset it logs a warning and
// returns nil, nil (graceful degradation).
func (g *GitClient) createPullRequest(ctx context.Context, options GitOptions, branch, chatSessionID string) (string, error) {
	logger := log.FromContext(ctx)

	info, err := parseRemoteURL(options.RepoURL)
	if err != nil {
		return "", fmt.Errorf("createPullRequest: %w", err)
	}

	title := fmt.Sprintf("feat(smooth): %s", chatSessionID)
	body := fmt.Sprintf("Automated change created by Smooth Operator.\nChatSession: %s", chatSessionID)
	base := options.BaseBranch
	if base == "" {
		base = "main"
	}

	switch {
	case info.host == "gitlab.com" || strings.HasSuffix(info.host, ".gitlab.com"):
		return g.createGitLabMR(ctx, info, branch, base, title, body)
	default:
		// Treat everything else as GitHub (including GHE)
		token := os.Getenv("GITHUB_TOKEN")
		if token == "" {
			logger.Info("GITHUB_TOKEN not set; skipping PR creation")
			return "", nil
		}
		pr, err := g.createGitHubPR(ctx, info, token, branch, base, title, body)
		if err != nil {
			return "", err
		}
		if pr == nil {
			return "", nil
		}
		return pr.URL, nil
	}
}

// createGitHubPR posts to the GitHub REST API and returns the PR URL and number.
func (g *GitClient) createGitHubPR(
	ctx context.Context,
	info repoInfo,
	token, head, base, title, body string,
) (*PRResult, error) {
	apiURL := fmt.Sprintf("%s/repos/%s/%s/pulls", g.githubAPIBase, info.owner, info.name)

	payload, _ := json.Marshal(map[string]string{
		"title": title,
		"head":  head,
		"base":  base,
		"body":  body,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("github PR: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github PR: request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		if len(raw) > 512 {
			raw = raw[:512]
		}
		return nil, fmt.Errorf("github PR: unexpected status %d: %s", resp.StatusCode, raw)
	}

	var out struct {
		HTMLURL string `json:"html_url"`
		Number  int    `json:"number"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("github PR: parse response: %w", err)
	}
	return &PRResult{URL: out.HTMLURL, Number: out.Number}, nil
}

// createGitLabMR posts to the GitLab REST API and returns the MR URL.
func (g *GitClient) createGitLabMR(
	ctx context.Context,
	info repoInfo,
	sourceBranch, targetBranch, title, description string,
) (string, error) {
	logger := log.FromContext(ctx)

	token := os.Getenv("GITLAB_TOKEN")
	if token == "" {
		logger.Info("GITLAB_TOKEN not set; skipping MR creation")
		return "", nil
	}

	// GitLab project ID is the URL-encoded "owner/name" path.
	// url.PathEscape encodes the full path (including subgroup slashes) as %2F,
	// which is required by the GitLab Projects API.
	projectID := url.PathEscape(info.owner + "/" + info.name)
	apiURL := fmt.Sprintf("%s/projects/%s/merge_requests", g.gitlabAPIBase, projectID)

	payload, _ := json.Marshal(map[string]string{
		"source_branch": sourceBranch,
		"target_branch": targetBranch,
		"title":         title,
		"description":   description,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("gitlab MR: build request: %w", err)
	}
	req.Header.Set("Private-Token", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gitlab MR: request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		if len(raw) > 512 {
			raw = raw[:512]
		}
		return "", fmt.Errorf("gitlab MR: unexpected status %d: %s", resp.StatusCode, raw)
	}

	var out struct {
		WebURL string `json:"web_url"`
		IID    int    `json:"iid"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("gitlab MR: parse response: %w", err)
	}
	return out.WebURL, nil
}
