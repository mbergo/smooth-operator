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
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newEnabledClient builds a LokiClient pointing at the given server URL with
// enabled=true so code paths that gate on l.enabled are exercised.
func newEnabledClient(t *testing.T, serverURL string) *LokiClient {
	t.Helper()
	opts := DefaultLokiOptions()
	opts.Address = serverURL
	opts.Enabled = true
	c, err := NewLokiClient(opts)
	if err != nil {
		t.Fatalf("NewLokiClient: %v", err)
	}
	return c
}

// lokiQueryResponse builds the JSON body that Loki's query_range endpoint
// returns for a single stream with the provided (timestamp-ns, line) pairs.
func lokiQueryResponse(pairs [][2]string) string {
	type lokiResponse struct {
		Data struct {
			Result []struct {
				Values [][]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}

	values := make([][]string, 0, len(pairs))
	for _, p := range pairs {
		values = append(values, []string{p[0], p[1]})
	}

	var r lokiResponse
	if len(values) > 0 {
		r.Data.Result = []struct {
			Values [][]string `json:"values"`
		}{{Values: values}}
	}

	b, _ := json.Marshal(r)
	return string(b)
}

// timestampNS converts a time.Time to the nanosecond-epoch string Loki uses.
func timestampNS(t time.Time) string {
	return strconv.FormatInt(t.UnixNano(), 10)
}

// ---------------------------------------------------------------------------
// QueryLogs tests
// ---------------------------------------------------------------------------

func TestQueryLogs_Success(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	line1 := "hello world"
	line2 := "second entry"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify path and required query params.
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("query") == "" {
			t.Error("query param missing")
		}
		if r.URL.Query().Get("start") == "" || r.URL.Query().Get("end") == "" {
			t.Error("start/end params missing")
		}

		body := lokiQueryResponse([][2]string{
			{timestampNS(now), line1},
			{timestampNS(now.Add(time.Second)), line2},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	defer ts.Close()

	c := newEnabledClient(t, ts.URL)
	entries, err := c.QueryLogs(context.Background(), `{app="test"}`, now.Add(-time.Minute), now, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Line != line1 {
		t.Errorf("entry[0].Line = %q, want %q", entries[0].Line, line1)
	}
	if entries[1].Line != line2 {
		t.Errorf("entry[1].Line = %q, want %q", entries[1].Line, line2)
	}
	// Timestamps must round-trip correctly.
	if !entries[0].Timestamp.Equal(now) {
		t.Errorf("entry[0].Timestamp = %v, want %v", entries[0].Timestamp, now)
	}
}

func TestQueryLogs_EmptyResult(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := lokiQueryResponse(nil)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	defer ts.Close()

	c := newEnabledClient(t, ts.URL)
	now := time.Now()
	entries, err := c.QueryLogs(context.Background(), `{app="empty"}`, now.Add(-time.Minute), now, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestQueryLogs_Server500(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer ts.Close()

	c := newEnabledClient(t, ts.URL)
	now := time.Now()
	_, err := c.QueryLogs(context.Background(), `{app="broken"}`, now.Add(-time.Minute), now, 10)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code 500, got: %v", err)
	}
}

func TestQueryLogs_BearerToken(t *testing.T) {
	const wantToken = "supersecret"
	t.Setenv("LOKI_TOKEN", wantToken)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+wantToken {
			t.Errorf("Authorization header = %q, want %q", auth, "Bearer "+wantToken)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, lokiQueryResponse(nil))
	}))
	defer ts.Close()

	c := newEnabledClient(t, ts.URL)
	now := time.Now()
	_, err := c.QueryLogs(context.Background(), `{app="secure"}`, now.Add(-time.Minute), now, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// HealthCheck tests
// ---------------------------------------------------------------------------

func TestHealthCheck_OK(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "ready")
	}))
	defer ts.Close()

	c := newEnabledClient(t, ts.URL)
	if err := c.HealthCheck(context.Background()); err != nil {
		t.Fatalf("expected healthy, got: %v", err)
	}
}

func TestHealthCheck_Fail(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	c := newEnabledClient(t, ts.URL)
	err := c.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error for unhealthy Loki, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should mention 503, got: %v", err)
	}
}

func TestHealthCheck_Disabled(t *testing.T) {
	opts := DefaultLokiOptions()
	opts.Enabled = false
	c, _ := NewLokiClient(opts)
	err := c.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error when client is disabled")
	}
}

// ---------------------------------------------------------------------------
// Integration: CollectLogs delegates to QueryLogs
// ---------------------------------------------------------------------------

func TestCollectLogs_Disabled(t *testing.T) {
	opts := DefaultLokiOptions()
	opts.Enabled = false
	c, _ := NewLokiClient(opts)

	summary, err := c.CollectLogs(context.Background(), "ns", "deploy", time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(summary.Errors) == 0 {
		t.Error("expected at least one entry in summary.Errors when disabled")
	}
}

func TestCollectLogs_QueryError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "query_range") {
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer ts.Close()

	c := newEnabledClient(t, ts.URL)
	summary, err := c.CollectLogs(context.Background(), "ns", "deploy", time.Minute)
	// CollectLogs absorbs query errors into summary.Errors rather than returning them.
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if len(summary.Errors) == 0 {
		t.Error("expected summary.Errors to contain the query failure")
	}
}
