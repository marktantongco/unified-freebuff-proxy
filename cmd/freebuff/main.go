// Command freebuff-unified is the unified Freebuff gateway: it merges the
// freebuff-proxy layer (OAuth credentials, stealth transport, dashboard) with
// the Freebuff2API passthrough layer (proxy backends) and the ai-stack status
// surface.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"freebuff-unified/internal/config"
	"freebuff-unified/internal/credentials"
	"freebuff-unified/internal/dashboard"
	evalpkg "freebuff-unified/internal/eval"
	"freebuff-unified/internal/freebuff"
	"freebuff-unified/internal/hermes"
	"freebuff-unified/internal/httpapi"
	"freebuff-unified/internal/lmarena"
	"freebuff-unified/internal/oauth"
	"freebuff-unified/internal/parallel"
	"freebuff-unified/internal/proxy"
	"freebuff-unified/internal/session"
	"freebuff-unified/internal/stealth"
	"freebuff-unified/internal/websearch"
)

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

func main() {
	logger := log.New(os.Stdout, "[freebuff-unified] ", log.LstdFlags|log.Lmicroseconds)

	// Load optional .env file (freebuff-proxy style).
	_ = godotenv.Load()

	var configPath string
	flag.StringVar(&configPath, "config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

	// Commands: serve (default), login, logout, check.
	args := flag.Args()
	if len(args) > 0 {
		switch args[0] {
		case "serve":
			// fall through to normal startup
		case "check":
			runCheck(cfg, configPath)
			return
		case "login":
			if err := runLogin(cfg); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "logout":
			if err := runLogout(cfg); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command: %s\nusage: freebuff-unified [serve|login|logout|check]\n", args[0])
			os.Exit(1)
		}
	}

	runServe(cfg, logger)
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

func runCheck(cfg *config.Config, configPath string) {
	fmt.Printf("config:    %s\n", configPath)
	fmt.Printf("listen:    %s\n", cfg.Server.ListenAddr)
	fmt.Printf("auth:      keys=%d breaker=%d/%s\n",
		len(cfg.Auth.APIKeys), cfg.Auth.Breaker.Threshold, cfg.Auth.Breaker.Cooldown)
	fmt.Printf("upstream:  %s (default=%s)\n", cfg.Upstream.BaseURL, cfg.Upstream.DefaultModel)
	fmt.Printf("proxy:     enabled=%v backend=%s\n", cfg.Proxy.Enabled, cfg.Proxy.BackendURL)
	fmt.Printf("stealth:   enabled=%v profile=%s validator=%s us_proxies=%d strip_headers=%v\n",
		cfg.Stealth.Enabled, cfg.Stealth.Profile, cfg.Stealth.Validator, len(cfg.Stealth.USProxies), cfg.Stealth.StripHeaders)
	fmt.Printf("limits:    global_rpm=%d\n", cfg.Limits.GlobalRPM)
	fmt.Printf("dashboard: enabled=%v addr=%s\n", cfg.Dashboard.Enabled, cfg.Dashboard.Addr)
	fmt.Printf("parallel:   enabled=%v mode=%s processor=%s key=%s\n",
		cfg.Parallel.Enabled, cfg.Parallel.DefaultMode, cfg.Parallel.DefaultProcessor, keySet(cfg.Parallel.APIKey))
	fmt.Printf("research:   enabled=%v max_queries=%d fan_out=%d timeout_ms=%d\n",
		cfg.Research.Enabled, cfg.Research.MaxQueries, cfg.Research.FanOut, cfg.Research.TimeoutMS)
	fmt.Printf("lmarena:    enabled=%v base=%s eval_dir=%s\n", cfg.LMArena.Enabled, cfg.LMArena.BaseURL, cfg.LMArena.EvalDir)
}

func runLogin(cfg *config.Config) error {
	ctx := context.Background()
	flow := newOAuthFlow(cfg)
	store := credentials.FileStore{Path: credentialsPath(cfg)}

	code, err := flow.RequestLoginCode(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Open this URL to log in:\n  %s\nWaiting for authentication (expires: %s)...\n",
		code.LoginURL, code.ExpiresAt)

	cred, err := flow.PollLoginStatus(ctx, code)
	if err != nil {
		return err
	}
	if err := store.Save(ctx, cred); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Logged in as %s <%s>.\nCredentials saved to %s.\n",
		cred.Name, cred.Email, store.Path)
	return nil
}

func runLogout(cfg *config.Config) error {
	ctx := context.Background()
	flow := newOAuthFlow(cfg)
	store := credentials.FileStore{Path: credentialsPath(cfg)}

	if err := flow.Logout(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: remote logout failed: %v\n", err)
	}
	if err := store.Clear(ctx); err != nil {
		return fmt.Errorf("clear local credentials: %w", err)
	}
	fmt.Fprintln(os.Stdout, "Logged out. Local credential state cleared.")
	return nil
}

// ---------------------------------------------------------------------------
// Serve
// ---------------------------------------------------------------------------

func runServe(cfg *config.Config, logger *log.Logger) {
	// ── Credentials store (freebuff-proxy file store) ──────────────────────
	credsStore := credentials.FileStore{Path: credentialsPath(cfg)}

	// Stealth observability (nil until stealth mode attaches its transport).
	var stealthMetrics *stealth.Metrics

	// ── Upstream client (Freebuff2API / freebuff-proxy upstream layer) ─────
	baseURL := cfg.Upstream.BaseURL
	if baseURL == "" {
		baseURL = "https://codebuff.com"
	}

	var chatService httpapi.ChatService
	var sessMgr *session.Manager
	upstreamClient, upstreamErr := freebuff.NewClient(baseURL, nil)
	if upstreamErr != nil {
		logger.Printf("upstream client: %v (chat endpoints will return 503)", upstreamErr)
	} else {
		sessMgr = session.NewManager(credsStore, upstreamClient, "freebuff-unified")

		// Live agent registry: model→agent pairings refreshed from upstream's
		// TS constants every 6h; static snapshot stays as offline fallback.
		freebuff.StartAgentRegistry(context.Background())
		// Pre-warm default model session 5s after boot so first query skips queue.
		if cfg.Upstream.DefaultModel != "" {
			sessMgr.Prewarm(context.Background(), cfg.Upstream.DefaultModel)
		}
		chatService = httpapi.FreebuffChatService{
			Store:    credsStore,
			Sessions: sessMgr,
			Upstream: upstreamClient,
		}
	}

	// ── Three-tier RPM policy (global/account/client) ──────────────────────
	limiter := httpapi.NewRateLimiter(cfg.Limits.GlobalRPM, cfg.Limits.AccountRPM, cfg.Limits.ClientRPM)
	logger.Printf("rate limits: global=%d rpm, account=%d rpm, client=%d rpm", cfg.Limits.GlobalRPM, cfg.Limits.AccountRPM, cfg.Limits.ClientRPM)

	// ── US SOCKS5 proxy pool (freebuff-unified) ────────────────────────────
	// Validator selects the engine: "prox5" (validation engine + mid-dial
	// retry) or anything else (internal sidecar-probed pool). prox5 build
	// failure falls back to internal — never a nil live pool.
	var usProxyPool stealth.ProxyDispenser
	if len(cfg.Stealth.USProxies) > 0 {
		if cfg.Stealth.Validator == "prox5" {
			p5, err := stealth.NewProx5Pool(cfg.Stealth.USProxies, logger)
			if err != nil {
				logger.Printf("prox5 pool: %v (falling back to internal)", err)
				usProxyPool = stealth.NewUSProxyPool(cfg.Stealth.USProxies, logger)
			} else {
				usProxyPool = p5
			}
		} else {
			usProxyPool = stealth.NewUSProxyPool(cfg.Stealth.USProxies, logger)
		}
	}

	// ── Dashboard (freebuff-proxy dashboard) ───────────────────────────────
	if cfg.Dashboard.Enabled {
		startDashboard(cfg.Dashboard, logger)
	}

	// Hermes stealth sidecar client (kori-lab/hermes vendored).
	var hermesClient *hermes.Client
	if cfg.Hermes.Enabled {
		if cfg.Stealth.Enabled {
			// Stealth mode needs headroom for long LLM completions relayed
			// through the sidecar (sidecar caps 280s for codebuff upstream).
			hermesClient = hermes.NewWithTimeout(cfg.Hermes.BaseURL, stealthUpstreamClientTimeout)
		} else {
			hermesClient = hermes.New(cfg.Hermes.BaseURL)
		}
		// Non-blocking health check: don't stall boot 60s if sidecar is down.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if h, err := hermesClient.Health(ctx); err != nil {
				logger.Printf("hermes sidecar at %s: %v (stealth proxy endpoints will 503)", cfg.Hermes.BaseURL, err)
			} else {
				logger.Printf("hermes sidecar ok: %s on %s (sessions=%d)", h.Hermes, h.Node, h.Sessions)
			}
		}()
	}

	// ── Stealth mode toggle (cfg.Stealth.Enabled) ──────────────────────────
	// Route non-streaming codebuff upstream calls through the hermes sidecar
	// (browser-like TLS 1.3 / HTTP2 fingerprint + rotating SOCKS5 egress).
	// SSE streaming bypasses the sidecar via the fallback transport.
	if cfg.Stealth.Enabled && hermesClient != nil && upstreamErr == nil && upstreamClient != nil {
		stealthMetrics = stealth.NewMetrics()
		fallback := http.DefaultTransport.(*http.Transport).Clone()
		fallback.ResponseHeaderTimeout = 60 * time.Second
		upstreamClient.UseTransport(&http.Client{
			Transport: &stealth.HermesRoundTripper{
				Sidecar:   hermesClient,
				Fallback:  fallback,
				HTTP2:     true,
				FailOpen:  true,
				TimeoutMS: stealthUpstreamTimeoutMS,
				Metrics:   stealthMetrics,
				Proxy: func(*http.Request) string {
					if usProxyPool == nil {
						return ""
					}
					if p := usProxyPool.Next(); p != nil {
						return p.String()
					}
					return ""
				},
			},
		})
		proxies := 0
		if usProxyPool != nil {
			proxies = usProxyPool.Size()
		}
		logger.Printf("stealth mode ON: upstream %s via hermes sidecar %s (socks5 pool: %d, failopen: on, sse: bypass)",
			baseURL, cfg.Hermes.BaseURL, proxies)
	}

	// ── Parallel Web APIs client (search/extract/task/responses) ──────────
	// Keyless-first: the client is wired whenever parallel.enabled is true;
	// the API key (config or PARALLEL_API_KEY) is optional and only sent when
	// present. Layer A /v1/deep-research and /v1/responses try the keyless
	// call and fall back to the native harness on auth rejection.
	parallelAPIKey := cfg.Parallel.APIKey
	if parallelAPIKey == "" {
		parallelAPIKey = os.Getenv("PARALLEL_API_KEY")
	}
	var parallelClient *parallel.Client
	if cfg.Parallel.Enabled {
		parallelClient = parallel.New(cfg.Parallel.BaseURL, parallelAPIKey)
		logger.Printf("parallel APIs enabled: base=%s default_mode=%s default_processor=%s key=%s",
			cfg.Parallel.BaseURL, cfg.Parallel.DefaultMode, cfg.Parallel.DefaultProcessor, keySet(parallelAPIKey))
	}

	// ── LMArena stealth proxy sidecar (deps/lmarena-stealth-proxy, :3103) ──
	// Session-based lmarena.ai REST API. Non-blocking health check so boot
	// never stalls when the sidecar is down (endpoints 503 until it answers).
	var lmarenaClient *lmarena.Client
	if cfg.LMArena.Enabled {
		lmarenaClient = lmarena.New(cfg.LMArena.BaseURL)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if h, err := lmarenaClient.Health(ctx); err != nil {
				logger.Printf("lmarena sidecar at %s: %v (/v1/lmarena/* will 502)", cfg.LMArena.BaseURL, err)
			} else {
				logger.Printf("lmarena sidecar ok: %s (uptime=%.0fs)", h.Status, h.Uptime)
			}
		}()
	}

	// ── Manual-eval harness (blind A/B store/score, no upstream fetch) ───
	// Human pastes outputs; gateway only stores. Boot never fails here: a
	// bad dir disables the endpoints (503) instead of crashing serve.
	var evalStore *evalpkg.Store
	if cfg.LMArena.EvalDir != "" {
		s, err := evalpkg.NewStore(cfg.LMArena.EvalDir)
		if err != nil {
			logger.Printf("eval harness disabled: %v", err)
		} else {
			evalStore = s
			logger.Printf("eval harness on: dir=%s", cfg.LMArena.EvalDir)
		}
	}

	// ── Public leaderboard snapshot (read-only HF data, disk cache) ──────
	// Lazy: first request fetches (~100 pages), then serves cache for the
	// refresh window. Stale cache covers HF downtime.
	var board *lmarena.Leaderboard
	if cfg.LMArena.Leaderboard {
		board = lmarena.NewLeaderboard(cfg.LMArena.EvalDir, "", time.Duration(cfg.LMArena.LeaderboardRefreshH)*time.Hour)
		logger.Printf("leaderboard snapshot on: refresh=%dh", cfg.LMArena.LeaderboardRefreshH)
	}

	// Keyless stealth web search (DuckDuckGo via hermes sidecar) — backs
	// /v1/parallel/search when no Parallel API key is configured.
	var webSearcher *websearch.Searcher
	if hermesClient != nil {
		webSearcher = websearch.New(hermesClient)
	}

	// SearXNG keyless search backend (optional). Instance URL from
	// SEARXNG_URL env or config; empty = disabled, log + continue.
	searxngURL := cfg.Stealth.SearxngURL
	if searxngURL == "" {
		searxngURL = os.Getenv("SEARXNG_URL")
	}
	var searxng *websearch.SearxngSearcher
	if searxngURL != "" {
		searxng = websearch.NewSearxngSearcher(searxngURL)
		logger.Printf("searxng search enabled: %s", searxngURL)
	}

	// Search cache: 15-min in-memory TTL + 24h best-effort file persistence.
	searchCache := websearch.NewCache(15*time.Minute, websearch.CacheDir())
	logger.Printf("search cache enabled: memory 15m + file 24h (%s)", websearch.CacheDir())

	// ── Proxy-pool auto-refresher (tests & hot-swaps SOCKS5 pool) ──────────
	var poolRefresher *stealth.Refresher
	if cfg.Stealth.AutoRefreshPool && hermesClient != nil && usProxyPool != nil {
		poolRefresher = stealth.NewRefresher(usProxyPool, hermesClient, logger)
		poolRefresher.RefreshInterval = time.Duration(cfg.Stealth.ProxyRefreshMins) * time.Minute
		if cfg.Stealth.MaxPoolProxies > 0 {
			poolRefresher.MaxProxies = cfg.Stealth.MaxPoolProxies
		}
		poolRefresher.OnEgress = func(ip string) { stealthMetrics.RecordEgress(ip) }
		stopRefresher := poolRefresher.Start(context.Background())
		defer stopRefresher()
		logger.Printf("proxy-pool auto-refresher ON: interval=%s max=%d (probes via sidecar)",
			poolRefresher.RefreshInterval, poolRefresher.MaxProxies)
	}

	// ── Fiber app ──────────────────────────────────────────────────────────
	apiKey := ""
	if len(cfg.Server.APIKeys) > 0 {
		apiKey = cfg.Server.APIKeys[0]
	}

	var tokenPool httpapi.PoolStatsProvider
	if len(cfg.Auth.APIKeys) > 0 {
		tokenPool = staticStats{map[string]any{
			"configured_keys": len(cfg.Auth.APIKeys),
			"healthy":         len(cfg.Auth.APIKeys),
		}}
	}

	// Front-door passthrough: explicit opt-in via proxy.mode: passthrough.
	// Default (proxy.mode unset/"report") keeps the native completion path.
	var passthrough *proxy.Handler
	if cfg.PassthroughEnabled() {
		passthrough = proxy.NewHandler(logger)
		logger.Printf("front-door passthrough ON: /v1/chat|models|messages relayed to %s (deep-research + /v1/responses stay native)", passthroughBackendURL(cfg))
		// In passthrough mode the backend owns sessions/chat. Point the
		// native Layer B research planner (and the /v1/responses fallback) at
		// the backend instead of the gateway's own freebuff client, which
		// does not share that backend's sessions.
		if apiKey != "" {
			chatService = httpapi.NewBackendChatService(passthroughBackendURL(cfg), apiKey)
			logger.Printf("mesh research chat: routed through backend %s (session owner)", passthroughBackendURL(cfg))
		}
	}

	app := httpapi.NewApp(httpapi.Options{
		Model:       cfg.Upstream.DefaultModel,
		ProxyAPIKey: apiKey,
		Chat:        chatService,
		TokenPool:   tokenPool,
		ProxyPool:   usProxyStats{pool: usProxyPool}, Hermes: hermesClient, LMArena: lmarenaClient, EvalStore: evalStore, Leaderboard: board, Parallel: parallelClient,
		EvalsDirFn:        func() string { return cfg.LMArena.EvalDir },
		Passthrough:       passthrough,
		BackendURL:        passthroughBackendURL(cfg),
		ParallelMode:      cfg.Parallel.DefaultMode,
		ParallelProcessor: cfg.Parallel.DefaultProcessor,
		Research:          researchConfig(cfg),
		Limiter:           limiter,
		WebSearcher:       webSearcher,
		Searxng:           searxng,
		SearchCache:       searchCache,
		Stealth:           stealthMetrics,
		Refresher: func() map[string]any {
			if poolRefresher == nil {
				return nil
			}
			return poolRefresher.Status()
		},
		ExtraHealth: func() map[string]any { return extraHealth(cfg, usProxyPool) },
		AIStack: httpapi.ServeStaleCache(func() map[string]any {
			return aiStackStatus(cfg, hermesClient, lmarenaClient, board, usProxyPool, parallelClient, webSearcher)
		}),
	})

	// ── Listen ─────────────────────────────────────────────────────────────
	addr := cfg.Server.ListenAddr
	if addr == "" {
		addr = ":8080"
	}
	logger.Printf("listening on %s | upstream=%s | proxy_backend=%s | keys=%d | stealth=%v",
		addr, baseURL, passthroughBackendURL(cfg), len(cfg.Auth.APIKeys), cfg.Stealth.Enabled)

	go func() {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
		sig := <-signals
		logger.Printf("received %s, shutting down...", sig)
		_ = app.Shutdown()
	}()

	if err := app.Listen(addr); err != nil {
		logger.Fatalf("listen: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Health payload helpers
// ---------------------------------------------------------------------------

var startTime = time.Now()

// Stealth-mode upstream relay timing: codebuff completions can run 3-5m.
// The sidecar previously capped at 120s (110s TimeoutMS) which forced a
// buffered non-stream path to hit infra 5m idle abort ("no data for 5m").
// Raise to >5m for codebuff upstream; ipify/probe paths keep 12-15s.
const (
	stealthUpstreamTimeoutMS     = 280_000
	stealthUpstreamClientTimeout = 300 * time.Second
)

// researchConfig maps the YAML research block onto the Layer B harness
// config. An empty Model lets the handler fall back to the upstream default.
func researchConfig(cfg *config.Config) httpapi.ResearchConfig {
	r := cfg.Research
	return httpapi.ResearchConfig{
		Enabled:         r.Enabled,
		Model:           r.Model,
		MaxQueries:      r.MaxQueries,
		FanOut:          r.FanOut,
		ExtractPerQuery: r.ExtractPerQuery,
		Timeout:         time.Duration(r.TimeoutMS) * time.Millisecond,
	}
}

// healthProbeCache caches probeBackend results for 5s so /healthz and
// /ai-stack/status don't synchronously dial 2x per request with 2s timeouts.
var (
	healthCacheMu sync.RWMutex
	healthCache   = map[string]struct {
		val string
		exp time.Time
	}{}
	healthCacheTTL = 5 * time.Second
)

func extraHealth(cfg *config.Config, usProxyPool stealth.ProxyDispenser) map[string]any {
	// USProxyPool methods dereference internal mutex state; guard the nil
	// case so /healthz works when the pool is disabled (us_proxies: []).
	poolSize := 0
	if usProxyPool != nil {
		poolSize = usProxyPool.Size()
	}
	body := map[string]any{
		"session_id": "freebuff-unified",
		"version":    "unified-v1",
		"uptime":     time.Since(startTime).Round(time.Second).String(),
		"ai_stack": map[string]any{
			"freebuff_gateway": map[string]any{"port": 18080, "status": "active"},
			"hermes_sidecar":   map[string]any{"port": 3101, "status": "active"},
			"lmarena_sidecar":  map[string]any{"port": 3103, "status": "active"},
			"us_socks5_pool":   poolSize,
		},
	}
	if n := len(cfg.Auth.APIKeys); n > 0 {
		body["healthy_keys"] = n
		body["total_keys"] = n
	}
	if usProxyPool != nil {
		body["proxies"] = usProxyPool.Size()
	}
	if cfg.Proxy.Enabled {
		backend := passthroughBackendURL(cfg)
		body["proxy_backend"] = backend
		body["proxy_status"] = probeBackend(backend)
	}
	return body
}

func aiStackStatus(cfg *config.Config, hermesClient *hermes.Client, lmarenaClient *lmarena.Client, board *lmarena.Leaderboard, usProxyPool stealth.ProxyDispenser, parallelClient *parallel.Client, webSearcher *websearch.Searcher) map[string]any {
	proxyBackend := passthroughBackendURL(cfg)
	return map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"gateway": map[string]any{
			"version": "unified-v1",
			"uptime":  time.Since(startTime).Round(time.Second).String(),
		},
		"infrastructure": map[string]any{
			"freebuff_gateway": map[string]any{
				"address":   "http://localhost:18080",
				"status":    "ok",
				"endpoints": []string{"/healthz", "/ai-stack/status", "/v1/models", "/v1/chat/completions", "/v1/deep-research", "/v1/responses", "/proxy/verify"},
			},
			"us_socks5_pool": usProxyPoolStatus(usProxyPool),
			"autoclaw_provider": map[string]any{
				"address": "http://localhost:31000",
				"status":  probeBackend("http://localhost:31000"),
				"models":  []string{"autoclaw/glm-5.2", "autoclaw/glm-5-turbo"},
			},
			"freebuff_proxy_backend": map[string]any{
				"address":   proxyBackend,
				"status":    probeBackend(proxyBackend),
				"endpoints": []string{"/healthz", "/v1/models", "/v1/chat/completions", "/proxy/verify"},
			},
			"hermes_stealth_sidecar": hermesStatus(cfg, hermesClient),
			"lmarena_stealth_proxy":  lmarenaStatus(cfg, lmarenaClient, board),
			"parallel_web_apis":      parallelStatus(parallelClient, webSearcher),
		},
		"providers": map[string]any{
			"live": []string{
				"Cloudflare Workers AI", "OpenRouter", "Fireworks",
				"Google Gemini", "Cohere", "Groq Direct", "Local Ollama",
			},
			"wallet_gated": []string{
				"Together", "DeepSeek", "Cerebras", "OpenAI", "Venice", "xAI/Grok", "ZenMux",
			},
			"blocked": []string{"NVIDIA", "Mistral"},
		},
	}
}

// hermesStatus reports the hermes stealth sidecar state for /ai-stack/status.
func hermesStatus(cfg *config.Config, client *hermes.Client) map[string]any {
	if !cfg.Hermes.Enabled || client == nil {
		return map[string]any{
			"address": cfg.Hermes.BaseURL,
			"status":  "disabled",
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h, err := client.Health(ctx)
	if err != nil {
		return map[string]any{
			"address": cfg.Hermes.BaseURL,
			"status":  "unreachable",
			"error":   err.Error(),
		}
	}
	return map[string]any{
		"address":   cfg.Hermes.BaseURL,
		"status":    h.Status,
		"hermes":    h.Hermes,
		"node":      h.Node,
		"sessions":  h.Sessions,
		"endpoints": []string{"/v1/hermes/fetch", "/v1/hermes/session/:id", "/hermes/healthz"},
	}
}

// lmarenaStatus reports the lmarena-stealth-proxy sidecar state for
// /ai-stack/status, plus the cached public leaderboard top-5 (cache read
// only — status must never trigger a 100-page network fetch).
func lmarenaStatus(cfg *config.Config, client *lmarena.Client, board *lmarena.Leaderboard) map[string]any {
	if !cfg.LMArena.Enabled || client == nil {
		return map[string]any{
			"address": cfg.LMArena.BaseURL,
			"status":  "disabled",
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h, err := client.Health(ctx)
	if err != nil {
		return map[string]any{
			"address": cfg.LMArena.BaseURL,
			"status":  "unreachable",
			"error":   err.Error(),
		}
	}
	out := map[string]any{
		"address":   cfg.LMArena.BaseURL,
		"status":    h.Status,
		"uptime_s":  h.Uptime,
		"endpoints": []string{"/v1/lmarena/v1/responses", "/v1/lmarena/v1/session", "/v1/lmarena/evals", "/v1/lmarena/leaderboard", "/lmarena/healthz"},
	}
	if board != nil {
		if snap, ok := board.Cached("overall"); ok {
			top := make([]string, 0, 5)
			for i, e := range snap.Entries {
				if i >= 5 {
					break
				}
				top = append(top, e.Model)
			}
			out["leaderboard_updated"] = snap.Updated
			out["leaderboard_top5"] = top
		}
	}
	return out
}

// keySet renders a masked key indicator for the check command.
func keySet(k string) string {
	if k == "" {
		return "not set"
	}
	return "set"
}

// parallelStatus reports the web-search integration state for
// /ai-stack/status: Parallel APIs when keyed, else the keyless stealth
// DuckDuckGo backend.
func parallelStatus(client *parallel.Client, searcher *websearch.Searcher) map[string]any {
	if client == nil {
		return map[string]any{
			"address":   "https://api.parallel.ai",
			"status":    "disabled",
			"backend":   "none",
			"endpoints": []string{"/v1/deep-research"},
		}
	}
	base := map[string]any{
		"address": "https://api.parallel.ai",
		"backend": "parallel",
	}
	if client.Enabled() {
		base["status"] = "ok"
	} else {
		base["status"] = "keyless"
		base["note"] = "Task runs + /v1/responses attempt calls without a key and fall back to the native harness on auth rejection"
	}
	if searcher != nil {
		base["endpoints"] = []string{"/v1/parallel/search", "/v1/parallel/extract", "/v1/deep-research", "/v1/responses"}
	} else {
		base["endpoints"] = []string{"/v1/deep-research", "/v1/responses"}
	}
	return base
}

// usProxyPoolStatus reports the built-in US SOCKS5 pool state for
// /ai-stack/status — the gateway's own stealth-egress layer.
func usProxyPoolStatus(pool stealth.ProxyDispenser) map[string]any {
	if pool == nil {
		return map[string]any{"status": "disabled"}
	}
	return map[string]any{
		"status":  "ok",
		"proxies": pool.Size(),
		"note":    "egress via hermes sidecar SOCKS5 handshake (RFC 1928)",
	}
}

// passthroughBackendURL returns the address of the freebuff-proxy bridge
// process behind this gateway. Priority: FREEBUFF_PROXY_BACKEND env, config
// proxy.backend_url, then the :3457 default.
func passthroughBackendURL(cfg *config.Config) string {
	if b := os.Getenv("FREEBUFF_PROXY_BACKEND"); b != "" {
		return b
	}
	if cfg.Proxy.BackendURL != "" {
		return cfg.Proxy.BackendURL
	}
	return "http://127.0.0.1:3457"
}

// ---------------------------------------------------------------------------
// Probe helpers
// ---------------------------------------------------------------------------

func probeBackend(base string) string {
	if base == "" {
		return "disabled"
	}
	// Cached 5s to avoid 2s*2 dial per /healthz under load.
	healthCacheMu.RLock()
	if e, ok := healthCache[base]; ok && time.Now().Before(e.exp) {
		healthCacheMu.RUnlock()
		return e.val
	}
	healthCacheMu.RUnlock()

	client := &http.Client{Timeout: 2 * time.Second}
	base = strings.TrimRight(base, "/")
	val := "unreachable"
	resp, err := client.Get(base + "/healthz")
	if err == nil {
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK:
			val = "ok"
		case resp.StatusCode != http.StatusNotFound:
			val = fmt.Sprintf("http_%d", resp.StatusCode)
		default:
			// /healthz 404 -> try /
			goto probeRoot
		}
		healthCacheMu.Lock()
		healthCache[base] = struct {
			val string
			exp time.Time
		}{val, time.Now().Add(healthCacheTTL)}
		healthCacheMu.Unlock()
		return val
	}
probeRoot:
	resp2, err := client.Get(base + "/")
	if err == nil {
		resp2.Body.Close()
		if resp2.StatusCode < http.StatusInternalServerError {
			val = "ok"
		} else {
			val = fmt.Sprintf("http_%d", resp2.StatusCode)
		}
	}
	healthCacheMu.Lock()
	healthCache[base] = struct {
		val string
		exp time.Time
	}{val, time.Now().Add(healthCacheTTL)}
	healthCacheMu.Unlock()
	return val
}

func probeHTTPS(addr string) string {
	if addr == "" {
		return "disabled"
	}
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"http/1.1"},
		ServerName:         strings.Split(addr, ":")[0],
	}
	dialer := &tls.Dialer{Config: tlsConfig}
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext:     dialer.DialContext,
			TLSClientConfig: tlsConfig,
			TLSNextProto:    make(map[string]func(string, *tls.Conn) http.RoundTripper),
		},
	}
	resp, err := client.Get("https://" + addr + "/healthz")
	if err != nil {
		return "unreachable"
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return "ok"
	}
	return fmt.Sprintf("https_%d", resp.StatusCode)
}

