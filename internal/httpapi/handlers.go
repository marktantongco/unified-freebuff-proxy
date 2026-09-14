package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"freebuff-unified/internal/anthropic"
	"freebuff-unified/internal/eval"
	"freebuff-unified/internal/hermes"
	"freebuff-unified/internal/lmarena"
	"freebuff-unified/internal/openai"
	"freebuff-unified/internal/parallel"
	"freebuff-unified/internal/stealth"
	"freebuff-unified/internal/websearch"
	"github.com/gofiber/fiber/v3"
)

// ChatService is the interface the HTTP layer expects from the chat adapter.
type ChatService interface {
	Complete(ctx context.Context, req openai.ChatCompletionRequest) (string, error)
	Stream(ctx context.Context, req openai.ChatCompletionRequest) (<-chan string, <-chan error)
}

// ServiceError carries a service-level error with HTTP status.
type ServiceError struct {
	Status  int
	Code    string
	Message string
}

func (e *ServiceError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return http.StatusText(e.Status)
}

const streamHeartbeatInterval = 15 * time.Second

// PoolStatsProvider is the interface for /healthz monitoring providers.
type PoolStatsProvider interface {
	Stats() any
}

type handlers struct {
	model             string
	chat              ChatService
	tokenPool         PoolStatsProvider
	proxyPool         PoolStatsProvider
	hermes            *hermes.Client
	lmarena           *lmarena.Client
	evals             *eval.Store
	board             *lmarena.Leaderboard
	evalsDirFn        func() string // injected: returns lmarena.eval_dir
	parallel          *parallel.Client
	parallelMode      string
	parallelProcessor string
	webSearcher       *websearch.Searcher
	searxng           *websearch.SearxngSearcher
	searchCache       *websearch.Cache
	research          ResearchConfig
	stealth           *stealth.Metrics
	refresher         func() map[string]any
	extraHealth       func() map[string]any
	aiStack           func() map[string]any
	adaptive          *adaptiveController
}

type notConfiguredChatService struct{}

func newHandlers(model string, chat ChatService, tokenPool, proxyPool PoolStatsProvider) *handlers {
	if chat == nil {
		chat = notConfiguredChatService{}
	}
	return &handlers{
		model:     model,
		chat:      chat,
		tokenPool: tokenPool,
		proxyPool: proxyPool,
	}
}

func (h *handlers) evalsDir() string {
	if h.evalsDirFn != nil {
		return h.evalsDirFn()
	}
	return ""
}

// HermesHealth reports the hermes stealth sidecar liveness.
func (h *handlers) HermesHealth(c fiber.Ctx) error {
	if h.hermes == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{
			"status": "disabled",
		})
	}
	health, err := h.hermes.Health(c)
	if err != nil {
		return c.Status(http.StatusBadGateway).JSON(map[string]any{
			"status": "unreachable",
			"error":  err.Error(),
		})
	}
	return c.Status(http.StatusOK).JSON(health)
}

