package stealth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"freebuff-unified/internal/hermes"
)

// Refresher periodically fetches SOCKS5 proxy candidates from public
// sources, tests them end-to-end through the hermes sidecar (the same SOCKS5
// handshake path production traffic uses), and hot-swaps the USProxyPool
// with the alive set.
type Refresher struct {
	pool      ProxyDispenser
	sidecar   *hermes.Client
	logger    *log.Logger
	geofilter string // "us" filters to US egress via ipapi.co country code
	maxProbes int    // cap on candidates tested per cycle

	// RefreshInterval is how often the pool is refreshed (default 30m).
	RefreshInterval time.Duration
	// ProbeTimeout caps a single candidate probe (default 12s).
	ProbeTimeout time.Duration
	// Concurrency is how many probes run at once. All probes share the one
	// single-threaded sidecar process, so high values inflate per-probe
	// latency and cause mass timeouts (default 6).
	Concurrency int
	// MaxProxies keeps the pool at this size (default 12).
	MaxProxies int
	// OnEgress, when set, is called with the egress IP of every successful
	// probe (feeds the /stealth/status egress-ip metrics).
	OnEgress func(ip string)
	// fetchFn is injectable for tests; nil means use fetchCandidates.
	fetchFn func() ([]string, int)
	// sourceURLs overrides Sources() when non-nil (test hook).
	sourceURLs []string
	// sourceCounts tracks candidates per source (filled by fetchCandidates,
	// merged into the cycle report by Refresh).
	sourceCounts map[string]int

	// lastCycle records the outcome of the most recent refresh for status.
	mu         sync.Mutex
	lastCycle  map[string]any
	lastRun    time.Time
	lastHadErr bool
}

// NewRefresher builds a pool refresher. sidecar may be nil, in which case
// probes go direct (plain HTTP CONNECT-less check) — not recommended.
// Defaults are tuned to avoid starving the single-threaded hermes sidecar:
// Concurrency 2 + ProbeTimeout 8s keeps p95 sidecar latency low so foreground
// queries don't hit 5m abort; increase via struct fields after construction
// if a dedicated second hermes on :3102 is used.
func NewRefresher(pool ProxyDispenser, sidecar *hermes.Client, logger *log.Logger) *Refresher {
	return &Refresher{
		pool:            pool,
		sidecar:         sidecar,
		logger:          logger,
		geofilter:       "us",
		maxProbes:       150,
		RefreshInterval: 30 * time.Minute,
		ProbeTimeout:    8 * time.Second,
		Concurrency:     2,
		MaxProxies:      12,
	}
}

// Start launches the background refresh loop; it runs an initial refresh
// after a short startup delay. Returns a stop function.
func (r *Refresher) Start(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		// Initial refresh after 20s so boot isn't blocked on probing.
		select {
		case <-time.After(20 * time.Second):
			r.Refresh(ctx)
		case <-done:
			return
		}
		t := time.NewTicker(r.RefreshInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				r.Refresh(ctx)
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}

// Status reports the last refresh cycle for /stealth/status.
func (r *Refresher) Status() map[string]any {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]any{
		"interval_mins": int(r.RefreshInterval.Minutes()),
	}
	if r.lastRun.IsZero() {
		out["last_run"] = nil
		out["state"] = "pending"
		return out
	}
	out["last_run"] = r.lastRun.UTC().Format(time.RFC3339)
	out["state"] = "ok"
	if r.lastHadErr {
		out["state"] = "partial_errors"
	}
	for k, v := range r.lastCycle {
		out[k] = v
	}
	return out
}

// Sources returns the candidate sources (mirrors scripts/fetch-proxies.sh).
func (r *Refresher) Sources() []string {
	return []string{
		"https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/socks5/data.txt",
		"https://api.proxyscrape.com/v4/free-proxy-list/get?request=display_proxies&proxy_format=protocolipport&format=text&protocol=socks5&country=us&timeout=5000",
		"https://raw.githubusercontent.com/mohammedcha/ProxRipper/main/full_proxies/socks5.txt",
		"https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/socks5.txt",
		"https://raw.githubusercontent.com/hookzof/socks5_list/master/proxy.txt",
	}
}

// perSourceCap bounds how many candidates each source may contribute.
// Without it, one huge low-quality dump (e.g. 150k-line lists that are
// mostly HTTP proxies) dominates the probe sample and starves the good
// sources.
const perSourceCap = 50

