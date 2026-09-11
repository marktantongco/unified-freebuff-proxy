package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"freebuff-unified/internal/openai"
	"freebuff-unified/internal/parallel"

	"github.com/gofiber/fiber/v3"
)

// ResearchConfig configures the native /v1/deep-research harness (Layer B).
// The gateway LLM plans sub-queries, they fan out to the Parallel Search API
// (fast mode) and/or the keyless DDG stealth searcher, the top pages are read,
// and the LLM synthesizes a cited markdown report.
type ResearchConfig struct {
	Enabled         bool
	Model           string
	MaxQueries      int
	FanOut          int
	ExtractPerQuery int
	Timeout         time.Duration
}

// maxEvidence bounds how many deduped sources feed the synthesis prompt.
const maxEvidence = 12

// researchEvent is one SSE frame emitted during a streaming deep-research run.
type researchEvent struct {
	Type string `json:"type"`
}

// researchPlan is the strict-JSON output of the planner LLM call.
type researchPlan struct {
	Queries []string `json:"queries"`
	Outline []string `json:"outline"`
}

// reference is a public, viewer-friendly source entry in the report.
type reference struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Query   string `json:"query"`
	Snippet string `json:"snippet"`
}

// evidence is an internal source with extracted content for the synthesis.
type evidence struct {
	reference
	Content string `json:"-"`
}

// researchMetrics summarizes a run for the client.
type researchMetrics struct {
	ElapsedMS      int64  `json:"elapsed_ms"`
	Queries        int    `json:"queries"`
	Sources        int    `json:"sources"`
	SearchesRun    int    `json:"searches_run"`
	ExtractsRun    int    `json:"extracts_run"`
	SearchBackend  string `json:"search_backend"`
	ExtractBackend string `json:"extract_backend"`
	PlannerModel   string `json:"planner_model"`
	SynthModel     string `json:"synth_model"`
}

// researchReport is the synchronous /v1/deep-research response payload.
type researchReport struct {
	Report     string          `json:"report"`
	Queries    []string        `json:"queries"`
	Outline    []string        `json:"outline"`
	References []reference     `json:"references"`
	Metrics    researchMetrics `json:"metrics"`
}

// researchOpts is the resolved per-request harness configuration.
type researchOpts struct {
	model           string
	input           string
	maxQueries      int
	fanOut          int
	extractPerQuery int
	stream          bool
	emit            func(any) error // frame sink; nil in buffered mode
}

