package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"freebuff-unified/internal/hermes"
	"freebuff-unified/internal/parallel"
	"freebuff-unified/internal/websearch"

	"github.com/gofiber/fiber/v3"
)

func parallelApp(client *parallel.Client) *fiber.App {
	h := newHandlers("m", nil, nil, nil)
	h.parallel = client
	h.parallelMode = "fast"
	app := fiber.New()
	// Mirror NewApp's auth-gated route registration minimally.
	app.Post("/v1/parallel/search", authMiddleware("testkey"), h.ParallelSearch)
	app.Post("/v1/parallel/extract", authMiddleware("testkey"), h.ParallelExtract)
	return app
}

func TestParallelRoutes_RequireAuth(t *testing.T) {
	app := parallelApp(parallel.New("", "")) // configured or not, auth comes first
	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/v1/parallel/search", nil))
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestParallelSearch_NotConfiguredReturns503(t *testing.T) {
	app := parallelApp(parallel.New("", ""))
	req := httptest.NewRequest(http.MethodPost, "/v1/parallel/search",
		bytes.NewReader(jsonBody(t, map[string]any{"objective": "test query"})))
	req.Header.Set("Authorization", "Bearer testkey")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestParallelSearch_MapsRequestAndResponse(t *testing.T) {
	var gotPath, gotKey, gotMode, gotObjective string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		var body struct {
			Mode      string   `json:"mode"`
			Objective string   `json:"objective"`
			Queries   []string `json:"search_queries"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotMode, gotObjective = body.Mode, body.Objective
		_ = body.Queries
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"search_id": "s1", "session_id": "sess",
			"results": []map[string]any{{
				"title": "Example", "url": "https://example.com",
				"excerpts": []string{"relevant excerpt"},
			}},
		})
	}))
	defer fake.Close()

	app := parallelApp(parallel.New(fake.URL, "sk_test"))
	req := httptest.NewRequest(http.MethodPost, "/v1/parallel/search",
		bytes.NewReader(jsonBody(t, map[string]any{
			"objective":      "latest vector db benchmarks",
			"search_queries": []string{"vector db benchmark"},
			"mode":           "turbo",
		})))
	req.Header.Set("Authorization", "Bearer testkey")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotPath != "/v1/search" {
		t.Errorf("upstream path = %q, want /v1/search", gotPath)
	}
	if gotKey != "sk_test" {
		t.Errorf("upstream x-api-key = %q", gotKey)
	}
	if gotMode != "turbo" || gotObjective != "latest vector db benchmarks" {
		t.Errorf("upstream body mode=%q objective=%q", gotMode, gotObjective)
	}
	var out parallel.SearchResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Results) != 1 || out.Results[0].URL != "https://example.com" {
		t.Errorf("response mapping wrong: %+v", out.Results)
	}
}

func TestParallelSearch_DefaultModeFromConfig(t *testing.T) {
	var gotMode string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode string `json:"mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotMode = body.Mode
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	}))
	defer fake.Close()

	app := parallelApp(parallel.New(fake.URL, "sk_test"))
	req := httptest.NewRequest(http.MethodPost, "/v1/parallel/search",
		bytes.NewReader(jsonBody(t, map[string]any{"objective": "q"})))
	req.Header.Set("Authorization", "Bearer testkey")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	resp.Body.Close()
	if gotMode != "fast" {
		t.Errorf("default mode = %q, want fast (handler parallelMode)", gotMode)
	}
}

func TestParallelExtract_BatchesAndValidates(t *testing.T) {
	var gotURLs []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URLs []string `json:"urls"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotURLs = body.URLs
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"title": "Doc", "url": gotURLs[0],
				"full_content": "# markdown",
			}},
		})
	}))
	defer fake.Close()

	app := parallelApp(parallel.New(fake.URL, "sk_test"))

	// Missing urls -> 400
	req := httptest.NewRequest(http.MethodPost, "/v1/parallel/extract",
		bytes.NewReader(jsonBody(t, map[string]any{"objective": "x"})))
	req.Header.Set("Authorization", "Bearer testkey")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty urls: status = %d, want 400", resp.StatusCode)
	}

	// Happy path
	req2 := httptest.NewRequest(http.MethodPost, "/v1/parallel/extract",
		bytes.NewReader(jsonBody(t, map[string]any{"urls": []string{"https://example.com/a", "https://example.com/b"}})))
	req2.Header.Set("Authorization", "Bearer testkey")
	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp2.StatusCode)
	}
	if len(gotURLs) != 2 {
		t.Errorf("upstream got %d urls, want 2", len(gotURLs))
	}
}

func TestParallelUpstreamErrorPassthrough(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"detail":"rate limited"}`))
	}))
	defer fake.Close()

	app := parallelApp(parallel.New(fake.URL, "sk_test"))
	req := httptest.NewRequest(http.MethodPost, "/v1/parallel/search",
		bytes.NewReader(jsonBody(t, map[string]any{"objective": "q"})))
	req.Header.Set("Authorization", "Bearer testkey")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 passthrough", resp.StatusCode)
	}
}

func jsonBody(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestParallelSearch_FallsBackToKeylessStealth(t *testing.T) {
	// Fake DDG HTML served by a stubbed hermes sidecar.
	fakeDDG := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":       200,
			"headers":      map[string]string{"content-type": "text/html"},
			"http_version": "2.0",
			"data":         `<a class="result__a" href="https://stealth.example/x">Stealth Hit</a>`,
		})
	}))
	defer fakeDDG.Close()

	h := newHandlers("m", nil, nil, nil)
	h.parallel = parallel.New("", "") // no key -> keyless path
	h.webSearcher = websearch.New(hermes.New(fakeDDG.URL))

	app := fiber.New()
	app.Post("/v1/parallel/search", authMiddleware("testkey"), h.ParallelSearch)

	req := httptest.NewRequest(http.MethodPost, "/v1/parallel/search",
		bytes.NewReader(jsonBody(t, map[string]any{"objective": "stealth query"})))
	req.Header.Set("Authorization", "Bearer testkey")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body := new(strings.Builder)
		_, _ = io.Copy(body, resp.Body)
		t.Fatalf("status = %d, want 200 from keyless backend; body=%s", resp.StatusCode, body.String())
	}
	var out websearch.Response
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Backend != "duckduckgo-stealth" || len(out.Results) != 1 ||
		out.Results[0].URL != "https://stealth.example/x" {
		t.Fatalf("keyless result wrong: %+v", out)
	}
}

func TestParallelSearch_NoBackendAtAllReturns503(t *testing.T) {
	h := newHandlers("m", nil, nil, nil)
	h.parallel = parallel.New("", "")
	h.webSearcher = nil // no sidecar either
	app := fiber.New()
	app.Post("/v1/parallel/search", authMiddleware("testkey"), h.ParallelSearch)

	req := httptest.NewRequest(http.MethodPost, "/v1/parallel/search",
		bytes.NewReader(jsonBody(t, map[string]any{"objective": "q"})))
	req.Header.Set("Authorization", "Bearer testkey")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}
