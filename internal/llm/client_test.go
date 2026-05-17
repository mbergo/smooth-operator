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

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mbergo/smooth-operator/internal/collector"
	"github.com/mbergo/smooth-operator/internal/metrics"
	openai "github.com/sashabaranov/go-openai"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newTestAggregated returns a minimal AggregatedContext suitable for client tests.
func newTestAggregated() *collector.AggregatedContext {
	return &collector.AggregatedContext{
		ClusterContext: &collector.ClusterContext{
			TargetNamespace: "test-ns",
			Deployments: []collector.DeploymentInfo{
				{Name: "web", Replicas: 1, ReadyReplicas: 1},
			},
		},
		MetricsSnapshots: map[string]*metrics.MetricsSnapshot{},
		Summary: collector.ContextSummary{
			TextSummary: "test summary",
		},
	}
}

// chatCompletionResponse crafts a minimal OpenAI-format JSON response with the
// supplied content string as the first choice's message body.
func chatCompletionResponse(content string) []byte {
	resp := openai.ChatCompletionResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "gpt-4-turbo-preview",
		Choices: []openai.ChatCompletionChoice{
			{
				Index: 0,
				Message: openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: content,
				},
				FinishReason: openai.FinishReasonStop,
			},
		},
		Usage: openai.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	return b
}

// openaiErrorBody returns an OpenAI-format error body for the given code/message.
func openaiErrorBody(statusCode int, message string) []byte {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "server_error",
			"code":    fmt.Sprintf("%d", statusCode),
		},
	})
	return body
}

// newTestClient builds an LLM Client wired to the provided httptest server URL.
func newTestClient(t *testing.T, serverURL string, opts ClientOptions) *Client {
	t.Helper()
	cfg := openai.DefaultConfig("test-key")
	cfg.BaseURL = serverURL + "/v1"
	oaiClient := openai.NewClientWithConfig(cfg)
	opts.Enabled = true
	if opts.MaxRequestsPerMinute == 0 {
		opts.MaxRequestsPerMinute = 60
	}
	if opts.Model == "" {
		opts.Model = "gpt-4-turbo-preview"
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 4096
	}
	return newClientFromOpenAI(oaiClient, opts)
}

// ---------------------------------------------------------------------------
// JSON parsing tests (no network)
// ---------------------------------------------------------------------------

func TestParseValidLLMResponse(t *testing.T) {
	raw := `{
		"inferredNeeds": [
			{"type": "HPA", "reason": "high CPU", "priority": "high", "spec": "minReplicas: 2"}
		],
		"patches": [
			{"kind": "Deployment", "yaml": "apiVersion: apps/v1"}
		],
		"confidence": 0.9,
		"risk": "low",
		"explanation": "scaling needed"
	}`

	var resp LLMResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if len(resp.InferredNeeds) != 1 {
		t.Errorf("InferredNeeds: got %d, want 1", len(resp.InferredNeeds))
	}
	if resp.InferredNeeds[0].Type != "HPA" {
		t.Errorf("InferredNeeds[0].Type: got %q, want HPA", resp.InferredNeeds[0].Type)
	}
	if len(resp.Patches) != 1 {
		t.Errorf("Patches: got %d, want 1", len(resp.Patches))
	}
	if resp.Confidence != 0.9 {
		t.Errorf("Confidence: got %v, want 0.9", resp.Confidence)
	}
	if resp.Risk != "low" {
		t.Errorf("Risk: got %q, want low", resp.Risk)
	}
	if resp.Explanation != "scaling needed" {
		t.Errorf("Explanation: got %q, want 'scaling needed'", resp.Explanation)
	}
}

func TestParseValidLLMResponse_EmptyArrays(t *testing.T) {
	raw := `{"inferredNeeds": [], "patches": [], "confidence": 0.5, "risk": "med", "explanation": "nothing to do"}`

	var resp LLMResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if len(resp.InferredNeeds) != 0 {
		t.Errorf("expected empty InferredNeeds, got %d", len(resp.InferredNeeds))
	}
	if len(resp.Patches) != 0 {
		t.Errorf("expected empty Patches, got %d", len(resp.Patches))
	}
}