// DeepResearch is the two-layer /v1/deep-research orchestrator.
//
// Layer A runs first whenever a Parallel client is configured (the API key is
// optional — keyless requests are attempted and this is the "free/no-key"
// path). It creates a Parallel Task run and, with stream:true, relays its SSE
// events inline. On any keyless/auth/upstream failure it falls through to
// Layer B, the native gateway harness (plan → fan-out → synthesize).
//
// Body: {"input", "stream", "model"?, "max_queries"?, "fan_out"?,
// "extract_per_query"?, "processor"?, "enable_events"?, "webhook_url"?,
// "output_schema"?}.
func (h *handlers) DeepResearch(c fiber.Ctx) error {
	if !hasJSONBody(c.Get(fiber.HeaderContentType)) {
		return writeOpenAIError(c, http.StatusUnsupportedMediaType, "invalid_request_error", "Content-Type must be application/json")
	}

	var body struct {
		Input           string          `json:"input"`
		Stream          bool            `json:"stream"`
		Model           string          `json:"model"`
		MaxQueries      int             `json:"max_queries"`
		FanOut          int             `json:"fan_out"`
		ExtractPerQuery int             `json:"extract_per_query"`
		Processor       string          `json:"processor"`
		EnableEvents    bool            `json:"enable_events"`
		WebhookURL      string          `json:"webhook_url"`
		OutputSchema    json.RawMessage `json:"output_schema"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "Request body must be valid JSON")
	}
	input := strings.TrimSpace(body.Input)
	if input == "" {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "input field is required")
	}

	// Layer A: Parallel Task runs (keyless-friendly). Only when the create
	// fails do we fall through to the native Layer B harness.
	if h.parallel != nil && h.parallel.Configured() {
		handled, err := h.parallelTaskResearch(c, body)
		if err == nil {
			return handled
		}
	}

	// Layer B: native gateway harness.
	if !h.research.Enabled {
		return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{
			"error": map[string]any{"message": "deep-research is not enabled (set research.enabled in config)", "type": "research_disabled"},
		})
	}
	if _, stub := h.chat.(notConfiguredChatService); stub {
		return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{
			"error": map[string]any{"message": "chat service not configured", "type": "research_unavailable"},
		})
	}
	if !h.searchBackendConfigured() {
		return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{
			"error": map[string]any{"message": "no search backend configured (set PARALLEL_API_KEY or enable hermes sidecar)", "type": "research_unavailable"},
		})
	}

	model := body.Model
	if model == "" {
		model = h.research.Model
	}
	if model == "" {
		model = h.model
	}
	opts := researchOpts{
		model:           model,
		input:           input,
		maxQueries:      clampInt(body.MaxQueries, h.research.MaxQueries, 1, 10),
		fanOut:          clampInt(body.FanOut, h.research.FanOut, 1, 32),
		extractPerQuery: clampInt(body.ExtractPerQuery, h.research.ExtractPerQuery, 1, 5),
		stream:          body.Stream,
	}

	if opts.stream {
		return h.streamResearch(c, opts)
	}
	return h.bufferedResearch(c, opts)
}

// parallelTaskResearch creates a Parallel Task run (Layer A). On success it
// writes the client response and returns (nil-handler-nil, nil). On failure it
// returns (nil-handler, err) so the caller falls back to the native harness.
func (h *handlers) parallelTaskResearch(c fiber.Ctx, body struct {
	Input           string          `json:"input"`
	Stream          bool            `json:"stream"`
	Model           string          `json:"model"`
	MaxQueries      int             `json:"max_queries"`
	FanOut          int             `json:"fan_out"`
	ExtractPerQuery int             `json:"extract_per_query"`
	Processor       string          `json:"processor"`
	EnableEvents    bool            `json:"enable_events"`
	WebhookURL      string          `json:"webhook_url"`
	OutputSchema    json.RawMessage `json:"output_schema"`
}) (error, error) {
	processor := strings.TrimSpace(body.Processor)
	if processor == "" {
		processor = h.parallelProcessor
	}
	if processor == "" {
		processor = "pro-fast"
	}
	schema := body.OutputSchema
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"auto"}`)
	}
	req := parallel.TaskRunRequest{
		Input:        body.Input,
		Processor:    processor,
		EnableEvents: body.Stream || body.EnableEvents,
		OutputSchema: schema,
	}
	if strings.TrimSpace(body.WebhookURL) != "" {
		req.Webhook = &parallel.TaskWebhook{URL: strings.TrimSpace(body.WebhookURL)}
	}

	status, err := h.parallel.CreateTaskRun(c.Context(), req)
	if err != nil {
		return nil, err
	}
	runID := status.RunID

	if body.Stream {
		return h.relayTaskEvents(c, runID), nil
	}
	return c.Status(http.StatusAccepted).JSON(map[string]any{
		"run_id":         runID,
		"interaction_id": status.InteractionID,
		"status":         firstNonEmpty(status.Status, "queued"),
		"processor":      processor,
		"backend":        "parallel",
		"result_url":     "/v1/deep-research/" + runID,
		"events_url":     "/v1/deep-research/" + runID + "/events",
	}), nil
}

// DeepResearchGet polls a Layer A run's status (and its result once complete).
func (h *handlers) DeepResearchGet(c fiber.Ctx) error {
	if h.parallel == nil || !h.parallel.Configured() {
		return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{
			"error": map[string]any{"message": "parallel task run backend not configured", "type": "research_unavailable"},
		})
	}
	runID := strings.TrimSpace(c.Params("id"))
	if runID == "" {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "run id is required")
	}
	status, err := h.parallel.GetTaskRun(c, runID)
	if err != nil {
		if strings.Contains(err.Error(), "http 404") {
			return c.Status(http.StatusNotFound).JSON(map[string]any{
				"error": map[string]any{"message": fmt.Sprintf("run %s not found", runID), "type": "not_found"},
			})
		}
		return writeServiceError(c, err)
	}
	out := map[string]any{
		"run_id":         status.RunID,
		"interaction_id": status.InteractionID,
		"status":         status.Status,
		"processor":      status.Processor,
		"backend":        "parallel",
		"created_at":     status.CreatedAt,
		"completed_at":   status.CompletedAt,
	}
	if status.Error != nil {
		out["error"] = status.Error
	}
	if status.Status == "completed" {
		if res, rerr := h.parallel.GetTaskRunResult(c, runID); rerr == nil && res != nil {
			out["result"] = res
		}
	}
	return c.JSON(out)
}

