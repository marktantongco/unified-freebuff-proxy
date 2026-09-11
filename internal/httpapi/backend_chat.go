package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"freebuff-unified/internal/openai"
)

// BackendChatService issues chat requests to the front-door passthrough
// backend (/v1/chat/completions). It backs the native Layer B research
// planner and the /v1/responses fallback when proxy.mode is "passthrough":
// there the backend owns sessions and chat, so the gateway's native freebuff
// client (which is not part of that path) must not be used.
type BackendChatService struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewBackendChatService dials an OpenAI-compatible /v1/chat/completions
// backend. apiKey may be empty; the Authorization header is then omitted.
func NewBackendChatService(baseURL, apiKey string) *BackendChatService {
	return &BackendChatService{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  apiKey,
		client: &http.Client{
			Transport: &http.Transport{
				// Planner calls carry a 60s ctx timeout; the backend may hold
				// the connection while composing (heartbeat below).
				ResponseHeaderTimeout: 55 * time.Second,
			},
		},
	}
}

func (s *BackendChatService) Complete(ctx context.Context, req openai.ChatCompletionRequest) (string, error) {
	body := req
	body.Stream = false
	payload, err := json.Marshal(body)
	if err != nil {
		return "", serviceUnavailable("backend_chat_unavailable", fmt.Sprintf("backend chat: %v", err))
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", serviceUnavailable("backend_chat_unavailable", fmt.Sprintf("backend chat: %v", err))
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return "", serviceUnavailable("backend_chat_unavailable", fmt.Sprintf("backend chat: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", backendChatStatusError(resp)
	}
	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", serviceUnavailable("backend_chat_unavailable", "backend chat: invalid JSON response")
	}
	if len(chatResp.Choices) == 0 || strings.TrimSpace(chatResp.Choices[0].Message.Content) == "" {
		return "", serviceUnavailable("backend_chat_unavailable", "backend chat: empty completion")
	}
	return chatResp.Choices[0].Message.Content, nil
}

// Stream relays an SSE chat stream from the backend, yielding content deltas.
func (s *BackendChatService) Stream(ctx context.Context, req openai.ChatCompletionRequest) (<-chan string, <-chan error) {
	deltas := make(chan string, 16)
	errs := make(chan error, 1)
	go func() {
		defer close(deltas)
		defer close(errs)

		body := req
		body.Stream = true
		payload, err := json.Marshal(body)
		if err != nil {
			sendStreamError(ctx, errs, serviceUnavailable("backend_chat_unavailable", fmt.Sprintf("backend chat: %v", err)))
			return
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/v1/chat/completions", bytes.NewReader(payload))
		if err != nil {
			sendStreamError(ctx, errs, serviceUnavailable("backend_chat_unavailable", fmt.Sprintf("backend chat: %v", err)))
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if s.apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)
		}

		resp, err := s.client.Do(httpReq)
		if err != nil {
			sendStreamError(ctx, errs, serviceUnavailable("backend_chat_unavailable", fmt.Sprintf("backend chat: %v", err)))
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			sendStreamError(ctx, errs, backendChatStatusError(resp))
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				return
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(data), &chunk) != nil || len(chunk.Choices) == 0 {
				continue
			}
			if delta := chunk.Choices[0].Delta.Content; delta != "" {
				select {
				case deltas <- delta:
				case <-ctx.Done():
					return
				}
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			sendStreamError(ctx, errs, serviceUnavailable("backend_chat_unavailable", fmt.Sprintf("backend chat: %v", err)))
		}
	}()
	return deltas, errs
}

// backendChatStatusError maps a non-200 backend reply to a ServiceError.
func backendChatStatusError(resp *http.Response) error {
	msg := fmt.Sprintf("backend chat: %s", resp.Status)
	var payload struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if b, _ := io.ReadAll(resp.Body); len(b) > 0 && json.Unmarshal(b, &payload) == nil && payload.Error != nil && payload.Error.Message != "" {
		msg = payload.Error.Message
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return &ServiceError{Status: http.StatusTooManyRequests, Code: "rate_limit_exceeded", Message: msg}
	case resp.StatusCode >= http.StatusInternalServerError:
		return &ServiceError{Status: http.StatusServiceUnavailable, Code: "backend_chat_unavailable", Message: msg}
	default:
		return &ServiceError{Status: resp.StatusCode, Code: "backend_chat_error", Message: msg}
	}
}
