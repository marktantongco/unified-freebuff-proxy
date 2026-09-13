package stealth

import (
	"context"
	"net"
	"net/http"
	"time"
)

// ClientConfig configures a stealth http.Client.
type ClientConfig struct {
	Profile         *Profile
	Timeout         time.Duration
	MaxIdleConns    int
	IdleConnTimeout time.Duration
	EnableJitter    bool
	JitterMin       time.Duration
	JitterMax       time.Duration
	SanitizeHeaders bool
	CustomUserAgent string
	ExtraHeaders    map[string]string
	PreserveHeaders []string
	RoundTripper    http.RoundTripper
	Resolver        func(ctx context.Context, network, addr string) (net.Conn, error)
	ProxyPool       *ProxyPool
}

// DefaultClientConfig returns a ClientConfig with sensible defaults.
func DefaultClientConfig() *ClientConfig {
	return &ClientConfig{
		Profile:         DefaultProfile,
		Timeout:         180 * time.Second,
		MaxIdleConns:    100,
		IdleConnTimeout: 90 * time.Second,
		EnableJitter:    false,
		SanitizeHeaders: true,
	}
}

// NewClient creates a drop-in replacement for http.Client with JA3 stealth.
func NewClient(cfg ClientConfig) *http.Client {
	if cfg.Profile == nil {
		cfg.Profile = DefaultProfile
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 180 * time.Second
	}
	if cfg.MaxIdleConns == 0 {
		cfg.MaxIdleConns = 100
	}
	if cfg.IdleConnTimeout == 0 {
		cfg.IdleConnTimeout = 90 * time.Second
	}

	var transport http.RoundTripper

	if cfg.RoundTripper != nil {
		transport = cfg.RoundTripper
	} else {
		// MaxIdleConnsPerHost must be raised well above Go's default of 2 to
		// prevent TLS-handshake storms under concurrent load. TLSHandshakeTimeout
		// is not applied when DialTLSContext is set — the dialer bounds the utls
		// handshake itself (tlsHandshakeTimeout).
		transport = &http.Transport{
			DialTLSContext:        Dialer(cfg.Profile, cfg.Resolver, WithProxyPool(cfg.ProxyPool)),
			MaxIdleConns:          cfg.MaxIdleConns,
			MaxIdleConnsPerHost:   cfg.MaxIdleConns,
			IdleConnTimeout:       cfg.IdleConnTimeout,
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ForceAttemptHTTP2:     true,
		}
	}

	if cfg.SanitizeHeaders {
		sanitizerOpts := []HeaderSanitizerOption{}
		if cfg.CustomUserAgent != "" {
			sanitizerOpts = append(sanitizerOpts, WithCustomUserAgent(cfg.CustomUserAgent))
		}
		for k, v := range cfg.ExtraHeaders {
			sanitizerOpts = append(sanitizerOpts, WithExtraHeader(k, v))
		}
		for _, h := range cfg.PreserveHeaders {
			sanitizerOpts = append(sanitizerOpts, PreserveHeader(h))
		}
		sanitizer := NewHeaderSanitizer(cfg.Profile, sanitizerOpts...)
		transport = &HeaderRoundTripper{
			next:      transport,
			sanitizer: sanitizer,
		}
	}

	if cfg.EnableJitter {
		jitterMin := cfg.JitterMin
		if jitterMin == 0 {
			jitterMin = 50 * time.Millisecond
		}
		jitterMax := cfg.JitterMax
		if jitterMax == 0 {
			jitterMax = 300 * time.Millisecond
		}
		transport = &jitterRoundTripper{
			next:       transport,
			randomizer: NewRandomizer(WithMinDelay(jitterMin), WithMaxDelay(jitterMax)),
		}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
	}
}

// NewDefaultClient returns a stealth http.Client with Chrome 120 fingerprint.
func NewDefaultClient() *http.Client {
	return NewClient(*DefaultClientConfig())
}

type HeaderRoundTripper struct {
	next      http.RoundTripper
	sanitizer *HeaderSanitizer
}

func (h *HeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	if req2.Header == nil {
		req2.Header = make(http.Header)
	}
	_ = h.sanitizer.Sanitize(req2.Header)
	return h.next.RoundTrip(req2)
}

type jitterRoundTripper struct {
	next       http.RoundTripper
	randomizer *Randomizer
}

func (j *jitterRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	j.randomizer.Wait()
	return j.next.RoundTrip(req)
}