// DeepResearchEvents relays a Layer A run's SSE event stream live.
func (h *handlers) DeepResearchEvents(c fiber.Ctx) error {
	if h.parallel == nil || !h.parallel.Configured() {
		return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{
			"error": map[string]any{"message": "parallel task run backend not configured", "type": "research_unavailable"},
		})
	}
	runID := strings.TrimSpace(c.Params("id"))
	if runID == "" {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "run id is required")
	}
	return h.relayTaskEvents(c, runID)
}

// relayTaskEvents pipes a run's upstream SSE to the client with a 15s
// keep-alive, then appends a {"type":"done"} frame carrying the final result.
func (h *handlers) relayTaskEvents(c fiber.Ctx, runID string) error {
	c.Set(fiber.HeaderContentType, "text/event-stream")
	c.Set(fiber.HeaderCacheControl, "no-cache")
	c.Set(fiber.HeaderConnection, "keep-alive")

	return c.SendStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := context.WithCancel(c.Context())
		defer cancel()

		resp, err := h.parallel.TaskEvents(ctx, runID)
		if err != nil {
			_ = writeSSE(w, map[string]any{"type": "error", "message": err.Error()})
			_ = w.Flush()
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			_ = writeSSE(w, map[string]any{"type": "error", "message": fmt.Sprintf("events http %d: %s", resp.StatusCode, truncate(string(b), 400))})
			_ = w.Flush()
			return
		}

		frames := make(chan []byte, 64)
		go func() {
			defer close(frames)
			emit := func(payload any) error {
				buf := &strings.Builder{}
				if err := writeSSE(buf, payload); err != nil {
					return err
				}
				select {
				case frames <- []byte(buf.String()):
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			sc := bufio.NewScanner(resp.Body)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			var chunk strings.Builder
			flush := func() bool {
				if chunk.Len() == 0 {
					return true
				}
				blk := chunk.String()
				chunk.Reset()
				select {
				case frames <- []byte(blk + "\n\n"):
					return true
				case <-ctx.Done():
					return false
				}
			}
			for sc.Scan() {
				ln := sc.Text()
				if ln == "" {
					if !flush() {
						return
					}
					continue
				}
				chunk.WriteString(ln)
				chunk.WriteByte('\n')
			}
			flush()
			res, rerr := h.parallel.GetTaskRunResult(ctx, runID)
			if rerr != nil || res == nil {
				_ = emit(map[string]any{"type": "error", "message": "run finished without a result"})
				return
			}
			_ = emit(map[string]any{
				"type":   "done",
				"run_id": res.RunID,
				"status": res.Status,
				"result": res.Output,
			})
		}()

		heartbeat := time.NewTicker(streamHeartbeatInterval)
		defer heartbeat.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case f, ok := <-frames:
				if !ok {
					return
				}
				if _, err := w.Write(f); err != nil {
					return
				}
				if err := w.Flush(); err != nil {
					return
				}
			case <-heartbeat.C:
				if !writeStreamComment(w) {
					return
				}
			}
		}
	})
}

// ResponsesPassthrough opens the /v1/responses-compatible surface. When a
// Parallel client is configured it relays to the Parallel Responses API
// (keyless-first: the key header is only attached when present, and auth
// failures fall back to the native gateway chat). Without a Parallel client it
// serves the native chat service in Responses shape.
func (h *handlers) ResponsesPassthrough(c fiber.Ctx) error {
	if !hasJSONBody(c.Get(fiber.HeaderContentType)) {
		return writeOpenAIError(c, http.StatusUnsupportedMediaType, "invalid_request_error", "Content-Type must be application/json")
	}
	body := c.Body()
	if h.parallel != nil && h.parallel.Configured() {
		resp, err := h.parallel.Responses(c.Context(), body)
		if err == nil && resp != nil {
			status := resp.StatusCode
			bad := status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusPaymentRequired
			if !bad {
				// relayResponses owns resp.Body for the duration of streaming.
				return h.relayResponses(c, resp)
			}
			resp.Body.Close()
		}
	}
	return h.responsesNative(c)
}

