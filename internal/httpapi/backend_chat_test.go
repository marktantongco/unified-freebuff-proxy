package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"freebuff-unified/internal/openai"
	"freebuff-unified/internal/parallel"
)

// stubChatBackend is an OpenAI-compatible /v1/chat/completions backend used to
// prove the research planner and synthesizer run through BackendChatService.
type stubChatBackend struct {
	chatErrStatus int // when set, all completions fail with this status
}

func (s *stubChatBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	var req openai.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
		return
	}
	if s.chatErrStatus != 0 {
		w.WriteHeader(s.chatErrStatus)
		fmt.Fprintf(w, `{"error":{"message":"backend down (%d)"}}`, s.chatErrStatus)
		return
	}

	content := "ok"
	if len(req.Messages) > 0 {
		system, _ := req.Messages[0].Content.(string)
		switch {
		case strings.Contains(system, "research planner"):
			content = `{"queries":["inference engine vLLM", "LLM serving framework"],"outline":["Intro","Comparison"]}`
		case strings.Contains(system, "research analyst"):
			content = "# Findings\n\nResearched via backend chat with [1].\n\n## References\n\n- [1] Parallel Docs: https://example.com/1"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if !req.Stream {
		fmt.Fprintf(w, `{"id":"cb-1","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":%q}}]}`, content)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, chunk := range []string{content[:1], content[1:]} {
		fmt.Fprintf(w, "data: {\"id\":\"cb-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", chunk)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestBackendChatServiceComplete(t *testing.T) {
	be := httptest.NewServer(&stubChatBackend{})
	t.Cleanup(be.Close)

	svc := NewBackendChatService(be.URL, "k")
	got, err := svc.Complete(t.Context(), openai.ChatCompletionRequest{
		Model:    "test-model",
		Messages: []openai.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "ok" {
		t.Errorf("Complete = %q, want ok", got)
	}
}

func TestBackendChatServiceStream(t *testing.T) {
	be := httptest.NewServer(&stubChatBackend{})
	t.Cleanup(be.Close)

	svc := NewBackendChatService(be.URL, "k")
	deltas, errs := svc.Stream(t.Context(), openai.ChatCompletionRequest{
		Model:    "test-model",
		Messages: []openai.ChatMessage{{Role: "user", Content: "hi"}},
	})
	var got strings.Builder
	for {
		select {
		case e, open := <-errs:
			if e != nil {
				t.Fatalf("Stream error: %v", e)
			}
			if !open {
				errs = nil
			}
		case d, open := <-deltas:
			if open {
				got.WriteString(d)
			} else {
				deltas = nil
			}
		}
		if deltas == nil && errs == nil {
			break
		}
	}
	if got.String() != "ok" {
		t.Errorf("stream deltas = %q, want ok", got.String())
	}
}

func TestBackendChatServiceErrors(t *testing.T) {
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusTooManyRequests, "rate_limit_exceeded"},
		{http.StatusInternalServerError, "backend_chat_unavailable"},
		{http.StatusBadRequest, "backend_chat_error"},
	}
	for _, tc := range cases {
		be := httptest.NewServer(&stubChatBackend{chatErrStatus: tc.status})
		svc := NewBackendChatService(be.URL, "k")
		_, err := svc.Complete(t.Context(), openai.ChatCompletionRequest{
			Model:    "test-model",
			Messages: []openai.ChatMessage{{Role: "user", Content: "hi"}},
		})
		be.Close()
		var se *ServiceError
		if !errors.As(err, &se) {
			t.Fatalf("status %d: not a ServiceError: %T %v", tc.status, err, err)
		}
		if se.Code != tc.code {
			t.Errorf("status %d: code = %q, want %q", tc.status, se.Code, tc.code)
		}
	}
}

// TestDeepResearchKeylessFallbackBackendChat exercises the full native Layer B
// path with the planner/synthesizer served by BackendChatService (mirrors the
// passthrough-mode production wiring where the backend owns chat).
func TestDeepResearchKeylessFallbackBackendChat(t *testing.T) {
	be := httptest.NewServer(&stubChatBackend{})
	t.Cleanup(be.Close)
	fake := &fakeParallelAPI{createFail: true}
	ts := httptest.NewServer(fake)
	t.Cleanup(ts.Close)

	app := NewApp(Options{
		Model:             "test-model",
		ProxyAPIKey:       "k",
		Chat:              NewBackendChatService(be.URL, "k"),
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

	status, body := postDeepResearch(t, app, `{"input":"LLM inference engines","stream":false}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (keyless Layer A 401 -> native Layer B)", status)
	}
	if fake.createHits != 1 {
		t.Errorf("createHits = %d, want 1 (keyless attempt recorded)", fake.createHits)
	}
	mustContain(t, body, `"report":"# Findings`)
	mustContain(t, body, `"queries":`)
	// The backend chat service drove both planner and synthesizer.
	mustContain(t, body, `"planner_model":"test-model"`)
	mustContain(t, body, `"synth_model":"test-model"`)
}
