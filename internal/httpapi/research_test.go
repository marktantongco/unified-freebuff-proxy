package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"freebuff-unified/internal/parallel"
)

// fakeParallelAPI is an in-process Parallel API used to exercise the Layer A
// Task-run endpoints and the keyed native search/extract paths. createFail
// makes POST /v1/tasks/runs return 401 so DeepResearch falls back to Layer B.
type fakeParallelAPI struct {
	createFail bool
	createHits int
}

func (f *fakeParallelAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/tasks/runs":
		f.createHits++
		if f.createFail {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintln(w, `{"error":{"message":"api key required"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"run_id":"run-1","interaction_id":"int-1","status":"queued","processor":"pro-fast"}`)

	case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/runs/run-1":
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"run_id":"run-1","interaction_id":"int-1","status":"completed","processor":"pro-fast"}`)

	case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/runs/run-1/result":
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"run_id":"run-1","status":"completed","output":{"content":"# Result\n\nBody text","basis":[{"field":"x","value":"y"}]}}`)

	case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/runs/run-1/events":
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"task.update\",\"status\":\"completed\"}\n\n")

	case r.Method == http.MethodPost && r.URL.Path == "/v1/search":
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"search_id":"s1","session_id":"deep-research","results":[{"title":"Parallel Docs","url":"https://example.com/1","excerpts":["Snippet one"]}]}`)

	case r.Method == http.MethodPost && r.URL.Path == "/v1/extract":
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"extract_id":"e1","results":[{"title":"Parallel Docs","url":"https://example.com/1","full_content":"Page content for synthesis."}]}`)

	case r.Method == http.MethodPost && r.URL.Path == "/v1/responses":
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_upstream","object":"response","status":"completed"}`)

	default:
		http.NotFound(w, r)
	}
}

// researchApp wires a keyed Parallel client pointing at fake through the real
// NewApp with native Layer B research enabled.
func researchApp(t *testing.T, fake *fakeParallelAPI) *fiber.App {
	t.Helper()
	ts := httptest.NewServer(fake)
	t.Cleanup(ts.Close)
	return NewApp(Options{
		Model:             "test-model",
		ProxyAPIKey:       "k",
		Chat:              okChatService{},
		Parallel:          parallel.New(ts.URL, "test-key"),
		ParallelMode:      "fast",
		ParallelProcessor: "pro-fast",
		Research: ResearchConfig{
			Enabled:         true,
			MaxQueries:      2,
			FanOut:          2,
			ExtractPerQuery: 1,
		},
	})
}

// postDeepResearch posts a JSON body to /v1/deep-research as an authed client.
func postDeepResearch(t *testing.T, app *fiber.App, body string) (int, string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("test body %q is not valid JSON: %v", body, err)
	}
	var sb strings.Builder
	sb.WriteString(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/deep-research", strings.NewReader(sb.String()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer k")
	return doRequest(t, app, req)
}

func mustContain(t *testing.T, body, substr string) {
	t.Helper()
	if !strings.Contains(body, substr) {
		t.Fatalf("response body does not contain %q:\n%s", substr, body)
	}
}

// ---------------------------------------------------------------------------
// Layer A: Task-run orchestration
// ---------------------------------------------------------------------------

func TestDeepResearchLayerACreate202(t *testing.T) {
	app := researchApp(t, &fakeParallelAPI{})
	status, body := postDeepResearch(t, app, `{"input":"top LLM inference engines","stream":false}`)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", status)
	}
	mustContain(t, body, `"run_id":"run-1"`)
	mustContain(t, body, `"backend":"parallel"`)
	mustContain(t, body, `"processor":"pro-fast"`)
	mustContain(t, body, `"result_url":"/v1/deep-research/run-1"`)
}

func TestDeepResearchLayerAGetStatusAndResult(t *testing.T) {
	app := researchApp(t, &fakeParallelAPI{})
	req := httptest.NewRequest(http.MethodGet, "/v1/deep-research/run-1", nil)
	req.Header.Set("Authorization", "Bearer k")
	status, body := doRequest(t, app, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	m := decodeBody(t, body)
	if m["result"] == nil && m["error"] != nil {
		t.Fatalf("error result payload: %v", m)
	}
	if m["backend"] != "parallel" {
		t.Errorf("backend = %v, want parallel", m["backend"])
	}
	if m["status"] != "completed" {
		t.Errorf("status = %v, want completed", m["status"])
	}
	if res, ok := m["result"].(map[string]any); !ok {
		t.Errorf("result payload missing: %v", m["result"])
	} else if out, ok := res["output"].(map[string]any); !ok || out["content"] == nil {
		t.Errorf("result.output.content missing: %v", m["result"])
	}
}

func TestDeepResearchLayerAStreamRelaysEvents(t *testing.T) {
	app := researchApp(t, &fakeParallelAPI{})
	status, body := postDeepResearch(t, app, `{"input":"top LLM inference engines","stream":true}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	mustContain(t, body, `"type":"task.update"`)
	mustContain(t, body, `"type":"done"`)
	mustContain(t, body, `"content":"# Result`)
}

// ---------------------------------------------------------------------------
// Layer B: native fallback on keyless/auth rejection
// ---------------------------------------------------------------------------

func TestDeepResearchKeylessFallbackBuffered(t *testing.T) {
	fake := &fakeParallelAPI{createFail: true}
	app := researchApp(t, fake)
	status, body := postDeepResearch(t, app, `{"input":"top LLM inference engines","stream":false}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (buffered native), body=%s", status, body)
	}
	m := decodeBody(t, body)
	if m["report"] != "ok" {
		t.Errorf("report = %v, want ok (chat service output)", m["report"])
	}
	if fake.createHits == 0 {
		t.Error("Layer A create was never attempted")
	}
}

func TestDeepResearchKeylessFallbackStream(t *testing.T) {
	// Reproduces the streaming path (plan → search/extract → synthesis frames)
	// that previously returned an empty body.
	fake := &fakeParallelAPI{createFail: true}
	app := researchApp(t, fake)
	status, body := postDeepResearch(t, app, `{"input":"top LLM inference engines","stream":true,"max_queries":2,"fan_out":2,"extract_per_query":1}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", status, body)
	}
	mustContain(t, body, `"type":"plan"`)
	mustContain(t, body, `"type":"synthesis"`)
	mustContain(t, body, `"type":"done"`)
	mustContain(t, body, `"report":"ok"`)
}

// ---------------------------------------------------------------------------
// Guards
// ---------------------------------------------------------------------------

func TestDeepResearchDisabled(t *testing.T) {
	app := NewApp(Options{
		Model:       "test-model",
		ProxyAPIKey: "k",
		Chat:        okChatService{},
	})
	status, body := postDeepResearch(t, app, `{"input":"anything","stream":false}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
	mustContain(t, body, "deep-research is not enabled")
}

func TestDeepResearchRequiresInput(t *testing.T) {
	app := researchApp(t, &fakeParallelAPI{})
	status, body := postDeepResearch(t, app, `{"stream":false}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	mustContain(t, body, "input field is required")
}

// ---------------------------------------------------------------------------
// /v1/responses
// ---------------------------------------------------------------------------

func TestResponsesPassthroughUpstream(t *testing.T) {
	app := researchApp(t, &fakeParallelAPI{})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"input":"say hi","model":"test-model"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer k")
	status, body := doRequest(t, app, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	mustContain(t, body, `"id":"resp_upstream"`)
}