// relayResponses copies a Parallel /v1/responses reply (stream or not) to the
// client verbatim, keep-aliving long SSE streams.
func (h *handlers) relayResponses(c fiber.Ctx, resp *http.Response) error {
	c.Status(resp.StatusCode)
	for k, vv := range resp.Header {
		if k == "Content-Length" || k == "Transfer-Encoding" {
			continue
		}
		for _, v := range vv {
			c.Append(k, v)
		}
	}
	c.Set(fiber.HeaderContentType, firstNonEmpty(resp.Header.Get("Content-Type"), "application/json"))

	return c.SendStreamWriter(func(w *bufio.Writer) {
		defer resp.Body.Close()
		ctx, cancel := context.WithCancel(c.Context())
		defer cancel()

		chunks := make(chan []byte, 16)
		go func() {
			defer close(chunks)
			buf := make([]byte, 32*1024)
			for {
				n, err := resp.Body.Read(buf)
				if n > 0 {
					cp := make([]byte, n)
					copy(cp, buf[:n])
					select {
					case chunks <- cp:
					case <-ctx.Done():
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()

		heartbeat := time.NewTicker(streamHeartbeatInterval)
		defer heartbeat.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
				if !writeStreamComment(w) {
					return
				}
			case ch, ok := <-chunks:
				if !ok {
					return
				}
				if _, err := w.Write(ch); err != nil {
					return
				}
				if err := w.Flush(); err != nil {
					return
				}
			}
		}
	})
}

// responsesNative serves Responses-shape requests through the gateway chat
// service when no Parallel backend is available or it rejected a keyless call.
func (h *handlers) responsesNative(c fiber.Ctx) error {
	if _, stub := h.chat.(notConfiguredChatService); stub {
		return writeOpenAIError(c, http.StatusServiceUnavailable, "responses_unavailable", "no chat service configured (parallel backend unavailable)")
	}
	var rb struct {
		Model  string          `json:"model"`
		Stream bool            `json:"stream"`
		Input  json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(c.Body(), &rb); err != nil {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "invalid JSON body")
	}
	messages, err := responsesInputToMessages(rb.Input)
	if err != nil {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	if len(messages) == 0 {
		return writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "input is required")
	}
	model := rb.Model
	if model == "" {
		model = h.model
	}
	req := openai.ChatCompletionRequest{Model: model, Messages: messages, Stream: rb.Stream}
	if rb.Stream {
		return h.responsesNativeStream(c, req)
	}

	ctx, cancel := context.WithTimeout(c, 5*time.Minute)
	defer cancel()
	text, err := h.chat.Complete(ctx, req)
	if err != nil {
		return writeServiceError(c, err)
	}
	return c.JSON(map[string]any{
		"id":            "resp_native",
		"object":        "response",
		"status":        "completed",
		"status_detail": "completed",
		"created_at":    time.Now().Unix(),
		"model":         req.Model,
		"output": []map[string]any{{
			"id":      "msg_native",
			"type":    "message",
			"role":    "assistant",
			"content": []map[string]any{{"type": "output_text", "text": text, "annotations": []any{}}},
		}},
	})
}

// responsesNativeStream streams the gateway chat output in Responses SSE shape.
func (h *handlers) responsesNativeStream(c fiber.Ctx, req openai.ChatCompletionRequest) error {
	c.Set(fiber.HeaderContentType, "text/event-stream")
	c.Set(fiber.HeaderCacheControl, "no-cache")
	c.Set(fiber.HeaderConnection, "keep-alive")

	return c.SendStreamWriter(func(w *bufio.Writer) {
		// ctx is created inside the callback so streaming is not canceled when
		// the handler returns (SendStreamWriter returns promptly).
		ctx, cancel := context.WithTimeout(c, 5*time.Minute)
		defer cancel()

		deltas, errs := h.chat.Stream(ctx, req)
		heartbeat := time.NewTicker(streamHeartbeatInterval)
		defer heartbeat.Stop()
		for deltas != nil {
			select {
			case d, ok := <-deltas:
				if !ok {
					deltas = nil
					continue
				}
				if !writeStreamData(w, map[string]any{
					"type":          "response.output_text.delta",
					"item_id":       "msg_native",
					"output_index":  0,
					"content_index": 0,
					"delta":         d,
				}) {
					return
				}
			case err, ok := <-errs:
				if !ok {
					errs = nil
					continue
				}
				if err != nil {
					_ = writeSSE(w, map[string]any{"type": "error", "message": err.Error()})
					_ = w.Flush()
					return
				}
			case <-heartbeat.C:
				if !writeStreamComment(w) {
					return
				}
			case <-c.Done():
				return
			}
		}
		if _, err := w.WriteString("data: [DONE]\n\n"); err != nil {
			return
		}
		_ = w.Flush()
	})
}

// responsesInputToMessages maps the Responses "input" field (string or message
// array) onto governance chat messages for the native fallback.
func responsesInputToMessages(raw json.RawMessage) ([]openai.ChatMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.TrimSpace(s) == "" {
			return nil, nil
		}
		return []openai.ChatMessage{{Role: "user", Content: s}}, nil
	}
	var arr []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("input must be a string or an array of {role, content} messages")
	}
	out := make([]openai.ChatMessage, 0, len(arr))
	for _, m := range arr {
		role := m.Role
		if role == "" {
			role = "user"
		}
		content := m.Content
		switch t := content.(type) {
		case string:
		case []any:
			var parts []string
			for _, p := range t {
				if pm, ok := p.(map[string]any); ok {
					if ts, ok := pm["text"].(string); ok && strings.TrimSpace(ts) != "" {
						parts = append(parts, ts)
					}
				}
			}
			content = strings.Join(parts, "\n")
		default:
			content = ""
		}
		out = append(out, openai.ChatMessage{Role: role, Content: content})
	}
	return out, nil
}