func TestParseMalformedJSON_Error(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty string", ""},
		{"not json", "this is not json at all"},
		{"truncated", `{"inferredNeeds": [`},
		{"wrong type for confidence", `{"confidence": "not-a-number"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var resp LLMResponse
			err := json.Unmarshal([]byte(tc.raw), &resp)
			if err == nil {
				t.Errorf("expected error for malformed JSON %q, got nil", tc.raw)
			}
		})
	}
}

func TestParseMissingRequiredFields_Defaults(t *testing.T) {
	// JSON is valid but omits all fields — Go sets zero values.
	// The application checks for non-zero confidence/risk at a higher layer,
	// but the JSON decoder itself must not error.
	raw := `{}`

	var resp LLMResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unexpected error on empty object: %v", err)
	}
	if resp.Confidence != 0 {
		t.Errorf("Confidence should default to 0, got %v", resp.Confidence)
	}
	if resp.Risk != "" {
		t.Errorf("Risk should default to empty string, got %q", resp.Risk)
	}
}

// ---------------------------------------------------------------------------
// Integration tests using httptest.Server
// ---------------------------------------------------------------------------

func TestGeneratePlan_Success(t *testing.T) {
	validContent := `{
		"inferredNeeds": [{"type": "HPA", "reason": "CPU", "priority": "high", "spec": ""}],
		"patches": [],
		"confidence": 0.85,
		"risk": "low",
		"explanation": "add HPA"
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(chatCompletionResponse(validContent))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{})
	resp, err := c.GeneratePlan(context.Background(), "add HPA", newTestAggregated())
	if err != nil {
		t.Fatalf("GeneratePlan unexpected error: %v", err)
	}
	if resp.Risk != "low" {
		t.Errorf("Risk: got %q, want low", resp.Risk)
	}
	if len(resp.InferredNeeds) != 1 {
		t.Errorf("InferredNeeds count: got %d, want 1", len(resp.InferredNeeds))
	}
}

func TestGeneratePlan_MalformedJSONFromServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Valid OpenAI envelope but the content is garbage JSON.
		_, _ = w.Write(chatCompletionResponse("not valid json {{{"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{})
	_, err := c.GeneratePlan(context.Background(), "test", newTestAggregated())
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention parse failure, got: %v", err)
	}
}

func TestGeneratePlan_EmptyChoices(t *testing.T) {
	// Server returns a valid OpenAI envelope with an empty Choices slice.
	empty := openai.ChatCompletionResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Model:   "gpt-4-turbo-preview",
		Choices: []openai.ChatCompletionChoice{},
	}
	b, _ := json.Marshal(empty)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{})
	_, err := c.GeneratePlan(context.Background(), "test", newTestAggregated())
	if err == nil {
		t.Fatal("expected error for empty choices, got nil")
	}
}

