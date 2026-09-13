package lmarena

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Public leaderboard snapshot served from HuggingFace read-only data
// (lmarena-ai/leaderboard-dataset, CC-BY-4.0) via the datasets-server JSON
// API — no parquet dependency, stdlib HTTP only. Refreshed at most once
// per Refresh interval; stale cache is served when a refresh fails, so the
// gateway never 502s on HF downtime after the first successful fetch.

// DefaultDatasetsServer is the public HF datasets-server endpoint.
const DefaultDatasetsServer = "https://datasets-server.huggingface.co"

// DefaultLeaderboardDataset is the CC-BY-4.0 community leaderboard dump.
const DefaultLeaderboardDataset = "lmarena-ai/leaderboard-dataset"

// Entry is one ranked model in a category.
type Entry struct {
	Model        string  `json:"model"`
	Organization string  `json:"organization,omitempty"`
	Rating       float64 `json:"rating"`
	RatingLower  float64 `json:"rating_lower,omitempty"`
	RatingUpper  float64 `json:"rating_upper,omitempty"`
	Votes        int     `json:"votes,omitempty"`
	Rank         int     `json:"rank"`
}

// Snapshot is a cached category ranking.
type Snapshot struct {
	Category  string    `json:"category"`
	Updated   string    `json:"updated,omitempty"` // leaderboard_publish_date
	FetchedAt time.Time `json:"fetched_at"`
	Entries   []Entry   `json:"entries"`
}

// Leaderboard fetches and caches snapshots. Zero value is disabled;
// construct with NewLeaderboard.
type Leaderboard struct {
	mu      sync.Mutex
	dir     string
	base    string
	dataset string
	refresh time.Duration
	client  *http.Client
}

// NewLeaderboard builds a snapshot source. Empty dir disables disk cache
// (memory-only per call); refresh <= 0 disables refresh (cache-only).
func NewLeaderboard(dir, base string, refresh time.Duration) *Leaderboard {
	if base == "" {
		base = DefaultDatasetsServer
	}
	return &Leaderboard{
		dir:     dir,
		base:    base,
		dataset: DefaultLeaderboardDataset,
		refresh: refresh,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (l *Leaderboard) cachePath(category string) string {
	return filepath.Join(l.dir, "leaderboard-"+category+".json")
}

func (l *Leaderboard) loadCache(category string) (Snapshot, bool) {
	var s Snapshot
	if l.dir == "" {
		return s, false
	}
	data, err := os.ReadFile(l.cachePath(category))
	if err != nil {
		return s, false
	}
	if err := json.Unmarshal(data, &s); err != nil || len(s.Entries) == 0 {
		return s, false
	}
	return s, true
}

func (l *Leaderboard) saveCache(s Snapshot) {
	if l.dir == "" {
		return
	}
	_ = os.MkdirAll(l.dir, 0o755)
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	_ = os.WriteFile(l.cachePath(s.Category), data, 0o644)
}

// Cached returns the on-disk snapshot without network. Used by status
// surfaces that must never block on a 100-page fetch.
func (l *Leaderboard) Cached(category string) (Snapshot, bool) {
	if category == "" {
		category = "overall"
	}
	return l.loadCache(category)
}

// Get returns the category snapshot, refreshing when the cache is older
// than Refresh. A failed refresh falls back to stale cache; with neither,
// it returns the fetch error.
func (l *Leaderboard) Get(ctx context.Context, category string) (Snapshot, error) {
	if category == "" {
		category = "overall"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cached, ok := l.loadCache(category)
	if ok && l.refresh > 0 && time.Since(cached.FetchedAt) < l.refresh {
		return cached, nil
	}
	snap, err := l.fetch(ctx, category)
	if err != nil {
		if ok {
			return cached, nil
		}
		return Snapshot{}, err
	}
	l.saveCache(snap)
	return snap, nil
}

// rowsPage mirrors the datasets-server /rows envelope.
type rowsPage struct {
	Rows []struct {
		Row map[string]any `json:"row"`
	} `json:"rows"`
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// fetch pages the whole config/split (latest = ~10k rows, length 100/page)
// with 8 workers sharing an atomic offset dispenser; workers stop once a
// short page arrives. Only rows of the requested category are kept.
func (l *Leaderboard) fetch(ctx context.Context, category string) (Snapshot, error) {
	const pageLen = 100
	const workers = 8
	var (
		mu       sync.Mutex
		all      []map[string]any
		fetchErr error
		done     atomic.Bool
		next     atomic.Int64
	)
	fetchPage := func(off int) ([]map[string]any, error) {
		q := url.Values{}
		q.Set("dataset", l.dataset)
		q.Set("config", "text")
		q.Set("split", "latest")
		q.Set("offset", strconv.Itoa(off))
		q.Set("length", strconv.Itoa(pageLen))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.base+"/rows?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := l.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("leaderboard fetch: http %d", resp.StatusCode)
		}
		var pg rowsPage
		if err := json.NewDecoder(resp.Body).Decode(&pg); err != nil {
			return nil, err
		}
		rows := make([]map[string]any, 0, len(pg.Rows))
		for _, r := range pg.Rows {
			rows = append(rows, r.Row)
		}
		return rows, nil
	}
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if done.Load() {
					return
				}
				off := int(next.Add(pageLen) - pageLen)
				rows, err := fetchPage(off)
				if err != nil {
					mu.Lock()
					if fetchErr == nil {
						fetchErr = err
					}
					mu.Unlock()
					done.Store(true)
					return
				}
				mu.Lock()
				all = append(all, rows...)
				mu.Unlock()
				if len(rows) < pageLen {
					done.Store(true)
					return
				}
			}
		}()
	}
	wg.Wait()
	if fetchErr != nil {
		return Snapshot{}, fetchErr
	}
	snap := Snapshot{Category: category, FetchedAt: time.Now().UTC()}
	for _, row := range all {
		if str(row["category"]) != category {
			continue
		}
		snap.Entries = append(snap.Entries, Entry{
			Model:        str(row["model_name"]),
			Organization: str(row["organization"]),
			Rating:       num(row["rating"]),
			RatingLower:  num(row["rating_lower"]),
			RatingUpper:  num(row["rating_upper"]),
			Votes:        int(num(row["vote_count"])),
			Rank:         int(num(row["rank"])),
		})
		if snap.Updated == "" {
			snap.Updated = str(row["leaderboard_publish_date"])
		}
	}
	if len(snap.Entries) == 0 {
		return Snapshot{}, fmt.Errorf("leaderboard: no rows for category %q", category)
	}
	// Pages arrive out of order under concurrency; rank is authoritative.
	sort.Slice(snap.Entries, func(i, j int) bool { return snap.Entries[i].Rank < snap.Entries[j].Rank })
	return snap, nil
}
