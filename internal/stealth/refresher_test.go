package stealth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"freebuff-unified/internal/hermes"
)

func TestMetricsRecording(t *testing.T) {
	m := NewMetrics()
	m.RecordSidecar(120 * time.Millisecond)
	m.RecordSidecar(80 * time.Millisecond)
	m.RecordSidecarError(errors.New("boom"))
	m.RecordFallback()
	m.RecordEgress("1.2.3.4")
	m.RecordEgress("1.2.3.4")
	m.RecordEgress("5.6.7.8")

	snap := m.Snapshot()
	if got := snap["sidecar_requests"]; got != int64(2) {
		t.Errorf("sidecar_requests = %v, want 2", got)
	}
	if got := snap["sidecar_errors"]; got != int64(1) {
		t.Errorf("sidecar_errors = %v, want 1", got)
	}
	if got := snap["fallbacks"]; got != int64(1) {
		t.Errorf("fallbacks = %v, want 1", got)
	}
	lat := snap["latency_ms"].(map[string]int64)
	if lat["last"] != 80 || lat["avg"] != 100 || lat["max"] != 120 {
		t.Errorf("latency = %v, want last=80 avg=100 max=120", lat)
	}
	if snap["egress_ips_used"] != 2 {
		t.Errorf("egress_ips_used = %v, want 2", snap["egress_ips_used"])
	}
	if snap["last_egress_ip"] != "5.6.7.8" {
		t.Errorf("last_egress_ip = %v", snap["last_egress_ip"])
	}
	if snap["last_sidecar_error"] != "boom" {
		t.Errorf("last_sidecar_error = %v", snap["last_sidecar_error"])
	}
}

func TestMetricsNilSafe(t *testing.T) {
	var m *Metrics
	m.RecordSidecar(time.Second) // must not panic
	m.RecordEgress("1.1.1.1")    // must not panic
	m.RecordSidecarError(nil)    // must not panic
	if m.Snapshot() != nil {
		t.Error("nil metrics Snapshot should be nil")
	}
}

func TestUSProxyPoolReplace(t *testing.T) {
	p := NewUSProxyPool([]string{"socks5://10.0.0.1:1080", "socks5://10.0.0.2:1080"}, nil)
	if p.Size() != 2 {
		t.Fatalf("initial size = %d", p.Size())
	}
	n := p.Replace([]string{"socks5://10.0.0.9:9999", "not a url", "", "socks5://10.0.0.8:1080"})
	if n != 2 {
		t.Fatalf("Replace returned %d, want 2 (invalid+empty skipped)", n)
	}
	got := map[string]bool{}
	for i := 0; i < 4; i++ {
		if u := p.Next(); u != nil {
			got[u.String()] = true
		}
	}
	if len(got) != 2 || !got["socks5://10.0.0.9:9999"] || !got["socks5://10.0.0.8:1080"] {
		t.Errorf("round-robin after replace = %v", got)
	}
}

func TestRefresherRefreshProbesAndSwaps(t *testing.T) {
	// Fake sidecar: succeeds only for proxies containing "good".
	sidecarSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req hermes.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Proxy, "good") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":       200,
				"headers":      map[string]string{"content-type": "application/json"},
				"http_version": "2.0",
				// The real sidecar parses JSON bodies and embeds them as JSON
				// objects, not strings.
				"data": json.RawMessage(`{"ip":"9.9.9.9"}`),
			})
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "socks5 handshake timeout"})
	}))
	defer sidecarSrv.Close()

	pool := NewUSProxyPool([]string{"socks5://1.1.1.1:1080"}, nil)
	var mu sync.Mutex
	var egressSeen []string
	ref := NewRefresher(pool, hermes.New(sidecarSrv.URL), nil)
	ref.MaxProxies = 2
	ref.geofilter = "" // skip geo lookups in test
	ref.ProbeTimeout = 3 * time.Second
	// Probes run concurrently: guard the observation slice.
	ref.OnEgress = func(ip string) {
		mu.Lock()
		egressSeen = append(egressSeen, ip)
		mu.Unlock()
	}
	ref.fetchFn = func() ([]string, int) {
		return []string{
			"socks5://good1.example:1080",
			"socks5://good2.example:1080",
			"socks5://bad1.example:1080",
			"socks5://bad2.example:1080",
		}, 0
	}

	ref.Refresh(context.Background())

	if pool.Size() != 2 {
		t.Fatalf("pool size after refresh = %d, want 2", pool.Size())
	}
	st := ref.Status()
	if st["state"] != "ok" || st["alive"] != 2 || st["swapped_in"] != 2 {
		t.Fatalf("refresher status = %v", st)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(egressSeen) != 2 {
		t.Errorf("egress observations = %v, want 2", egressSeen)
	}
	// Round-robin serves only the surviving proxies.
	served := map[string]bool{}
	for i := 0; i < 4; i++ {
		if u := pool.Next(); u != nil {
			served[u.String()] = true
		}
	}
	if len(served) != 2 {
		t.Errorf("served proxies = %v, want the 2 good ones", served)
	}
}

func TestRefresherKeepsPoolWhenAllDead(t *testing.T) {
	sidecarSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req hermes.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "timeout"})
	}))
	defer sidecarSrv.Close()

	pool := NewUSProxyPool([]string{"socks5://1.1.1.1:1080"}, nil)
	ref := NewRefresher(pool, hermes.New(sidecarSrv.URL), nil)
	ref.geofilter = ""
	ref.ProbeTimeout = 2 * time.Second
	ref.fetchFn = func() ([]string, int) {
		return []string{"socks5://dead.example:1080"}, 0
	}

	ref.Refresh(context.Background())

	if pool.Size() != 1 {
		t.Fatalf("pool size = %d, want 1 (existing pool kept)", pool.Size())
	}
	st := ref.Status()
	if st["action"] != "kept_existing_pool" {
		t.Errorf("action = %v, want kept_existing_pool", st["action"])
	}
}

func TestRefresherFetchCandidatesParsesAndDedupes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "a"):
			_, _ = w.Write([]byte("1.1.1.1:1080\nsocks5://2.2.2.2:1080\n#comment\n\n0.0.0.0:80\n"))
		case strings.HasSuffix(r.URL.Path, "b"):
			_, _ = w.Write([]byte("1.1.1.1:1080\n3.3.3.3:1080\n"))
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()

	ref := NewRefresher(nil, nil, nil)
	ref.fetchFn = nil
	candidates, errs := fetchCandidatesFromURLs(ref, []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/err"})
	if errs != 1 {
		t.Errorf("source errors = %d, want 1", errs)
	}
	want := map[string]bool{
		"socks5://1.1.1.1:1080": true,
		"socks5://2.2.2.2:1080": true,
		"socks5://3.3.3.3:1080": true,
	}
	if len(candidates) != len(want) {
		t.Fatalf("candidates = %v, want %v", candidates, want)
	}
	for _, c := range candidates {
		if !want[c] {
			t.Errorf("unexpected candidate %q", c)
		}
	}
}

// fetchCandidatesFromURLs runs Refresher.fetchCandidates against explicit
// sources by temporarily overriding them.
func fetchCandidatesFromURLs(ref *Refresher, sources []string) ([]string, int) {
	// fetchCandidates reads r.Sources(); inject ours for the call.
	orig := ref.sourceURLs
	ref.sourceURLs = sources
	defer func() { ref.sourceURLs = orig }()
	return ref.fetchCandidates()
}
