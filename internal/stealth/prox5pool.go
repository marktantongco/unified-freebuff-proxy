package stealth

import (
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	prox5 "git.tcp.direct/kayos/prox5"
)

// Prox5Pool is a USProxyPool-compatible pool backed by the prox5 validation
// engine (git.tcp.direct/kayos/prox5, MIT). It exposes the same
// Next/Size/Replace surface so the sidecar-probed refresher and metrics
// keep working unchanged; the engine adds its own continuous validation
// plus mid-dial retry (failed proxy → next proxy, client conn held).
//
// Credential mapping: prox5 dispenses host:port endpoints; auth comes from
// the configured URL table keyed by host:port, so user:pass survives the
// engine round-trip.
type Prox5Pool struct {
	mu     sync.RWMutex
	engine *prox5.ProxyEngine
	creds  map[string]*url.URL // host:port → original URL (auth preserved)
	logger *log.Logger
}

// NewProx5Pool builds the engine, loads each URL via LoadSingleProxy, and
// starts validation workers. Invalid URLs are skipped (logged); an empty
// final table is an error — fail closed, never an empty live pool.
func NewProx5Pool(proxyURLs []string, logger *log.Logger) (*Prox5Pool, error) {
	engine := prox5.NewProxyEngine()
	p := &Prox5Pool{engine: engine, creds: map[string]*url.URL{}, logger: logger}
	for _, raw := range proxyURLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			if logger != nil {
				logger.Printf("prox5: skipping invalid URL %q", raw)
			}
			continue
		}
		// LoadSingleProxy takes schemeless host:port (or user:pass@host:port)
		// endpoints — strip the socks5:// scheme first.
		if !engine.LoadSingleProxy(schemeless(u)) {
			if logger != nil {
				logger.Printf("prox5: rejected URL %q", u.Redacted())
			}
			continue
		}
		p.creds[u.Host] = u
	}
	if len(p.creds) == 0 {
		return nil, fmt.Errorf("prox5: no usable proxies configured")
	}
	if err := engine.Start(); err != nil {
		return nil, fmt.Errorf("prox5: engine start: %w", err)
	}
	if logger != nil {
		logger.Printf("prox5: loaded %d proxies, engine started", len(p.creds))
	}
	return p, nil
}

// Next dispenses a validated proxy URL. Never blocks: zero validated
// endpoints (or a slow dispenser) is a miss → nil, and callers fall back
// to direct — the same contract as USProxyPool.Next on an empty pool.
// GetAnySOCKS itself blocks until one is available, so it runs behind a
// short timeout.
func (p *Prox5Pool) Next() *url.URL {
	p.mu.RLock()
	defer p.mu.RUnlock()
	st := p.engine.GetStatistics()
	if st.Valid5.Load()+st.Valid4.Load()+st.Valid4a.Load() == 0 {
		return nil
	}
	type result struct {
		ep *prox5.Proxy
	}
	ch := make(chan result, 1)
	go func() { ch <- result{p.engine.GetAnySOCKS()} }()
	var ep *prox5.Proxy
	select {
	case r := <-ch:
		ep = r.ep
	case <-time.After(500 * time.Millisecond):
		return nil
	}
	if ep == nil {
		return nil
	}
	key := ep.Endpoint
	if u, ok := p.creds[key]; ok {
		return u
	}
	// Validated endpoint without configured creds (e.g. discovered via
	// engine recycling): synthesize a bare socks5 URL.
	u, err := url.Parse("socks5://" + key)
	if err != nil {
		return nil
	}
	return u
}

// schemeless renders a proxy URL as the host:port (or user:pass@host:port)
// endpoint string prox5's LoadSingleProxy filter accepts.
func schemeless(u *url.URL) string {
	endpoint := u.Host
	if ui := u.User; ui != nil {
		if pw, ok := ui.Password(); ok {
			endpoint = ui.Username() + ":" + pw + "@" + u.Host
		} else if ui.Username() != "" {
			endpoint = ui.Username() + "@" + u.Host
		}
	}
	return endpoint
}

// Size reports configured endpoints (mirrors USProxyPool.Size).
func (p *Prox5Pool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.creds)
}

// Replace swaps the credential table and reloads the engine. The engine
// keeps its validation history; unknown endpoints simply stop being
// dispensed once their table entry is gone. Returns the new table size.
func (p *Prox5Pool) Replace(proxyURLs []string) int {
	next := map[string]*url.URL{}
	for _, raw := range proxyURLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		if p.engine.LoadSingleProxy(schemeless(u)) {
			next[u.Host] = u
		}
	}
	p.mu.Lock()
	p.creds = next
	p.mu.Unlock()
	if p.logger != nil {
		p.logger.Printf("prox5: pool replaced (%d proxies)", len(next))
	}
	return len(next)
}

// Stats exposes engine statistics for /stealth/status parity.
func (p *Prox5Pool) Stats() any {
	return p.engine.GetStatistics()
}
