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
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/mbergo/smooth-operator/internal/collector"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Client orchestrates the two-stage Reasoner -> Generator chain against
// Anthropic's Messages API.
type Client struct {
	api           anthropic.Client
	promptBuilder *PromptBuilder

	model     anthropic.Model
	maxTokens int64
	effort    anthropic.OutputConfigEffort
	enabled   bool

	maxRetries  int
	retryDelay  time.Duration
	rateLimiter *RateLimiter
}

// RateLimiter is a simple token bucket used to cap requests per minute.
type RateLimiter struct {
	mu         sync.Mutex
	tokens     int
	maxTokens  int
	refillRate time.Duration
	lastRefill time.Time
}

// ClientOptions configures the Anthropic client.
type ClientOptions struct {
	APIKey    string
	BaseURL   string // override for tests
	Model     string
	MaxTokens int64
	Effort    anthropic.OutputConfigEffort
	Enabled   bool

	MaxRequestsPerMinute int
	MaxRetries           int
	RetryDelay           time.Duration
}

// DefaultClientOptions returns sensible defaults for Opus 4.7.
func DefaultClientOptions() ClientOptions {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	return ClientOptions{
		APIKey:               apiKey,
		Model:                string(anthropic.ModelClaudeOpus4_7),
		MaxTokens:            16000,
		Effort:               anthropic.OutputConfigEffortXhigh,
		Enabled:              apiKey != "",
		MaxRequestsPerMinute: 10,
		MaxRetries:           3,
		RetryDelay:           500 * time.Millisecond,
	}
}

// NewClient builds a Client. Returns a disabled client (no error) when
// ANTHROPIC_API_KEY is empty so the operator can run in policy-only mode.
func NewClient(options ClientOptions) (*Client, error) {
	if !options.Enabled {
		return &Client{enabled: false, promptBuilder: NewPromptBuilder()}, nil
	}
	if options.APIKey == "" {
		return nil, fmt.Errorf("Anthropic API key is required (set ANTHROPIC_API_KEY env var)")
	}
	opts := []option.RequestOption{option.WithAPIKey(options.APIKey)}
	if options.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(options.BaseURL))
	}
	api := anthropic.NewClient(opts...)
	return newClientFromAPI(api, options), nil
}

// newClientFromAPI shares the construction path between production and tests.
func newClientFromAPI(api anthropic.Client, options ClientOptions) *Client {
	retries := options.MaxRetries
	if retries < 0 {
		retries = 0
	}
	delay := options.RetryDelay
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}
	effort := options.Effort
	if effort == "" {
		effort = anthropic.OutputConfigEffortXhigh
	}
	rpm := options.MaxRequestsPerMinute
	if rpm <= 0 {
		rpm = 10
	}
	model := anthropic.Model(options.Model)
	if model == "" {
		model = anthropic.ModelClaudeOpus4_7
	}
	maxTokens := options.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16000
	}
	return &Client{
		api:           api,
		promptBuilder: NewPromptBuilder(),
		model:         model,
		maxTokens:     maxTokens,
		effort:        effort,
		enabled:       true,
		maxRetries:    retries,
		retryDelay:    delay,
		rateLimiter: &RateLimiter{
			tokens:     rpm,
			maxTokens:  rpm,
			refillRate: time.Minute / time.Duration(maxInt(rpm, 1)),
			lastRefill: time.Now(),
		},
	}
}

// IsEnabled reports whether the client will call the API.
func (c *Client) IsEnabled() bool { return c.enabled }

// PromptBuilder exposes the underlying builder so callers can inject custom
// policy summaries.
func (c *Client) PromptBuilder() *PromptBuilder { return c.promptBuilder }

// ----------------------------------------------------------------------------
// Public API
// ----------------------------------------------------------------------------

// GeneratePlan runs the full Reasoner -> Generator chain and returns the
// combined LLMResponse expected by the planner. If the Generator fails but
// the Reasoner succeeded, the returned response contains inferredNeeds + risk
// + confidence and an empty Patches slice — the caller may still use it for
// Suggest mode.
func (c *Client) GeneratePlan(
	ctx context.Context,
	userPrompt string,
	aggregated *collector.AggregatedContext,
) (*LLMResponse, error) {
	if !c.enabled {
		return nil, fmt.Errorf("LLM client is not enabled")
	}
	logger := log.FromContext(ctx)

	reasoner, err := c.RunReasoner(ctx, userPrompt, aggregated)
	if err != nil {
		return nil, fmt.Errorf("reasoner stage: %w", err)
	}

	ns := ""
	if aggregated != nil {
		ns = aggregated.ClusterContext.TargetNamespace
	}

	combined := &LLMResponse{
		InferredNeeds: reasoner.InferredNeeds,
		Confidence:    reasoner.Confidence,
		Risk:          reasoner.Risk,
		Explanation:   reasoner.Explanation,
	}

	// If the Reasoner found nothing to do, skip the Generator entirely.
	if len(reasoner.InferredNeeds) == 0 {
		logger.Info("Reasoner returned no inferred needs; skipping Generator stage")
		return combined, nil
	}

	generator, err := c.RunGenerator(ctx, userPrompt, ns, reasoner)
	if err != nil {
		// Reasoner output is still useful for Suggest mode — return it but
		// surface the Generator failure as a wrapped error so the caller can
		// decide whether to apply.
		logger.Error(err, "Generator stage failed; returning Reasoner output without patches")
		return combined, fmt.Errorf("generator stage: %w", err)
	}

	combined.Patches = generator.Patches
	if generator.Notes != "" {
		if combined.Explanation == "" {
			combined.Explanation = generator.Notes
		} else {
			combined.Explanation = combined.Explanation + "\n\nGenerator notes: " + generator.Notes
		}
	}
	return combined, nil
}