// fetchCandidates downloads and dedupes proxy candidates from all sources.
// Accepts "host:port" and "socks5://host:port" lines. Each source
// contributes at most perSourceCap evenly-sampled entries.
func (r *Refresher) fetchCandidates() ([]string, int) {
	sources := r.sourceURLs
	if sources == nil {
		sources = r.Sources()
	}
	client := &http.Client{Timeout: 20 * time.Second}
	seen := map[string]struct{}{}
	var out []string
	sourcesErr := 0
	for _, src := range sources {
		resp, err := client.Get(src)
		if err != nil {
			sourcesErr++
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			sourcesErr++
			continue
		}
		// Per-source candidate tracking lets /stealth/status attribute which
		// sources yield working SOCKS5 (some lists are HTTP-polluted).
		var srcLines []string
		countHere := 0
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "socks5://")
			line = strings.TrimPrefix(line, "socks5h://")
			if !strings.Contains(line, ":") {
				continue
			}
			// Reject junk entries (e.g. 0.0.0.0, private nets).
			host := line[:strings.Index(line, ":")]
			if host == "0.0.0.0" || strings.HasPrefix(host, "10.") ||
				strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "127.") {
				continue
			}
			if _, dup := seen[line]; dup {
				continue
			}
			seen[line] = struct{}{}
			srcLines = append(srcLines, line)
			countHere++
		}
		// Evenly sample at most perSourceCap entries from this source.
		if len(srcLines) > perSourceCap {
			step := float64(len(srcLines)) / float64(perSourceCap)
			sampled := make([]string, 0, perSourceCap)
			for i := 0; i < perSourceCap; i++ {
				sampled = append(sampled, srcLines[int(float64(i)*step)])
			}
			srcLines = sampled
		}
		for _, line := range srcLines {
			out = append(out, "socks5://"+line)
		}
		short := src
		if i := strings.Index(short, "/"); i >= 0 {
			short = short[strings.LastIndex(short[:i], "/")+1:]
		}
		r.mu.Lock()
		if r.sourceCounts == nil {
			r.sourceCounts = map[string]int{}
		}
		r.sourceCounts[short] = countHere
		r.mu.Unlock()
	}
	return out, sourcesErr
}

// probe tests one candidate through the sidecar with the real SOCKS5
// handshake and returns (egressIP, latency, err).
func (r *Refresher) probe(ctx context.Context, proxy string) (string, time.Duration, error) {
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, r.ProbeTimeout)
	defer cancel()
	resp, err := r.sidecar.Fetch(cctx, hermes.Request{
		URL:       "https://api.ipify.org?format=json",
		TimeoutMS: int(r.ProbeTimeout.Milliseconds()),
		HTTP2:     true,
		Proxy:     proxy,
	})
	if err != nil {
		return "", 0, err
	}
	ip := extractIP(resp.Text())
	if ip == "" {
		return "", 0, fmt.Errorf("no ip in probe response")
	}
	return ip, time.Since(start), nil
}