// ---------------------------------------------------------------------------
// PoolStatsProvider adapters
// ---------------------------------------------------------------------------

type staticStats struct{ v any }

func (s staticStats) Stats() any { return s.v }

type usProxyStats struct{ pool stealth.ProxyDispenser }

func (s usProxyStats) Stats() any {
	if s.pool == nil {
		return map[string]any{"size": 0}
	}
	return map[string]any{"size": s.pool.Size(), "mode": "round-robin"}
}

// ---------------------------------------------------------------------------
// OAuth wiring
// ---------------------------------------------------------------------------

func newOAuthFlow(cfg *config.Config) *oauth.Flow {
	baseURL := cfg.Upstream.BaseURL
	if baseURL == "" {
		baseURL = "https://codebuff.com"
	}
	return &oauth.Flow{
		BaseURL:      baseURL,
		PollInterval: 2 * time.Second,
		PollTimeout:  5 * time.Minute,
	}
}

// credentialsPath mirrors the freebuff-proxy default credential location:
// $FREEBUFF_CREDS_DIR/credentials.json, auth.dir from config, or
// ~/.freebuff/credentials.json.
func credentialsPath(cfg *config.Config) string {
	if p := os.Getenv("FREEBUFF_CREDS_DIR"); p != "" {
		return filepath.Join(p, "credentials.json")
	}
	if cfg.Auth.Dir != "" {
		return filepath.Join(cfg.Auth.Dir, "credentials.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".freebuff", "credentials.json")
	}
	return filepath.Join(home, ".freebuff", "credentials.json")
}

// ---------------------------------------------------------------------------
// Dashboard
// ---------------------------------------------------------------------------

func startDashboard(dcfg config.DashboardConfig, logger *log.Logger) {
	engine := dashboard.NewProbeEngine(5*time.Second, nil)
	engine.Start()

	mux := dashboard.NewHandler(engine, dcfg.Prefix)
	server := &http.Server{Addr: dcfg.Addr, Handler: dashboard.LogMiddleware(mux)}

	go func() {
		logger.Printf("[dashboard] starting on %s (prefix: %s)", dcfg.Addr, dcfg.Prefix)
		logger.Printf("[dashboard] UI http://%s/ | API http://%s/api/status | SSE http://%s/api/status/stream",
			dcfg.Addr, dcfg.Addr, dcfg.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("[dashboard] server error: %v", err)
		}
	}()
}
