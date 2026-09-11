// Package websearch provides a keyless web search backend that runs through
// the hermes stealth sidecar (browser-like TLS 1.3 / HTTP2 fingerprint and
// optional SOCKS5 egress). It scrapes DuckDuckGo's HTML endpoints — no API
// key, no signup — and normalizes results into the same shape as the
// Parallel Search API so callers can switch between them transparently.
package websearch

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"

	"freebuff-unified/internal/hermes"
)

// Result mirrors the Parallel Search result shape.
type Result struct {
	Title    string   `json:"title"`
	URL      string   `json:"url"`
	Snippet  string   `json:"snippet"`
	Excerpts []string `json:"excerpts"`
}

// Response is the normalized search reply.
type Response struct {
	Query     string   `json:"query"`
	Backend   string   `json:"backend"`
	Results   []Result `json:"results"`
	LatencyMS int64    `json:"latency_ms"`
}

// Searcher performs keyless web searches via the stealth sidecar.
type Searcher struct {
	sidecar *hermes.Client
	// Proxy returns the egress proxy for a search request (optional).
	Proxy func() string
	// Timeout per fetch (default 12s).
	Timeout time.Duration
}

// New builds a keyless searcher on top of the hermes sidecar.
func New(sidecar *hermes.Client) *Searcher {
	return &Searcher{sidecar: sidecar, Timeout: 12 * time.Second}
}