func TestGeneratePlan_Retry429(t *testing.T) {
	const wantAttempts = 3 // 1 initial + 2 retries
	var callCount atomic.Int32

	validContent := `{"inferredNeeds":[],"patches":[],"confidence":0.7,"risk":"low","explanation":"ok"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(callCount.Add(1))
		if n < wantAttempts {
			// Return 429 for the first (wantAttempts-1) calls
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write(openaiErrorBody(http.StatusTooManyRequests, "rate limited"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(chatCompletionResponse(validContent))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{
		MaxRetries: 3,
		RetryDelay: 1 * time.Millisecond, // fast for tests
	})
	resp, err := c.GeneratePlan(context.Background(), "test", newTestAggregated())
	if err != nil {
		t.Fatalf("unexpected error after retries: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if int(callCount.Load()) != wantAttempts {
		t.Errorf("server call count: got %d, want %d", callCount.Load(), wantAttempts)
	}
}

func TestGeneratePlan_Retry5xx(t *testing.T) {
	const wantAttempts = 2 // 1 initial 500 + 1 retry succeeds
	var callCount atomic.Int32

	validContent := `{"inferredNeeds":[],"patches":[],"confidence":0.6,"risk":"med","explanation":"recovered"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(callCount.Add(1))
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write(openaiErrorBody(http.StatusInternalServerError, "internal error"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(chatCompletionResponse(validContent))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{
		MaxRetries: 2,
		RetryDelay: 1 * time.Millisecond,
	})
	resp, err := c.GeneratePlan(context.Background(), "test", newTestAggregated())
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if resp.Risk != "med" {
		t.Errorf("Risk: got %q, want med", resp.Risk)
	}
	if int(callCount.Load()) != wantAttempts {
		t.Errorf("server call count: got %d, want %d", callCount.Load(), wantAttempts)
	}
}

func TestGeneratePlan_ExhaustedRetries_Returns5xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(openaiErrorBody(http.StatusInternalServerError, "always fails"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{
		MaxRetries: 2,
		RetryDelay: 1 * time.Millisecond,
	})
	_, err := c.GeneratePlan(context.Background(), "test", newTestAggregated())
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if !strings.Contains(err.Error(), "OpenAI API error") {
		t.Errorf("error should wrap OpenAI API error, got: %v", err)
	}
}

func TestGeneratePlan_Non5xxNoRetry(t *testing.T) {
	// 400 Bad Request is not retryable; only one call should be made.
	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(openaiErrorBody(http.StatusBadRequest, "bad request"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{
		MaxRetries: 3,
		RetryDelay: 1 * time.Millisecond,
	})
	_, err := c.GeneratePlan(context.Background(), "test", newTestAggregated())
	if err == nil {
		t.Fatal("expected error for 400, got nil")
	}
	if int(callCount.Load()) != 1 {
		t.Errorf("server should be called exactly once for non-retryable error; got %d", callCount.Load())
	}
}

func TestGeneratePlan_TokenCapRespected(t *testing.T) {
	const wantMaxTokens = 512

	var receivedMaxTokens int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody openai.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err == nil {
			receivedMaxTokens = reqBody.MaxTokens
		}
		validContent := `{"inferredNeeds":[],"patches":[],"confidence":0.5,"risk":"low","explanation":""}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(chatCompletionResponse(validContent))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{
		MaxTokens: wantMaxTokens,
	})
	_, err := c.GeneratePlan(context.Background(), "test", newTestAggregated())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedMaxTokens != wantMaxTokens {
		t.Errorf("MaxTokens in request: got %d, want %d", receivedMaxTokens, wantMaxTokens)
	}
}

func TestGeneratePlan_ContextCancelledDuringRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(openaiErrorBody(http.StatusTooManyRequests, "rate limited"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	c := newTestClient(t, srv.URL, ClientOptions{
		MaxRetries: 10,
		RetryDelay: 100 * time.Millisecond, // long enough that cancel fires first
	})

	// Cancel after a short time so the retry sleep is interrupted.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := c.GeneratePlan(ctx, "test", newTestAggregated())
	if err == nil {
		t.Fatal("expected error from context cancellation, got nil")
	}
}

// ---------------------------------------------------------------------------
// isRetryableError unit tests
// ---------------------------------------------------------------------------

func TestIsRetryableError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantTrue bool
	}{
		{"nil", nil, false},
		{"429 APIError", &openai.APIError{HTTPStatusCode: 429}, true},
		{"500 APIError", &openai.APIError{HTTPStatusCode: 500}, true},
		{"503 APIError", &openai.APIError{HTTPStatusCode: 503}, true},
		{"400 APIError", &openai.APIError{HTTPStatusCode: 400}, false},
		{"401 APIError", &openai.APIError{HTTPStatusCode: 401}, false},
		{"generic error", fmt.Errorf("some network error"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isRetryableError(tc.err)
			if got != tc.wantTrue {
				t.Errorf("isRetryableError(%v) = %v, want %v", tc.err, got, tc.wantTrue)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NewClient disabled path
// ---------------------------------------------------------------------------

func TestNewClient_Disabled(t *testing.T) {
	c, err := NewClient(ClientOptions{Enabled: false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.IsEnabled() {
		t.Error("client should be disabled")
	}
}

func TestNewClient_MissingAPIKey(t *testing.T) {
	_, err := NewClient(ClientOptions{Enabled: true, APIKey: ""})
	if err == nil {
		t.Fatal("expected error for missing API key, got nil")
	}
}

func TestGeneratePlan_DisabledClient(t *testing.T) {
	c, _ := NewClient(ClientOptions{Enabled: false})
	_, err := c.GeneratePlan(context.Background(), "test", newTestAggregated())
	if err == nil {
		t.Fatal("expected error from disabled client, got nil")
	}
}
