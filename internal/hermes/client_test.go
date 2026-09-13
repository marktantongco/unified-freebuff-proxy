package hermes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeSidecar is a minimal stand-in for the Node hermes sidecar.
func fakeSidecar(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(srv.URL), srv
}

func TestFetchSuccess(t *testing.T) {
	client, _ := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/fetch" {
			t.Errorf("path = %s, want /v1/fetch", r.URL.Path)
		}
		var req Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.URL != "https://example.com" {
			t.Errorf("url = %s", req.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":200,"headers":{"content-type":"application/json"},"http_version":"1.1","data":{"hello":"world"},"elapsed_ms":42}`))
	})

	resp, err := client.Fetch(context.Background(), Request{URL: "https://example.com", Method: "GET"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("status = %d, want 200", resp.Status)
	}
	if resp.HTTPVersion != "1.1" {
		t.Errorf("http_version = %s", resp.HTTPVersion)
	}
	if resp.ElapsedMS != 42 {
		t.Errorf("elapsed_ms = %d", resp.ElapsedMS)
	}
	if resp.Text() == "" {
		t.Error("Text() empty for JSON body")
	}
}

func TestFetchBinaryB64(t *testing.T) {
	client, _ := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":200,"headers":{},"http_version":"1.1","data_b64":"SGVsbG8=","elapsed_ms":1}`))
	})
	resp, err := client.Fetch(context.Background(), Request{URL: "https://example.com/x.png"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	b, ok := resp.Decoded.([]byte)
	if !ok {
		t.Fatalf("Decoded type = %T, want []byte", resp.Decoded)
	}
	if string(b) != "Hello" {
		t.Errorf("decoded = %q, want Hello", b)
	}
}

func TestFetchUpstreamError(t *testing.T) {
	client, _ := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"getaddrinfo ENOTFOUND nope.invalid"}`))
	})
	resp, err := client.Fetch(context.Background(), Request{URL: "https://nope.invalid"})
	if err == nil {
		t.Fatalf("expected error, got response %+v", resp)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	client, _ := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			_, _ = w.Write([]byte(`{"deleted":true}`))
			return
		}
		if r.URL.Path != "/v1/session/abc" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":200,"headers":{},"http_version":"1.1","data":"ok","elapsed_ms":3}`))
	})

	resp, err := client.FetchWithSession(context.Background(), "abc", Request{URL: "https://example.com"})
	if err != nil {
		t.Fatalf("FetchWithSession: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("status = %d", resp.Status)
	}
	if err := client.DropSession(context.Background(), "abc"); err != nil {
		t.Fatalf("DropSession: %v", err)
	}
}

func TestHealth(t *testing.T) {
	client, _ := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok","service":"hermes-sidecar","hermes":"1.3.4","node":"v22","sessions":2,"uptime_s":10}`))
	})
	h, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Status != "ok" || h.Hermes != "1.3.4" || h.Sessions != 2 {
		t.Errorf("health = %+v", h)
	}
}