// ParallelSearch proxies a web search through the Parallel Search API.
// Body: {"objective": str, "search_queries": [..], "mode": "fast", "max_results": 10}
func (h *handlers) ParallelSearch(c fiber.Ctx) error {
	// No Parallel key AND no keyless stealth backend configured: the endpoint
	// cannot serve anything. When only the key is missing, the keyless
	// stealth search backend (hermes sidecar) takes over below.
	if (h.parallel == nil || !h.parallel.Enabled()) && h.webSearcher == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{
			"error": map[string]any{"message": "no search backend configured (set PARALLEL_API_KEY or enable hermes sidecar)", "type": "search_unavailable"},
		})
	}
	var body struct {
		Objective      string   `json:"objective"`
		SearchQueries  []string `json:"search_queries"`
		Mode           string   `json:"mode"`
		MaxResults     int      `json:"max_results"`
		ClientModel    string   `json:"client_model"`
		MaxCharsTotal  int      `json:"max_chars_total"`
		SessionID      string   `json:"session_id"`
		Location       string   `json:"location"`
		ExcludeDomains []string `json:"exclude_domains"`
		Proxy          string   `json:"proxy"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return c.Status(http.StatusBadRequest).JSON(map[string]any{
			"error": map[string]any{"message": "invalid JSON body", "type": "invalid_request_error"},
		})
	}
	if strings.TrimSpace(body.Objective) == "" && len(body.SearchQueries) == 0 {
		return c.Status(http.StatusBadRequest).JSON(map[string]any{
			"error": map[string]any{"message": "objective or search_queries required", "type": "invalid_request_error"},
		})
	}
	// Keyless stealth backend: used when Parallel has no API key. Search
	// queries merge into the objective for keyword-style engines.
	if h.parallel == nil || !h.parallel.Enabled() {
		q := strings.TrimSpace(body.Objective)
		if q == "" && len(body.SearchQueries) > 0 {
			q = strings.Join(body.SearchQueries, " ")
		}
		resp, err := h.webSearcher.Search(c, q, body.MaxResults)
		if err != nil {
			return c.Status(http.StatusBadGateway).JSON(map[string]any{
				"error": map[string]any{"message": err.Error(), "type": "search_upstream_error"},
			})
		}
		return c.JSON(resp)
	}
	mode := body.Mode
	if mode == "" {
		mode = h.parallelMode
	}
	req := parallel.SearchRequest{
		Objective:     body.Objective,
		SearchQueries: body.SearchQueries,
		Mode:          mode,
		MaxCharsTotal: body.MaxCharsTotal,
		ClientModel:   body.ClientModel,
		SessionID:     body.SessionID,
	}
	if body.MaxResults > 0 || body.Location != "" || len(body.ExcludeDomains) > 0 {
		req.AdvancedSettings = &parallel.AdvancedSettings{
			Location:     body.Location,
			MaxResults:   body.MaxResults,
			SourcePolicy: &parallel.SourcePolicy{ExcludeDomains: body.ExcludeDomains},
		}
	}
	resp, err := h.parallel.Search(c, req)
	if err != nil {
		return parallelUpstreamError(c, err)
	}
	return c.JSON(resp)
}

// ParallelExtract proxies URL fetching through the Parallel Extract API.
// Body: {"urls": [..], "objective": str} (up to 20 URLs per call).
func (h *handlers) ParallelExtract(c fiber.Ctx) error {
	// Extract has no keyless fallback (DDG backend is search-only).
	if h.parallel == nil || !h.parallel.Enabled() {
		return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{
			"error": map[string]any{"message": "Parallel API key not configured (PARALLEL_API_KEY)", "type": "parallel_not_configured"},
		})
	}
	var body struct {
		URLs      []string `json:"urls"`
		Objective string   `json:"objective"`
		SessionID string   `json:"session_id"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return c.Status(http.StatusBadRequest).JSON(map[string]any{
			"error": map[string]any{"message": "invalid JSON body", "type": "invalid_request_error"},
		})
	}
	if len(body.URLs) == 0 {
		return c.Status(http.StatusBadRequest).JSON(map[string]any{
			"error": map[string]any{"message": "urls required (up to 20 per call)", "type": "invalid_request_error"},
		})
	}
	resp, err := h.parallel.Extract(c, parallel.ExtractRequest{
		URLs:      body.URLs,
		Objective: body.Objective,
		SessionID: body.SessionID,
	})
	if err != nil {
		return parallelUpstreamError(c, err)
	}
	return c.JSON(resp)
}

// parallelUpstreamError maps Parallel client errors to HTTP responses.
func parallelUpstreamError(c fiber.Ctx, err error) error {
	var apiErr *parallel.APIError
	if errors.As(err, &apiErr) {
		status := apiErr.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		return c.Status(status).JSON(map[string]any{
			"error": map[string]any{
				"message":         fmt.Sprintf("Parallel API error: %s", apiErr.Body),
				"type":            "parallel_upstream_error",
				"upstream_status": apiErr.StatusCode,
			},
		})
	}
	if errors.Is(err, parallel.ErrNotConfigured) {
		return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "parallel_not_configured"},
		})
	}
	return c.Status(http.StatusBadGateway).JSON(map[string]any{
		"error": map[string]any{"message": err.Error(), "type": "parallel_upstream_error"},
	})
}

