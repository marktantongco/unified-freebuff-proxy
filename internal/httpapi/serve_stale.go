package httpapi

import (
	"sync"
	"time"
)

// ServeStaleCache wraps a status builder so expensive status endpoints
// (/ai-stack/status) serve a cached payload for ttl instead of rebuilding —
// and re-probing hermes, the proxy backend, and peers — on every request.
//
// Within the TTL the cached map is returned; on a failed rebuild (nil result)
// the last good payload is served (serve-stale semantics). The cached map is
// treated as read-only by callers (Fiber serializes JSON rendering).
func ServeStaleCache(build func() map[string]any) func() map[string]any {
	return ServeStaleCacheWithTTL(build, 5*time.Second)
}

// ServeStaleCacheWithTTL is ServeStaleCache with an explicit TTL. A nil build
// function yields a provider that always returns nil.
func ServeStaleCacheWithTTL(build func() map[string]any, ttl time.Duration) func() map[string]any {
	if build == nil {
		return func() map[string]any { return nil }
	}
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	var (
		mu      sync.RWMutex
		payload map[string]any
		builtAt time.Time
	)
	return func() map[string]any {
		mu.RLock()
		p, at := payload, builtAt
		mu.RUnlock()
		if p != nil && time.Since(at) < ttl {
			return p
		}
		fresh := build()
		mu.Lock()
		if fresh != nil {
			payload, builtAt = fresh, time.Now()
		}
		mu.Unlock()
		if fresh == nil {
			return p // rebuild failed: serve last good payload
		}
		return fresh
	}
}
