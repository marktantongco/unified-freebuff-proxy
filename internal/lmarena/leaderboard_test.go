package lmarena

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubRows serves datasets-server shaped pages from mem rows.
func stubRows(t *testing.T, rows []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		n, _ := strconv.Atoi(r.URL.Query().Get("length"))
		if n <= 0 {
			n = 100
		}
		if off > len(rows) {
			off = len(rows)
		}
		end := off + n
		if end > len(rows) {
			end = len(rows)
		}
		type row struct {
			Row map[string]any `json:"row"`
		}
		out := map[string]any{"rows": []row{}}
		for _, m := range rows[off:end] {
			out["rows"] = append(out["rows"].([]row), row{Row: m})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
}

func TestLeaderboardFetchFilterCache(t *testing.T) {
	var rows []map[string]any
	for i := 1; i <= 250; i++ {
		cat := "overall"
		if i%5 == 0 {
			cat = "coding"
		}
		rows = append(rows, map[string]any{
			"model_name": "model-" + strconv.Itoa(i),
			"rating":     float64(1500 - i),
			"rank":       float64(i),
			"category":   cat,
		})
	}
	srv := stubRows(t, rows)
	defer srv.Close()

	lb := NewLeaderboard(t.TempDir(), srv.URL, time.Hour)
	snap, err := lb.Get(context.Background(), "overall")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(snap.Entries) != 200 {
		t.Fatalf("want 200 overall rows, got %d", len(snap.Entries))
	}
	if snap.Entries[0].Model != "model-1" || snap.Entries[0].Rank != 1 {
		t.Fatalf("bad first entry: %+v", snap.Entries[0])
	}
	// Kill server: cache must serve stale.
	srv.Close()
	snap2, err := lb.Get(context.Background(), "overall")
	if err != nil || len(snap2.Entries) != 200 {
		t.Fatalf("stale cache must serve: %v %d", err, len(snap2.Entries))
	}
}

func TestLeaderboardNoRows(t *testing.T) {
	srv := stubRows(t, []map[string]any{{"category": "coding"}})
	defer srv.Close()
	lb := NewLeaderboard(t.TempDir(), srv.URL, time.Hour)
	if _, err := lb.Get(context.Background(), "overall"); err == nil {
		t.Fatal("empty category must fail")
	}
}

func TestLeaderboardServerDown(t *testing.T) {
	lb := NewLeaderboard(t.TempDir(), "http://127.0.0.1:1", time.Hour)
	if _, err := lb.Get(context.Background(), "overall"); err == nil {
		t.Fatal("unreachable server with cold cache must fail")
	}
}

func TestLeaderboardConcurrentSingleRefresh(t *testing.T) {
	var rows []map[string]any
	for i := 1; i <= 350; i++ {
		rows = append(rows, map[string]any{
			"model_name": "m" + strconv.Itoa(i),
			"rating":     float64(2000 - i),
			"rank":       float64(i),
			"category":   "overall",
		})
	}
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(20 * time.Millisecond) // widen the race window
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		n, _ := strconv.Atoi(r.URL.Query().Get("length"))
		if n <= 0 {
			n = 100
		}
		if off > len(rows) {
			off = len(rows)
		}
		end := off + n
		if end > len(rows) {
			end = len(rows)
		}
		type row struct {
			Row map[string]any `json:"row"`
		}
		out := map[string]any{"rows": []row{}}
		for _, m := range rows[off:end] {
			out["rows"] = append(out["rows"].([]row), row{Row: m})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	lb := NewLeaderboard(t.TempDir(), srv.URL, time.Hour)
	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = lb.Get(context.Background(), "overall")
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Get: %v", err)
		}
	}
	// 350 rows = 4 pages; allow small overlap from racing workers, but one
	// shared refresh must not multiply into 5x.
	if h := hits.Load(); h > 12 {
		t.Fatalf("concurrent refresh must single-flight, got %d page hits", h)
	}
	// Fresh cache: next Get hits zero pages.
	before := hits.Load()
	if _, err := lb.Get(context.Background(), "overall"); err != nil {
		t.Fatalf("cached Get: %v", err)
	}
	if hits.Load() != before {
		t.Fatal("fresh cache must serve without network")
	}
}