// StealthStatus reports stealth-transport observability: sidecar latency,
// fallback counts, egress IPs used, and the proxy-pool auto-refresh state.
func (h *handlers) StealthStatus(c fiber.Ctx) error {
	if h.stealth == nil && h.refresher == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{
			"status": "disabled",
			"hint":   "enable stealth.enabled and stealth.auto_refresh_pool in config.yaml",
		})
	}
	payload := map[string]any{
		"status": "ok",
	}
	if h.stealth != nil {
		payload["metrics"] = h.stealth.Snapshot()
	}
	if h.refresher != nil {
		payload["pool_refresher"] = h.refresher()
	}
	return c.Status(http.StatusOK).JSON(payload)
}

// HermesFetch proxies a stealth request through the hermes sidecar.
// Body: {"url": ..., "method": ..., "headers": {...}, "payload": ...,
//
//	"proxy": "host:port", "timeout_ms": 15000, "http2": false, "json": true}
func (h *handlers) HermesFetch(c fiber.Ctx) error {
	if h.hermes == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{
			"error": map[string]any{
				"message": "hermes sidecar is not enabled",
				"type":    "hermes_disabled",
			},
		})
	}
	var req hermes.Request
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "Request body must be valid JSON")
	}
	if req.URL == "" {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "url field is required")
	}
	resp, err := h.hermes.Fetch(c, req)
	if err != nil {
		return c.Status(http.StatusBadGateway).JSON(map[string]any{
			"error": map[string]any{
				"message": err.Error(),
				"type":    "hermes_upstream_error",
			},
		})
	}
	return c.Status(http.StatusOK).JSON(resp)
}

// HermesSessionDelete drops a server-side cookie session.
func (h *handlers) HermesSessionDelete(c fiber.Ctx) error {
	if h.hermes == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{
			"error": map[string]any{
				"message": "hermes sidecar is not enabled",
				"type":    "hermes_disabled",
			},
		})
	}
	sessionID := c.Params("id")
	if sessionID == "" {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "session id is required")
	}
	if err := h.hermes.DropSession(c, sessionID); err != nil {
		return c.Status(http.StatusBadGateway).JSON(map[string]any{
			"error": map[string]any{
				"message": err.Error(),
				"type":    "hermes_upstream_error",
			},
		})
	}
	return c.Status(http.StatusOK).JSON(map[string]any{"deleted": true})
}

// HermesSessionFetch proxies a session-scoped stealth request (cookie jar).
func (h *handlers) HermesSessionFetch(c fiber.Ctx) error {
	if h.hermes == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{
			"error": map[string]any{
				"message": "hermes sidecar is not enabled",
				"type":    "hermes_disabled",
			},
		})
	}
	sessionID := c.Params("id")
	if sessionID == "" {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "session id is required")
	}
	var req hermes.Request
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "Request body must be valid JSON")
	}
	if req.URL == "" {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "url field is required")
	}
	resp, err := h.hermes.FetchWithSession(c, sessionID, req)
	if err != nil {
		return c.Status(http.StatusBadGateway).JSON(map[string]any{
			"error": map[string]any{
				"message": err.Error(),
				"type":    "hermes_upstream_error",
			},
		})
	}
	return c.Status(http.StatusOK).JSON(resp)
}

func (h *handlers) Health(c fiber.Ctx) error {
	body := map[string]any{"status": "ok"}
	if h.extraHealth != nil {
		for k, v := range h.extraHealth() {
			if _, exists := body[k]; !exists {
				body[k] = v
			}
		}
	}
	if h.tokenPool != nil {
		body["token_pool"] = h.tokenPool.Stats()
	}
	if h.proxyPool != nil {
		body["proxy_pool"] = h.proxyPool.Stats()
	}
	return c.Status(http.StatusOK).JSON(body)
}

// AIStackStatus serves the full ai-stack infrastructure report.
func (h *handlers) AIStackStatus(c fiber.Ctx) error {
	if h.aiStack != nil {
		return c.Status(http.StatusOK).JSON(h.aiStack())
	}
	return h.Health(c)
}

func (h *handlers) Models(c fiber.Ctx) error {
	return c.Status(http.StatusOK).JSON(openai.Models(h.model))
}

