package stealth

import (
	"fmt"
	"net/http"
	"strings"
)

// HeaderSanitizer cleans outbound HTTP headers to remove proxy fingerprints
// and inject browser-typical headers.
type HeaderSanitizer struct {
	profile  *Profile
	customUA string
	extra    map[string]string
	preserve []string
}

// HeaderSanitizerOption configures a HeaderSanitizer.
type HeaderSanitizerOption func(*HeaderSanitizer)

// WithCustomUserAgent overrides the profile's default User-Agent.
func WithCustomUserAgent(ua string) HeaderSanitizerOption {
	return func(hs *HeaderSanitizer) {
		hs.customUA = ua
	}
}

// WithExtraHeader adds an additional header to inject on every request.
func WithExtraHeader(key, value string) HeaderSanitizerOption {
	return func(hs *HeaderSanitizer) {
		if hs.extra == nil {
			hs.extra = make(map[string]string)
		}
		hs.extra[key] = value
	}
}

// PreserveHeader prevents the sanitizer from stripping the given header.
func PreserveHeader(key string) HeaderSanitizerOption {
	return func(hs *HeaderSanitizer) {
		hs.preserve = append(hs.preserve, strings.ToLower(key))
	}
}

// NewHeaderSanitizer creates a new HeaderSanitizer from the given profile.
func NewHeaderSanitizer(profile *Profile, opts ...HeaderSanitizerOption) *HeaderSanitizer {
	if profile == nil {
		profile = DefaultProfile
	}
	hs := &HeaderSanitizer{profile: profile}
	for _, opt := range opts {
		opt(hs)
	}
	return hs
}

// HeadersToStrip lists headers that identify HTTP clients as proxies or automation tools.
var HeadersToStrip = []string{
	"X-Forwarded-For",
	"X-Forwarded-Proto",
	"X-Forwarded-Host",
	"X-Real-IP",
	"X-Proxy-User-IP",
	"Via",
	"X-Via",
	"Proxy-Connection",
	"X-Proxy-Agent",
	"X-Request-ID",
	"CF-Connecting-IP",
	"CF-IPCountry",
	"CF-Ray",
	"CF-Visitor",
	"True-Client-IP",
	"X-Originating-IP",
	"X-Remote-IP",
	"X-Remote-Addr",
	"X-Client-IP",
	"X-Host",
	"X-Correlation-ID",
	"X-Trace-ID",
	"X-Amzn-Trace-Id",
	"X-Cache",
	"X-Served-By",
}

// Clean removes proxy-identifying headers from the request.
func (hs *HeaderSanitizer) Clean(h http.Header) []string {
	var removed []string
	preserve := make(map[string]bool)
	for _, k := range hs.preserve {
		preserve[k] = true
	}

	for _, hdr := range HeadersToStrip {
		lower := strings.ToLower(hdr)
		if preserve[lower] {
			continue
		}
		if v := h.Get(hdr); v != "" {
			h.Del(hdr)
			removed = append(removed, fmt.Sprintf("%s: %s", hdr, v))
		}
		if v := h.Get(lower); v != "" && !preserve[lower] {
			h.Del(lower)
		}
	}

	return removed
}

// Inject adds browser-typical headers to the request.
func (hs *HeaderSanitizer) Inject(h http.Header, force bool) {
	setOrSkip := func(key, value string) {
		if value == "" {
			return
		}
		if force || h.Get(key) == "" {
			h.Set(key, value)
		}
	}

	ua := hs.profile.UserAgent
	if hs.customUA != "" {
		ua = hs.customUA
	}
	if ua == "" {
		ua = randomUserAgent()
	}
	setOrSkip("User-Agent", ua)

	if hs.profile.SecChUA != "" {
		setOrSkip("Sec-CH-UA", hs.profile.SecChUA)
	}
	if hs.profile.SecChUAPlatform != "" {
		setOrSkip("Sec-CH-UA-Platform", hs.profile.SecChUAPlatform)
	}
	setOrSkip("Sec-CH-UA-Mobile", "?0")

	setOrSkip("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	setOrSkip("Accept-Language", hs.profile.AcceptLanguage)
	setOrSkip("Accept-Encoding", hs.profile.AcceptEncoding)

	setOrSkip("Sec-Fetch-Site", "cross-site")
	setOrSkip("Sec-Fetch-Mode", "navigate")
	setOrSkip("Sec-Fetch-Dest", "document")
	setOrSkip("Upgrade-Insecure-Requests", "1")

	for k, v := range hs.extra {
		h.Set(k, v)
	}
}

// Sanitize performs a full clean + inject on the given headers.
func (hs *HeaderSanitizer) Sanitize(h http.Header) []string {
	removed := hs.Clean(h)
	hs.Inject(h, false)
	return removed
}