// RunReasoner executes stage 1 only. Exported for direct testing.
func (c *Client) RunReasoner(
	ctx context.Context,
	userPrompt string,
	aggregated *collector.AggregatedContext,
) (*ReasonerOutput, error) {
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit: %w", err)
	}

	system := c.promptBuilder.BuildReasonerSystem()
	user := c.promptBuilder.BuildReasonerUser(userPrompt, aggregated)

	raw, usage, err := c.callMessages(ctx, "reasoner", system, user)
	if err != nil {
		return nil, err
	}

	var out ReasonerOutput
	if err := decodeJSON(raw, &out); err != nil {
		logger := log.FromContext(ctx)
		logger.V(1).Info("reasoner JSON decode failed", "rawSnippet", truncate(raw, 512))
		return nil, fmt.Errorf("reasoner JSON decode: %w", err)
	}
	logUsage(ctx, "reasoner", usage)
	return &out, nil
}

// RunGenerator executes stage 2 only. Exported for direct testing.
func (c *Client) RunGenerator(
	ctx context.Context,
	userPrompt string,
	namespace string,
	reasoner *ReasonerOutput,
) (*GeneratorOutput, error) {
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit: %w", err)
	}
	system := c.promptBuilder.BuildGeneratorSystem()
	user := c.promptBuilder.BuildGeneratorUser(userPrompt, namespace, reasoner)

	raw, usage, err := c.callMessages(ctx, "generator", system, user)
	if err != nil {
		return nil, err
	}

	var out GeneratorOutput
	if err := decodeJSON(raw, &out); err != nil {
		logger := log.FromContext(ctx)
		logger.V(1).Info("generator JSON decode failed", "rawSnippet", truncate(raw, 512))
		return nil, fmt.Errorf("generator JSON decode: %w", err)
	}
	logUsage(ctx, "generator", usage)
	return &out, nil
}

// ----------------------------------------------------------------------------
// Internals
// ----------------------------------------------------------------------------

// callMessages issues a single Messages.New request with prompt caching on the
// system block + adaptive thinking + structured JSON output via Opus 4.7.
func (c *Client) callMessages(
	ctx context.Context,
	stage string,
	system string,
	user string,
) (string, anthropic.Usage, error) {
	logger := log.FromContext(ctx).WithValues("stage", stage, "model", string(c.model))

	adaptive := anthropic.ThinkingConfigAdaptiveParam{}
	params := anthropic.MessageNewParams{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		System: []anthropic.TextBlockParam{{
			Text:         system,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
		},
		Thinking: anthropic.ThinkingConfigParamUnion{OfAdaptive: &adaptive},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: c.effort,
		},
	}

	start := time.Now()
	var resp *anthropic.Message
	var err error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			logger.V(1).Info("retrying", "attempt", attempt, "max", c.maxRetries)
			select {
			case <-time.After(c.retryDelay):
			case <-ctx.Done():
				return "", anthropic.Usage{}, fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			}
		}
		resp, err = c.api.Messages.New(ctx, params)
		if err == nil {
			break
		}
		if !isRetryableError(err) {
			break
		}
	}
	if err != nil {
		// Log the full SDK error at V(1) only — it may contain request headers
		// or other operator-only diagnostic material that must not reach callers.
		logger.V(1).Info("messages.new failed (full error)", "err", err.Error())
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return "", anthropic.Usage{}, fmt.Errorf("anthropic messages.new failed: status=%d", apiErr.StatusCode)
		}
		return "", anthropic.Usage{}, fmt.Errorf("anthropic messages.new failed: transport error")
	}
	if resp == nil {
		return "", anthropic.Usage{}, fmt.Errorf("anthropic messages.new: nil response")
	}

	logger.V(1).Info("messages.new succeeded",
		"duration", time.Since(start).String(),
		"stopReason", string(resp.StopReason),
	)

	// Extract the first text block; thinking blocks are skipped.
	text := extractText(resp)
	if text == "" {
		return "", resp.Usage, fmt.Errorf("anthropic messages.new: no text block in response")
	}
	return text, resp.Usage, nil
}

func extractText(msg *anthropic.Message) string {
	var b strings.Builder
	for _, block := range msg.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			b.WriteString(v.Text)
		}
	}
	return b.String()
}

// decodeJSON tolerates models that wrap output in ```json fences despite
// the system prompt forbidding it.
func decodeJSON(raw string, out any) error {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	return json.Unmarshal([]byte(s), out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func logUsage(ctx context.Context, stage string, u anthropic.Usage) {
	log.FromContext(ctx).V(1).Info("token usage",
		"stage", stage,
		"input", u.InputTokens,
		"output", u.OutputTokens,
		"cacheRead", u.CacheReadInputTokens,
		"cacheCreation", u.CacheCreationInputTokens,
	)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// isRetryableError reports whether an Anthropic API error is worth retrying.
// Retryable: 429 (rate-limited) and 5xx (server errors).
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		s := apiErr.StatusCode
		return s == http.StatusTooManyRequests || s >= http.StatusInternalServerError
	}
	return false
}

// Wait blocks until a token is available (rate limiting).
func (rl *RateLimiter) Wait(ctx context.Context) error {
	if rl == nil {
		return nil
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rl.lastRefill)
	tokensToAdd := int(elapsed / rl.refillRate)

	if tokensToAdd > 0 {
		rl.tokens += tokensToAdd
		if rl.tokens > rl.maxTokens {
			rl.tokens = rl.maxTokens
		}
		rl.lastRefill = now
	}

	if rl.tokens <= 0 {
		waitTime := rl.refillRate - (elapsed % rl.refillRate)
		select {
		case <-time.After(waitTime):
			rl.tokens = 1
			rl.lastRefill = time.Now()
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	rl.tokens--
	return nil
}