func (h *handlers) ChatCompletions(c fiber.Ctx) error {
	if !hasJSONBody(c.Get(fiber.HeaderContentType)) {
		return writeOpenAIError(c, http.StatusUnsupportedMediaType, "invalid_request_error", "Content-Type must be application/json")
	}

	var req openai.ChatCompletionRequest
	decoder := json.NewDecoder(bytes.NewReader(c.Body()))
	if err := decoder.Decode(&req); err != nil {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "Request body must be valid JSON")
	}
	if err := ensureSingleJSONValue(decoder); err != nil {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "Request body must be a single JSON object")
	}
	if req.Model == "" {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "model field is required")
	}
	if len(req.Messages) == 0 {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "messages field must contain at least one message")
	}

	if req.Stream {
		return h.streamChatCompletions(c, req)
	}

	// Non-stream is legacy buffered path: it holds the connection with zero
	// bytes until the full LLM response is ready, which triggers 5m infra
	// idle aborts ("no data for 5 minutes"). Keep the connection alive with
	// a 15s heartbeat (mirrors stream path) and hint clients to use stream.
	c.Set("X-Accel-Buffering", "no")
	c.Set("X-Freebuff-NonStream", "deprecated — use stream:true to avoid 5m timeout")
	// Run Complete in background and heartbeat until it finishes. This prevents
	// LBs/CF from killing an otherwise healthy long generation.
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	// Hoisted: the background completion and the heartbeat streamer below
	// outlive the handler, and Fiber recycles c on return. chatCtx keeps
	// disconnect propagation (parented on the request ctx); clientGone
	// replaces post-return c.Done() reads.
	chatCtx, chatCancel := context.WithCancel(c.Context())
	clientGone := c.Done()
	go func() {
		t, e := h.chat.Complete(chatCtx, req)
		done <- result{t, e}
	}()

	heartbeat := time.NewTicker(streamHeartbeatInterval)
	defer heartbeat.Stop()

	// Use non-buffered JSON write with periodic keep-alive via hijacked writer.
	// Fiber's SendStreamWriter lets us flush ": keep-alive" comments without
	// breaking the final JSON payload for clients that ignore leading whitespace.
	// Instead, for pure JSON clients, we just ensure headers are flushed early
	// and use a lightweight ticker that writes to the underlying connection
	// if available — fallback is to simply wait (WriteTimeout 310s handles it).
	select {
	case r := <-done:
		chatCancel()
		if r.err != nil {
			return writeServiceError(c, r.err)
		}
		return c.Status(http.StatusOK).JSON(openai.CompletionFromText(req.Model, r.text))
	case <-heartbeat.C:
		// On first tick, switch to chunked whitespace heartbeat so LBs see bytes
		// but JSON stays valid (leading whitespace is ignored per RFC 7159).
		// Rare path: only when generation >15s.
		c.Set("Content-Type", "application/json")
		return c.SendStreamWriter(func(w *bufio.Writer) {
			defer chatCancel()
			ticker := time.NewTicker(streamHeartbeatInterval)
			defer ticker.Stop()
			// Send initial whitespace chunk so headers + first byte are flushed.
			_, _ = w.Write([]byte(" "))
			_ = w.Flush()
			for {
				select {
				case r := <-done:
					if r.err != nil {
						// Flush any leading whitespace already sent, then error JSON.
						// Client will see " {\"error\":...}" which is valid JSON with leading space.
						_ = writeSSE(w, openai.Error(serviceErrorStatus(r.err), serviceErrorCode(r.err), serviceErrorMessage(r.err)))
						_ = w.Flush()
						return
					}
					payload := openai.CompletionFromText(req.Model, r.text)
					data, _ := json.Marshal(payload)
					_, _ = w.Write(data)
					_ = w.Flush()
					return
				case <-ticker.C:
					// Whitespace heartbeat — keep LB/CF from 5m idle kill, no JSON breakage.
					if _, err := w.Write([]byte(" ")); err != nil {
						return
					}
					if err := w.Flush(); err != nil {
						return
					}
				case <-clientGone:
					return
				}
			}
		})
	case <-c.Done():
		chatCancel()
		return writeServiceError(c, &ServiceError{Status: http.StatusRequestTimeout, Code: "request_cancelled", Message: "client disconnected"})
	}
}

