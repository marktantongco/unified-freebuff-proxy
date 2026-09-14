package freebuff

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// cliUserAgent is the User-Agent the official Freebuff CLI pins on chat
// calls alone (ai-sdk openai-compatible client). Upstream's free-mode gate
// 403s chat requests that do not carry it with free_mode_cli_required.
const cliUserAgent = "ai-sdk/openai-compatible/1.0.0/codebuff"

// cliSystemMarker is the canonical identity prefix the CLI puts at the root
// of every free-mode system prompt. Upstream requires the first system
// message to OPEN with it (position 0, after whitespace trim only).
const cliSystemMarker = "You are Buffy, the strategic coding assistant. You are the AI agent behind the product, Freebuff, a tool where users can chat with you to code with AI for free."

const (
	sessionEndpointPath          = "/api/v1/freebuff/session"
	headerAuthorization          = "Authorization"
	headerInstanceID             = "x-freebuff-instance-id"
	headerModel                  = "x-freebuff-model"
	chatEndpointPath             = "/api/v1/chat/completions"
	agentRunsEndpointPath        = "/api/v1/agent-runs"
	freebuffCostMode             = "free"
	defaultFreeAgentID           = "base2-free"
	defaultResponseHeaderTimeout = 30 * time.Second

	// defaultTLSHandshakeTimeout bounds the TLS handshake on the direct path.
	// Also mirrors the intent of a per-attempt budget: dial + TLS + headers
	// each get their own ceiling instead of one giant 180s client timeout.
	defaultTLSHandshakeTimeout = 10 * time.Second
)

// transport-level retry tuning for doJSONRequest (transport errors only).
// maxTransportAttempts is the TOTAL number of sends (initial + 1 retry).
const (
	maxTransportAttempts = 2
	transportRetryDelay  = 200 * time.Millisecond
)

// Static offline-fallback snapshot of upstream's free-mode (agent, model)
// pairings. The live agent registry (registry.go) is the primary source —
// this map only serves when no registry refresh has ever succeeded. Retired
// models (minimax-m2.7, kimi-k2.6) were dropped upstream 2026-09; glm's
// pairing was added upstream the same month.
var freebuffAgentIDsByModel = map[string]string{
	"z-ai/glm-5.3-flash":         "base2-free-glm-5-3-flash",
	"deepseek/deepseek-v4-pro":   "base2-free-deepseek",
	"deepseek/deepseek-v4-flash": "base2-free-deepseek-flash",
	"freebuff-chat-verified":     "base2-free",
}

var canonicalFreebuffModelsByAlias = map[string]string{
	"deepseek-v4-pro":        "deepseek/deepseek-v4-pro",
	"deepseek-v4-flash":      "deepseek/deepseek-v4-flash",
	"deepseek-v3.1-terminus": "deepseek/deepseek-v4-pro",
}

// SessionStatus defines the Freebuff session lifecycle statuses.
type SessionStatus string

const (
	SessionDisabled         SessionStatus = "disabled"
	SessionNone             SessionStatus = "none"
	SessionQueued           SessionStatus = "queued"
	SessionActive           SessionStatus = "active"
	SessionEnded            SessionStatus = "ended"
	SessionSuperseded       SessionStatus = "superseded"
	SessionCountryBlocked   SessionStatus = "country_blocked"
	SessionModelLocked      SessionStatus = "model_locked"
	SessionModelUnavailable SessionStatus = "model_unavailable"
	SessionBanned           SessionStatus = "banned"
	SessionRateLimited      SessionStatus = "rate_limited"
)

// Session represents the fields the proxy needs for Freebuff session decisions.
type Session struct {
	Status         SessionStatus `json:"status"`
	AccessTier     string        `json:"accessTier,omitempty"`
	InstanceID     string        `json:"instanceId,omitempty"`
	Model          string        `json:"model,omitempty"`
	Position       int           `json:"position,omitempty"`
	QueueDepth     int           `json:"queueDepth,omitempty"`
	AdmittedAt     time.Time     `json:"admittedAt,omitempty"`
	ExpiresAt      time.Time     `json:"expiresAt,omitempty"`
	RemainingMS    int64         `json:"remainingMs,omitempty"`
	Message        string        `json:"message,omitempty"`
	RequestedModel string        `json:"requestedModel,omitempty"`
	CurrentModel   string        `json:"currentModel,omitempty"`
	RetryAfterMS   int64         `json:"retryAfterMs,omitempty"`
}

