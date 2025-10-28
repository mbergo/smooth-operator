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

	// Simple rate limiting
	rateLimiter *RateLimiter
}

// RateLimiter implements a simple token bucket rate limiter
type RateLimiter struct {
	mu            sync.Mutex
	tokens        int
	maxTokens     int
	refillRate    time.Duration
	lastRefill    time.Time
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

	client := openai.NewClient(options.APIKey)

	rateLimiter := &RateLimiter{
		tokens:     options.MaxRequestsPerMinute,
		maxTokens:  options.MaxRequestsPerMinute,
		refillRate: time.Minute / time.Duration(options.MaxRequestsPerMinute),
		lastRefill: time.Now(),
	}

	return &Client{
		client:        client,
		promptBuilder: NewPromptBuilder(),
		model:         options.Model,
		maxTokens:     options.MaxTokens,
		temperature:   options.Temperature,
		enabled:       true,
		rateLimiter:   rateLimiter,
	}, nil
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

	log := log.FromContext(ctx)

	// Rate limiting
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit: %w", err)
	}

	log.Info("Generating LLM plan",
		"model", c.model,
		"namespace", aggregated.ClusterContext.TargetNamespace,
		"deployments", len(aggregated.ClusterContext.Deployments),
	)

	// Build the prompt
	systemPrompt := c.promptBuilder.BuildSystemPrompt()
	userPromptText := c.promptBuilder.BuildPrompt(userPrompt, aggregated)

	log.V(1).Info("LLM prompt built", "systemPromptLength", len(systemPrompt), "userPromptLength", len(userPromptText))

	// Call OpenAI
	startTime := time.Now()
	resp, err := c.client.CreateChatCompletion(
		ctx,
		openai.ChatCompletionRequest{
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
		},
	)
	duration := time.Since(startTime)

	if err != nil {
		log.Error(err, "LLM API call failed")
		return nil, fmt.Errorf("OpenAI API error: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response from LLM")
	}

	responseText := resp.Choices[0].Message.Content

	log.Info("LLM response received",
		"duration", duration.String(),
		"tokensUsed", resp.Usage.TotalTokens,
		"responseLength", len(responseText),
	)

	// Parse JSON response
	var llmResp LLMResponse
	if err := json.Unmarshal([]byte(responseText), &llmResp); err != nil {
		log.Error(err, "Failed to parse LLM JSON response", "response", responseText)
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	log.Info("LLM plan generated successfully",
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