func (h *handlers) AnthropicMessages(c fiber.Ctx) error {
	if !hasJSONBody(c.Get(fiber.HeaderContentType)) {
		return writeAnthropicError(c, http.StatusUnsupportedMediaType, "invalid_request_error", "Content-Type must be application/json")
	}

	var req anthropic.MessageRequest
	decoder := json.NewDecoder(bytes.NewReader(c.Body()))
	if err := decoder.Decode(&req); err != nil {
		return writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Request body must be valid JSON")
	}
	if err := ensureSingleJSONValue(decoder); err != nil {
		return writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Request body must be a single JSON object")
	}
	if req.Model == "" {
		return writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "model field is required")
	}
	if len(req.Messages) == 0 {
		return writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "messages field must contain at least one message")
	}

	upstreamReq, err := anthropic.ToOpenAI(req)
	if err != nil {
		return writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	inputTokens := anthropic.CountTokens(req)
	if req.Stream {
		return h.streamAnthropicMessages(c, req, upstreamReq, inputTokens)
	}

	text, err := h.chat.Complete(c, upstreamReq)
	if err != nil {
		return writeAnthropicServiceError(c, err)
	}

	return c.Status(http.StatusOK).JSON(anthropic.MessageFromText(req.Model, text, inputTokens))
}

func (h *handlers) AnthropicCountTokens(c fiber.Ctx) error {
	if !hasJSONBody(c.Get(fiber.HeaderContentType)) {
		return writeAnthropicError(c, http.StatusUnsupportedMediaType, "invalid_request_error", "Content-Type must be application/json")
	}

	var req anthropic.MessageRequest
	decoder := json.NewDecoder(bytes.NewReader(c.Body()))
	if err := decoder.Decode(&req); err != nil {
		return writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Request body must be valid JSON")
	}
	if err := ensureSingleJSONValue(decoder); err != nil {
		return writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Request body must be a single JSON object")
	}

	return c.Status(http.StatusOK).JSON(anthropic.CountTokensResponse{InputTokens: anthropic.CountTokens(req)})
}

func (h *handlers) streamChatCompletions(c fiber.Ctx, req openai.ChatCompletionRequest) error {
	streamCtx, cancel := context.WithCancel(c)
	deltas, errs := h.chat.Stream(streamCtx, req)
	if err, ok, closed := receiveImmediateError(errs); ok {
		cancel()
		return writeServiceError(c, err)
	} else if closed {
		errs = nil
	}

	c.Set(fiber.HeaderContentType, "text/event-stream")
	c.Set(fiber.HeaderCacheControl, "no-cache")
	c.Set(fiber.HeaderConnection, "keep-alive")

	metadata := openai.NewStreamMetadata()
	return c.SendStreamWriter(func(w *bufio.Writer) {
		defer cancel()

		heartbeat := time.NewTicker(streamHeartbeatInterval)
		defer heartbeat.Stop()

		for deltas != nil || errs != nil {
			select {
			case <-streamCtx.Done():
				return
			case <-heartbeat.C:
				if !writeStreamComment(w) {
					return
				}
			case delta, ok := <-deltas:
				if !ok {
					deltas = nil
					continue
				}
				chunk := openai.ChunkFromDeltaWithMetadata(req.Model, delta, &metadata)
				if !writeStreamData(w, chunk) {
					return
				}
			case err, ok := <-errs:
				if !ok {
					errs = nil
					continue
				}
				if err == nil {
					continue
				}
				_ = writeSSE(w, openai.Error(http.StatusServiceUnavailable, serviceErrorCode(err), serviceErrorMessage(err)))
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
				_ = w.Flush()
				return
			}
		}

		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		_ = w.Flush()
	})
}