// APIError represents a failed Freebuff response with HTTP status and server message.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

// Client is the HTTP client that talks to Freebuff session and chat endpoints.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

// NewClient creates a new Freebuff client from the base URL.
func NewClient(baseURL string, httpClient *http.Client) (*Client, error) {
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse freebuff base url: %w", err)
	}
	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		return nil, fmt.Errorf("parse freebuff base url: missing scheme or host")
	}
	if httpClient == nil {
		httpClient = defaultHTTPClient()
	}
	return &Client{
		baseURL:    parsedURL,
		httpClient: httpClient,
	}, nil
}

// UseTransport replaces the underlying HTTP client. Used to attach the
// stealth (hermes sidecar) transport after construction.
func (c *Client) UseTransport(httpClient *http.Client) {
	if httpClient != nil {
		c.httpClient = httpClient
	}
}

func defaultHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = defaultResponseHeaderTimeout
	// Raise per-host idle pool above Go's default of 2 to prevent TLS
	// handshake storms under concurrent load.
	transport.MaxIdleConnsPerHost = 100
	transport.TLSHandshakeTimeout = defaultTLSHandshakeTimeout
	transport.ExpectContinueTimeout = 1 * time.Second
	return &http.Client{Transport: transport}
}

func (c *Client) GetSession(ctx context.Context, token string, instanceID string) (Session, error) {
	return c.doSessionRequest(ctx, http.MethodGet, token, instanceID, "")
}

func (c *Client) StartSession(ctx context.Context, token string, instanceID string, model string) (Session, error) {
	return c.doSessionRequest(ctx, http.MethodPost, token, instanceID, model)
}

func (c *Client) EndSession(ctx context.Context, token string, instanceID string) (Session, error) {
	return c.doSessionRequest(ctx, http.MethodDelete, token, instanceID, "")
}

func (c *Client) doSessionRequest(ctx context.Context, method string, token string, instanceID string, model string) (Session, error) {
	requestURL := c.baseURL.ResolveReference(&url.URL{Path: sessionEndpointPath})

	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), nil)
	if err != nil {
		return Session{}, fmt.Errorf("build freebuff session request: %w", err)
	}

	if token != "" {
		req.Header.Set(headerAuthorization, "Bearer "+token)
	}
	if instanceID != "" {
		req.Header.Set(headerInstanceID, instanceID)
	}
	if model != "" {
		req.Header.Set(headerModel, model)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Session{}, fmt.Errorf("send freebuff session request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		apiErr, err := decodeAPIError(resp)
		if err != nil {
			return Session{}, fmt.Errorf("decode freebuff error response: %w", err)
		}
		return Session{}, apiErr
	}

	var session Session
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return Session{}, fmt.Errorf("decode freebuff session response: %w", err)
	}

	return session, nil
}

func decodeAPIError(resp *http.Response) (*APIError, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read freebuff error response: %w", err)
	}

	apiErr := &APIError{
		StatusCode: resp.StatusCode,
		Message:    http.StatusText(resp.StatusCode),
	}

	if len(body) == 0 {
		return apiErr, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return apiErr, nil
	}

	if code, ok := payload["code"].(string); ok {
		apiErr.Code = code
	}
	if message, ok := payload["message"].(string); ok {
		apiErr.Message = message
	}
	if apiErr.Code == "" {
		if code, ok := payload["error"].(string); ok {
			apiErr.Code = code
		}
	}
	if apiErr.Message == "" {
		if message, ok := payload["error"].(string); ok {
			apiErr.Message = message
		}
	}

	return apiErr, nil
}

// CanonicalModelName converts a Freebuff model alias to the canonical model name.
func CanonicalModelName(model string) string {
	if canonicalModel, ok := canonicalFreebuffModelsByAlias[model]; ok {
		return canonicalModel
	}
	return model
}

