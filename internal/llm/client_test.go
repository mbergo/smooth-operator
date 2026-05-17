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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/mbergo/smooth-operator/internal/collector"
	"github.com/mbergo/smooth-operator/internal/metrics"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newTestAggregated returns a minimal AggregatedContext for client tests.
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

// anthropicMessageResponse builds a minimal Anthropic Messages API response body
// with the supplied content as the first (and only) text block.
func anthropicMessageResponse(content string) []byte {
	resp := map[string]any{
		"id":    "msg_test",
		"type":  "message",
		"role":  "assistant",
		"model": "claude-opus-4-7",
		"content": []map[string]any{
			{"type": "text", "text": content},
		},
		"stop_reason": "end_turn",
		"usage": map[string]any{
			"input_tokens":  100,
			"output_tokens": 50,
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	return b
}

// anthropicErrorBody returns a minimal Anthropic error response body.
func anthropicErrorBody(statusCode int, msg string) []byte {
	body := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "api_error",
			"message": msg,
		},
	}
	b, _ := json.Marshal(body)
	return b
}

// newTestClient wires a Client to the provided httptest server URL.
// SDK-level retries are disabled (option.WithMaxRetries(0)); the client's
// own retry loop is controlled by opts.MaxRetries.
func newTestClient(t *testing.T, serverURL string, opts ClientOptions) *Client {
	t.Helper()
	opts.Enabled = true
	if opts.MaxRequestsPerMinute == 0 {
		opts.MaxRequestsPerMinute = 1000
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 1024
	}
	if opts.Model == "" {
		opts.Model = string(anthropic.ModelClaudeOpus4_7)
	}
	api := anthropic.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL(serverURL),
		option.WithMaxRetries(0), // disable SDK built-in retries
	)
	return newClientFromAPI(api, opts)
}

// reasonerJSON returns a minimal valid ReasonerOutput JSON.
func reasonerJSON() string {
	return `{
		"inferredNeeds": [
			{"type":"HPA","reason":"high CPU","priority":"high","spec":"minReplicas=2"}
		],
		"confidence": 0.9,
		"risk": "low",
		"explanation": "scaling is needed"
	}`
}

// generatorJSON returns a minimal valid GeneratorOutput JSON.
func generatorJSON() string {
	return `{
		"patches": [
			{"kind":"HorizontalPodAutoscaler","yaml":"apiVersion: autoscaling/v2"}
		],
		"notes": "applied HPA"
	}`
}

// ---------------------------------------------------------------------------
// JSON parsing (no network)
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
			if err := json.Unmarshal([]byte(tc.raw), &resp); err == nil {
				t.Errorf("expected error for malformed JSON %q, got nil", tc.raw)
			}
		})
	}
}

func TestParseMissingRequiredFields_Defaults(t *testing.T) {
	raw := `{}`
	var resp LLMResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unexpected error on empty object: %v", err)
	}
	if resp.Confidence != 0 {
		t.Errorf("Confidence should default to 0, got %v", resp.Confidence)
	}
}

// ---------------------------------------------------------------------------
// RunReasoner
// ---------------------------------------------------------------------------

func TestRunReasoner_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(anthropicMessageResponse(reasonerJSON()))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{MaxRetries: 0})
	out, err := c.RunReasoner(context.Background(), "scale up", newTestAggregated())
	if err != nil {
		t.Fatalf("RunReasoner unexpected error: %v", err)
	}
	if len(out.InferredNeeds) != 1 {
		t.Errorf("InferredNeeds count: got %d, want 1", len(out.InferredNeeds))
	}
	if out.InferredNeeds[0].Type != "HPA" {
		t.Errorf("InferredNeeds[0].Type: got %q, want HPA", out.InferredNeeds[0].Type)
	}
	if out.Confidence != 0.9 {
		t.Errorf("Confidence: got %v, want 0.9", out.Confidence)
	}
	if out.Risk != "low" {
		t.Errorf("Risk: got %q, want low", out.Risk)
	}
}

func TestRunReasoner_FencedJSON(t *testing.T) {
	fenced := "```json\n" + reasonerJSON() + "\n```"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(anthropicMessageResponse(fenced))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{MaxRetries: 0})
	out, err := c.RunReasoner(context.Background(), "scale up", newTestAggregated())
	if err != nil {
		t.Fatalf("RunReasoner with fenced JSON unexpected error: %v", err)
	}
	if len(out.InferredNeeds) == 0 {
		t.Error("expected non-empty InferredNeeds from fenced JSON response")
	}
}