var (
	// html/links.duckduckgo.com/html/?q=... result rows.
	reResult = regexp.MustCompile(`(?s)<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	// Snippet block following each result anchor.
	reSnippet = regexp.MustCompile(`(?s)<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)
	// Anti-blocking lite endpoint results: <a rel="nofollow" class="result-link" href="...">title</a>
	reLite = regexp.MustCompile(`(?s)<a[^>]+class="result-link"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	reTag  = regexp.MustCompile(`<[^>]+>`)
	// DDG wraps URLs as /l/?uddg=<urlencoded>&...
	reUddg = regexp.MustCompile(`uddg=([^&]+)`)
	// <title> fallback for pages without a usable heading.
	reTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	// Collapse runs of whitespace left after tag stripping.
	reSpaces = regexp.MustCompile(`\s+`)
	// Block these page regions to keep only readable article content. Go's
	// RE2 has no backreferences, so each (non-nesting) block tag gets its own
	// pattern rather than one `<(script|...)>.*?</\1>`.
	blockTags = []string{"script", "style", "noscript", "svg", "head", "nav", "footer", "aside", "header"}
	reBlocks  = func() []*regexp.Regexp {
		out := make([]*regexp.Regexp, 0, len(blockTags))
		for _, t := range blockTags {
			out = append(out, regexp.MustCompile(`(?is)<`+t+`[^>]*>.*?</`+t+`>`))
		}
		return out
	}()
)

// maxPageChars caps how much of a fetched page is kept for synthesis.
const maxPageChars = 16_000

// Search runs one query and returns normalized results.
func (s *Searcher) Search(ctx context.Context, query string, maxResults int) (*Response, error) {
	if s.sidecar == nil {
		return nil, fmt.Errorf("websearch: stealth sidecar not configured")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("websearch: empty query")
	}
	if maxResults <= 0 {
		maxResults = 10
	}
	start := time.Now()

	body, err := s.fetchDDG(ctx, query)
	if err != nil {
		return nil, err
	}
	results := parseResults(body, maxResults)
	return &Response{
		Query:     query,
		Backend:   "duckduckgo-stealth",
		Results:   results,
		LatencyMS: time.Since(start).Milliseconds(),
	}, nil
}

func (s *Searcher) fetchDDG(ctx context.Context, query string) (string, error) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	endpoints := []struct{ url, parser string }{
		{"https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query), "html"},
		{"https://lite.duckduckgo.com/lite/?q=" + url.QueryEscape(query), "lite"},
	}
	var lastErr error
	for _, ep := range endpoints {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		resp, err := s.sidecar.Fetch(cctx, hermes.Request{
			URL:       ep.url,
			Method:    "GET",
			TimeoutMS: int(timeout.Milliseconds()),
			HTTP2:     true,
			Headers:   map[string]string{"Accept": "text/html", "Accept-Language": "en-US,en;q=0.9"},
			JSON:      boolPtr(false),
		})
		cancel()
		if err == nil && resp.Status == 200 {
			text := resp.Text()
			if strings.Contains(text, "result__a") || strings.Contains(text, "result-link") {
				return text, nil
			}
			lastErr = fmt.Errorf("ddg returned no results markup (possible bot challenge)")
			continue
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("ddg http %d", resp.Status)
		}
	}
	return "", fmt.Errorf("websearch: all ddg endpoints failed: %w", lastErr)
}

func parseResults(body string, max int) []Result {
	var out []Result
	matches := reResult.FindAllStringSubmatch(body, -1)
	snippets := reSnippet.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		// Lite endpoint fallback parsing.
		for _, m := range reLite.FindAllStringSubmatch(body, -1) {
			u := unwrapDDG(m[1])
			out = append(out, Result{
				Title:    stripTags(m[2]),
				URL:      u,
				Snippet:  "",
				Excerpts: []string{},
			})
			if len(out) >= max {
				break
			}
		}
		return out
	}
	for i, m := range matches {
		u := unwrapDDG(m[1])
		snip := ""
		if i < len(snippets) {
			snip = stripTags(snippets[i][1])
		}
		out = append(out, Result{
			Title:    stripTags(m[2]),
			URL:      u,
			Snippet:  snip,
			Excerpts: []string{},
		})
		if len(out) >= max {
			break
		}
	}
	return out
}

// Page is a single keyless full-page read via the stealth sidecar. It backs
// /v1/deep-research extraction when no Parallel API key is configured.
type Page struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Text  string `json:"text"`
}

// FetchPage reads one page through the stealth sidecar and returns cleaned
// article text (script/style/nav stripped, whitespace collapsed, 16k cap).
func (s *Searcher) FetchPage(ctx context.Context, rawURL string) (*Page, error) {
	if s.sidecar == nil {
		return nil, fmt.Errorf("websearch: stealth sidecar not configured")
	}
	if strings.TrimSpace(rawURL) == "" {
		return nil, fmt.Errorf("websearch: empty url")
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := s.sidecar.Fetch(cctx, hermes.Request{
		URL:       rawURL,
		Method:    "GET",
		TimeoutMS: int(timeout.Milliseconds()),
		HTTP2:     true,
		Headers:   map[string]string{"Accept": "text/html,text/plain", "Accept-Language": "en-US,en;q=0.9"},
		JSON:      boolPtr(false),
	})
	if err != nil {
		return nil, err
	}
	if resp.Status != 200 {
		return nil, fmt.Errorf("page http %d", resp.Status)
	}
	raw := resp.Text()
	text := cleanPageText(raw)
	if len(text) > maxPageChars {
		text = text[:maxPageChars]
	}
	return &Page{Title: extractTitle(raw), URL: rawURL, Text: text}, nil
}

// extractTitle pulls the <title> text (or first <h1>) from a raw page.
func extractTitle(raw string) string {
	if m := reTitle.FindStringSubmatch(raw); m != nil {
		if t := strings.TrimSpace(stripTags(m[1])); t != "" {
			return t
		}
	}
	return ""
}

// cleanPageText strips non-content regions, removes tags, unescapes entities,
// and collapses whitespace into readable text.
func cleanPageText(raw string) string {
	for _, re := range reBlocks {
		raw = re.ReplaceAllString(raw, " ")
	}
	raw = stripTags(raw)
	raw = reSpaces.ReplaceAllString(raw, " ")
	return strings.TrimSpace(raw)
}

// unwrapDDG resolves DDG's /l/?uddg= redirect wrapper to the real URL.
func unwrapDDG(href string) string {
	href = strings.TrimSpace(href)
	if m := reUddg.FindStringSubmatch(href); m != nil {
		if u, err := url.QueryUnescape(m[1]); err == nil {
			return u
		}
	}
	return href
}

func stripTags(s string) string {
	s = reTag.ReplaceAllString(s, "")
	return strings.TrimSpace(html.UnescapeString(s))
}

func boolPtr(b bool) *bool { return &b }
