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

package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// LokiClient handles queries to Loki for log aggregation
type LokiClient struct {
	address      string
	enabled      bool
	httpClient   *http.Client
	queryTimeout time.Duration
}

// LogEntry represents a single log line returned from Loki.
type LogEntry struct {
	// Timestamp is the nanosecond-epoch string as returned by Loki, parsed to time.Time.
	Timestamp time.Time
	// Line is the raw log line text.
	Line string
}

// LogSummary contains aggregated log information
type LogSummary struct {
	Namespace  string
	Deployment string

	// Log statistics
	TotalLogLines   int64
	ErrorLogLines   int64
	WarningLogLines int64

	// Recent errors (last 10)
	RecentErrors []string

	// Time window
	WindowStart time.Time
	WindowEnd   time.Time

	// Collection errors
	Errors []string
}

// LokiOptions configures the Loki client
type LokiOptions struct {
	// Address is the Loki server address (e.g., "http://loki:3100")
	Address string

	// QueryTimeout is the timeout for individual queries
	QueryTimeout time.Duration

	// LogsWindow is the time window for log queries
	LogsWindow string

	// Enabled determines if Loki collection is active
	Enabled bool
}

// DefaultLokiOptions returns sensible defaults
func DefaultLokiOptions() LokiOptions {
	return LokiOptions{
		Address:      "http://loki.monitoring.svc:3100",
		QueryTimeout: 30 * time.Second,
		LogsWindow:   "10m",
		Enabled:      false, // Disabled by default (optional feature)
	}
}

// NewLokiClient creates a new Loki client
func NewLokiClient(options LokiOptions) (*LokiClient, error) {
	qt := options.QueryTimeout
	if qt <= 0 {
		qt = 10 * time.Second
	}
	return &LokiClient{
		address:      options.Address,
		enabled:      options.Enabled,
		httpClient:   &http.Client{},
		queryTimeout: qt,
	}, nil
}

// QueryLogs runs the given LogQL string verbatim; callers MUST validate/sanitize untrusted input.
//
// The caller supplies an already-cancelled or deadline-carrying context; an additional
// per-client timeout (default 10 s) is applied on top so individual requests never
// block indefinitely.
func (l *LokiClient) QueryLogs(ctx context.Context, logql string, start, end time.Time, limit int) ([]LogEntry, error) {
	reqCtx, cancel := context.WithTimeout(ctx, l.queryTimeout)
	defer cancel()

	endpoint, err := url.Parse(l.address)
	if err != nil {
		return nil, fmt.Errorf("loki: invalid base URL %q: %w", l.address, err)
	}
	endpoint.Path = "/loki/api/v1/query_range"

	q := endpoint.Query()
	q.Set("query", logql)
	q.Set("start", start.UTC().Format(time.RFC3339Nano))
	q.Set("end", end.UTC().Format(time.RFC3339Nano))
	q.Set("limit", strconv.Itoa(limit))
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("loki: building request: %w", err)
	}

	if token := os.Getenv("LOKI_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := l.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loki: executing query: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("loki: reading response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki: query returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Loki response shape:
	// {"data":{"result":[{"values":[["<ns-ts>","<line>"],...]}]}}
	var parsed struct {
		Data struct {
			Result []struct {
				Values [][]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("loki: decoding response: %w", err)
	}

	var entries []LogEntry
	for _, stream := range parsed.Data.Result {
		for _, pair := range stream.Values {
			if len(pair) < 2 {
				continue
			}
			// Loki timestamps are nanosecond-epoch strings.
			ns, err := strconv.ParseInt(pair[0], 10, 64)
			if err != nil {
				// Fall back to zero time rather than aborting the whole result set.
				entries = append(entries, LogEntry{Line: pair[1]})
				continue
			}
			entries = append(entries, LogEntry{
				Timestamp: time.Unix(0, ns).UTC(),
				Line:      pair[1],
			})
		}
	}
	return entries, nil
}

// CollectLogs gathers log information from Loki for a namespace/deployment
func (l *LokiClient) CollectLogs(ctx context.Context, namespace, deployment string, window time.Duration) (*LogSummary, error) {
	if !l.enabled {
		return &LogSummary{
			Namespace:   namespace,
			Deployment:  deployment,
			WindowEnd:   time.Now(),
			WindowStart: time.Now().Add(-window),
			Errors:      []string{"Loki client is not enabled"},
		}, nil
	}

	logger := log.FromContext(ctx)
	logger.Info("Collecting Loki logs",
		"namespace", namespace,
		"deployment", deployment,
		"window", window.String(),
	)

	now := time.Now()
	summary := &LogSummary{
		Namespace:   namespace,
		Deployment:  deployment,
		WindowEnd:   now,
		WindowStart: now.Add(-window),
		Errors:      []string{},
	}

	logql := fmt.Sprintf(`{namespace=%q, deployment=%q}`, namespace, deployment)
	entries, err := l.QueryLogs(ctx, logql, summary.WindowStart, summary.WindowEnd, 1000)
	if err != nil {
		summary.Errors = append(summary.Errors, fmt.Sprintf("query failed: %v", err))
		return summary, nil
	}

	summary.TotalLogLines = int64(len(entries))
	return summary, nil
}

// IsEnabled returns whether the Loki client is enabled
func (l *LokiClient) IsEnabled() bool {
	return l.enabled
}

// HealthCheck performs a basic health check against Loki's /ready endpoint.
// It applies a 5-second deadline on top of any deadline already present in ctx.
func (l *LokiClient) HealthCheck(ctx context.Context) error {
	if !l.enabled {
		return fmt.Errorf("loki client is not enabled")
	}

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	endpoint, err := url.JoinPath(l.address, "/ready")
	if err != nil {
		return fmt.Errorf("loki: building health-check URL: %w", err)
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("loki: building health-check request: %w", err)
	}

	resp, err := l.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("loki: health check failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("loki: unhealthy (HTTP %d): %s", resp.StatusCode, string(body))
	}
	return nil
}
