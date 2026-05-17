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
	"sync"
	"time"

	"github.com/mbergo/smooth-operator/internal/collector"
	openai "github.com/sashabaranov/go-openai"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Client handles communication with OpenAI API
type Client struct {
	client        *openai.Client
	promptBuilder *PromptBuilder
	model         string
	maxTokens     int
	temperature   float32
	enabled       bool

	// Retry configuration
	maxRetries int
	retryDelay time.Duration

	// Simple rate limiting
	rateLimiter *RateLimiter
}

// RateLimiter implements a simple token bucket rate limiter
type RateLimiter struct {
	mu         sync.Mutex
	tokens     int
	maxTokens  int
	refillRate time.Duration
	lastRefill time.Time
}

// ClientOptions configures the LLM client
type ClientOptions struct {
	APIKey      string
	Model       string
	MaxTokens   int
	Temperature float32
	Enabled     bool

	// Rate limiting
	MaxRequestsPerMinute int

	// Retry configuration (0 = no retries)
	MaxRetries int
	RetryDelay time.Duration
}

// DefaultClientOptions returns sensible defaults
func DefaultClientOptions() ClientOptions {
	return ClientOptions{
		APIKey:               os.Getenv("OPENAI_API_KEY"),
		Model:                "gpt-4-turbo-preview",
		MaxTokens:            4096,
		Temperature:          0.3, // Lower for more consistent output
		Enabled:              os.Getenv("OPENAI_API_KEY") != "",
		MaxRequestsPerMinute: 10,
		MaxRetries:           3,
		RetryDelay:           500 * time.Millisecond,
	}
}

// NewClient creates a new LLM client
func NewClient(options ClientOptions) (*Client, error) {
	if !options.Enabled {
		return &Client{
			enabled: false,
		}, nil
	}

	if options.APIKey == "" {
		return nil, fmt.Errorf("OpenAI API key is required (set OPENAI_API_KEY env var)")
	}

	oaiClient := openai.NewClient(options.APIKey)
	return newClientFromOpenAI(oaiClient, options), nil
}

// newClientFromOpenAI constructs a Client from an already-configured openai.Client.
// This is the shared construction path used by both NewClient and test helpers.
func newClientFromOpenAI(oaiClient *openai.Client, options ClientOptions) *Client {
	maxRetries := options.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 0
	}
	retryDelay := options.RetryDelay
	if retryDelay <= 0 {
		retryDelay = 500 * time.Millisecond
	}

	// Clamp MaxRequestsPerMinute to a sensible minimum: a zero or negative
	// value would otherwise initialize the limiter with zero tokens, causing
	// Wait() to block forever instead of failing fast.
	rpm := options.MaxRequestsPerMinute
	if rpm <= 0 {
		rpm = 1
	}
	rateLimiter := &RateLimiter{
		tokens:     rpm,
		maxTokens:  rpm,
		refillRate: time.Minute / time.Duration(rpm),
		lastRefill: time.Now(),
	}

	return &Client{
		client:        oaiClient,
		promptBuilder: NewPromptBuilder(),
		model:         options.Model,
		maxTokens:     options.MaxTokens,
		temperature:   options.Temperature,
		enabled:       true,
		maxRetries:    maxRetries,
		retryDelay:    retryDelay,
		rateLimiter:   rateLimiter,
	}
}

// GeneratePlan calls the LLM to generate a plan based on cluster context
func (c *Client) GeneratePlan(
	ctx context.Context,
	userPrompt string,
	aggregated *collector.AggregatedContext,
) (*LLMResponse, error) {
	if !c.enabled {
		return nil, fmt.Errorf("LLM client is not enabled")
	}

	logger := log.FromContext(ctx)

	// Rate limiting
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit: %w", err)
	}

	logger.Info("Generating LLM plan",
		"model", c.model,
		"namespace", aggregated.ClusterContext.TargetNamespace,
		"deployments", len(aggregated.ClusterContext.Deployments),
	)

	// Build the prompt
	systemPrompt := c.promptBuilder.BuildSystemPrompt()
	userPromptText := c.promptBuilder.BuildPrompt(userPrompt, aggregated)

	logger.V(1).Info("LLM prompt built", "systemPromptLength", len(systemPrompt), "userPromptLength", len(userPromptText))

	req := openai.ChatCompletionRequest{
		Model: c.model,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleSystem,
				Content: systemPrompt,
			},
			{
				Role:    openai.ChatMessageRoleUser,
				Content: userPromptText,
			},
		},
		MaxTokens:   c.maxTokens,
		Temperature: c.temperature,
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
	}

	// Call OpenAI with retry on transient errors (429, 5xx)
	startTime := time.Now()
	var resp openai.ChatCompletionResponse
	var err error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			logger.V(1).Info("Retrying LLM request", "attempt", attempt, "maxRetries", c.maxRetries)
			select {
			case <-time.After(c.retryDelay):
			case <-ctx.Done():
				return nil, fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			}
		}
		resp, err = c.client.CreateChatCompletion(ctx, req)
		if err == nil {
			break
		}
		if !isRetryableError(err) {
			break
		}
	}
	duration := time.Since(startTime)

	if err != nil {
		logger.Error(err, "LLM API call failed")
		return nil, fmt.Errorf("OpenAI API error: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response from LLM")
	}

	responseText := resp.Choices[0].Message.Content

	logger.Info("LLM response received",
		"duration", duration.String(),
		"tokensUsed", resp.Usage.TotalTokens,
		"responseLength", len(responseText),
	)

	// Parse JSON response
	var llmResp LLMResponse
	if err := json.Unmarshal([]byte(responseText), &llmResp); err != nil {
		logger.Error(err, "Failed to parse LLM JSON response", "response", responseText)
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	logger.Info("LLM plan generated successfully",
		"inferredNeeds", len(llmResp.InferredNeeds),
		"patches", len(llmResp.Patches),
		"confidence", llmResp.Confidence,
		"risk", llmResp.Risk,
	)

	return &llmResp, nil
}

// IsEnabled returns whether the LLM client is enabled
func (c *Client) IsEnabled() bool {
	return c.enabled
}

// isRetryableError reports whether an OpenAI API error is worth retrying.
// Retryable conditions: rate-limit (429) and server errors (5xx).
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatusCode == http.StatusTooManyRequests ||
			apiErr.HTTPStatusCode >= http.StatusInternalServerError
	}
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		return reqErr.HTTPStatusCode == http.StatusTooManyRequests ||
			reqErr.HTTPStatusCode >= http.StatusInternalServerError
	}
	return false
}

// Wait blocks until a token is available (rate limiting)
func (rl *RateLimiter) Wait(ctx context.Context) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Refill tokens based on time passed
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

	// If no tokens available, wait
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
