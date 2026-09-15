package stealth

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProxyDispenser is the seam the gateway programs against: dispense the
// next validated SOCKS5 URL, report size, hot-swap contents. Implemented
// by USProxyPool (internal sidecar-probed validation) and Prox5Pool
// (prox5 engine validation + mid-dial retry).
type ProxyDispenser interface {
	Next() *url.URL
	Size() int
	Replace(proxyURLs []string) int
}

// USProxyPool is a round-robin pool of SOCKS5 US proxies from freebuff-unified.
type USProxyPool struct {
	mu      sync.RWMutex
	proxies []*url.URL
	counter uint64
	logger  *log.Logger
}

// NewUSProxyPool creates a new US proxy pool from URL strings.
func NewUSProxyPool(proxyURLs []string, logger *log.Logger) *USProxyPool {
	p := &USProxyPool{logger: logger}
	for _, raw := range proxyURLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			if logger != nil {
				logger.Printf("proxy: skipping invalid URL %q", raw)
			}
			continue
		}
		p.proxies = append(p.proxies, u)
	}
	if logger != nil && len(p.proxies) > 0 {
		logger.Printf("proxy: loaded %d US proxies", len(p.proxies))
	}
	return p
}

// Next returns the next proxy in round-robin order.
func (p *USProxyPool) Next() *url.URL {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.proxies) == 0 {
		return nil
	}
	idx := atomic.AddUint64(&p.counter, 1) - 1
	return p.proxies[idx%uint64(len(p.proxies))]
}

// Size returns the number of proxies.
func (p *USProxyPool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.proxies)
}

// Replace atomically swaps the pool contents (hot refresh) and resets the
// round-robin counter.
func (p *USProxyPool) Replace(proxyURLs []string) int {
	var next []*url.URL
	for _, raw := range proxyURLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			if p.logger != nil {
				p.logger.Printf("proxy: skipping invalid URL %q", raw)
			}
			continue
		}
		next = append(next, u)
	}
	p.mu.Lock()
	p.proxies = next
	p.counter = 0
	p.mu.Unlock()
	if p.logger != nil {
		p.logger.Printf("proxy: pool replaced (%d proxies)", len(next))
	}
	return len(next)
}

// Verify checks all proxies and returns their status.
func (p *USProxyPool) Verify(ctx context.Context) ([]ProxyStatus, error) {
	if len(p.proxies) == 0 {
		return nil, fmt.Errorf("no proxies configured")
	}
	results := make([]ProxyStatus, len(p.proxies))
	for i, proxy := range p.proxies {
		results[i] = p.verifySingle(ctx, proxy)
	}
	return results, nil
}

// ProxyStatus holds the result of verifying a single proxy.
type ProxyStatus struct {
	Proxy   string
	OK      bool
	IP      string
	Latency time.Duration
	Error   string
}

func (p *USProxyPool) verifySingle(ctx context.Context, proxy *url.URL) ProxyStatus {
	status := ProxyStatus{Proxy: proxy.Redacted()}

	t := &http.Transport{
		Proxy: http.ProxyURL(proxy),
	}
	client := &http.Client{Transport: t, Timeout: 8 * time.Second}

	start := time.Now()
	// api.ipify.org over HTTPS: httpbin.org has persistent DNS failures on
	// some hosts, and plain-HTTP targets give proxies an easy out.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org?format=json", nil)
	if err != nil {
		status.Error = err.Error()
		return status
	}

	resp, err := client.Do(req)
	status.Latency = time.Since(start)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		status.Error = err.Error()
		return status
	}

	status.OK = resp.StatusCode == 200
	raw := string(body)
	if idx := strings.Index(raw, `"origin"`); idx >= 0 {
		rest := raw[idx+len(`"origin"`):]
		rest = strings.TrimSpace(rest)
		rest = strings.TrimLeft(rest, ": ")
		rest = strings.Trim(rest, "\" \n\r")
		if end := strings.IndexAny(rest, "\"}"); end >= 0 {
			rest = rest[:end]
		}
		status.IP = rest
	} else {
		status.IP = strings.TrimSpace(raw)
	}
	return status
}
