package httpapi

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"freebuff-unified/internal/hermes"
	"freebuff-unified/internal/parallel"
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
	// /v1/parallel/extract proxies to the Parallel Web APIs.
	Parallel     *parallel.Client
	ParallelMode string
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

	app.Get("/healthz", handlers.Health)
	app.Get("/ai-stack/status", handlers.AIStackStatus)
	app.Get("/proxy/verify", handlers.Health)
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
