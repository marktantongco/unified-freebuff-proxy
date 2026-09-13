package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Upstream  UpstreamConfig  `yaml:"upstream"`
	Auth      AuthConfig      `yaml:"auth"`
	Stealth   StealthConfig   `yaml:"stealth"`
	Limits    LimitsConfig    `yaml:"limits"`
	Proxy     ProxyConfig     `yaml:"proxy"`
	Logging   LoggingConfig   `yaml:"logging"`
	Dashboard DashboardConfig `yaml:"dashboard"`
	Hermes    HermesConfig    `yaml:"hermes"`
	LMArena   LMArenaConfig   `yaml:"lmarena"`
	Parallel  ParallelConfig  `yaml:"parallel"`
	Research  ResearchConfig  `yaml:"research"`
}

// ParallelConfig points at the Parallel Web APIs (search/extract/task/responses).
type ParallelConfig struct {
	Enabled bool   `yaml:"enabled"`
	BaseURL string `yaml:"base_url"`
	// APIKey is read from PARALLEL_API_KEY when empty. Optional: Layer A
	// deep-research + /v1/responses work keyless-first and fall back to the
	// native harness when the API rejects a call without a key.
	APIKey string `yaml:"api_key"`
	// DefaultMode for /v1/parallel/search when the caller omits it
	// (turbo|fast|basic|advanced; server default is advanced).
	DefaultMode string `yaml:"default_mode"`
	// DefaultProcessor for Layer A /v1/deep-research Task runs when the
	// caller omits it (default pro-fast).
	DefaultProcessor string `yaml:"default_processor"`
}

// HermesConfig points at the hermes stealth sidecar (deps/hermes-service).
type HermesConfig struct {
	Enabled bool   `yaml:"enabled"`
	BaseURL string `yaml:"base_url"`
}

// LMArenaConfig points at the lmarena-stealth-proxy sidecar
// (deps/lmarena-stealth-proxy, :3103), plus the manual-eval harness store.
type LMArenaConfig struct {
	Enabled bool   `yaml:"enabled"`
	BaseURL string `yaml:"base_url"`
	// EvalDir holds manual-eval JSON files (one per eval). Pasted model
	// outputs are user data: keep this dir untracked, like auths/.
	EvalDir string `yaml:"eval_dir"`
	// Leaderboard enables the cached public text-leaderboard snapshot
	// (GET /v1/lmarena/leaderboard). Read-only HF datasets-server data.
	Leaderboard bool `yaml:"leaderboard"`
	// LeaderboardRefreshH bounds snapshot refresh to once per N hours.
	LeaderboardRefreshH int `yaml:"leaderboard_refresh_hours"`
}

// ResearchConfig tunes the native /v1/deep-research harness (Layer B): the
// gateway LLM plans sub-queries, they fan out to the Parallel Search API and/or
// the keyless DDG stealth searcher, the top pages are read, and the LLM
// synthesizes a cited markdown report.
type ResearchConfig struct {
	// Enabled turns on /v1/deep-research (default true; requires a chat
	// service plus at least one search backend at request time).
	Enabled bool `yaml:"enabled"`
	// Model overrides the LLM used for planning + synthesis (default:
	// upstream default_model).
	Model string `yaml:"model"`
	// MaxQueries caps the planner's sub-query count (default 5).
	MaxQueries int `yaml:"max_queries"`
	// FanOut caps concurrent search+extract workers (default 8).
	FanOut int `yaml:"fan_out"`
	// ExtractPerQuery is how many top results are deep-read per sub-query
	// (default 3).
	ExtractPerQuery int `yaml:"extract_per_query"`
	// TimeoutMS bounds the whole plan→synthesize run (default 300000).
	TimeoutMS int `yaml:"timeout_ms"`
}

type ServerConfig struct {
	ListenAddr  string   `yaml:"listen"`
	APIKeys     []string `yaml:"api_keys"`
	AutoApprove bool     `yaml:"auto_approve"`
}

type UpstreamConfig struct {
	BaseURL      string `yaml:"base_url"`
	CostMode     string `yaml:"cost_mode"`
	DefaultModel string `yaml:"default_model"`
}

type AuthConfig struct {
	APIKeys []string      `yaml:"api_keys"`
	Dir     string        `yaml:"dir"`
	Breaker BreakerConfig `yaml:"breaker"`
}

type BreakerConfig struct {
	Threshold int           `yaml:"threshold"`
	Cooldown  time.Duration `yaml:"cooldown"`
}

