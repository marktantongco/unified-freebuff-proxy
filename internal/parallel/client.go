// Package parallel is a thin client for the Parallel Web APIs
// (https://docs.parallel.ai): Search (LLM-optimized web excerpts) and
// Extract (URL -> clean markdown). Auth uses the x-api-key header.
//
// Docs: https://docs.parallel.ai/api-reference/search/search
package parallel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.parallel.ai"

// Client talks to the Parallel REST API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New creates a Parallel client. baseURL may be empty (default). apiKey must
// be non-empty; the caller decides whether the integration is enabled.
func New(baseURL, apiKey string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 45 * time.Second},
	}
}

// Enabled reports whether the client has credentials.
func (c *Client) Enabled() bool { return c != nil && c.apiKey != "" }

// ---------------------------------------------------------------------------
// Search — POST /v1/search
// ---------------------------------------------------------------------------

// SearchRequest is the /v1/search payload.
type SearchRequest struct {
	// Objective is a natural-language research goal (max 5000 chars).
	Objective string `json:"objective,omitempty"`
	// SearchQueries: 1-5 keyword queries, 3-6 words each.
	SearchQueries []string `json:"search_queries,omitempty"`
	// Mode: turbo (~200ms) | fast (~700ms) | basic (~1s) | advanced (~3s).
	// Empty defaults to advanced server-side.
	Mode string `json:"mode,omitempty"`
	// MaxCharsTotal caps total excerpt characters (default model-dependent).
	MaxCharsTotal int `json:"max_chars_total,omitempty"`
	// ClientModel tunes excerpt shaping for the consuming model.
	ClientModel string `json:"client_model,omitempty"`
	// SessionID groups related Search/Extract calls for one task.
	SessionID string `json:"session_id,omitempty"`
	// AdvancedSettings: source policy, location, max_results, excerpts.
	AdvancedSettings *AdvancedSettings `json:"advanced_settings,omitempty"`
}

// AdvancedSettings mirrors the documented advanced_settings object.
type AdvancedSettings struct {
	SourcePolicy *SourcePolicy `json:"source_policy,omitempty"`
	Location     string        `json:"location,omitempty"` // ISO 3166-1 alpha-2
	MaxResults   int           `json:"max_results,omitempty"`
}

// SourcePolicy includes/excludes domains; AfterDate bounds freshness.
type SourcePolicy struct {
	IncludeDomains []string `json:"include_domains,omitempty"`
	ExcludeDomains []string `json:"exclude_domains,omitempty"`
	AfterDate      string   `json:"after_date,omitempty"` // YYYY-MM-DD
}

// SearchResult is one ranked page with LLM-optimized excerpts.
type SearchResult struct {
	Title    string   `json:"title"`
	URL      string   `json:"url"`
	Excerpts []string `json:"excerpts"`
}

// SearchResponse is the /v1/search reply.
type SearchResponse struct {
	SearchID  string         `json:"search_id"`
	SessionID string         `json:"session_id"`
	Results   []SearchResult `json:"results"`
	Warnings  []string       `json:"warnings,omitempty"`
}

// Search performs a web search and returns ranked excerpts.
func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var out SearchResponse
	if err := c.post(ctx, "/v1/search", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------------------------------------------------------------------------
// Extract — POST /v1/extract
// ---------------------------------------------------------------------------

// ExtractRequest is the /v1/extract payload (up to 20 URLs per call).
type ExtractRequest struct {
	URLs      []string `json:"urls"`
	Objective string   `json:"objective,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
}

// ExtractResult is one fetched page.
type ExtractResult struct {
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	Excerpts    []string `json:"excerpts,omitempty"`
	FullContent string   `json:"full_content,omitempty"`
	PublishDate string   `json:"publish_date,omitempty"`
}

// ExtractError describes a failed URL fetch.
type ExtractError struct {
	URL       string `json:"url"`
	ErrorType string `json:"error_type"`
}

// ExtractResponse is the /v1/extract reply.
type ExtractResponse struct {
	ExtractID string          `json:"extract_id"`
	SessionID string          `json:"session_id"`
	Results   []ExtractResult `json:"results"`
	Errors    []ExtractError  `json:"errors,omitempty"`
}

// Extract fetches up to 20 URLs and returns clean markdown/excerpts.
func (c *Client) Extract(ctx context.Context, req ExtractRequest) (*ExtractResponse, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	if len(req.URLs) == 0 {
		return nil, fmt.Errorf("parallel: at least one url is required")
	}
	if len(req.URLs) > 20 {
		req.URLs = req.URLs[:20] // API hard cap; extra URLs are dropped
	}
	var out ExtractResponse
	if err := c.post(ctx, "/v1/extract", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------------------------------------------------------------------------

// ErrNotConfigured is returned when no API key is set.
var ErrNotConfigured = fmt.Errorf("parallel: PARALLEL_API_KEY not configured")

func (c *Client) post(ctx context.Context, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("parallel: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("parallel: request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("parallel: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return &APIError{StatusCode: resp.StatusCode, Body: truncate(raw, 400)}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("parallel: decode response: %w", err)
	}
	return nil
}

// APIError is a non-200 from the Parallel API.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("parallel: http %d: %s", e.StatusCode, e.Body)
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "...(truncated)"
}
