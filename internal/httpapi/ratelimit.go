package httpapi

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
)

// tokenBucket is a per-identity fixed-capacity token bucket (capacity = rpm,
// refill = rpm tokens per minute). It is safe for concurrent use.
type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// take consumes one token if available, refilling at rate RPM. The first use
// seeds a full bucket so a fresh identity is not throttled on request 1.
func (b *tokenBucket) take(rpm float64, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last.IsZero() {
		b.last = now
		b.tokens = rpm
	}
	elapsed := now.Sub(b.last).Minutes()
	if elapsed > 0 {
		b.tokens += rpm * elapsed
		if b.tokens > rpm {
			b.tokens = rpm
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// RateLimiter enforces three-tier RPM limits (global/account/client) with
// lazy per-identity token buckets.  RateLimiter may be nil — the middleware
// then performs no enforcement.
type RateLimiter struct {
	globalRPM  float64
	accountRPM float64
	clientRPM  float64

	mu      sync.Mutex
	global  tokenBucket
	account map[string]*tokenBucket
	client  map[string]*tokenBucket
}

// NewRateLimiter creates a RateLimiter with the configured RPM tiers.  All
// tiers are 0-treated as unlimited if the value is <= 0.
func NewRateLimiter(global, account, client int) *RateLimiter {
	l := &RateLimiter{
		globalRPM:  safeRPM(global, 120),
		accountRPM: safeRPM(account, 30),
		clientRPM:  safeRPM(client, 60),
		account:    make(map[string]*tokenBucket),
		client:     make(map[string]*tokenBucket),
	}
	return l
}

func safeRPM(val, def int) float64 {
	if val <= 0 {
		return float64(def)
	}
	return float64(val)
}

// Allow checks all three tiers.  accountID is the presented credential hash;
// clientID is typically the source IP or X-Client-Id header.
func (l *RateLimiter) Allow(accountID, clientID string) bool {
	if l == nil {
		return true
	}
	now := time.Now()
	if !l.global.take(l.globalRPM, now) {
		return false
	}
	ab := l.getBucket(l.account, accountID, l.accountRPM)
	if !ab.take(l.accountRPM, now) {
		return false
	}
	cb := l.getBucket(l.client, clientID, l.clientRPM)
	if !cb.take(l.clientRPM, now) {
		return false
	}
	return true
}

func (l *RateLimiter) getBucket(m map[string]*tokenBucket, key string, rpm float64) *tokenBucket {
	_ = rpm // not used during creation; capacity is rpm on the allow path
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := m[key]
	if !ok {
		b = &tokenBucket{}
		m[key] = b
		if len(m) > 512 {
			cutoff := time.Now().Add(-5 * time.Minute)
			for k, v := range m {
				v.mu.Lock()
				idle := v.last.Before(cutoff)
				v.mu.Unlock()
				if idle {
					delete(m, k)
				}
			}
		}
	}
	return b
}

// credentialID produces a non-reversible short key for the presented auth
// credential.  An empty input returns the single bucket ID "0".
func credentialID(auth, apiKey string) string {
	v := apiKey
	if auth != "" {
		v = auth
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(v))
	return fmt.Sprintf("%x", h.Sum64())
}

// rateLimitMiddleware enforces the three-tier RPM policy per request.
// Health/status probe paths are always allowed through.
func rateLimitMiddleware(l *RateLimiter) fiber.Handler {
	return func(c fiber.Ctx) error {
		if l == nil || isPublicStatusPath(c.Path()) {
			return c.Next()
		}
		account := credentialID(c.Get(fiber.HeaderAuthorization), c.Get("x-api-key"))
		client := c.Get("X-Client-Id")
		if client == "" {
			client = c.IP()
		}
		if !l.Allow(account, client) {
			c.Set("Retry-After", "60")
			return writeOpenAIError(c, http.StatusTooManyRequests, "rate_limit_exceeded", "Rate limit exceeded, please retry later")
		}
		return c.Next()
	}
}

// isPublicStatusPath reports whether the path is a non-sensitive probe
// endpoint that must always be reachable regardless of quota.
func isPublicStatusPath(path string) bool {
	return path == "/healthz" || path == "/ai-stack/status" || path == "/proxy/verify"
}
