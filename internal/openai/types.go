package openai

import (
	"fmt"
	"time"
)

// ChatCompletionRequest is the OpenAI-compatible chat completion request.
type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Stream      bool          `json:"stream,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	TopP        *float64      `json:"top_p,omitempty"`
	Stop        any           `json:"stop,omitempty"`
	Tools       []ChatTool    `json:"tools,omitempty"`
	ToolChoice  any           `json:"tool_choice,omitempty"`
}

// ChatTool is a tool definition in a chat completion request.
type ChatTool struct {
	Type     string       `json:"type,omitempty"`
	Function ChatFunction `json:"function,omitempty"`
}

// ChatFunction is a function tool definition.
type ChatFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// ChatMessage is a single message in a chat completion request.
type ChatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// StreamMetadata tracks event ID and counter for SSE streams.
type StreamMetadata struct {
	eventID int
}

// NewStreamMetadata returns a new StreamMetadata.
func NewStreamMetadata() StreamMetadata {
	return StreamMetadata{}
}

// ChunkFromDeltaWithMetadata builds an SSE chunk with event ID tracking.
func ChunkFromDeltaWithMetadata(model, delta string, meta *StreamMetadata) map[string]any {
	meta.eventID++
	return map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", meta.eventID),
		"object":  "chat.completion.chunk",
		"created": currentUnix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"role":    "assistant",
					"content": delta,
				},
			},
		},
	}
}

func currentUnix() int64 {
	return time.Now().Unix()
}

// Models returns an OpenAI-compatible model list response.
func Models(defaultModel string) map[string]any {
	return map[string]any{
		"object": "list",
		"data": []map[string]any{
			{
				"id":       defaultModel,
				"object":   "model",
				"owned_by": "freebuff-unified",
			},
		},
	}
}

// Error returns an OpenAI-compatible error response.
func Error(status int, code, message string) map[string]any {
	return map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    code,
		},
	}
}

// CompletionFromText builds a chat completion response from a text answer.
func CompletionFromText(model, text string) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-unified",
		"object":  "chat.completion",
		"created": currentUnix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": text,
				},
				"finish_reason": "stop",
			},
		},
	}
}

// ChunkFromDelta builds an SSE chunk from a delta string.
func ChunkFromDelta(model, delta string) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-unified",
		"object":  "chat.completion.chunk",
		"created": currentUnix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"role":    "assistant",
					"content": delta,
				},
			},
		},
	}
}
