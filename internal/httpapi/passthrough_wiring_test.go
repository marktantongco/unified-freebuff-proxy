package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"freebuff-unified/internal/proxy"
)

func TestPassthroughModeRelaysV1AndKeepsNativeHealth(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"backend":"marker"}`))
	}))
	t.Cleanup(backend.Close)

	app := NewApp(Options{
		Passthrough: proxy.NewHandler(nil),
		BackendURL:  backend.URL,
	})

	// /v1/* must be relayed to the backend verbatim.
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if err != nil {
		t.Fatalf("v1 request: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(readAll(t, resp), "marker") {
		t.Fatalf("passthrough /v1/models = %d, want 200 with backend marker", resp.StatusCode)
	}

	// /healthz must stay native.
	resp2, err := app.Test(httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	if resp2.StatusCode != http.StatusOK || !strings.Contains(readAll(t, resp2), "status") {
		t.Fatalf("native /healthz = %d, want 200 native JSON", resp2.StatusCode)
	}
}

func TestDefaultModeKeepsNativeRoutes(t *testing.T) {
	app := NewApp(Options{Chat: okChatService{}})

	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(readAll(t, resp), "ok") {
		t.Fatalf("native chat = %d, want 200 with native payload", resp.StatusCode)
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return sb.String()
}
