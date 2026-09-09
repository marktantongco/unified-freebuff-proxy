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

	"freebuff-unified/internal/hermes"
)

// fakeSidecarBackend simulates the Node sidecar behind the gateway.
func fakeSidecarBackend(t *testing.T) *hermes.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/healthz":
			_, _ = w.Write([]byte(`{"status":"ok","service":"hermes-sidecar","hermes":"1.3.4","node":"v22","sessions":0,"uptime_s":1}`))
		case r.URL.Path == "/v1/fetch":
			var req hermes.Request
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.URL == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"url is required"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":200,"headers":{},"http_version":"1.1","data":{"via":"hermes"},"elapsed_ms":5}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unknown endpoint"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return hermes.New(srv.URL)
}

func doJSON(t *testing.T, app *fiber.App, method, path, body string) (int, string) {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(b)
}

func TestHermesFetchRequiresKey(t *testing.T) {
	app := NewApp(Options{Model: "m", ProxyAPIKey: "sk", Chat: okChatService{}, Hermes: fakeSidecarBackend(t)})

	status, _ := doJSON(t, app, http.MethodPost, "/v1/hermes/fetch", `{"url":"https://example.com"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without key", status)
	}
}

func TestHermesFetchDisabledReturns503(t *testing.T) {
	app := NewApp(Options{Model: "m", ProxyAPIKey: "sk", Chat: okChatService{}})

	req := httptest.NewRequest(http.MethodPost, "/v1/hermes/fetch", strings.NewReader(`{"url":"https://example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when hermes disabled", resp.StatusCode)
	}
}

func TestHermesFetchRoundTrip(t *testing.T) {
	app := NewApp(Options{Model: "m", ProxyAPIKey: "sk", Chat: okChatService{}, Hermes: fakeSidecarBackend(t)})

	status, body := doJSONWithKey(t, app, http.MethodPost, "/v1/hermes/fetch", `{"url":"https://example.com","http2":true}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	m := decodeBody(t, body)
	if m["status"] != float64(200) {
		t.Errorf("status = %v, want 200", m["status"])
	}
	data, ok := m["data"].(map[string]any)
	if !ok || data["via"] != "hermes" {
		t.Errorf("data = %v, want via=hermes", m["data"])
	}
}

func TestHermesFetchRequiresURL(t *testing.T) {
	app := NewApp(Options{Model: "m", ProxyAPIKey: "sk", Chat: okChatService{}, Hermes: fakeSidecarBackend(t)})

	status, body := doJSONWithKey(t, app, http.MethodPost, "/v1/hermes/fetch", `{}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", status, body)
	}
}

func TestHermesSessionLifecycle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/session/cookies1":
			_, _ = w.Write([]byte(`{"deleted":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/session/cookies1":
			_, _ = w.Write([]byte(`{"status":200,"headers":{},"http_version":"1.1","data":"ok","elapsed_ms":2}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unexpected"}`))
		}
	}))
	t.Cleanup(srv.Close)
	app := NewApp(Options{Model: "m", ProxyAPIKey: "sk", Chat: okChatService{}, Hermes: hermes.New(srv.URL)})

	status, _ := doJSONWithKey(t, app, http.MethodPost, "/v1/hermes/session/cookies1", `{"url":"https://example.com"}`)
	if status != http.StatusOK {
		t.Fatalf("session fetch status = %d, want 200", status)
	}

	status, body := doJSONWithKey(t, app, http.MethodDelete, "/v1/hermes/session/cookies1", "")
	if status != http.StatusOK || !strings.Contains(body, `"deleted":true`) {
		t.Fatalf("delete status = %d body = %s, want deleted:true", status, body)
	}
}

func TestHermesHealthRoute(t *testing.T) {
	app := NewApp(Options{Model: "m", ProxyAPIKey: "sk", Chat: okChatService{}, Hermes: fakeSidecarBackend(t)})

	// Health route is behind the API key gate (not in the public allowlist).
	status, body := doJSONWithKey(t, app, http.MethodGet, "/hermes/healthz", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	m := decodeBody(t, body)
	if m["status"] != "ok" {
		t.Errorf("status = %v", m["status"])
	}
	_ = context.Background
}

// doJSONWithKey sends a request with the test API key attached.
func doJSONWithKey(t *testing.T, app *fiber.App, method, path, body string) (int, string) {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer sk")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(b)
}
