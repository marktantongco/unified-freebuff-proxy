package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

// TestTokenBucketBasic: a 120 RPM bucket admits 120 tokens immediately, then
// the next take fails within the same 1s window (no refill elapsed yet).
func TestTokenBucketBasic(t *testing.T) {
	now := time.Now()
	b := &tokenBucket{tokens: 120, last: now}
	for i := 0; i < 120; i++ {
		if !b.take(120, now) {
			t.Fatalf("take %d returned false, want true", i+1)
		}
	}
	if b.take(120, now) {
		t.Fatal("121st take returned true, want false (quota exhausted)")
	}
}

// TestTokenBucketRefill: one take followed by a 1s advance refills the bucket
// by rate/60 per second, so a second take succeeds.
func TestTokenBucketRefill(t *testing.T) {
	now := time.Now()
	b := &tokenBucket{tokens: 1, last: now}
	if !b.take(120, now) {
		t.Fatal("first take failed")
	}
	// 2s later => 120 * (2/60) = 4 tokens refilled; cap at capacity 120.
	later := now.Add(2 * time.Second)
	if !b.take(120, later) {
		t.Fatal("take after 2s refill returned false, want true")
	}
}

// TestRateLimiterAllTiers: with global=2, account=1, client=10, the second
// unique request is denied on the account tier and the third on the global
// tier — both before the client tier ever caps.
func TestRateLimiterAllTiers(t *testing.T) {
	l := NewRateLimiter(2, 1, 10)
	if !l.Allow("acct-a", "client-a") {
		t.Fatal("request 1 denied, want allowed")
	}
	if l.Allow("acct-a", "client-a") {
		t.Fatal("request 2 allowed, want denied (account RPM hit)")
	}
	if l.Allow("acct-b", "client-b") {
		t.Fatal("request 3 allowed, want denied (global RPM hit)")
	}
}

// TestRateLimitMiddlewareSkipsPublic: probe endpoints always pass even after
// the quota is exhausted, while a protected route returns 429.
func TestRateLimitMiddlewareSkipsPublic(t *testing.T) {
	app := fiber.New()
	app.Use(rateLimitMiddleware(NewRateLimiter(1, 1, 1)))
	app.Get("/healthz", func(c fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/ai-stack/status", func(c fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/proxy/verify", func(c fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/protected", func(c fiber.Ctx) error { return c.SendString("ok") })

	pub := []string{"/healthz", "/ai-stack/status", "/proxy/verify"}
	for _, path := range pub {
		status, _ := doRequest(t, app, httptest.NewRequest(http.MethodGet, path, nil))
		if status != http.StatusOK {
			t.Fatalf("public %s: status %d, want 200 before quota", path, status)
		}
	}

	// Burn the single request allowed by the 1 RPM tiers.
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer k")
	status, _ := doRequest(t, app, req)
	if status != http.StatusOK {
		t.Fatalf("protected first: status %d, want 200", status)
	}

	// Probes stay open after exhaustion.
	for _, path := range pub {
		status, _ := doRequest(t, app, httptest.NewRequest(http.MethodGet, path, nil))
		if status != http.StatusOK {
			t.Fatalf("public %s: status %d, want 200 after quota", path, status)
		}
	}

	// Same credential is now denied on the protected route.
	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer k")
	status, body := doRequest(t, app, req)
	if status != http.StatusTooManyRequests {
		t.Fatalf("protected after quota: status %d, want 429\n%s", status, body)
	}
}

// TestRateLimitMiddlewareReturns429: an overloaded credential is throttled
// with the OpenAI-style error code and a Retry-After header.
func TestRateLimitMiddlewareReturns429(t *testing.T) {
	app := fiber.New()
	app.Use(rateLimitMiddleware(NewRateLimiter(1, 1, 1)))
	app.Get("/v1/chat/completions", func(c fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer k")
	status, _ := doRequest(t, app, req)
	if status != http.StatusOK {
		t.Fatalf("first: status %d, want 200", status)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer k")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("overloaded: status %d, want 429", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want 60", got)
	}
}

// TestCredentialID: distinct credentials map to distinct deterministic keys;
// the same credential always maps to the same key.
func TestCredentialID(t *testing.T) {
	a := credentialID("Bearer k", "")
	b := credentialID("Bearer k", "")
	c := credentialID("Bearer other", "")
	if a == "" || a != b || a == c {
		t.Fatalf("credentialID inconsistent: a=%q b=%q c=%q", a, b, c)
	}
	unauthedA := credentialID("", "x")
	unauthedB := credentialID("", "x")
	if unauthedA != unauthedB {
		t.Fatalf("x-api-key credentialID not stable: %q vs %q", unauthedA, unauthedB)
	}
}
