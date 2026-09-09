package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newBackend(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestNoBackendConfigured(t *testing.T) {
	h := NewHandler(nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "not configured") {
		t.Fatalf("expected 'not configured' message, got %q", rr.Body.String())
	}
}

func TestSetBackendInvalidAndEmpty(t *testing.T) {
	h := NewHandler(nil)

	h.SetBackend("://not-a-url")
	if got := h.BackendURL(); got != "" {
		t.Fatalf("invalid backend should be rejected, got %q", got)
	}

	h.SetBackend("http://127.0.0.1:3457")
	if got := h.BackendURL(); got != "http://127.0.0.1:3457" {
		t.Fatalf("expected backend set, got %q", got)
	}

	h.SetBackend("")
	if got := h.BackendURL(); got != "" {
		t.Fatalf("empty backend should disable proxying, got %q", got)
	}
}

func TestForwardPathQueryMethodAndBody(t *testing.T) {
	var (
		mu        sync.Mutex
		gotPath   string
		gotQuery  string
		gotMethod string
		gotBody   string
	)
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok")
	})

	h := NewHandler(nil)
	h.SetBackend(backend.URL)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?stream=true", strings.NewReader(`{"model":"z-ai/glm-5.3-flash"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", gotPath)
	}
	if gotQuery != "stream=true" {
		t.Errorf("query = %q, want stream=true", gotQuery)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotBody != `{"model":"z-ai/glm-5.3-flash"}` {
		t.Errorf("body = %q, want original body", gotBody)
	}
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Errorf("response = %d %q, want 200 ok", rr.Code, rr.Body.String())
	}
}

func TestForwardHeadersAndStripHopByHop(t *testing.T) {
	var mu sync.Mutex
	received := http.Header{}
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		received = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	})

	h := NewHandler(nil)
	h.SetBackend(backend.URL)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	req.Header.Set("X-Custom", "keep-me")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Proxy-Authorization", "Basic dXNlcjpwYXNz")

	h.ServeHTTP(httptest.NewRecorder(), req)

	for _, key := range []string{"Connection", "Keep-Alive", "Upgrade", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding"} {
		if got := received.Get(key); got != "" {
			t.Errorf("hop-by-hop header %s forwarded: %q", key, got)
		}
	}
	for key, want := range map[string]string{
		"Authorization":   "Bearer client-token",
		"X-Custom":        "keep-me",
		"X-Forwarded-For": "203.0.113.7",
	} {
		if got := received.Get(key); got != want {
			t.Errorf("header %s = %q, want %q", key, got, want)
		}
	}
}

func TestErrorRelay(t *testing.T) {
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Backend", "yes")
		w.WriteHeader(http.StatusPaymentRequired)
		io.WriteString(w, `{"error":{"message":"insufficient credits"}}`)
	})

	h := NewHandler(nil)
	h.SetBackend(backend.URL)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)))

	if rr.Code != http.StatusPaymentRequired {
		t.Errorf("status = %d, want 402", rr.Code)
	}
	if body := strings.TrimSpace(rr.Body.String()); body != `{"error":{"message":"insufficient credits"}}` {
		t.Errorf("body = %q, want upstream error relayed unchanged", body)
	}
	if rr.Header().Get("X-Backend") != "yes" {
		t.Errorf("backend header X-Backend not relayed")
	}
	if rr.Header().Get("Content-Type") != "application/json" {
		t.Errorf("content-type = %q, want application/json", rr.Header().Get("Content-Type"))
	}
}

func TestSSEStreamingFlushesPerFrame(t *testing.T) {
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		for _, frame := range []string{"data: frame0\n\n", "data: frame1\n\n", "data: [DONE]\n\n"} {
			io.WriteString(w, frame)
			f.Flush()
			time.Sleep(30 * time.Millisecond)
		}
	})

	h := NewHandler(nil)
	h.SetBackend(backend.URL)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("content-type = %q, want text/event-stream", got)
	}
	if !rr.Flushed {
		t.Error("SSE response was never flushed")
	}
	body := rr.Body.String()
	for _, want := range []string{"data: frame0", "data: frame1", "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream body missing %q; got %q", want, body)
		}
	}
}

func TestSSENonStreamResponseNotFlushed(t *testing.T) {
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"choices":[]}`)
	})

	h := NewHandler(nil)
	h.SetBackend(backend.URL)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)))

	if rr.Flushed {
		t.Error("non-SSE response should not require explicit flushing")
	}
	if body := strings.TrimSpace(rr.Body.String()); body != `{"choices":[]}` {
		t.Errorf("body = %q, want relayed unchanged", body)
	}
}

func TestBackendUnreachable(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := backend.URL
	backend.Close() // now refuses connections

	h := NewHandler(nil)
	h.SetBackend(deadURL)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "unreachable") {
		t.Errorf("expected 'unreachable' message, got %q", rr.Body.String())
	}
}

func TestBackendPathJoin(t *testing.T) {
	// backend URL with a base path must be preserved before the request path
	var mu sync.Mutex
	var gotPath string
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})

	h := NewHandler(nil)
	h.SetBackend(backend.URL + "/v1")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/models", nil))

	if gotPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", gotPath)
	}
}