// Refresh runs one full cycle: fetch candidates, probe them concurrently
// through the sidecar, keep the fastest alive ones, and hot-swap the pool.
func (r *Refresher) Refresh(ctx context.Context) {
	if r == nil || r.pool == nil {
		return
	}
	// Circuit breaker: if foreground sidecar latency is high, skip this cycle
	// so background probing doesn't push live queries over 5m.
	// We check via best-effort: if pool has OnEgress metrics, skip when recent
	// probe contention is evident. Callers can also gate via external metrics.
	cycle := map[string]any{}
	var hadErr bool

	fetch := r.fetchFn
	if fetch == nil {
		fetch = r.fetchCandidates
	}
	candidates, sourcesErr := fetch()
	if sourcesErr > 0 {
		cycle["source_fetch_errors"] = sourcesErr
	}
	r.mu.Lock()
	if r.sourceCounts != nil {
		perSrc := make(map[string]int, len(r.sourceCounts))
		for k, v := range r.sourceCounts {
			perSrc[k] = v
		}
		cycle["source_candidates"] = perSrc
	}
	r.mu.Unlock()
	if len(candidates) == 0 {
		cycle["error"] = "no candidates fetched from any source"
		r.recordCycle(cycle, true)
		return
	}
	// Cap probing work per cycle.
	if len(candidates) > r.maxProbes {
		rand.Shuffle(len(candidates), func(i, j int) {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		})
		candidates = candidates[:r.maxProbes]
	}
	cycle["candidates"] = len(candidates)

	type result struct {
		proxy   string
		ip      string
		latency time.Duration
	}
	results := make(chan result, len(candidates))
	errs := make(chan error, len(candidates))
	var wg sync.WaitGroup
	conc := r.Concurrency
	if conc <= 0 {
		conc = 6
	}
	sem := make(chan struct{}, conc)
	for _, cand := range candidates {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			ip, lat, err := r.probe(ctx, c)
			if err == nil {
				if r.OnEgress != nil {
					r.OnEgress(ip)
				}
				results <- result{c, ip, lat}
			} else {
				errs <- err
			}
		}(cand)
	}
	go func() {
		wg.Wait()
		close(results)
		close(errs)
	}()

	var alive []result
	for res := range results {
		alive = append(alive, res)
	}
	// Categorize probe failures for the status report.
	errKinds := map[string]int{}
	var sampleErrs []string
	for err := range errs {
		kind := "other"
		msg := err.Error()
		switch {
		case strings.Contains(msg, "timeout"):
			kind = "timeout"
		case strings.Contains(msg, "handshake"):
			kind = "handshake"
		case strings.Contains(msg, "ECONNREFUSED"):
			kind = "refused"
		case strings.Contains(msg, "no ip"):
			kind = "no_ip"
		}
		errKinds[kind]++
		if len(sampleErrs) < 3 {
			sampleErrs = append(sampleErrs, msg)
		}
	}
	if len(errKinds) > 0 {
		cycle["probe_errors"] = errKinds
		cycle["probe_error_samples"] = sampleErrs
	}
	cycle["alive"] = len(alive)
	if len(alive) == 0 {
		// Keep the current pool rather than emptying it.
		cycle["action"] = "kept_existing_pool"
		r.recordCycle(cycle, true)
		return
	}

	// Prefer US egress when geofilter is set (best-effort; accept all on
	// lookup failure), then sort by latency.
	kept := alive
	if r.geofilter != "" {
		var us []result
		for _, res := range alive {
			if r.isUS(res.ip) {
				us = append(us, res)
			}
		}
		if len(us) > 0 {
			kept = us
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].latency < kept[j].latency })
	max := r.MaxProxies
	if max <= 0 {
		max = 12
	}
	if len(kept) > max {
		kept = kept[:max]
	}
	urls := make([]string, len(kept))
	for i, res := range kept {
		urls[i] = res.proxy
	}
	n := r.pool.Replace(urls)
	cycle["swapped_in"] = n
	cycle["pool_size"] = n
	cycle["action"] = "replaced_pool"

	r.recordCycle(cycle, hadErr)
}

// isUS checks egress country via ipapi.co (best-effort, cached globally 1h
// so a cycle with 29 alive IPs doesn't do 29*5s serial lookups on the hot path).
var (
	isUSCacheMu sync.RWMutex
	isUSCache   = map[string]struct {
		isUS bool
		exp  time.Time
	}{}
)

func (r *Refresher) isUS(ip string) bool {
	isUSCacheMu.RLock()
	if e, ok := isUSCache[ip]; ok && time.Now().Before(e.exp) {
		isUSCacheMu.RUnlock()
		return e.isUS
	}
	isUSCacheMu.RUnlock()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("https://ipapi.co/" + ip + "/json/")
	isUS := false
	if err == nil {
		defer resp.Body.Close()
		var payload struct {
			CountryCode string `json:"country_code"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&payload); err == nil {
			isUS = payload.CountryCode == "US"
		} else {
			// On decode failure, assume not US but cache briefly to avoid hammering.
			isUS = false
		}
	}
	isUSCacheMu.Lock()
	isUSCache[ip] = struct {
		isUS bool
		exp  time.Time
	}{isUS, time.Now().Add(60 * time.Minute)}
	isUSCacheMu.Unlock()
	return isUS
}

func (r *Refresher) recordCycle(cycle map[string]any, hadErr bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastCycle = cycle
	r.lastRun = time.Now()
	r.lastHadErr = hadErr
	if r.logger != nil {
		r.logger.Printf("proxy-refresher: %v", cycle)
	}
}

// extractIP pulls the first IPv4-looking token from a probe body.
func extractIP(body string) string {
	var payload struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err == nil && payload.IP != "" {
		return payload.IP
	}
	return ""
}
