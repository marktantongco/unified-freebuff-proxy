// Package stealth — hermes-backed transport for upstream requests.
//
// HermesRoundTripper routes non-streaming HTTP calls through the hermes
// stealth sidecar (deps/hermes-service), which performs the request with a
// browser-like TLS 1.3 / HTTP2 fingerprint and optional SOCKS5 egress.
// Streaming calls (SSE) bypass the sidecar via the fallback RoundTripper
// because the sidecar buffers full responses.
package stealth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"freebuff-unified/internal/hermes"
)

// maxHermesBody caps how large a request body we push through the sidecar.
const maxHermesBody = 4 << 20 // 4 MiB

// HermesRoundTripper is an http.RoundTripper that sends requests through the
// hermes stealth sidecar. SSE/streaming requests always fall back to
// Fallback; other requests fall back too when FailOpen is set and the
// sidecar errors.
type HermesRoundTripper struct {
	// Sidecar is the hermes sidecar client.
	Sidecar *hermes.Client
	// Fallback handles streaming (SSE) requests that the sidecar cannot,
	// and (with FailOpen) any request the sidecar fails.
	Fallback http.RoundTripper
	// Proxy, when set, returns the egress proxy URL (e.g. socks5://h:p) for
	// each request, or "" for direct egress. Optional.
	Proxy func(*http.Request) string
	// HTTP2 enables HTTP/2 for sidecar-routed requests.
	HTTP2 bool
	// TimeoutMS caps a single sidecar request (0 = sidecar default 15s).
	TimeoutMS int
	// FailOpen routes via Fallback when the sidecar itself errors. Response
	// status codes (e.g. upstream 4xx/5xx) are always passed through.
	FailOpen bool
	// Metrics, when set, records sidecar latency, errors, and fallbacks.
	Metrics *Metrics
}

// RoundTrip implements http.RoundTripper.
func (rt *HermesRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.Sidecar == nil {
		rt.Metrics.RecordFallback()
		return rt.fallback(req, errors.New("stealth: no sidecar configured"))
	}

	// Streaming responses must bypass the sidecar: it buffers the full body
	// before replying, which would defeat SSE.
	if strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		rt.Metrics.RecordFallback()
		return rt.fallback(req, errors.New("stealth: streaming request"))
	}

	start := time.Now()
	resp, err := rt.viaSidecar(req)
	if err != nil {
		rt.Metrics.RecordSidecarError(err)
		if rt.FailOpen {
			rt.Metrics.RecordFallback()
			return rt.fallback(req, err)
		}
		return nil, err
	}
	rt.Metrics.RecordSidecar(time.Since(start))
	return resp, nil
}

func (rt *HermesRoundTripper) fallback(req *http.Request, cause error) (*http.Response, error) {
	if rt.Fallback == nil {
		return nil, fmt.Errorf("%w (no fallback transport)", cause)
	}
	return rt.Fallback.RoundTrip(req)
}

func (rt *HermesRoundTripper) viaSidecar(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(req.Body, maxHermesBody+1))
		if err != nil {
			return nil, fmt.Errorf("stealth: read request body: %w", err)
		}
		if len(raw) > maxHermesBody {
			return nil, errors.New("stealth: request body exceeds hermes limit")
		}
		body = raw
	}

	sidecarReq := hermes.Request{
		URL:       req.URL.String(),
		Method:    req.Method,
		TimeoutMS: rt.TimeoutMS,
		HTTP2:     rt.HTTP2,
	}
	for k, vv := range req.Header {
		if len(vv) > 0 {
			if sidecarReq.Headers == nil {
				sidecarReq.Headers = map[string]string{}
			}
			sidecarReq.Headers[k] = vv[0]
		}
	}
	if len(body) > 0 {
		// hermes.Request.Payload is JSON; non-JSON bodies travel as a JSON
		// string (the sidecar turns it back into raw bytes).
		if !json.Valid(body) {
			encoded, err := json.Marshal(string(body))
			if err != nil {
				return nil, fmt.Errorf("stealth: encode body: %w", err)
			}
			body = encoded
		}
		sidecarReq.Payload = body
	}
	if rt.Proxy != nil {
		sidecarReq.Proxy = rt.Proxy(req)
	}

	resp, err := rt.Sidecar.Fetch(req.Context(), sidecarReq)
	if err != nil {
		return nil, fmt.Errorf("stealth: sidecar fetch: %w", err)
	}
	if resp.Status == 0 {
		return nil, errors.New("stealth: sidecar returned no status")
	}

	raw := resp.Text()
	out := &http.Response{
		Status:     fmt.Sprintf("%d %s", resp.Status, http.StatusText(resp.Status)),
		StatusCode: resp.Status,
		Proto:      "HTTP/" + resp.HTTPVersion,
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header, len(resp.Headers)),
		Body:       io.NopCloser(strings.NewReader(raw)),
		Request:    req,
	}
	for k, v := range resp.Headers {
		// HTTP/2 pseudo-headers (:status) are not valid downstream header
		// names; skip them. Hop-by-hop framing is reconstructed below.
		if strings.HasPrefix(k, ":") ||
			strings.EqualFold(k, "Content-Length") ||
			strings.EqualFold(k, "Transfer-Encoding") ||
			strings.EqualFold(k, "Connection") {
			continue
		}
		out.Header.Set(k, v)
	}
	out.ContentLength = int64(len(raw))
	if cl := resp.Headers["content-length"]; cl != "" {
		if n, err := strconv.Atoi(cl); err == nil && n == len(raw) {
			out.ContentLength = int64(n)
		}
	}
	return out, nil
}
