package stealth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"freebuff-unified/internal/hermes"
)

// fakeSidecar builds a hermes.Client pointed at a stub sidecar that returns
// canned sidecar responses and records the requests it received.
type fakeSidecar struct {
	*hermes.Client
	server   *httptest.Server
	mu       chan struct{}
	gotReqs  chan hermes.Request
	respCode int
	respBody string
}

func newFakeSidecar(t *testing.T, status int, body string, headers map[string]string) *fakeSidecar {
	t.Helper()
	f := &fakeSidecar{
		gotReqs:  make(chan hermes.Request, 8),
		respCode: status,
		respBody: body,
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req hermes.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.gotReqs <- req
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"status":       f.respCode,
			"headers":      headers,
			"http_version": "2.0",
			"data":         f.respBody,
			"elapsed_ms":   5,
		}
		if headers["content-type"] == "" {
			// raw text body -> sidecar would return data as string
			resp["data"] = f.respBody
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	f.Client = hermes.New(f.server.URL)
	t.Cleanup(f.server.Close)
	return f
}

func TestHermesRoundTripper_RoutesNonSSEThroughSidecar(t *testing.T) {
	fake := newFakeSidecar(t, 200, `{"ok":true}`, map[string]string{
		"content-type": "application/json",
		"x-stealth":    "yes",
	})
	rt := &HermesRoundTripper{
		Sidecar:   fake.Client,
		Fallback:  http.DefaultTransport,
		HTTP2:     true,
		TimeoutMS: 5000,
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://codebuff.com/api/session", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer tok")

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Errorf("content-type = %q, want json", ct)
	}

	got := <-fake.gotReqs
	if got.URL != "https://codebuff.com/api/session" || got.Method != "POST" {
		t.Errorf("sidecar got url=%q method=%q", got.URL, got.Method)
	}
	if got.Headers["Authorization"] != "Bearer tok" {
		t.Errorf("sidecar missing auth header: %v", got.Headers)
	}
	if string(got.Payload) != `{"model":"m"}` {
		t.Errorf("sidecar payload = %s", got.Payload)
	}
}

func TestHermesRoundTripper_SSEBypassesSidecar(t *testing.T) {
	fake := newFakeSidecar(t, 200, "", nil)
	fb := &countingTransport{}
	rt := &HermesRoundTripper{
		Sidecar:  fake.Client,
		Fallback: fb,
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://codebuff.com/api/chat", nil)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	select {
	case r := <-fake.gotReqs:
		t.Fatalf("sidecar should not have received SSE request: %+v", r)
	default:
	}
	if fb.calls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fb.calls)
	}
}

func TestHermesRoundTripper_FailOpen(t *testing.T) {
	// Sidecar that always fails (status 0 -> sidecar-level failure).
	fake := newFakeSidecar(t, 0, "", nil)
	fb := &countingTransport{}
	rt := &HermesRoundTripper{
		Sidecar:  fake.Client,
		Fallback: fb,
		FailOpen: true,
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://codebuff.com/api/session", nil)

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip (failopen): %v", err)
	}
	resp.Body.Close()
	if fb.calls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fb.calls)
	}
}

func TestHermesRoundTripper_FailClosed(t *testing.T) {
	fake := newFakeSidecar(t, 0, "", nil)
	fb := &countingTransport{}
	rt := &HermesRoundTripper{
		Sidecar:  fake.Client,
		Fallback: fb,
		FailOpen: false,
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://codebuff.com/api/session", nil)

	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("expected error with FailOpen=false and failing sidecar")
	}
	if fb.calls != 0 {
		t.Fatalf("fallback should not be called when fail-closed, got %d", fb.calls)
	}
}

func TestHermesRoundTripper_ProxySelector(t *testing.T) {
	fake := newFakeSidecar(t, 200, "ok", map[string]string{"content-type": "text/plain"})
	rt := &HermesRoundTripper{
		Sidecar:  fake.Client,
		Fallback: http.DefaultTransport,
		Proxy: func(*http.Request) string {
			return "socks5://10.0.0.1:1080"
		},
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://codebuff.com/ip", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	got := <-fake.gotReqs
	if got.Proxy != "socks5://10.0.0.1:1080" {
		t.Errorf("sidecar proxy = %q, want socks5://10.0.0.1:1080", got.Proxy)
	}
}

// countingTransport counts fallback invocations and returns a trivial 200.
type countingTransport struct {
	calls int
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.calls++
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Body:       http.NoBody,
		Request:    r,
	}, nil
}
