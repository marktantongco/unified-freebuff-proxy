package httpapi

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
)

// /health/all — aggregator that probes every sidecar in one round-trip
// + caches results for 10s to avoid hammering upstream services when
// monitoring scrapes frequently.
//
// Probes (with default base URLs overridable via env):
//   freebuff-unified  → http://127.0.0.1:18080/healthz
//   lmarena-sidecar   → http://127.0.0.1:18080/lmarena/healthz
//   hermes-sidecar    → http://127.0.0.1:18080/hermes/healthz
//   owl-agent         → http://127.0.0.1:8080/v1/models
//   owl-agent metrics → http://127.0.0.1:9101/metrics
//   ollama            → http://127.0.0.1:11434/v1/models
//
// Auth endpoints: caller may append ?bearer=KEY to the URL; probeURL
// strips the query param and uses the value as the Authorization header.
//
// Returns JSON: {"checked_at": "...", "cache_ttl": "10s", "results": {name: {ok, code, ms, error?}}}

const healthAllCacheTTL = 10 * time.Second

type probeResult struct {
	OK    bool   `json:"ok"`
	Code  int    `json:"code"`
	MS    int64  `json:"ms"`
	Error string `json:"error,omitempty"`
}

type healthAllPayload struct {
	CheckedAt string                  `json:"checked_at"`
	CacheTTL  string                  `json:"cache_ttl"`
	Results   map[string]*probeResult `json:"results"`
}

var (
	healthAllMu    sync.Mutex
	healthAllCache *healthAllPayload
	healthAllAt    time.Time
)

func (h *handlers) HealthAll(c fiber.Ctx) error {
	if cached := cachedHealthAll(); cached != nil {
		c.Set("X-Health-Cache", "HIT")
		return c.Status(http.StatusOK).JSON(cached)
	}
	results := map[string]*probeResult{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, target := range healthAllTargets() {
		wg.Add(1)
		go func(name, url string) {
			defer wg.Done()
			res := probeURL(url, 2*time.Second)
			mu.Lock()
			results[name] = res
			mu.Unlock()
		}(name, target)
	}
	wg.Wait()
	payload := &healthAllPayload{
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
		CacheTTL:  healthAllCacheTTL.String(),
		Results:   results,
	}
	healthAllMu.Lock()
	healthAllCache = payload
	healthAllAt = time.Now()
	healthAllMu.Unlock()
	c.Set("X-Health-Cache", "MISS")
	return c.Status(http.StatusOK).JSON(payload)
}

func cachedHealthAll() *healthAllPayload {
	healthAllMu.Lock()
	defer healthAllMu.Unlock()
	if healthAllCache == nil || time.Since(healthAllAt) > healthAllCacheTTL {
		return nil
	}
	return healthAllCache
}

func probeURL(url string, timeout time.Duration) *probeResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return &probeResult{Error: err.Error(), MS: time.Since(start).Milliseconds()}
	}
	if v := req.URL.Query().Get("bearer"); v != "" {
		req.Header.Set("Authorization", "Bearer "+v)
		req.URL.RawQuery = ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &probeResult{Error: err.Error(), MS: time.Since(start).Milliseconds()}
	}
	defer resp.Body.Close()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 400
	return &probeResult{
		OK:   ok,
		Code: resp.StatusCode,
		MS:   time.Since(start).Milliseconds(),
	}
}

func healthAllTargets() map[string]string {
	key := os.Getenv("FREEBUFF_API_KEY")
	return map[string]string{
		"freebuff-unified": "http://127.0.0.1:18080/healthz",
		"lmarena-sidecar":  "http://127.0.0.1:18080/lmarena/healthz?bearer=" + key,
		"hermes-sidecar":   "http://127.0.0.1:18080/hermes/healthz?bearer=" + key,
		"stealth-status":   "http://127.0.0.1:18080/stealth/status?bearer=" + key,
		"owl-models":       "http://127.0.0.1:8080/v1/models",
		"owl-metrics":      "http://127.0.0.1:9101/metrics",
		"ollama-models":    "http://127.0.0.1:11434/v1/models",
	}
}
