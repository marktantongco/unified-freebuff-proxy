package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"freebuff-unified/internal/eval"
	"github.com/gofiber/fiber/v3"
)

func newEvalTestApp(t *testing.T) *fiber.App {
	t.Helper()
	store, err := eval.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("eval store: %v", err)
	}
	return NewApp(Options{
		Model:       "test-model",
		ProxyAPIKey: "k",
		Chat:        okChatService{},
		EvalStore:   store,
	})
}

func evalReq(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer k")
	return req
}

func TestEvalFullFlow(t *testing.T) {
	app := newEvalTestApp(t)

	// Create.
	code, body := doRequest(t, app, evalReq(t, http.MethodPost, "/v1/lmarena/evals", `{"name":"b1"}`))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	id := decodeBody(t, body)["eval"].(map[string]any)["id"].(string)

	// Empty name rejected.
	if code, _ := doRequest(t, app, evalReq(t, http.MethodPost, "/v1/lmarena/evals", `{"name":""}`)); code != http.StatusBadRequest {
		t.Fatalf("empty name: want 400, got %d", code)
	}

	// Add sealed round.
	code, body = doRequest(t, app, evalReq(t, http.MethodPost, "/v1/lmarena/evals/"+id+"/rounds",
		`{"prompt":"2+2?","output_a":"4","output_b":"four","model_a":"zenith","model_b":"summit"}`))
	if code != http.StatusCreated {
		t.Fatalf("add round: %d %s", code, body)
	}
	round := decodeBody(t, body)["round"].(map[string]any)
	rid := round["id"].(string)
	if _, sealed := round["model_a"]; sealed {
		t.Fatal("sealed labels must not leak on create")
	}

	// Get stays blind, score 0 voted.
	code, body = doRequest(t, app, evalReq(t, http.MethodGet, "/v1/lmarena/evals/"+id, ""))
	if code != http.StatusOK {
		t.Fatalf("get: %d %s", code, body)
	}
	got := decodeBody(t, body)
	if got["score"].(map[string]any)["voted"].(float64) != 0 {
		t.Fatalf("fresh score must be 0 voted: %s", body)
	}

	// Bad winner rejected.
	if code, _ := doRequest(t, app, evalReq(t, http.MethodPost, "/v1/lmarena/evals/"+id+"/rounds/"+rid+"/vote", `{"winner":"c"}`)); code != http.StatusBadRequest {
		t.Fatalf("bad winner: want 400, got %d", code)
	}

	// Vote b.
	if code, _ := doRequest(t, app, evalReq(t, http.MethodPost, "/v1/lmarena/evals/"+id+"/rounds/"+rid+"/vote", `{"winner":"b"}`)); code != http.StatusOK {
		t.Fatalf("vote: want 200, got %d", code)
	}

	// Double vote rejected.
	if code, _ := doRequest(t, app, evalReq(t, http.MethodPost, "/v1/lmarena/evals/"+id+"/rounds/"+rid+"/vote", `{"winner":"a"}`)); code != http.StatusBadRequest {
		t.Fatalf("double vote: want 400, got %d", code)
	}

	// Reveal shows labels + model score.
	code, body = doRequest(t, app, evalReq(t, http.MethodPost, "/v1/lmarena/evals/"+id+"/reveal", `{}`))
	if code != http.StatusOK {
		t.Fatalf("reveal: %d %s", code, body)
	}
	rev := decodeBody(t, body)
	models := rev["score"].(map[string]any)["models"].(map[string]any)
	if models["summit"].(map[string]any)["wins"].(float64) != 1 {
		t.Fatalf("summit must have 1 win: %s", body)
	}
	if rev["eval"].(map[string]any)["rounds"].([]any)[0].(map[string]any)["model_a"].(string) != "zenith" {
		t.Fatalf("reveal must restore labels: %s", body)
	}

	// List + unknown id.
	if code, _ := doRequest(t, app, evalReq(t, http.MethodGet, "/v1/lmarena/evals", "")); code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", code)
	}
	if code, _ := doRequest(t, app, evalReq(t, http.MethodGet, "/v1/lmarena/evals/nope", "")); code != http.StatusNotFound {
		t.Fatalf("unknown: want 404, got %d", code)
	}
}

func TestEvalDisabled(t *testing.T) {
	app := NewApp(Options{Model: "m", ProxyAPIKey: "k", Chat: okChatService{}})
	req := httptest.NewRequest(http.MethodPost, "/v1/lmarena/evals", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Authorization", "Bearer k")
	if code, _ := doRequest(t, app, req); code != http.StatusServiceUnavailable {
		t.Fatalf("disabled: want 503, got %d", code)
	}
}

func TestEvalUnauthorized(t *testing.T) {
	store, err := eval.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("eval store: %v", err)
	}
	app := NewApp(Options{Model: "m", ProxyAPIKey: "k", Chat: okChatService{}, EvalStore: store})
	req := httptest.NewRequest(http.MethodGet, "/v1/lmarena/evals", nil)
	if code, _ := doRequest(t, app, req); code != http.StatusUnauthorized {
		t.Fatalf("no key: want 401, got %d", code)
	}
}
