// Package proxy implements the front-door passthrough: the unified engine
// receives /v1/* requests, applies its own policy (rate limits, auth) and
// then relays them unchanged to a freebuff-proxy backend. The backend does
// the protocol work (OpenAI + Anthropic shapes, SSE streaming, session
// lifecycle, token pool, dashboard); this package only moves bytes.
package proxy

import (
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// hopByHopHeaders must not be forwarded (RFC 7230 §6.1).
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

type Handler struct {
	mu      sync.RWMutex
	backend *url.URL // nil until SetBackend is called
	client  *http.Client
	logger  *log.Logger
}

// NewHandler returns a passthrough handler. Use SetBackend to configure (and
// later re-configure, on hot reload) the target freebuff-proxy URL.
func NewHandler(logger *log.Logger) *Handler {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 60 * time.Second
	return &Handler{
		client: &http.Client{Transport: transport},
		logger: logger,
	}
}

// SetBackend updates the target backend URL ("" disables proxying).
func (h *Handler) SetBackend(rawURL string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if strings.TrimSpace(rawURL) == "" {
		h.backend = nil
		return
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawURL), "/"))
	if err != nil {
		if h.logger != nil {
			h.logger.Printf("passthrough: invalid backend URL %q: %v", rawURL, err)
		}
		h.backend = nil
		return
	}
	h.backend = u
	if h.logger != nil {
		h.logger.Printf("passthrough: backend set to %s", u.String())
	}
}

func (h *Handler) BackendURL() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.backend == nil {
		return ""
	}
	return h.backend.String()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	backend := h.backend
	h.mu.RUnlock()
	if backend == nil {
		http.Error(w, "proxy backend not configured (set proxy.backend_url)", http.StatusServiceUnavailable)
		return
	}

	target := *backend
	target.Path = backend.Path + r.URL.Path
	target.RawQuery = r.URL.RawQuery

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		http.Error(w, "proxy: build request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	copyHeaders(outReq.Header, r.Header)
	outReq.Host = target.Host

	if h.logger != nil {
		h.logger.Printf("passthrough: %s %s", r.Method, target.String())
	}

	resp, err := h.client.Do(outReq)
	if err != nil {
		if h.logger != nil {
			h.logger.Printf("passthrough: backend error: %v", err)
		}
		http.Error(w, "proxy backend unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	RelayResponse(w, resp)
}

// RelayResponse copies an upstream *http.Response to the client verbatim:
// headers (minus hop-by-hop and Content-Length), status, and body. SSE
// responses are flushed after every chunk so frames arrive immediately.
func RelayResponse(w http.ResponseWriter, resp *http.Response) {
	copyHeaders(w.Header(), resp.Header)
	w.Header().Del("Content-Length")
	w.WriteHeader(resp.StatusCode)

	if isEventStream(resp.Header.Get("Content-Type")) {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		copyStream(w, resp.Body)
		return
	}
	io.Copy(w, resp.Body)
}

// copyStream copies while flushing after every chunk so SSE frames reach the
// client immediately instead of buffering until the stream ends.
func copyStream(dst io.Writer, src io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
			if f, ok := dst.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func isEventStream(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		if isHopByHop(key) {
			continue
		}
		for _, v := range values {
			dst.Add(key, v)
		}
	}
}

func isHopByHop(key string) bool {
	for _, h := range hopByHopHeaders {
		if strings.EqualFold(h, key) {
			return true
		}
	}
	return false
}
