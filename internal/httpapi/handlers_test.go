package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"freebuff-unified/internal/openai"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type fakePool struct{ stats map[string]any }

func (f fakePool) Stats() any { return f.stats }

type okChatService struct{}

func (okChatService) Complete(ctx context.Context, req openai.ChatCompletionRequest) (string, error) {
	return "ok", nil
}

func (okChatService) Stream(ctx context.Context, req openai.ChatCompletionRequest) (<-chan string, <-chan error) {
	deltas := make(chan string, 1)
	deltas <- "ok"
	close(deltas)
	errs := make(chan error)
	close(errs)
	return deltas, errs
}

// doRequest runs req against app and returns the response body.
func doRequest(t *testing.T, app *fiber.App, req *http.Request) (int, string) {
	t.Helper()
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(body)
}

func decodeBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	return m
}

// ---------------------------------------------------------------------------
// ExtraHealth hook
// ---------------------------------------------------------------------------

func TestHealthMergesExtraHealth(t *testing.T) {
	extra := map[string]any{
		"uptime":   "1h",
		"version":  "unified-v1",
		"ai_stack": map[string]any{"freebuff_gateway": map[string]any{"port": 18080}},
	}
	app := NewApp(Options{
		Model:       "test-model",
		ProxyAPIKey: "k",
		Chat:        okChatService{},
		ExtraHealth: func() map[string]any { return extra },
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	status, body := doRequest(t, app, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	m := decodeBody(t, body)
	if m["status"] != "ok" {
		t.Errorf("status = %v, want ok", m["status"])
	}
	if m["uptime"] != "1h" {
		t.Errorf("uptime not merged from ExtraHealth: %v", m["uptime"])
	}
	if m["version"] != "unified-v1" {
		t.Errorf("version not merged from ExtraHealth: %v", m["version"])
	}
	if _, ok := m["ai_stack"]; !ok {
		t.Error("ai_stack not merged from ExtraHealth")
	}
}

func TestHealthWithoutExtraHealth(t *testing.T) {
	app := NewApp(Options{
		Model:       "test-model",
		ProxyAPIKey: "k",
		Chat:        okChatService{},
		TokenPool:   fakePool{stats: map[string]any{"configured_keys": 2}},
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	status, body := doRequest(t, app, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	m := decodeBody(t, body)
	if m["status"] != "ok" {
		t.Errorf("status = %v, want ok", m["status"])
	}
	if _, ok := m["uptime"]; ok {
		t.Error("uptime should not appear without ExtraHealth")
	}
	tokenPool, ok := m["token_pool"].(map[string]any)
	if !ok {
		t.Fatalf("token_pool missing or wrong type: %v", m["token_pool"])
	}
	if tokenPool["configured_keys"] != float64(2) {
		t.Errorf("token_pool.configured_keys = %v, want 2", tokenPool["configured_keys"])
	}
}

func TestExtraHealthDoesNotOverrideCoreStatus(t *testing.T) {
	// ExtraHealth tries to override the core "status" field; Health must keep
	// its own "ok" value.
	app := NewApp(Options{
		Model:       "test-model",
		ProxyAPIKey: "k",
		Chat:        okChatService{},
		ExtraHealth: func() map[string]any { return map[string]any{"status": "overridden"} },
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	status, body := doRequest(t, app, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	m := decodeBody(t, body)
	if m["status"] != "ok" {
		t.Errorf("status = %v, want core ok to win over ExtraHealth", m["status"])
	}
}

// ---------------------------------------------------------------------------
// AIStack hook
// ---------------------------------------------------------------------------

func TestAIStackStatusUsesAIStackHook(t *testing.T) {
	want := map[string]any{
		"generated_at": "now",
		"infrastructure": map[string]any{
			"freebuff_gateway":       map[string]any{"status": "ok"},
			"freebuff_proxy_backend": map[string]any{"status": "ok"},
		},
	}
	app := NewApp(Options{
		Model:       "test-model",
		ProxyAPIKey: "k",
		Chat:        okChatService{},
		AIStack:     func() map[string]any { return want },
	})

	req := httptest.NewRequest(http.MethodGet, "/ai-stack/status", nil)
	status, body := doRequest(t, app, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	m := decodeBody(t, body)
	if m["generated_at"] != "now" {
		t.Errorf("generated_at = %v, want from AIStack hook", m["generated_at"])
	}
	infra, ok := m["infrastructure"].(map[string]any)
	if !ok {
		t.Fatalf("infrastructure missing: %v", m)
	}
	if _, ok := infra["freebuff_gateway"]; !ok {
		t.Error("infrastructure.freebuff_gateway missing")
	}
	if _, ok := infra["freebuff_proxy_backend"]; !ok {
		t.Error("infrastructure.freebuff_proxy_backend missing")
	}
}

func TestAIStackStatusFallsBackToHealth(t *testing.T) {
	// No AIStack hook: /ai-stack/status must behave like /healthz.
	app := NewApp(Options{
		Model:       "test-model",
		ProxyAPIKey: "k",
		Chat:        okChatService{},
		ExtraHealth: func() map[string]any { return map[string]any{"version": "unified-v1"} },
	})

	req := httptest.NewRequest(http.MethodGet, "/ai-stack/status", nil)
	status, body := doRequest(t, app, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	m := decodeBody(t, body)
	if m["status"] != "ok" {
		t.Errorf("status = %v, want ok (health fallback)", m["status"])
	}
	if m["version"] != "unified-v1" {
		t.Errorf("version = %v, want merged ExtraHealth in fallback", m["version"])
	}
}

// ---------------------------------------------------------------------------
// Route registration and public access
// ---------------------------------------------------------------------------

func TestAIStackStatusRouteIsPublic(t *testing.T) {
	// Even with an API key configured, /ai-stack/status must not 401.
	app := NewApp(Options{
		Model:       "test-model",
		ProxyAPIKey: "secret-key",
		Chat:        okChatService{},
		AIStack:     func() map[string]any { return map[string]any{"generated_at": "now"} },
	})

	req := httptest.NewRequest(http.MethodGet, "/ai-stack/status", nil)
	status, _ := doRequest(t, app, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 for public /ai-stack/status", status)
	}
}

func TestProxyVerifyRouteIsPublic(t *testing.T) {
	app := NewApp(Options{
		Model:       "test-model",
		ProxyAPIKey: "secret-key",
		Chat:        okChatService{},
		ExtraHealth: func() map[string]any { return map[string]any{"proxies": 9} },
	})

	req := httptest.NewRequest(http.MethodGet, "/proxy/verify", nil)
	status, body := doRequest(t, app, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 for public /proxy/verify", status)
	}
	if !strings.Contains(body, `"proxies":9`) {
		t.Errorf("body = %s, want proxies:9 merged from ExtraHealth", body)
	}
}

func TestProtectedRouteRequiresKey(t *testing.T) {
	app := NewApp(Options{
		Model:       "test-model",
		ProxyAPIKey: "secret-key",
		Chat:        okChatService{},
	})

	// No key -> 401
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	status, _ := doRequest(t, app, req)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without key", status)
	}

	// Valid key -> 200
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	status, body := doRequest(t, app, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 with key", status)
	}
	if !strings.Contains(body, "test-model") {
		t.Errorf("body = %s, want test-model in model list", body)
	}
}

// ---------------------------------------------------------------------------
// Chat still routed through ChatService
// ---------------------------------------------------------------------------

func TestChatCompletionsWithConfiguredChat(t *testing.T) {
	app := NewApp(Options{
		Model:       "test-model",
		ProxyAPIKey: "k",
		Chat:        okChatService{},
	})

	payload := `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer k")
	status, body := doRequest(t, app, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	m := decodeBody(t, body)
	choices, ok := m["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("choices missing: %v", m)
	}
}

func TestChatCompletionsWithoutChatReturns503(t *testing.T) {
	// Chat nil -> notConfiguredChatService -> 503.
	app := NewApp(Options{
		Model:       "test-model",
		ProxyAPIKey: "k",
	})

	payload := `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer k")
	status, body := doRequest(t, app, req)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", status, body)
	}
}
