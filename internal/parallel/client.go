// Package parallel is a thin client for the Parallel Web APIs
// (https://docs.parallel.ai): Search (LLM-optimized web excerpts), Extract
// (URL -> clean markdown), Task runs (Deep Research) and the Responses API.
//
// Auth is keyless-first: the x-api-key header is only sent when a key is
// configured, so free/no-key deployments can still attempt Task runs and
// /v1/responses and fall back to the native harness on auth rejection.
//
// Docs: https://docs.parallel.ai/api-reference/search/search
package parallel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// New creates a Parallel client. baseURL may be empty (default). apiKey is
// optional: keyless clients omit the x-api-key header and are primarily used
// for the free/no-key Task-run and /v1/responses paths.
func New(baseURL, apiKey string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: 45 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// NewWithTimeout creates a Parallel client with a custom overall HTTP timeout
// (useful for SSE streams that need longer than the default 45s).
func NewWithTimeout(baseURL, apiKey string, timeout time.Duration) *Client {
	c := New(baseURL, apiKey)
	c.http = &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     5 * time.Minute,
		},
	}
	return c
}

// Configured reports whether the client has a base URL. Unlike Enabled() it
// does not require an API key: keyless deployments still exercise the
// Task-run (Layer A) and /v1/responses paths and fall back to the native
// harness when the API rejects the keyless call.
func (c *Client) Configured() bool { return c != nil && strings.TrimSpace(c.baseURL) != "" }

// Enabled reports whether the client has credentials (a non-empty API key).
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
// Task runs (Deep Research) — Layer A, keyless-first
// ---------------------------------------------------------------------------

// TaskRunRequest is the /v1/tasks/runs payload (Deep Research orchestration).
type TaskRunRequest struct {
	Input        string `json:"input"`
	Processor    string `json:"processor"`
	EnableEvents bool   `json:"enable_events,omitempty"`
	// OutputSchema selects the report shape: {"type":"auto"} adds basis +
	// citations, {"type":"text"} returns plain markdown, or a raw JSON schema.
	OutputSchema          json.RawMessage `json:"output_schema,omitempty"`
	PreviousInteractionID string          `json:"previous_interaction_id,omitempty"`
	Webhook               *TaskWebhook    `json:"webhook,omitempty"`
}

// TaskWebhook registers an HMAC-SHA256-signed callback for run events.
type TaskWebhook struct {
	URL        string   `json:"url"`
	EventTypes []string `json:"event_types,omitempty"`
}

// TaskRunStatus is the run object returned by create and status polls.
type TaskRunStatus struct {
	RunID         string `json:"run_id"`
	InteractionID string `json:"interaction_id,omitempty"`
	Status        string `json:"status"`
	Processor     string `json:"processor,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	CompletedAt   string `json:"completed_at,omitempty"`
	TaskgroupID   string `json:"taskgroup_id,omitempty"`
	Error         any    `json:"error,omitempty"`
	Warnings      any    `json:"warnings,omitempty"`
}

// TaskRunResult is the /v1/tasks/runs/{id}/result reply.
type TaskRunResult struct {
	RunID  string      `json:"run_id"`
	Status string      `json:"status"`
	Output *TaskOutput `json:"output,omitempty"`
	Error  any         `json:"error,omitempty"`
}

// TaskOutput holds the finished report content and its supporting citations.
type TaskOutput struct {
	Content string      `json:"content,omitempty"`
	Schema  string      `json:"schema,omitempty"`
	Basis   []TaskBasis `json:"basis,omitempty"`
}

// TaskBasis is one cited fact backing an output field.
type TaskBasis struct {
	Field      string   `json:"field"`
	Value      any      `json:"value"`
	Citations  []string `json:"citations,omitempty"`
	Confidence string   `json:"confidence,omitempty"`
}

// CreateTaskRun starts a Deep Research task run.
func (c *Client) CreateTaskRun(ctx context.Context, req TaskRunRequest) (*TaskRunStatus, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	var out TaskRunStatus
	if err := c.post(ctx, "/v1/tasks/runs", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTaskRun polls a run's status by ID.
func (c *Client) GetTaskRun(ctx context.Context, runID string) (*TaskRunStatus, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	var out TaskRunStatus
	if err := c.get(ctx, "/v1/tasks/runs/"+url.PathEscape(runID), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTaskRunResult fetches a completed run's report.
func (c *Client) GetTaskRunResult(ctx context.Context, runID string) (*TaskRunResult, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	var out TaskRunResult
	if err := c.get(ctx, "/v1/tasks/runs/"+url.PathEscape(runID)+"/result", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TaskEvents opens the SSE event stream for a run; the caller owns the
// returned *http.Response body. Uses a 5-min timeout client so long streams
// do not abort at the default 45s deadline.
func (c *Client) TaskEvents(ctx context.Context, runID string) (*http.Response, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	return c.rawStream(ctx, http.MethodGet, "/v1/tasks/runs/"+url.PathEscape(runID)+"/events", nil, "")
}

// Responses performs a raw passthrough to POST /v1/responses. The returned
// *http.Response body is undrained so stream:true bodies relay verbatim.
// Uses a 5-min timeout client so long streams do not abort at the default 45s
// deadline.
func (c *Client) Responses(ctx context.Context, body []byte) (*http.Response, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	return c.rawStream(ctx, http.MethodPost, "/v1/responses", body, "application/json")
}

// rawStream performs a passthrough request with a dedicated 5-min timeout
// transport for SSE streams. The caller owns the returned *http.Response body.
func (c *Client) rawStream(ctx context.Context, method, path string, body []byte, contentType string) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		httpReq.Header.Set("x-api-key", c.apiKey)
	}
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}

	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     5 * time.Minute,
	}
	client := &http.Client{
		Timeout:   5 * time.Minute,
		Transport: transport,
	}
	return client.Do(httpReq)
}

// HasKey reports whether the client carries credentials (nil-safe).
func (c *Client) HasKey() bool { return c != nil && c.apiKey != "" }

// IsAuthError reports whether err means the API rejected the (missing) key.
// Used to decide when to fall back to the native keyless harness.
func IsAuthError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusUnauthorized ||
			apiErr.StatusCode == http.StatusForbidden ||
			apiErr.StatusCode == http.StatusPaymentRequired
	}
	return false
}

// ---------------------------------------------------------------------------

// ErrNotConfigured is returned when no Parallel client is configured.
var ErrNotConfigured = fmt.Errorf("parallel: client not configured")

func (c *Client) post(ctx context.Context, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("parallel: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		httpReq.Header.Set("x-api-key", c.apiKey)
	}
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

func (c *Client) get(ctx context.Context, path string, out any) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		httpReq.Header.Set("x-api-key", c.apiKey)
	}
	httpReq.Header.Set("Accept", "application/json")

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

// raw performs a passthrough request and returns the live response so
// stream:true payloads (SSE) can be relayed without being buffered.
func (c *Client) raw(ctx context.Context, method, path string, body []byte, contentType string) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		httpReq.Header.Set("x-api-key", c.apiKey)
	}
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	return c.http.Do(httpReq)
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
