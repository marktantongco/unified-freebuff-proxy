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
	Parallel  ParallelConfig  `yaml:"parallel"`
}

// ParallelConfig points at the Parallel Web APIs (search/extract).
type ParallelConfig struct {
	Enabled bool   `yaml:"enabled"`
	BaseURL string `yaml:"base_url"`
	// APIKey is read from PARALLEL_API_KEY when empty.
	APIKey string `yaml:"api_key"`
	// DefaultMode for /v1/parallel/search when the caller omits it
	// (turbo|fast|basic|advanced; server default is advanced).
	DefaultMode string `yaml:"default_mode"`
}

// HermesConfig points at the hermes stealth sidecar (deps/hermes-service).
type HermesConfig struct {
	Enabled bool   `yaml:"enabled"`
	BaseURL string `yaml:"base_url"`
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
	if c.Parallel.BaseURL == "" {
		c.Parallel.BaseURL = "https://api.parallel.ai"
	}
	if c.Parallel.DefaultMode == "" {
		c.Parallel.DefaultMode = "fast"
	}
	if c.Dashboard.Addr == "" {
		c.Dashboard.Addr = ":9091"
	}
	if c.Dashboard.Prefix == "" {
		c.Dashboard.Prefix = "/dashboard"
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