// bufferedResearch runs the harness in the background and returns the JSON
// report, heartbeating with whitespace so long runs survive LB/CF 5m idle
// aborts (misror of the legacy non-stream ChatCompletions path).
func (h *handlers) bufferedResearch(c fiber.Ctx, opts researchOpts) error {
	c.Set("X-Accel-Buffering", "no")
	c.Set("X-Freebuff-NonStream", "use stream:true for live progress")

	type result struct {
		rep *researchReport
		err error
	}
	done := make(chan result, 1)
	go func() {
		// ctx lives inside the goroutine so it is not canceled when the handler
		// returns (defer cancel at handler scope would kill runs >15s that have
		// switched to the heartbeat/streaming writer).
		ctx, cancel := context.WithTimeout(c, h.research.timeout())
		defer cancel()
		rep, err := h.runResearch(ctx, opts)
		done <- result{rep, err}
	}()

	heartbeat := time.NewTicker(streamHeartbeatInterval)
	defer heartbeat.Stop()

	select {
	case r := <-done:
		if r.err != nil {
			return writeServiceError(c, r.err)
		}
		return c.Status(http.StatusOK).JSON(r.rep)
	case <-heartbeat.C:
		c.Set("Content-Type", "application/json")
		return c.SendStreamWriter(func(w *bufio.Writer) {
			ticker := time.NewTicker(streamHeartbeatInterval)
			defer ticker.Stop()
			_, _ = w.Write([]byte(" "))
			_ = w.Flush()
			for {
				select {
				case r := <-done:
					if r.err != nil {
						_ = writeSSE(w, openai.Error(serviceErrorStatus(r.err), serviceErrorCode(r.err), serviceErrorMessage(r.err)))
						_ = w.Flush()
						return
					}
					data, _ := json.Marshal(r.rep)
					_, _ = w.Write(data)
					_ = w.Flush()
					return
				case <-ticker.C:
					if _, err := w.Write([]byte(" ")); err != nil {
						return
					}
					if err := w.Flush(); err != nil {
						return
					}
				case <-c.Done():
					return
				}
			}
		})
	case <-c.Done():
		return writeServiceError(c, &ServiceError{Status: http.StatusRequestTimeout, Code: "request_cancelled", Message: "client disconnected"})
	}
}