// Complete sends a non-stream chat request to the Freebuff upstream chat endpoint.
func (c *Client) Complete(ctx context.Context, token string, activeSession Session, model string, messages []ChatMessage) (string, error) {
	upstreamModel := modelForActiveSession(activeSession, model)
	runID, err := c.startAgentRun(ctx, token, upstreamModel)
	if err != nil {
		return "", err
	}

	upstreamReq, err := c.buildUpstreamChatRequest(upstreamModel, messages, false, runID, activeSession)
	if err != nil {
		return "", err
	}

	resp, err := c.doChatRequest(ctx, token, upstreamReq, false)
	if err != nil {
		return "", err
	}

	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	resp.Body.Close()

	if readErr != nil && resp.StatusCode >= 400 {
		return "", chatDecodeError()
	}

	if resp.StatusCode >= 400 && shouldRecoverSessionFromResponse(resp.StatusCode, bodyBytes) {
		newSession, retryOK := c.retryWithFreshSession(ctx, token, upstreamModel, activeSession.InstanceID)
		if retryOK {
			return c.Complete(ctx, token, newSession, model, messages)
		}
		return "", makeChatStatusError(resp.StatusCode, bodyBytes)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", makeChatStatusError(resp.StatusCode, bodyBytes)
	}

	var payload ChatCompletionResponse
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return "", chatDecodeError()
	}

	if len(payload.Choices) == 0 || payload.Choices[0].Message == nil {
		return "", chatDecodeError()
	}

	return payload.Choices[0].Message.Content, nil
}

// Stream sends a streaming chat request and returns delta/errs channels.
func (c *Client) Stream(ctx context.Context, token string, activeSession Session, model string, messages []ChatMessage) (<-chan string, <-chan error) {
	upstreamModel := modelForActiveSession(activeSession, model)
	runID, err := c.startAgentRun(ctx, token, upstreamModel)
	if err != nil {
		return failedChatStream(err)
	}

	upstreamReq, err := c.buildUpstreamChatRequest(upstreamModel, messages, true, runID, activeSession)
	if err != nil {
		return failedChatStream(err)
	}

	resp, err := c.doChatRequest(ctx, token, upstreamReq, true)
	if err != nil {
		return failedChatStream(err)
	}

	if resp.StatusCode >= 400 {
		bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()

		if readErr != nil {
			return failedChatStream(chatDecodeError())
		}

		if shouldRecoverSessionFromResponse(resp.StatusCode, bodyBytes) {
			newSession, retryOK := c.retryWithFreshSession(ctx, token, upstreamModel, activeSession.InstanceID)
			if retryOK {
				return c.Stream(ctx, token, newSession, model, messages)
			}
			return failedChatStream(makeChatStatusError(resp.StatusCode, bodyBytes))
		}
		return failedChatStream(makeChatStatusError(resp.StatusCode, bodyBytes))
	}

	return c.streamFromBody(resp)
}

func (c *Client) streamFromBody(resp *http.Response) (<-chan string, <-chan error) {
	deltas := make(chan string)
	errs := make(chan error, 1)

	go func() {
		defer close(deltas)
		defer close(errs)
		defer resp.Body.Close()

		if err := scanChatStream(context.Background(), resp.Body, deltas); err != nil {
			errs <- err
		}
	}()

	return deltas, errs
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Stream      bool          `json:"stream,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
}

type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
}

type ChatCompletionChoice struct {
	Index        int          `json:"index"`
	Message      *ChatMessage `json:"message"`
	FinishReason string       `json:"finish_reason"`
}

type ChatCompletionChunk struct {
	ID      string                      `json:"id"`
	Object  string                      `json:"object"`
	Created int64                       `json:"created"`
	Model   string                      `json:"model"`
	Choices []ChatCompletionChunkChoice `json:"choices"`
}

type ChatCompletionChunkChoice struct {
	Index int          `json:"index"`
	Delta *ChatMessage `json:"delta"`
}

type upstreamChatRequest struct {
	Model            string           `json:"model"`
	Messages         []ChatMessage    `json:"messages"`
	Stream           bool             `json:"stream,omitempty"`
	Temperature      *float64         `json:"temperature,omitempty"`
	MaxTokens        *int             `json:"max_tokens,omitempty"`
	CodebuffMetadata codebuffMetadata `json:"codebuff_metadata"`
}

type codebuffMetadata struct {
	RunID              string `json:"run_id"`
	ClientID           string `json:"client_id"`
	CostMode           string `json:"cost_mode"`
	FreebuffInstanceID string `json:"freebuff_instance_id,omitempty"`
}

func (c *Client) startAgentRun(ctx context.Context, token string, model string) (string, error) {
	agentID := agentIDForModel(model)
	resp, err := c.doJSONRequest(ctx, token, agentRunsEndpointPath, map[string]any{
		"action":  "START",
		"agentId": agentID,
	}, "application/json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", chatStatusError(resp, "agent_run")
	}

	var payload struct {
		RunID string `json:"runId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", chatDecodeError()
	}
	if strings.TrimSpace(payload.RunID) == "" {
		return "", chatDecodeError()
	}

	return payload.RunID, nil
}

