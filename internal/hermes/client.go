// Package hermes is a thin client for the hermes stealth sidecar
// (deps/hermes-service), which wraps the vendored @kori_xyz/hermes Node
// library (TLS 1.3 / HTTP2 / proxy stealth requests) behind a local HTTP API.
package hermes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to the hermes sidecar.
type Client struct {
	baseURL string
	http    *http.Client
}

// New creates a client for the sidecar at baseURL (e.g. http://127.0.0.1:3101).
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// NewWithTimeout creates a sidecar client with a custom overall HTTP timeout
// (useful when relaying long upstream LLM completions through the sidecar).
func NewWithTimeout(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		return New(baseURL)
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

// Request describes a stealth request to execute via the sidecar.
type Request struct {
	URL       string            `json:"url"`
	Method    string            `json:"method,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Payload   json.RawMessage   `json:"payload,omitempty"`
	Proxy     string            `json:"proxy,omitempty"`
	TimeoutMS int               `json:"timeout_ms,omitempty"`
	HTTP2     bool              `json:"http2,omitempty"`
	JSON      *bool             `json:"json,omitempty"`
}

// Response is the sidecar's normalized reply.
type Response struct {
	Status      int               `json:"status"`
	Headers     map[string]string `json:"headers"`
	HTTPVersion string            `json:"http_version"`
	Data        json.RawMessage   `json:"data"`
	DataB64     string            `json:"data_b64"`
	ElapsedMS   int64             `json:"elapsed_ms"`

	// Decoded holds the response body: JSON parsed into any, text as string,
	// or binary decoded from DataB64 into []byte.
	Decoded any `json:"-"`
}

// Health is the sidecar /healthz payload.
type Health struct {
	Status   string `json:"status"`
	Service  string `json:"service"`
	Hermes   string `json:"hermes"`
	Node     string `json:"node"`
	Sessions int    `json:"sessions"`
	UptimeS  int    `json:"uptime_s"`
}

// Fetch performs a one-shot stealth request (no cookie jar).
func (c *Client) Fetch(ctx context.Context, req Request) (*Response, error) {
	return c.do(ctx, "/v1/fetch", req)
}

// FetchWithSession performs a stealth request using (and updating) the named
// server-side cookie session. Sessions expire server-side after 30 minutes
// of inactivity.
func (c *Client) FetchWithSession(ctx context.Context, sessionID string, req Request) (*Response, error) {
	return c.do(ctx, "/v1/session/"+url.PathEscape(sessionID), req)
}

// DropSession removes a server-side cookie session.
func (c *Client) DropSession(ctx context.Context, sessionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.baseURL+"/v1/session/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("drop session: http %d", resp.StatusCode)
	}
	return nil
}

// Health probes the sidecar.
func (c *Client) Health(ctx context.Context) (*Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hermes health: http %d", resp.StatusCode)
	}
	var h Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, err
	}
	return &h, nil
}

func (c *Client) do(ctx context.Context, path string, req Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hermes fetch: http %d: %s", httpResp.StatusCode, truncate(raw, 200))
	}

	var r Response
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	r.decode()
	return &r, nil
}

func (r *Response) decode() {
	switch {
	case r.DataB64 != "":
		if b, err := base64.StdEncoding.DecodeString(r.DataB64); err == nil {
			r.Decoded = b
		}
	case len(r.Data) > 0:
		// The sidecar returns text/HTML bodies as JSON-encoded strings
		// (e.g. data: "<a class=\"result__a\" ...>"). Unwrap the outer JSON
		// string so Text() yields the real body bytes; JSON objects/arrays
		// stay as RawMessage.
		var s string
		if err := json.Unmarshal(r.Data, &s); err == nil {
			r.Decoded = s
			return
		}
		r.Decoded = json.RawMessage(r.Data)
	}
}

// Text returns the body as a string (best-effort for JSON bodies too).
func (r *Response) Text() string {
	switch v := r.Decoded.(type) {
	case []byte:
		return string(v)
	case string:
		return v
	case json.RawMessage:
		return string(v)
	default:
		return ""
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "...(truncated)"
}
