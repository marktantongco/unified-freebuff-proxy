package anthropic

// CountTokensResponse is the response from /v1/messages/count_tokens.
type CountTokensResponse struct {
	InputTokens int `json:"input_tokens"`
}

// MessageRequest is the Anthropic /v1/messages request.
type MessageRequest struct {
	Model         string    `json:"model"`
	Messages      []Message `json:"messages"`
	MaxTokens     int       `json:"max_tokens,omitempty"`
	StopSequences []string  `json:"stop_sequences,omitempty"`
	System        any       `json:"system,omitempty"`
	Tools         []Tool    `json:"tools,omitempty"`
	ToolChoice    any       `json:"tool_choice,omitempty"`
	Stream        bool      `json:"stream,omitempty"`
	Temperature   *float64  `json:"temperature,omitempty"`
	TopP          *float64  `json:"top_p,omitempty"`
	Thinking      any       `json:"thinking,omitempty"`
	User          string    `json:"user,omitempty"`
}

// Message is a single message in an Anthropic request.
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// Tool is an Anthropic tool definition.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema,omitempty"`
}

// Error returns an Anthropic-compatible error response.
func Error(code, message string) map[string]any {
	return map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    code,
			"message": message,
		},
	}
}

// MessageFromText builds a non-stream Anthropic message response from text.
func MessageFromText(model, text string, inputTokens int) map[string]any {
	return map[string]any{
		"id":    "msg-unified",
		"type":  "message",
		"role":  "assistant",
		"model": model,
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": len(text),
		},
	}
}

// StreamStartMessage returns the message_start event payload.
func StreamStartMessage(model string, inputTokens int) map[string]any {
	return map[string]any{
		"id":            "msg-unified",
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       []any{},
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": 0,
		},
	}
}

// ContentBlock represents a content block for content_block_start events.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Usage represents token usage for message_delta events.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}
