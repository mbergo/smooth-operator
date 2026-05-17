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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// parseRemoteURL
// ---------------------------------------------------------------------------

func TestParseRemoteURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rawURL    string
		wantHost  string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:      "github HTTPS with .git suffix",
			rawURL:    "https://github.com/acme/my-repo.git",
			wantHost:  "github.com",
			wantOwner: "acme",
			wantRepo:  "my-repo",
		},
		{
			name:      "github HTTPS without .git suffix",
			rawURL:    "https://github.com/acme/my-repo",
			wantHost:  "github.com",
			wantOwner: "acme",
			wantRepo:  "my-repo",
		},
		{
			name:      "github SSH SCP form",
			rawURL:    "git@github.com:acme/my-repo.git",
			wantHost:  "github.com",
			wantOwner: "acme",
			wantRepo:  "my-repo",
		},
		{
			name:      "github SSH SCP form without .git",
			rawURL:    "git@github.com:acme/my-repo",
			wantHost:  "github.com",
			wantOwner: "acme",
			wantRepo:  "my-repo",
		},
		{
			name:      "gitlab HTTPS",
			rawURL:    "https://gitlab.com/org/project.git",
			wantHost:  "gitlab.com",
			wantOwner: "org",
			wantRepo:  "project",
		},
		{
			name:      "gitlab SSH SCP form",
			rawURL:    "git@gitlab.com:org/project.git",
			wantHost:  "gitlab.com",
			wantOwner: "org",
			wantRepo:  "project",
		},
		{
			name:      "ssh:// scheme",
			rawURL:    "ssh://git@github.com/acme/repo.git",
			wantHost:  "github.com",
			wantOwner: "acme",
			wantRepo:  "repo",
		},
		{
			name:    "empty URL returns error",
			rawURL:  "",
			wantErr: true,
		},
		{
			name:    "URL without owner/repo returns error",
			rawURL:  "https://github.com/onlyone",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseRemoteURL(tc.rawURL)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil (result=%+v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.host != tc.wantHost {
				t.Errorf("host: want %q, got %q", tc.wantHost, got.host)
			}
			if got.owner != tc.wantOwner {
				t.Errorf("owner: want %q, got %q", tc.wantOwner, got.owner)
			}
			if got.name != tc.wantRepo {
				t.Errorf("repo: want %q, got %q", tc.wantRepo, got.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// createGitHubPR – mocked with httptest
// ---------------------------------------------------------------------------

func TestCreateGitHubPR_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected Authorization header: %q", r.Header.Get("Authorization"))
		}

		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		for _, field := range []string{"title", "head", "base"} {
			if body[field] == "" {
				t.Errorf("missing required field %q in request body", field)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"html_url": "https://github.com/acme/repo/pull/42",
			"number":   42,
		})
	}))
	defer srv.Close()

	gc := &GitClient{
		workDir:       t.TempDir(),
		httpClient:    srv.Client(),
		githubAPIBase: srv.URL,
		gitlabAPIBase: srv.URL,
	}
	info := repoInfo{host: "github.com", owner: "acme", name: "repo"}

	pr, err := gc.createGitHubPR(context.Background(), info, "test-token", "feature/x", "main", "Test PR", "body")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pr == nil {
		t.Fatal("expected non-nil PRResult")
	}
	if pr.Number != 42 {
		t.Errorf("PR number: want 42, got %d", pr.Number)
	}
	if pr.URL != "https://github.com/acme/repo/pull/42" {
		t.Errorf("PR URL: want https://github.com/acme/repo/pull/42, got %q", pr.URL)
	}
}

func TestCreateGitHubPR_ErrorStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Validation Failed"}`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	gc := &GitClient{
		workDir:       t.TempDir(),
		httpClient:    srv.Client(),
		githubAPIBase: srv.URL,
		gitlabAPIBase: srv.URL,
	}
	info := repoInfo{host: "github.com", owner: "acme", name: "repo"}
	_, err := gc.createGitHubPR(context.Background(), info, "tok", "branch", "main", "title", "body")
	if err == nil {
		t.Fatal("expected error for non-201 status, got nil")
	}
}

func TestCreateGitLabMR_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Private-Token") != "gl-test-token" {
			t.Errorf("unexpected Private-Token header: %q", r.Header.Get("Private-Token"))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"web_url": "https://gitlab.com/org/project/-/merge_requests/7",
			"iid":     7,
		})
	}))
	defer srv.Close()

	t.Setenv("GITLAB_TOKEN", "gl-test-token")

	gc := &GitClient{
		workDir:       t.TempDir(),
		httpClient:    srv.Client(),
		githubAPIBase: srv.URL,
		gitlabAPIBase: srv.URL,
	}
	info := repoInfo{host: "gitlab.com", owner: "org", name: "project"}

	url, err := gc.createGitLabMR(context.Background(), info, "feature/y", "main", "MR title", "desc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://gitlab.com/org/project/-/merge_requests/7" {
		t.Errorf("MR URL: want https://gitlab.com/org/project/-/merge_requests/7, got %q", url)
	}
}

// ---------------------------------------------------------------------------
// Graceful degradation when tokens are absent
// ---------------------------------------------------------------------------

func TestCreatePullRequest_NoToken_GracefulDegradation(t *testing.T) {
	// t.Setenv and t.Parallel are mutually exclusive; run sequentially.
	t.Setenv("GITHUB_TOKEN", "")

	gc := NewGitClient()
	opts := GitOptions{
		RepoURL:    "https://github.com/acme/repo.git",
		BaseBranch: "main",
		CommitMode: "pr",
	}
	url, err := gc.createPullRequest(context.Background(), opts, "smooth/test-session", "test-session")
	if err != nil {
		t.Fatalf("expected nil error on missing token, got: %v", err)
	}
	if url != "" {
		t.Errorf("expected empty URL on missing token, got %q", url)
	}
}

func TestCreatePullRequest_GitLabNoToken_GracefulDegradation(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "")

	gc := NewGitClient()
	opts := GitOptions{
		RepoURL:    "https://gitlab.com/org/project.git",
		BaseBranch: "main",
		CommitMode: "pr",
	}
	url, err := gc.createPullRequest(context.Background(), opts, "smooth/test-session", "test-session")
	if err != nil {
		t.Fatalf("expected nil error on missing gitlab token, got: %v", err)
	}
	if url != "" {
		t.Errorf("expected empty URL on missing gitlab token, got %q", url)
	}
}

// ---------------------------------------------------------------------------
// createPullRequest routing — GitHub token present, uses mocked server
// ---------------------------------------------------------------------------

func TestCreatePullRequest_GitHub_EndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"html_url": "https://github.com/acme/repo/pull/1",
			"number":   1,
		})
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "my-test-token")

	gc := &GitClient{
		workDir:       t.TempDir(),
		httpClient:    srv.Client(),
		githubAPIBase: srv.URL,
		gitlabAPIBase: srv.URL,
	}
	opts := GitOptions{
		RepoURL:    "https://github.com/acme/repo.git",
		BaseBranch: "main",
		CommitMode: "pr",
	}
	url, err := gc.createPullRequest(context.Background(), opts, "smooth/session-1", "session-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url == "" {
		t.Error("expected non-empty PR URL")
	}
}

