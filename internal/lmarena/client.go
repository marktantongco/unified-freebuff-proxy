// Package lmarena is a thin client for the lmarena-stealth-proxy sidecar
// (deps/lmarena-stealth-proxy, vendored from
// https://github.com/YourBoiiLevi/lmarena-stealth-proxy), which turns the
// public lmarena.ai web UI into a session-based REST API
// (POST /v1/responses, /v1/session) on :3103.
//
// The gateway does not reimplement the protocol: it relays /v1/lmarena/*
// byte-level to the sidecar (see internal/httpapi lmarena relay) and only
// probes /health here.
package lmarena

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client talks to the lmarena sidecar.
type Client struct {
	baseURL string
	http    *http.Client
}

// New creates a client for the sidecar at baseURL (e.g. http://127.0.0.1:3103).
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// BaseURL returns the configured sidecar address.
func (c *Client) BaseURL() string { return c.baseURL }

// Health is the sidecar /health payload.
type Health struct {
	Status    string  `json:"status"`
	Timestamp string  `json:"timestamp"`
	Uptime    float64 `json:"uptime"`
}

// Health probes the sidecar.
func (c *Client) Health(ctx context.Context) (*Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lmarena health: http %d", resp.StatusCode)
	}
	var h Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, err
	}
	return &h, nil
}