// streamResearch runs the harness on a side goroutine and pipes SSE frames to
// the client with a 15s keep-alive comment.
func (h *handlers) streamResearch(c fiber.Ctx, opts researchOpts) error {
	c.Set(fiber.HeaderContentType, "text/event-stream")
	c.Set(fiber.HeaderCacheControl, "no-cache")
	c.Set(fiber.HeaderConnection, "keep-alive")

	return c.SendStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := context.WithTimeout(c, h.research.timeout())
		defer cancel()
		frames := make(chan []byte, 64)

		go func() {
			defer close(frames)
			emit := func(payload any) error {
				buf := &strings.Builder{}
				if err := writeSSE(buf, payload); err != nil {
					return err
				}
				select {
				case frames <- []byte(buf.String()):
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			rep, err := h.runResearch(ctx, opts.withEmit(emit))
			if err != nil {
				_ = emit(map[string]any{"type": "error", "message": err.Error()})
				return
			}
			_ = emit(map[string]any{
				"type":       "done",
				"report":     rep.Report,
				"queries":    rep.Queries,
				"outline":    rep.Outline,
				"references": rep.References,
				"metrics":    rep.Metrics,
			})
		}()

		heartbeat := time.NewTicker(streamHeartbeatInterval)
		defer heartbeat.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
				if !writeStreamComment(w) {
					return
				}
			case f, ok := <-frames:
				if !ok {
					return
				}
				if _, err := w.Write(f); err != nil {
					return
				}
				if err := w.Flush(); err != nil {
					return
				}
			}
		}
	})
}

// withEmit returns a copy of opts with the frame sink set.
func (o researchOpts) withEmit(emit func(any) error) researchOpts {
	o.emit = emit
	return o
}

// runResearch executes the native harness: plan → fan-out search+extract →
// synthesize. emit frames are optional (nil for buffered runs).
func (h *handlers) runResearch(ctx context.Context, opts researchOpts) (*researchReport, error) {
	start := time.Now()
	metrics := researchMetrics{
		PlannerModel: opts.model,
		SynthModel:   opts.model,
	}

	// Phase 1: planner LLM turns the brief into sub-queries.
	plan, err := h.planQueries(ctx, opts.model, opts.input, opts.maxQueries)
	if err != nil {
		return nil, err
	}
	if len(plan.Queries) == 0 {
		plan.Queries = []string{opts.input}
	}
	queries := plan.Queries
	if len(queries) > opts.maxQueries {
		queries = queries[:opts.maxQueries]
	}
	metrics.Queries = len(queries)
	_ = emitFrame(opts.emit, map[string]any{"type": "plan", "queries": queries, "outline": plan.Outline})

	searchBackend, extractBackend := h.researchBackends()

	// Phase 2: fan out bounded-concurrency search + extract.
	var (
		mu          sync.Mutex
		evidenceMap = make(map[string]evidence) // url → evidence
		extractRun  int
	)
	sem := make(chan struct{}, opts.fanOut)
	var wg sync.WaitGroup
	for _, q := range queries {
		q := q
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			items, fetched, err := h.researchQuery(ctx, q, opts.input, opts.extractPerQuery)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				_ = emitFrame(opts.emit, map[string]any{"type": "search_error", "query": q, "message": err.Error()})
				return
			}
			mu.Lock()
			for _, it := range items {
				evidenceMap[it.URL] = it
			}
			extractRun += fetched
			mu.Unlock()
			_ = emitFrame(opts.emit, map[string]any{"type": "search", "query": q, "results": len(items)})
		}()
	}
	wg.Wait()

	evidenceList := sortedEvidence(evidenceMap)
	if len(evidenceList) > maxEvidence {
		evidenceList = evidenceList[:maxEvidence]
	}
	metrics.SearchesRun = len(queries)
	metrics.ExtractsRun = extractRun
	metrics.Sources = len(evidenceList)
	metrics.SearchBackend = searchBackend
	metrics.ExtractBackend = extractBackend

	if len(evidenceList) == 0 {
		return nil, &ServiceError{
			Status:  http.StatusBadGateway,
			Code:    "research_no_evidence",
			Message: "research gathered no usable sources; try a different brief or check search backends",
		}
	}

	// Phase 3: synthesize the cited report.
	report, err := h.synthesize(ctx, opts, evidenceList)
	if err != nil {
		return nil, err
	}
	metrics.ElapsedMS = time.Since(start).Milliseconds()

	refs := make([]reference, 0, len(evidenceList))
	for _, e := range evidenceList {
		refs = append(refs, e.reference)
	}
	return &researchReport{
		Report:     report,
		Queries:    queries,
		Outline:    plan.Outline,
		References: refs,
		Metrics:    metrics,
	}, nil
}