// clientIDForRun derives the per-run client session id deterministically from
// the run_id, in the SDK-faithful 13-char base36 shape (the CLI's
// Math.random().toString(36).substring(2,15) equivalent). One run_id always
// pairs with one client_id — a per-call draw fans one run out across N ids,
// which upstream refuses as free_mode_run_fanout. Other shapes — sess:/run:
// prefixes, bare hex — are what upstream fingerprints as a proxy (#103).
func clientIDForRun(runID string) string {
	sum := sha256.Sum256([]byte(runID))
	n := new(big.Int).SetBytes(sum[:])
	mod := new(big.Int).Exp(big.NewInt(36), big.NewInt(13), nil)
	id := n.Mod(n, mod).Text(36)
	for len(id) < 13 {
		id = "0" + id
	}
	return id
}

func (c *Client) buildUpstreamChatRequest(model string, messages []ChatMessage, stream bool, runID string, activeSession Session) (upstreamChatRequest, error) {
	clientID := clientIDForRun(runID)

	// Free-mode gate: the first system message must OPEN with the CLI's
	// canonical identity marker (position 0). Prepend only when no system
	// message already opens with it — never clobber a canonical prompt.
	alreadyMarked := false
	for _, m := range messages {
		if m.Role != "system" {
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(m.Content, " \t\n\r"), cliSystemMarker) {
			alreadyMarked = true
			break
		}
	}
	if !alreadyMarked {
		msgs := make([]ChatMessage, 0, len(messages)+1)
		msgs = append(msgs, ChatMessage{Role: "system", Content: cliSystemMarker})
		msgs = append(msgs, messages...)
		messages = msgs
	}

	return upstreamChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   stream,
		CodebuffMetadata: codebuffMetadata{
			RunID:              runID,
			ClientID:           clientID,
			CostMode:           freebuffCostMode,
			FreebuffInstanceID: activeSession.InstanceID,
		},
	}, nil
}


func agentIDForModel(model string) string {
	// Live registry first (refreshed from upstream's TS constants every 6h);
	// static snapshot as offline fallback; defaultFreeAgentID last.
	if agentID, ok := liveAgentRegistry.get(CanonicalModelName(model)); ok {
		return agentID
	}
	if agentID, ok := freebuffAgentIDsByModel[CanonicalModelName(model)]; ok {
		return agentID
	}
	return defaultFreeAgentID
}

func modelForActiveSession(session Session, fallback string) string {
	for _, m := range []string{session.Model, session.CurrentModel} {
		if strings.TrimSpace(m) != "" {
			return CanonicalModelName(m)
		}
	}
	return fallback
}

func (c *Client) doChatRequest(ctx context.Context, token string, req upstreamChatRequest, stream bool) (*http.Response, error) {
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	return c.doJSONRequest(ctx, token, chatEndpointPath, req, accept)
}