func (h *handlers) streamAnthropicMessages(c fiber.Ctx, req anthropic.MessageRequest, upstreamReq openai.ChatCompletionRequest, inputTokens int) error {
	streamCtx, cancel := context.WithCancel(c)
	deltas, errs := h.chat.Stream(streamCtx, upstreamReq)
	if err, ok, closed := receiveImmediateError(errs); ok {
		cancel()
		return writeAnthropicServiceError(c, err)
	} else if closed {
		errs = nil
	}

	c.Set(fiber.HeaderContentType, "text/event-stream")
	c.Set(fiber.HeaderCacheControl, "no-cache")
	c.Set(fiber.HeaderConnection, "keep-alive")

	return c.SendStreamWriter(func(w *bufio.Writer) {
		defer cancel()

		if !writeAnthropicEvent(w, "message_start", anthropic.StreamStartMessage(req.Model, inputTokens)) {
			return
		}
		if !writeAnthropicEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": anthropic.ContentBlock{Type: "text", Text: ""}}) {
			return
		}

		outputTokens := 0
		heartbeat := time.NewTicker(streamHeartbeatInterval)
		defer heartbeat.Stop()

		for deltas != nil || errs != nil {
			select {
			case <-streamCtx.Done():
				return
			case <-heartbeat.C:
				if !writeStreamComment(w) {
					return
				}
			case delta, ok := <-deltas:
				if !ok {
					deltas = nil
					continue
				}
				outputTokens += 1
				if !writeAnthropicEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": delta}}) {
					return
				}
			case err, ok := <-errs:
				if !ok {
					errs = nil
					continue
				}
				if err == nil {
					continue
				}
				_ = writeNamedSSE(w, "error", anthropic.Error(serviceErrorCode(err), serviceErrorMessage(err)))
				_ = w.Flush()
				return
			}
		}

		stopReason := "end_turn"
		if !writeAnthropicEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}) {
			return
		}
		if !writeAnthropicEvent(w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil}, "usage": anthropic.Usage{OutputTokens: outputTokens}}) {
			return
		}
		_ = writeNamedSSE(w, "message_stop", map[string]string{"type": "message_stop"})
		_ = w.Flush()
	})
}

func (notConfiguredChatService) Complete(ctx context.Context, req openai.ChatCompletionRequest) (string, error) {
	_ = ctx
	_ = req
	return "", &ServiceError{
		Status:  http.StatusServiceUnavailable,
		Code:    "service_unavailable",
		Message: "Chat service not yet configured",
	}
}

func (notConfiguredChatService) Stream(ctx context.Context, req openai.ChatCompletionRequest) (<-chan string, <-chan error) {
	_ = ctx
	_ = req
	deltas := make(chan string)
	close(deltas)
	errs := make(chan error, 1)
	errs <- &ServiceError{
		Status:  http.StatusServiceUnavailable,
		Code:    "service_unavailable",
		Message: "Chat service not yet configured",
	}
	close(errs)
	return deltas, errs
}

func hasJSONBody(contentType string) bool {
	if contentType == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "application/json"
}

func ensureSingleJSONValue(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("unexpected extra JSON value")
}

func receiveImmediateError(errs <-chan error) (error, bool, bool) {
	if errs == nil {
		return nil, false, true
	}
	select {
	case err, ok := <-errs:
		if !ok {
			return nil, false, true
		}
		if err == nil {
			return nil, false, false
		}
		return err, true, false
	default:
		return nil, false, false
	}
}

func writeOpenAIError(c fiber.Ctx, status int, code string, message string) error {
	return c.Status(status).JSON(openai.Error(status, code, message))
}

func writeServiceError(c fiber.Ctx, err error) error {
	status := serviceErrorStatus(err)
	return writeOpenAIError(c, status, serviceErrorCode(err), serviceErrorMessage(err))
}

func writeAnthropicServiceError(c fiber.Ctx, err error) error {
	return writeAnthropicError(c, serviceErrorStatus(err), serviceErrorCode(err), serviceErrorMessage(err))
}

func writeAnthropicError(c fiber.Ctx, status int, code string, message string) error {
	return c.Status(status).JSON(anthropic.Error(code, message))
}

func serviceErrorStatus(err error) int {
	var serviceErr *ServiceError
	if errors.As(err, &serviceErr) && serviceErr.Status > 0 {
		return serviceErr.Status
	}
	return http.StatusServiceUnavailable
}

func serviceErrorCode(err error) string {
	var serviceErr *ServiceError
	if errors.As(err, &serviceErr) && serviceErr.Code != "" {
		return serviceErr.Code
	}
	return "service_unavailable"
}

func serviceErrorMessage(err error) string {
	if err == nil {
		return http.StatusText(http.StatusServiceUnavailable)
	}
	return err.Error()
}

func writeStreamData(w *bufio.Writer, payload any) bool {
	if err := writeSSE(w, payload); err != nil {
		return false
	}
	return w.Flush() == nil
}

func writeStreamComment(w *bufio.Writer) bool {
	if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
		return false
	}
	return w.Flush() == nil
}

func writeSSE(w io.Writer, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

func writeAnthropicEvent(w *bufio.Writer, event string, payload any) bool {
	if err := writeNamedSSE(w, event, payload); err != nil {
		return false
	}
	return w.Flush() == nil
}

func writeNamedSSE(w io.Writer, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	return err
}