// planQueries asks the gateway LLM to turn the brief into strict-JSON
// sub-queries (3–5). A failed parse falls back to the raw brief as a single
// query so the harness still works without a compliant planner.
func (h *handlers) planQueries(ctx context.Context, model, input string, max int) (*researchPlan, error) {
	system := "You are a research planner. Convert the user's research brief into 3-5 distinct, self-contained web search sub-queries (each 3-6 words, no operators) and a short markdown report outline. Respond with ONLY a JSON object with no code fences, no prose:\n{\"queries\": [\"...\"], \"outline\": [\"...\"]}"
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	text, err := h.chat.Complete(cctx, openai.ChatCompletionRequest{
		Model:       model,
		Messages:    []openai.ChatMessage{{Role: "system", Content: system}, {Role: "user", Content: "Research brief: " + input}},
		Temperature: floatPtr(0.2),
		// Reasoning models burn budget on reasoning_content before any
		// content; keep the cap generous or the plan arrives empty.
		MaxTokens: intPtr(4096),
	})
	if err != nil {
		return nil, fmt.Errorf("deep-research planner: %w", err)
	}
	return parsePlanJSON(text, max), nil
}

// parsePlanJSON extracts the planner's JSON object from possibly-noisy LLM
// text (code fences stripped) and normalizes the query list.
func parsePlanJSON(text string, max int) *researchPlan {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return &researchPlan{Queries: nil, Outline: nil}
	}
	var p researchPlan
	if err := json.Unmarshal([]byte(text[start:end+1]), &p); err != nil {
		return &researchPlan{Queries: nil, Outline: nil}
	}
	queries := make([]string, 0, len(p.Queries))
	for _, q := range p.Queries {
		q = strings.TrimSpace(q)
		if q != "" && !strings.Contains(q, "\n") {
			queries = append(queries, q)
		}
		if len(queries) >= max {
			break
		}
	}
	return &researchPlan{Queries: queries, Outline: p.Outline}
}

// researchBackends reports which search/extract paths are live.
func (h *handlers) researchBackends() (string, string) {
	search := "none"
	extract := "none"
	if h.parallel != nil && h.parallel.Enabled() {
		search, extract = "parallel+duckduckgo-stealth", "parallel"
		return search, extract
	}
	if h.webSearcher != nil {
		search, extract = "duckduckgo-stealth", "duckduckgo-stealth"
	}
	return search, extract
}

// researchQuery searches one sub-query across the available backends, selects
// the top URLs, and reads them. Returns evidence plus how many pages fetched.
func (h *handlers) researchQuery(ctx context.Context, query, objective string, extractPer int) ([]evidence, int, error) {
	type hit struct {
		title, url, snippet string
	}
	var hits []hit
	seen := make(map[string]bool)
	addHit := func(title, url, snippet string) {
		title = strings.TrimSpace(title)
		url = strings.TrimSpace(url)
		if title == "" || url == "" {
			return
		}
		if seen[url] {
			return
		}
		seen[url] = true
		hits = append(hits, hit{title, url, snippet})
	}

	if h.parallel != nil && h.parallel.Enabled() {
		if resp, err := h.parallel.Search(ctx, parallel.SearchRequest{
			Objective:     objective,
			SearchQueries: []string{query},
			Mode:          "fast",
			MaxCharsTotal: 15_000,
			SessionID:     "deep-research",
		}); err == nil && resp != nil {
			for _, r := range resp.Results {
				snip := ""
				if len(r.Excerpts) > 0 {
					snip = r.Excerpts[0]
				}
				addHit(r.Title, r.URL, snip)
			}
		}
	}
	if h.webSearcher != nil {
		if resp, err := h.webSearcher.Search(ctx, query, 8); err == nil {
			for _, r := range resp.Results {
				addHit(r.Title, r.URL, r.Snippet)
			}
		}
	}

	if len(hits) == 0 {
		return nil, 0, fmt.Errorf("no results for %q", query)
	}
	top := hits
	if len(top) > extractPer {
		top = top[:extractPer]
	}

	items := make([]evidence, 0, len(top))
	if h.parallel != nil && h.parallel.Enabled() {
		urls := make([]string, len(top))
		for i, hh := range top {
			urls[i] = hh.url
		}
		if resp, err := h.parallel.Extract(ctx, parallel.ExtractRequest{URLs: urls, Objective: objective}); err == nil {
			byURL := make(map[string]parallel.ExtractResult, len(resp.Results))
			for _, r := range resp.Results {
				byURL[normalizeURL(r.URL)] = r
			}
			for _, hh := range top {
				r := byURL[normalizeURL(hh.url)]
				items = append(items, evidence{
					reference: reference{Title: firstNonEmpty(r.Title, hh.title), URL: hh.url, Query: query, Snippet: hh.snippet},
					Content:   r.FullContent,
				})
			}
			return items, len(top), nil
		}
		// Extract failed: fall through to keyless page reads.
	}
	if h.webSearcher != nil {
		for _, hh := range top {
			p, err := h.webSearcher.FetchPage(ctx, hh.url)
			if err != nil {
				items = append(items, evidence{
					reference: reference{Title: hh.title, URL: hh.url, Query: query, Snippet: hh.snippet},
					Content:   "",
				})
				continue
			}
			title := hh.title
			if p.Title != "" {
				title = p.Title
			}
			items = append(items, evidence{
				reference: reference{Title: title, URL: hh.url, Query: query, Snippet: hh.snippet},
				Content:   p.Text,
			})
		}
		return items, len(top), nil
	}
	return nil, 0, fmt.Errorf("no extract backend for %q", query)
}