func (c *Client) doJSONRequest(ctx context.Context, token string, path string, payload any, accept string) (*http.Response, error) {
	if strings.TrimSpace(token) == "" {
		return nil, &APIError{
			StatusCode: http.StatusUnauthorized,
			Code:       "freebuff_auth_missing",
			Message:    "Freebuff credentials not found",
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, chatEncodeError()
	}

	var lastErr error
	requestURL := c.baseURL.ResolveReference(&url.URL{Path: path})

	// Transport-level retry: transient dial/TLS/reset blips (dead proxy pick,
	// handshake storm leftovers) are retried once with a short backoff instead
	// of surfacing as user-visible timeouts. Never retried: HTTP status errors
	// (only transport errors reach this path), caller cancellation/deadlines.
	for attempt := 0; attempt < maxTransportAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(transportRetryDelay):
			case <-ctx.Done():
				return nil, &APIError{
					Code:    "upstream_chat_unavailable",
					Message: "Freebuff chat upstream request canceled",
				}
			}
		}

		// Fresh body reader per attempt: bytes.Reader is consumed by the first send.
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build freebuff chat request: %w", err)
		}

		httpReq.Header.Set(headerAuthorization, "Bearer "+token)
		httpReq.Header.Set("Content-Type", "application/json")
		if path == chatEndpointPath {
			// The CLI pins the ai-sdk UA on chat calls alone; every other
			// endpoint keeps Go's default (or the caller's) UA.
			httpReq.Header.Set("User-Agent", cliUserAgent)
		}
		if accept != "" {
			httpReq.Header.Set("Accept", accept)
		}

		resp, err := c.httpClient.Do(httpReq)
		if err == nil {
			return resp, nil
		}

		lastErr = err
		if !isTransportError(err) || ctx.Err() != nil {
			break
		}
	}

	_ = lastErr
	return nil, &APIError{
		Code:    "upstream_chat_unavailable",
		Message: "Freebuff chat upstream request failed",
	}
}

// isTransportError reports whether err is a low-level transport failure worth
// retrying (dial failures, TLS handshake errors, connection resets, EOF).
func isTransportError(err error) bool {
	if err == nil {
		return false
	}
	// Caller cancellation/deadline is never retryable.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE)
}

func failedChatStream(err error) (<-chan string, <-chan error) {
	deltas := make(chan string)
	close(deltas)

	errs := make(chan error, 1)
	if err != nil {
		errs <- err
	}
	close(errs)

	return deltas, errs
}

func scanChatStream(ctx context.Context, body io.Reader, deltas chan<- string) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var dataLines []string
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			finished, err := handleChatStreamEvent(ctx, dataLines, deltas)
			dataLines = nil
			if err != nil || finished {
				return err
			}
			continue
		}

		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}

	if err := scanner.Err(); err != nil {
		return &APIError{Code: "upstream_chat_error", Message: "Freebuff chat stream could not be read"}
	}

	_, err := handleChatStreamEvent(ctx, dataLines, deltas)
	return err
}

