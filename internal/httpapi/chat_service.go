package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"freebuff-unified/internal/credentials"
	"freebuff-unified/internal/freebuff"
	"freebuff-unified/internal/openai"
	"freebuff-unified/internal/session"
)

// SessionEnsurer ensures a Freebuff session is active before chat.
type SessionEnsurer interface {
	EnsureActive(ctx context.Context, model string) (freebuff.Session, error)
}

// CredentialStore loads the Freebuff auth token.
type CredentialStore interface {
	Load(ctx context.Context) (credentials.Credential, error)
}

// UpstreamChatClient sends chat requests to the Freebuff upstream.
type UpstreamChatClient interface {
	Complete(ctx context.Context, token string, activeSession freebuff.Session, model string, messages []freebuff.ChatMessage) (string, error)
	Stream(ctx context.Context, token string, activeSession freebuff.Session, model string, messages []freebuff.ChatMessage) (<-chan string, <-chan error)
}

// FreebuffChatService wires credential store, session manager, and upstream client.
type FreebuffChatService struct {
	Store    CredentialStore
	Sessions SessionEnsurer
	Upstream UpstreamChatClient
}

func (s FreebuffChatService) Complete(ctx context.Context, req openai.ChatCompletionRequest) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}

	activeSession, err := s.Sessions.EnsureActive(ctx, freebuff.CanonicalModelName(req.Model))
	if err != nil {
		return "", normalizeSessionSetupError(err)
	}

	cred, err := s.Store.Load(ctx)
	if err != nil {
		return "", fmt.Errorf("load freebuff credentials: %w", err)
	}

	messages := toFreebuffMessages(req.Messages)

	text, err := s.Upstream.Complete(ctx, cred.AuthToken, activeSession, freebuff.CanonicalModelName(req.Model), messages)
	if err != nil {
		return "", normalizeUpstreamChatError(err)
	}

	return text, nil
}

func (s FreebuffChatService) Stream(ctx context.Context, req openai.ChatCompletionRequest) (<-chan string, <-chan error) {
	if err := s.validate(); err != nil {
		return failedStream(err)
	}

	activeSession, err := s.Sessions.EnsureActive(ctx, freebuff.CanonicalModelName(req.Model))
	if err != nil {
		return failedStream(normalizeSessionSetupError(err))
	}

	cred, err := s.Store.Load(ctx)
	if err != nil {
		return failedStream(fmt.Errorf("load freebuff credentials: %w", err))
	}

	streamCtx, cancel := context.WithCancel(ctx)
	upstreamDeltas, upstreamErrs := s.Upstream.Stream(streamCtx, cred.AuthToken, activeSession, freebuff.CanonicalModelName(req.Model), toFreebuffMessages(req.Messages))
	if upstreamDeltas == nil && upstreamErrs == nil {
		cancel()
		return failedStream(serviceUnavailable("upstream_chat_unavailable", "Upstream chat stream did not return channels"))
	}

	start, err := inspectStreamStart(upstreamDeltas, upstreamErrs)
	if err != nil {
		cancel()
		return failedStream(normalizeUpstreamChatError(err))
	}

	deltas := make(chan string)
	errs := make(chan error, 1)
	go func() {
		defer cancel()
		defer close(deltas)
		defer close(errs)

		if start.hasFirstDelta && !sendStreamDelta(streamCtx, deltas, start.firstDelta) {
			return
		}
		if start.firstErr != nil && !sendStreamError(streamCtx, errs, normalizeUpstreamChatError(start.firstErr)) {
			return
		}

		forwardStream(streamCtx, deltas, errs, start.deltas, start.errs)
	}()

	return deltas, errs
}

func (s FreebuffChatService) validate() error {
	if s.Sessions == nil {
		return serviceUnavailable("session_manager_unavailable", "Freebuff session manager not configured")
	}
	if s.Store == nil {
		return serviceUnavailable("credential_store_unavailable", "Freebuff credential store not configured")
	}
	if s.Upstream == nil {
		return serviceUnavailable("upstream_chat_unavailable", "Freebuff upstream chat client not configured")
	}
	return nil
}

type streamStart struct {
	deltas        <-chan string
	errs          <-chan error
	firstDelta    string
	hasFirstDelta bool
	firstErr      error
}

func failedStream(err error) (<-chan string, <-chan error) {
	deltas := make(chan string)
	close(deltas)
	errs := make(chan error, 1)
	if err != nil {
		errs <- err
	}
	close(errs)
	return deltas, errs
}

func inspectStreamStart(upstreamDeltas <-chan string, upstreamErrs <-chan error) (streamStart, error) {
	start := streamStart{deltas: upstreamDeltas, errs: upstreamErrs}
	if upstreamErrs == nil {
		return start, nil
	}

	select {
	case err, ok := <-upstreamErrs:
		if !ok {
			start.errs = nil
			return start, nil
		}
		if err == nil {
			return start, nil
		}
		if upstreamDeltas == nil {
			return start, err
		}

		select {
		case delta, ok := <-upstreamDeltas:
			if !ok {
				return start, err
			}
			start.firstDelta = delta
			start.hasFirstDelta = true
			start.firstErr = err
			return start, nil
		default:
			return start, err
		}
	default:
		return start, nil
	}
}