// synthesize builds the cited markdown report from the collected evidence.
// In stream mode the LLM output is piped through as {"type":"synthesis"} deltas.
func (h *handlers) synthesize(ctx context.Context, opts researchOpts, ev []evidence) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "RESEARCH BRIEF: %s\n\nSOURCES:\n", opts.input)
	for i, e := range ev {
		fmt.Fprintf(&b, "[%d] %s\nURL: %s\n", i+1, e.Title, e.URL)
		if e.Snippet != "" {
			fmt.Fprintf(&b, "Snippet: %s\n", e.Snippet)
		}
		if e.Content != "" {
			fmt.Fprintf(&b, "Content:\n%s\n", truncate(e.Content, 4000))
		}
		b.WriteString("\n")
	}

	system := "You are a research analyst. Write a comprehensive, well-structured markdown report (roughly 600-900 words) that answers the research brief using ONLY the sources provided. Cite sources inline as [n] matching the numbering above. End with a \"## References\" section listing each cited source as \"- [n] <title>: <URL>\". Do not invent facts, claims, or URLs not present in the sources. If the sources are insufficient to answer the brief, say so explicitly and summarize what the sources do cover."

	req := openai.ChatCompletionRequest{
		Model:       opts.model,
		Messages:    []openai.ChatMessage{{Role: "system", Content: system}, {Role: "user", Content: b.String()}},
		Temperature: floatPtr(0.4),
		// Same reasoning-budget headroom as the planner: reasoning models can
		// burn the entire cap on reasoning_content and return empty content.
		MaxTokens: intPtr(4096),
	}

	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	if !opts.stream || opts.emit == nil {
		return h.chat.Complete(cctx, req)
	}

	deltas, errs := h.chat.Stream(cctx, req)
	var out strings.Builder
	for deltas != nil || errs != nil {
		select {
		case d, ok := <-deltas:
			if !ok {
				deltas = nil
				continue
			}
			out.WriteString(d)
			if err := emitFrame(opts.emit, map[string]any{"type": "synthesis", "delta": d}); err != nil {
				return "", err
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return "", fmt.Errorf("deep-research synthesis: %w", err)
			}
		case <-cctx.Done():
			return "", cctx.Err()
		}
	}
	return out.String(), nil
}

// sortedEvidence returns deduped evidence in a stable query-then-URL order so
// citation numbering does not shuffle between runs.
func sortedEvidence(m map[string]evidence) []evidence {
	out := make([]evidence, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Query != out[j].Query {
			return out[i].Query < out[j].Query
		}
		return out[i].URL < out[j].URL
	})
	return out
}

// normalizeURL trims trailing slashes so search hits match Extract results.
func normalizeURL(u string) string {
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func clampInt(v, dflt, lo, hi int) int {
	if v == 0 {
		v = dflt
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }

// emitFrame writes one research frame; unused (nil emitter) in buffered mode.
func emitFrame(emit func(any) error, payload any) error {
	if emit == nil {
		return nil
	}
	return emit(payload)
}

func (r ResearchConfig) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 5 * time.Minute
}

// searchBackendConfigured reports whether the native Layer B harness has at
// least one usable search backend: a keyed Parallel search, or the keyless
// stealth searcher (hermes sidecar).
func (h *handlers) searchBackendConfigured() bool {
	return (h.parallel != nil && h.parallel.Enabled()) || h.webSearcher != nil
}