func handleChatStreamEvent(ctx context.Context, dataLines []string, deltas chan<- string) (bool, error) {
	if len(dataLines) == 0 {
		return false, nil
	}

	payload := strings.Join(dataLines, "\n")
	if payload == "[DONE]" {
		return true, nil
	}

	var errorEvent struct {
		Error *APIErrorObject `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &errorEvent); err != nil {
		return false, chatDecodeError()
	}
	if errorEvent.Error != nil {
		return false, chatStreamEventError(errorEvent.Error.Code)
	}

	var chunk ChatCompletionChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return false, chatDecodeError()
	}

	if len(chunk.Choices) == 0 || chunk.Choices[0].Delta == nil || chunk.Choices[0].Delta.Content == "" {
		return false, nil
	}

	select {
	case deltas <- chunk.Choices[0].Delta.Content:
	case <-ctx.Done():
		return true, &APIError{Code: "upstream_chat_unavailable", Message: "Freebuff chat stream cancelled"}
	}

	return false, nil
}

type APIErrorObject struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func sendChatError(ctx context.Context, errs chan<- error, err error) {
	if err == nil {
		return
	}
	select {
	case errs <- err:
	case <-ctx.Done():
	}
}

func chatStatusError(resp *http.Response, stage string) *APIError {
	statusCode := resp.StatusCode
	if code, ok := safeUpstreamErrorCode(resp.Body); ok {
		return &APIError{StatusCode: statusCode, Code: code, Message: safeUpstreamErrorMessages[code]}
	}

	apiErr := &APIError{StatusCode: statusCode}
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		apiErr.Code = "freebuff_auth_failed"
		apiErr.Message = "Freebuff chat authorization failed"
	case http.StatusTooManyRequests:
		apiErr.Code = "freebuff_rate_limited"
		apiErr.Message = "Freebuff chat rate limit exceeded"
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		apiErr.Code = "upstream_chat_unavailable"
		apiErr.Message = "Freebuff chat upstream temporarily unavailable"
	default:
		if stage == "agent_run" {
			apiErr.Code = "upstream_agent_run_error"
			apiErr.Message = fmt.Sprintf("Freebuff agent run upstream error: status %d", statusCode)
		} else {
			apiErr.Code = "upstream_chat_error"
			apiErr.Message = fmt.Sprintf("Freebuff chat upstream error: status %d", statusCode)
		}
	}

	return apiErr
}

var safeUpstreamErrorMessages = map[string]string{
	"freebuff_update_required":       "Freebuff session information is missing or outdated",
	"session_expired":                "Freebuff session has expired",
	"session_model_mismatch":         "Freebuff session model does not match request model",
	"session_superseded":             "Freebuff session was superseded by another session",
	"waiting_room_queued":            "Freebuff session is still in queue",
	"waiting_room_required":          "Freebuff waiting room session is required",
	"free_mode_invalid_agent_model": "Free mode is only available for specific agent and model combinations",
}

func safeUpstreamErrorCode(body io.Reader) (string, bool) {
	if body == nil {
		return "", false
	}

	var payload map[string]any
	if err := json.NewDecoder(io.LimitReader(body, 8*1024)).Decode(&payload); err != nil {
		return "", false
	}

	for _, code := range candidateUpstreamErrorCodes(payload) {
		if _, ok := safeUpstreamErrorMessages[code]; ok {
			return code, true
		}
	}

	return "", false
}

func candidateUpstreamErrorCodes(payload map[string]any) []string {
	var codes []string
	if code, ok := payload["code"].(string); ok {
		codes = append(codes, code)
	}
	if code, ok := payload["error"].(string); ok {
		codes = append(codes, code)
	}
	if errorObject, ok := payload["error"].(map[string]any); ok {
		if code, ok := errorObject["code"].(string); ok {
			codes = append(codes, code)
		}
	}

	return codes
}

func makeChatStatusError(statusCode int, body []byte) *APIError {
	if code, ok := safeUpstreamErrorCodeBytes(body); ok {
		return &APIError{StatusCode: statusCode, Code: code, Message: safeUpstreamErrorMessages[code]}
	}

	apiErr := &APIError{StatusCode: statusCode}
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		apiErr.Code = "freebuff_auth_failed"
		apiErr.Message = "Freebuff chat authorization failed"
	case http.StatusTooManyRequests:
		apiErr.Code = "freebuff_rate_limited"
		apiErr.Message = "Freebuff chat rate limit exceeded"
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		apiErr.Code = "upstream_chat_unavailable"
		apiErr.Message = "Freebuff chat upstream temporarily unavailable"
	default:
		apiErr.Code = "upstream_chat_error"
		apiErr.Message = fmt.Sprintf("Freebuff chat upstream error: status %d", statusCode)
	}

	return apiErr
}

func safeUpstreamErrorCodeBytes(body []byte) (string, bool) {
	if len(body) == 0 {
		return "", false
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false
	}

	for _, code := range candidateUpstreamErrorCodes(payload) {
		if _, ok := safeUpstreamErrorMessages[code]; ok {
			return code, true
		}
	}

	return "", false
}

func chatStreamEventError(code string) *APIError {
	switch code {
	case "freebuff_auth_failed":
		return &APIError{StatusCode: http.StatusUnauthorized, Code: "freebuff_auth_failed", Message: "Freebuff chat authorization failed"}
	case "free_mode_invalid_agent_model":
		return &APIError{StatusCode: http.StatusForbidden, Code: "free_mode_invalid_agent_model", Message: "Free mode is only available for specific agent and model combinations"}
	case "freebuff_rate_limited":
		return &APIError{StatusCode: http.StatusTooManyRequests, Code: "freebuff_rate_limited", Message: "Freebuff chat rate limit exceeded"}
	case "upstream_chat_unavailable":
		return &APIError{StatusCode: http.StatusServiceUnavailable, Code: "upstream_chat_unavailable", Message: "Freebuff chat upstream temporarily unavailable"}
	default:
		return &APIError{StatusCode: http.StatusBadGateway, Code: "upstream_chat_error", Message: "Freebuff chat stream returned an error"}
	}
}

func chatEncodeError() *APIError {
	return &APIError{
		Code:    "upstream_chat_error",
		Message: "Freebuff chat request could not be prepared",
	}
}

func chatDecodeError() *APIError {
	return &APIError{
		Code:    "upstream_chat_error",
		Message: "Freebuff chat response could not be decoded",
	}
}

// Session recovery

var sessionRecoveryStatuses = []int{409, 410, 426, 428, 429}

var sessionRecoveryCodes = map[string]bool{
	"freebuff_update_required": true,
	"session_expired":          true,
	"session_model_mismatch":   true,
	"session_superseded":       true,
	"waiting_room_queued":      true,
	"waiting_room_required":    true,
}

func shouldRecoverSessionFromResponse(statusCode int, body []byte) bool {
	if !shouldRecoverSession(statusCode, true) {
		return false
	}
	if len(body) == 0 {
		return false
	}

	var payload struct {
		Code  string `json:"code"`
		Error any    `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}

	if sessionRecoveryCodes[payload.Code] {
		return true
	}

	if errorStr, ok := payload.Error.(string); ok && sessionRecoveryCodes[errorStr] {
		return true
	}

	if errorObj, ok := payload.Error.(map[string]any); ok {
		if code, ok := errorObj["code"].(string); ok && sessionRecoveryCodes[code] {
			return true
		}
	}

	return false
}