type StealthConfig struct {
	USProxies        []string `yaml:"us_proxies"`
	StripHeaders     bool     `yaml:"strip_headers"`
	Enabled          bool     `yaml:"enabled"`
	Profile          string   `yaml:"profile"`
	ProxyURL         string   `yaml:"proxy_url"`
	ProxyRefreshMins int      `yaml:"proxy_refresh_mins"`
	StrictGeo        bool     `yaml:"strict_geo"`
	GeoVerify        bool     `yaml:"geo_verify"`
	// AutoRefreshPool enables the background refresher that fetches,
	// probes, and hot-swaps the SOCKS5 pool (default false).
	AutoRefreshPool bool `yaml:"auto_refresh_pool"`
	// MaxPoolProxies caps the refreshed pool size (default 12).
	MaxPoolProxies int `yaml:"max_pool_proxies"`
	// SearxngURL is the base URL of a SearXNG instance (e.g. http://127.0.0.1:8888).
	// Empty = SearXNG backend disabled. Can also be set via SEARXNG_URL env.
	SearxngURL string `yaml:"searxng_url"`
}

type LimitsConfig struct {
	GlobalRPM  int `yaml:"global_rpm"`
	AccountRPM int `yaml:"account_rpm"`
	ClientRPM  int `yaml:"client_rpm"`
}

type ProxyConfig struct {
	Enabled    bool   `yaml:"enabled"`
	BackendURL string `yaml:"backend_url"`
	// Mode selects how the gateway treats the backend:
	//   "" or "report"      — health reporting only (default; native path serves /v1/*)
	//   "passthrough"       — front-door mode: /v1/* is relayed byte-level to BackendURL
	Mode string `yaml:"mode"`
}

// PassthroughEnabled reports whether the front-door passthrough should be
// wired (explicit opt-in via mode: passthrough plus a backend URL).
func (c *Config) PassthroughEnabled() bool {
	return c.Proxy.Mode == "passthrough" && c.Proxy.BackendURL != ""
}

type DashboardConfig struct {
	Enabled bool   `yaml:"enabled"`
	Addr    string `yaml:"addr"`
	Prefix  string `yaml:"prefix"`
}

type LoggingConfig struct {
	Level string `yaml:"level"`
}

func (c *Config) ApplyDefaults() {
	if c.Server.ListenAddr == "" {
		c.Server.ListenAddr = ":18080"
	}
	if c.Upstream.BaseURL == "" {
		c.Upstream.BaseURL = "https://www.codebuff.com"
	}
	if c.Upstream.CostMode == "" {
		c.Upstream.CostMode = "free"
	}
	if c.Upstream.DefaultModel == "" {
		c.Upstream.DefaultModel = "deepseek/deepseek-v4-pro"
	}
	if c.Auth.Dir == "" {
		c.Auth.Dir = "auths"
	}
	if c.Auth.Breaker.Threshold <= 0 {
		c.Auth.Breaker.Threshold = 3
	}
	if c.Auth.Breaker.Cooldown <= 0 {
		c.Auth.Breaker.Cooldown = 12 * time.Hour
	}
	if c.Limits.GlobalRPM <= 0 {
		c.Limits.GlobalRPM = 120
	}
	if c.Limits.AccountRPM <= 0 {
		c.Limits.AccountRPM = 30
	}
	if c.Limits.ClientRPM <= 0 {
		c.Limits.ClientRPM = 60
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Stealth.ProxyRefreshMins <= 0 {
		c.Stealth.ProxyRefreshMins = 30
	}
	if c.Hermes.BaseURL == "" {
		c.Hermes.BaseURL = "http://127.0.0.1:3101"
	}
	if c.LMArena.BaseURL == "" {
		c.LMArena.BaseURL = "http://127.0.0.1:3103"
	}
	if c.LMArena.EvalDir == "" {
		c.LMArena.EvalDir = "evals"
	}
	if c.LMArena.LeaderboardRefreshH <= 0 {
		c.LMArena.LeaderboardRefreshH = 24
	}
	if c.Parallel.BaseURL == "" {
		c.Parallel.BaseURL = "https://api.parallel.ai"
	}
	if c.Parallel.DefaultMode == "" {
		c.Parallel.DefaultMode = "fast"
	}
	if c.Parallel.DefaultProcessor == "" {
		c.Parallel.DefaultProcessor = "pro-fast"
	}
	if c.Dashboard.Addr == "" {
		c.Dashboard.Addr = ":9091"
	}
	if c.Dashboard.Prefix == "" {
		c.Dashboard.Prefix = "/dashboard"
	}
	if c.Research.MaxQueries <= 0 {
		c.Research.MaxQueries = 5
	}
	if c.Research.FanOut <= 0 {
		c.Research.FanOut = 8
	}
	if c.Research.ExtractPerQuery <= 0 {
		c.Research.ExtractPerQuery = 3
	}
	if c.Research.TimeoutMS <= 0 {
		c.Research.TimeoutMS = 300_000
	}
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	cfg.ApplyDefaults()
	return cfg, nil
}
