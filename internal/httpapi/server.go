package httpapi

import (
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"

	"freebuff-unified/internal/hermes"
	"freebuff-unified/internal/parallel"
	"freebuff-unified/internal/proxy"
	"freebuff-unified/internal/stealth"
	"freebuff-unified/internal/websearch"
)

// Options holds the external dependencies for the HTTP API server.
type Options struct {
	Model       string
	ProxyAPIKey string
	Chat        ChatService
	TokenPool   PoolStatsProvider
	ProxyPool   PoolStatsProvider
	// Hermes, when set, exposes the stealth sidecar endpoints
	// (/hermes/healthz, /v1/hermes/fetch, /v1/hermes/session/:id).
	Hermes *hermes.Client
	// Parallel, when enabled, exposes key-gated /v1/parallel/search and
	// /v1/parallel/extract proxies to the Parallel Web APIs, plus the Layer A
	// keyless-first /v1/deep-research Task orchestration and /v1/responses.
	Parallel          *parallel.Client
	ParallelMode      string
	ParallelProcessor string
	// Research tunes the native Layer B harness (/v1/deep-research fallback).
	Research ResearchConfig
	// Limiter, when set, enforces the three-tier RPM policy
	// (global/account/client) on all credentialed routes.
	Limiter *RateLimiter
	// WebSearcher is the keyless stealth search backend (hermes sidecar).
	// /v1/parallel/search falls back to it when Parallel has no API key.
	WebSearcher *websearch.Searcher
	// Stealth, when set, backs GET /stealth/status with transport metrics
	// (sidecar latency, fallback counts, egress IPs used).
	Stealth *stealth.Metrics
	// Refresher, when set, contributes the proxy-pool auto-refresh state to
	// GET /stealth/status.
	Refresher func() map[string]any
	// ExtraHealth, when set, contributes additional fields to the /healthz
	// and /proxy/verify payloads (ai-stack peers, pool sizes, uptime).
	ExtraHealth func() map[string]any
	// AIStack, when set, backs the /ai-stack/status endpoint with a full
	// infrastructure report instead of the plain health summary.
	AIStack func() map[string]any
	// Passthrough, when set, exposes the front-door byte-level relay to a
	// freebuff-proxy backend. Activated only when cfg.Proxy.Mode is
	// "passthrough"; otherwise native routes are registered (default).
	Passthrough *proxy.Handler
	// BackendURL is the freebuff-proxy backend address used when Passthrough
	// is wired (from proxy.backend_url / FREEBUFF_PROXY_BACKEND).
	BackendURL string
}

// NewApp creates a Fiber v3 app with health, models, and chat routes.
func NewApp(opts Options) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:      "freebuff-unified",
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 310 * time.Second,
		IdleTimeout:  120 * time.Second,
	})

	handlers := newHandlers(opts.Model, opts.Chat, opts.TokenPool, opts.ProxyPool)
	handlers.hermes = opts.Hermes
	handlers.parallel = opts.Parallel
	handlers.parallelMode = opts.ParallelMode
	handlers.parallelProcessor = opts.ParallelProcessor
	handlers.research = opts.Research
	handlers.webSearcher = opts.WebSearcher
	handlers.stealth = opts.Stealth
	handlers.refresher = opts.Refresher
	handlers.extraHealth = opts.ExtraHealth
	handlers.aiStack = opts.AIStack

	if opts.ProxyAPIKey != "" {
		app.Use(func(c fiber.Ctx) error {
			// Health, ai-stack status, and proxy verification endpoints are
			// always public so monitors can probe them without a key.
			if c.Path() == "/healthz" || c.Path() == "/ai-stack/status" || c.Path() == "/proxy/verify" {
				return c.Next()
			}
			return authMiddleware(opts.ProxyAPIKey)(c)
		})
	}

	// Three-tier RPM policy (global/account/client). Probes stay public.
	if opts.Limiter != nil {
		app.Use(rateLimitMiddleware(opts.Limiter))
	}

	app.Get("/healthz", handlers.Health)
	app.Get("/ai-stack/status", handlers.AIStackStatus)
	app.Get("/proxy/verify", handlers.Health)

	// Deep-research (Layer A Task runs, keyless-first, native Layer B
	// fallback) and the /v1/responses surface. Registered before the
	// front-door relay so they stay native — key-gated by the auth middleware
	// above — even in passthrough mode.
	app.Post("/v1/deep-research", handlers.DeepResearch)
	app.Get("/v1/deep-research/:id", handlers.DeepResearchGet)
	app.Get("/v1/deep-research/:id/events", handlers.DeepResearchEvents)
	app.Post("/v1/responses", handlers.ResponsesPassthrough)

	if opts.Passthrough != nil {
		// Front-door mode: /v1/* is relayed verbatim to the freebuff-proxy
		// backend (its sessions, token pool, and dashboard do the protocol
		// work). Everything else stays native.
		opts.Passthrough.SetBackend(opts.BackendURL)
		app.All("/v1/*", adaptor.HTTPHandler(opts.Passthrough))
		return app
	}

	app.Get("/v1/models", handlers.Models)
	app.Post("/v1/chat/completions", handlers.ChatCompletions)
	app.Post("/v1/messages", handlers.AnthropicMessages)
	app.Post("/v1/messages/count_tokens", handlers.AnthropicCountTokens)

	// Hermes stealth sidecar (key-protected, auth middleware above).
	app.Get("/hermes/healthz", handlers.HermesHealth)
	app.Get("/stealth/status", handlers.StealthStatus)
	app.Post("/v1/hermes/fetch", handlers.HermesFetch)
	app.Post("/v1/hermes/session/:id", handlers.HermesSessionFetch)
	app.Delete("/v1/hermes/session/:id", handlers.HermesSessionDelete)

	// Parallel Web APIs (key-protected, auth middleware above).
	app.Post("/v1/parallel/search", handlers.ParallelSearch)
	app.Post("/v1/parallel/extract", handlers.ParallelExtract)

	return app
}