func shouldRecoverSession(statusCode int, freeMode bool) bool {
	if !freeMode {
		return false
	}
	for _, code := range sessionRecoveryStatuses {
		if statusCode == code {
			return true
		}
	}
	return false
}

func (c *Client) retryWithFreshSession(ctx context.Context, token string, model string, instanceID string) (Session, bool) {
	session, err := c.recoverSession(ctx, token, model, instanceID)
	if err != nil {
		return Session{}, false
	}
	if session.Status != SessionActive {
		return Session{}, false
	}
	return session, true
}

func (c *Client) recoverSession(ctx context.Context, token string, model string, instanceID string) (Session, error) {
	if instanceID == "" {
		instanceID = "freebuff-unified-recovery"
	}

	session, err := c.StartSession(ctx, token, instanceID, model)
	if err != nil {
		existing, getErr := c.GetSession(ctx, token, instanceID)
		if getErr == nil && existing.InstanceID != "" {
			return existing, nil
		}
		return Session{}, fmt.Errorf("start recovery session: %w", err)
	}

	if session.Status == SessionActive {
		return session, nil
	}

	if session.Status == SessionQueued {
		return c.pollSessionUntilActive(ctx, token, session, instanceID)
	}

	return session, nil
}

func (c *Client) pollSessionUntilActive(ctx context.Context, token string, session Session, instanceID string) (Session, error) {
	timeout := 2 * time.Minute
	interval := 5 * time.Second

	deadline := time.Now().Add(timeout)
	current := session

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return current, ctx.Err()
		case <-time.After(interval):
		}

		next, err := c.GetSession(ctx, token, instanceID)
		if err != nil {
			return current, fmt.Errorf("poll session: %w", err)
		}

		current = next

		switch current.Status {
		case SessionActive:
			return current, nil
		case SessionQueued:
			continue
		case SessionDisabled, SessionCountryBlocked, SessionBanned, SessionRateLimited:
			return current, fmt.Errorf("session unrecoverable: %s", current.Status)
		}
	}

	return current, fmt.Errorf("session did not become active within %v", timeout)
}