func TestRunReasoner_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(anthropicMessageResponse("not json"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{MaxRetries: 0})
	_, err := c.RunReasoner(context.Background(), "scale up", newTestAggregated())
	if err == nil {
		t.Fatal("expected error for malformed JSON response, got nil")
	}
	if !strings.Contains(err.Error(), "JSON decode") {
		t.Errorf("error should mention 'JSON decode', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RunGenerator
// ---------------------------------------------------------------------------

func TestRunGenerator_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(anthropicMessageResponse(generatorJSON()))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{MaxRetries: 0})
	reasoner := &ReasonerOutput{
		InferredNeeds: []InferredNeed{
			{Type: "HPA", Reason: "cpu", Priority: "high"},
		},
		Confidence: 0.9,
		Risk:       "low",
	}
	out, err := c.RunGenerator(context.Background(), "add HPA", "production", reasoner)
	if err != nil {
		t.Fatalf("RunGenerator unexpected error: %v", err)
	}
	if len(out.Patches) != 1 {
		t.Errorf("Patches count: got %d, want 1", len(out.Patches))
	}
	if out.Patches[0].Kind != "HorizontalPodAutoscaler" {
		t.Errorf("Patches[0].Kind: got %q, want HorizontalPodAutoscaler", out.Patches[0].Kind)
	}
}

// ---------------------------------------------------------------------------
// GeneratePlan full chain
// ---------------------------------------------------------------------------

func TestGeneratePlan_FullChain(t *testing.T) {
	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(callCount.Add(1))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		switch n {
		case 1:
			// First call: reasoner
			_, _ = w.Write(anthropicMessageResponse(reasonerJSON()))
		default:
			// Second call: generator
			_, _ = w.Write(anthropicMessageResponse(generatorJSON()))
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{MaxRetries: 0})
	resp, err := c.GeneratePlan(context.Background(), "scale up", newTestAggregated())
	if err != nil {
		t.Fatalf("GeneratePlan unexpected error: %v", err)
	}
	if len(resp.InferredNeeds) == 0 {
		t.Error("expected non-empty InferredNeeds in combined response")
	}
	if len(resp.Patches) == 0 {
		t.Error("expected non-empty Patches in combined response")
	}
	if int(callCount.Load()) != 2 {
		t.Errorf("expected 2 calls (reasoner + generator), got %d", callCount.Load())
	}
}

func TestGeneratePlan_NoNeedsSkipsGenerator(t *testing.T) {
	var callCount atomic.Int32

	noNeedsJSON := `{"inferredNeeds":[],"confidence":1.0,"risk":"low","explanation":"nothing needed"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(anthropicMessageResponse(noNeedsJSON))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{MaxRetries: 0})
	resp, err := c.GeneratePlan(context.Background(), "review", newTestAggregated())
	if err != nil {
		t.Fatalf("GeneratePlan unexpected error: %v", err)
	}
	if len(resp.InferredNeeds) != 0 {
		t.Errorf("expected empty InferredNeeds, got %d", len(resp.InferredNeeds))
	}
	if int(callCount.Load()) != 1 {
		t.Errorf("generator must NOT be called when inferredNeeds is empty; got %d calls", callCount.Load())
	}
}

func TestGeneratePlan_GeneratorFails_ReturnsPartial(t *testing.T) {
	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(callCount.Add(1))
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(anthropicMessageResponse(reasonerJSON()))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write(anthropicErrorBody(http.StatusInternalServerError, "generator exploded"))
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{MaxRetries: 0})
	resp, err := c.GeneratePlan(context.Background(), "scale up", newTestAggregated())

	// Must return an error wrapping "generator stage".
	if err == nil {
		t.Fatal("expected error when generator fails, got nil")
	}
	if !strings.Contains(err.Error(), "generator stage") {
		t.Errorf("error should wrap 'generator stage', got: %v", err)
	}
	// The partial response should still have inferredNeeds from the reasoner.
	if resp == nil {
		t.Fatal("expected non-nil partial response even when generator fails")
	}
	if len(resp.InferredNeeds) == 0 {
		t.Error("partial response should have InferredNeeds from reasoner")
	}
	if len(resp.Patches) != 0 {
		t.Errorf("partial response should have empty Patches, got %d", len(resp.Patches))
	}
}

// ---------------------------------------------------------------------------
// Retry behaviour
// ---------------------------------------------------------------------------

func TestRetry_429(t *testing.T) {
	// Server returns 429 on first call, 200 on second.
	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(callCount.Add(1))
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write(anthropicErrorBody(http.StatusTooManyRequests, "rate limited"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(anthropicMessageResponse(reasonerJSON()))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{
		MaxRetries: 2,
		RetryDelay: 1 * time.Millisecond,
	})
	out, err := c.RunReasoner(context.Background(), "test", newTestAggregated())
	if err != nil {
		t.Fatalf("expected success after retry on 429, got: %v", err)
	}
	if out == nil {
		t.Fatal("expected non-nil response after retry")
	}
	if int(callCount.Load()) != 2 {
		t.Errorf("expected 2 calls (1 fail + 1 retry success), got %d", callCount.Load())
	}
}

func TestRetry_5xx(t *testing.T) {
	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(callCount.Add(1))
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write(anthropicErrorBody(http.StatusServiceUnavailable, "service unavailable"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(anthropicMessageResponse(reasonerJSON()))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{
		MaxRetries: 2,
		RetryDelay: 1 * time.Millisecond,
	})
	out, err := c.RunReasoner(context.Background(), "test", newTestAggregated())
	if err != nil {
		t.Fatalf("expected success after retry on 503, got: %v", err)
	}
	if out == nil {
		t.Fatal("expected non-nil response after retry")
	}
}

func TestRetry_Exhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(anthropicErrorBody(http.StatusInternalServerError, "always fails"))
	}))
	defer srv.Close()

	const maxRetries = 2
	c := newTestClient(t, srv.URL, ClientOptions{
		MaxRetries: maxRetries,
		RetryDelay: 1 * time.Millisecond,
	})
	_, err := c.RunReasoner(context.Background(), "test", newTestAggregated())
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
}

func TestRetry_400NoRetry(t *testing.T) {
	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(anthropicErrorBody(http.StatusBadRequest, "bad request"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, ClientOptions{
		MaxRetries: 3,
		RetryDelay: 1 * time.Millisecond,
	})
	_, err := c.RunReasoner(context.Background(), "test", newTestAggregated())
	if err == nil {
		t.Fatal("expected error for 400, got nil")
	}
	if int(callCount.Load()) != 1 {
		t.Errorf("server must be called exactly once for 400 (non-retryable); got %d", callCount.Load())
	}
}

// ---------------------------------------------------------------------------
// Disabled client
// ---------------------------------------------------------------------------

func TestDisabledClient(t *testing.T) {
	c, err := NewClient(ClientOptions{Enabled: false})
	if err != nil {
		t.Fatalf("NewClient with Enabled=false should not error: %v", err)
	}
	if c.IsEnabled() {
		t.Error("client should report IsEnabled()=false")
	}

	_, err = c.GeneratePlan(context.Background(), "test", newTestAggregated())
	if err == nil {
		t.Fatal("GeneratePlan on disabled client should return an error")
	}
	if !strings.Contains(err.Error(), "not enabled") {
		t.Errorf("error should mention 'not enabled', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Request body inspection tests
// ---------------------------------------------------------------------------

// captureTransport records the last request body and delegates to the real
// server for the response.
type captureTransport struct {
	lastBody []byte
	delegate http.RoundTripper
}

func (ct *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err == nil {
			ct.lastBody = body
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
	}
	return ct.delegate.RoundTrip(req)
}

// newTestClientWithCapture creates a client that captures outbound request bodies
// while routing to a real httptest.Server for responses.
func newTestClientWithCapture(t *testing.T, serverURL string, ct *captureTransport, opts ClientOptions) *Client {
	t.Helper()
	opts.Enabled = true
	if opts.MaxRequestsPerMinute == 0 {
		opts.MaxRequestsPerMinute = 1000
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 1024
	}
	if opts.Model == "" {
		opts.Model = string(anthropic.ModelClaudeOpus4_7)
	}
	ct.delegate = &http.Transport{}
	api := anthropic.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL(serverURL),
		option.WithMaxRetries(0),
		option.WithHTTPClient(&http.Client{Transport: ct}),
	)
	return newClientFromAPI(api, opts)
}

func TestPromptCaching_SystemBlockMarked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(anthropicMessageResponse(reasonerJSON()))
	}))
	defer srv.Close()

	ct := &captureTransport{}
	c := newTestClientWithCapture(t, srv.URL, ct, ClientOptions{MaxRetries: 0})

	_, err := c.RunReasoner(context.Background(), "test", newTestAggregated())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ct.lastBody) == 0 {
		t.Fatal("no request body captured")
	}

	var body map[string]any
	if err := json.Unmarshal(ct.lastBody, &body); err != nil {
		t.Fatalf("failed to parse request body as JSON: %v", err)
	}

	system, ok := body["system"]
	if !ok {
		t.Fatal("request body missing 'system' field")
	}
	systemArr, ok := system.([]any)
	if !ok || len(systemArr) == 0 {
		t.Fatalf("'system' should be a non-empty array, got: %T %v", system, system)
	}
	firstBlock, ok := systemArr[0].(map[string]any)
	if !ok {
		t.Fatalf("system[0] should be an object, got: %T", systemArr[0])
	}
	if _, hasCacheControl := firstBlock["cache_control"]; !hasCacheControl {
		t.Error("system block[0] should have a 'cache_control' field (prompt caching)")
	}
}

func TestAdaptiveThinking_Set(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(anthropicMessageResponse(reasonerJSON()))
	}))
	defer srv.Close()

	ct := &captureTransport{}
	c := newTestClientWithCapture(t, srv.URL, ct, ClientOptions{MaxRetries: 0})

	_, err := c.RunReasoner(context.Background(), "test", newTestAggregated())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ct.lastBody) == 0 {
		t.Fatal("no request body captured")
	}

	var body map[string]any
	if err := json.Unmarshal(ct.lastBody, &body); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	thinking, ok := body["thinking"]
	if !ok {
		t.Fatal("request body missing 'thinking' field")
	}
	thinkingObj, ok := thinking.(map[string]any)
	if !ok {
		t.Fatalf("'thinking' should be an object, got: %T", thinking)
	}
	thinkingType, _ := thinkingObj["type"].(string)
	if thinkingType != "adaptive" {
		t.Errorf("thinking.type should be 'adaptive', got %q", thinkingType)
	}
}

func TestModelIsOpus47(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(anthropicMessageResponse(reasonerJSON()))
	}))
	defer srv.Close()

	ct := &captureTransport{}
	c := newTestClientWithCapture(t, srv.URL, ct, ClientOptions{
		MaxRetries: 0,
		Model:      string(anthropic.ModelClaudeOpus4_7),
	})

	_, err := c.RunReasoner(context.Background(), "test", newTestAggregated())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ct.lastBody) == 0 {
		t.Fatal("no request body captured")
	}

	var body map[string]any
	if err := json.Unmarshal(ct.lastBody, &body); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	model, _ := body["model"].(string)
	if model != "claude-opus-4-7" {
		t.Errorf("model in request: got %q, want %q", model, "claude-opus-4-7")
	}
}

// ---------------------------------------------------------------------------
// isRetryableError unit tests
// ---------------------------------------------------------------------------

func TestIsRetryableError_anthropicError(t *testing.T) {
	// Construct *anthropic.Error values via the real HTTP stack path by using
	// a mock server and checking that our isRetryableError identifies them.
	cases := []struct {
		name       string
		statusCode int
		wantRetry  bool
	}{
		{"429 rate limit", http.StatusTooManyRequests, true},
		{"500 server error", http.StatusInternalServerError, true},
		{"503 unavailable", http.StatusServiceUnavailable, true},
		{"400 bad request", http.StatusBadRequest, false},
		{"401 unauthorized", http.StatusUnauthorized, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write(anthropicErrorBody(tc.statusCode, "test error"))
			}))
			defer srv.Close()

			api := anthropic.NewClient(
				option.WithAPIKey("test-key"),
				option.WithBaseURL(srv.URL),
				option.WithMaxRetries(0),
			)
			_, err := api.Messages.New(context.Background(), anthropic.MessageNewParams{
				Model:     anthropic.ModelClaudeOpus4_7,
				MaxTokens: 10,
				Messages: []anthropic.MessageParam{
					anthropic.NewUserMessage(anthropic.NewTextBlock("hi")),
				},
			})
			if err == nil {
				t.Skipf("server returned %d but no error; cannot test isRetryableError", tc.statusCode)
			}
			got := isRetryableError(err)
			if got != tc.wantRetry {
				t.Errorf("isRetryableError for %d: got %v, want %v", tc.statusCode, got, tc.wantRetry)
			}
		})
	}
}

func TestIsRetryableError_nilAndGeneric(t *testing.T) {
	if isRetryableError(nil) {
		t.Error("isRetryableError(nil) should return false")
	}
	if isRetryableError(fmt.Errorf("network timeout")) {
		t.Error("isRetryableError on plain error should return false")
	}
}

// ---------------------------------------------------------------------------
// NewClient construction
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