func serviceUnavailable(code string, message string) *ServiceError {
	return &ServiceError{Status: http.StatusServiceUnavailable, Code: code, Message: message}
}

func normalizeSessionSetupError(err error) error {
	if err == nil {
		return nil
	}

	wrapped := fmt.Errorf("ensure freebuff session active: %w", err)

	var serviceErr *ServiceError
	if errors.As(wrapped, &serviceErr) {
		return serviceErr
	}

	var apiErr *freebuff.APIError
	if errors.As(wrapped, &apiErr) {
		return sanitizedFreebuffAPIError(apiErr)
	}

	var statusErr *session.StatusError
	if errors.As(wrapped, &statusErr) {
		return sanitizedSessionStatusError(statusErr.Status)
	}

	return serviceUnavailable("freebuff_session_unavailable", "Freebuff session could not be prepared")
}

func normalizeUpstreamChatError(err error) error {
	if err == nil {
		return nil
	}

	var serviceErr *ServiceError
	if errors.As(err, &serviceErr) {
		return serviceErr
	}

	var apiErr *freebuff.APIError
	if errors.As(err, &apiErr) {
		status := apiErr.StatusCode
		if status == 0 {
			status = http.StatusServiceUnavailable
		}
		code := apiErr.Code
		if code == "" {
			code = "upstream_chat_error"
		}
		message := apiErr.Message
		if message == "" {
			message = http.StatusText(status)
		}
		return &ServiceError{Status: status, Code: code, Message: message}
	}

	return err
}

func sanitizedSessionStatusError(status freebuff.SessionStatus) *ServiceError {
	switch status {
	case freebuff.SessionRateLimited:
		return &ServiceError{Status: http.StatusTooManyRequests, Code: "freebuff_rate_limited", Message: "Freebuff session rate limit exceeded"}
	case freebuff.SessionDisabled, freebuff.SessionCountryBlocked, freebuff.SessionBanned:
		return &ServiceError{Status: http.StatusForbidden, Code: "freebuff_session_unavailable", Message: "Freebuff session is not available"}
	case freebuff.SessionModelLocked, freebuff.SessionModelUnavailable:
		return &ServiceError{Status: http.StatusServiceUnavailable, Code: "freebuff_model_unavailable", Message: "Freebuff model is not available"}
	default:
		return serviceUnavailable("freebuff_session_unavailable", "Freebuff session could not be prepared")
	}
}

func sanitizedFreebuffAPIError(apiErr *freebuff.APIError) *ServiceError {
	status := apiErr.StatusCode
	if status == 0 {
		status = http.StatusServiceUnavailable
	}

	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &ServiceError{Status: status, Code: "freebuff_auth_failed", Message: "Freebuff authentication failed"}
	case http.StatusTooManyRequests:
		return &ServiceError{Status: status, Code: "freebuff_rate_limited", Message: "Freebuff session rate limit exceeded"}
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return &ServiceError{Status: status, Code: "upstream_chat_unavailable", Message: "Freebuff session service is currently unavailable"}
	default:
		return &ServiceError{Status: status, Code: "freebuff_session_error", Message: "Freebuff session service returned an error"}
	}
}

func forwardStream(ctx context.Context, deltas chan<- string, errs chan<- error, upstreamDeltas <-chan string, upstreamErrs <-chan error) {
	for upstreamDeltas != nil || upstreamErrs != nil {
		select {
		case <-ctx.Done():
			return
		case delta, ok := <-upstreamDeltas:
			if !ok {
				upstreamDeltas = nil
				continue
			}
			if !sendStreamDelta(ctx, deltas, delta) {
				return
			}
		case err, ok := <-upstreamErrs:
			if !ok {
				upstreamErrs = nil
				continue
			}
			if err == nil {
				continue
			}
			if !sendStreamError(ctx, errs, normalizeUpstreamChatError(err)) {
				return
			}
		}
	}
}

func sendStreamDelta(ctx context.Context, deltas chan<- string, delta string) bool {
	select {
	case <-ctx.Done():
		return false
	case deltas <- delta:
		return true
	}
}

func sendStreamError(ctx context.Context, errs chan<- error, err error) bool {
	if err == nil {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case errs <- err:
		return true
	}
}

func toFreebuffMessages(messages []openai.ChatMessage) []freebuff.ChatMessage {
	out := make([]freebuff.ChatMessage, len(messages))
	for i, m := range messages {
		out[i] = freebuff.ChatMessage{
			Role:    m.Role,
			Content: extractContentString(m.Content),
		}
	}
	return out
}

func extractContentString(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok && t != "" {
					parts = append(parts, t)
				}
			}
		}
		if len(parts) > 0 {
			return parts[0]
		}
		return ""
	default:
		return ""
	}
}
