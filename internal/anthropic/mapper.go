package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"freebuff-unified/internal/freebuff"
	"freebuff-unified/internal/openai"
) // ToOpenAI converts an Anthropic /v1/messages request to an OpenAI-compatible chat request.
func ToOpenAI(req MessageRequest) (openai.ChatCompletionRequest, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return openai.ChatCompletionRequest{}, fmt.Errorf("model is required")
	}

	out := openai.ChatCompletionRequest{
		Model:    freebuff.CanonicalModelName(model),
		Messages: make([]openai.ChatMessage, 0),
		Stream:   req.Stream,
	}

	if req.MaxTokens > 0 {
		out.MaxTokens = &req.MaxTokens
	}

	if req.Temperature != nil {
		out.Temperature = req.Temperature
	} else if req.TopP != nil {
		out.TopP = req.TopP
	}

	for _, msg := range req.Messages {
		out.Messages = append(out.Messages, openai.ChatMessage{
			Role:    msg.Role,
			Content: extractTextContent(msg.Content),
		})
	}

	if req.System != nil {
		systemText := extractTextContent(req.System)
		if systemText != "" {
			out.Messages = append([]openai.ChatMessage{{
				Role:    "system",
				Content: systemText,
			}}, out.Messages...)
		}
	}

	return out, nil
}

func extractTextContent(content any) string {
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
		return strings.Join(parts, "\n\n")
	case map[string]any:
		if t, ok := v["text"].(string); ok {
			return t
		}
		b, _ := json.Marshal(v)
		return string(b)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// CountTokens returns a rough token count for the request.
func CountTokens(req MessageRequest) int {
	total := 0
	total += len(req.Model)
	for _, msg := range req.Messages {
		total += len(msg.Role) + estimateTextTokens(msg.Content)
	}
	if req.System != nil {
		total += estimateTextTokens(req.System)
	}
	return total
}

func estimateTextTokens(content any) int {
	text := ""
	switch v := content.(type) {
	case string:
		text = v
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					text += t
				}
			}
		}
	case map[string]any:
		if t, ok := v["text"].(string); ok {
			text = t
		}
	}
	// rough estimate: 1 token ~ 4 chars
	if text == "" {
		return 0
	}
	return len(text) / 4
}
