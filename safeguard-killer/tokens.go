package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

// promptKeys are the request fields that carry prompt content across the client protocols
// (OpenAI chat/responses, Claude messages, Gemini contents).
var promptKeys = []string{"system", "messages", "tools", "tool_choice", "functions", "input", "instructions", "prompt", "contents", "systemInstruction", "system_instruction"}

var (
	tokenCodecOnce sync.Once
	tokenCodec     tokenizer.Codec
	tokenCodecErr  error
)

type countTokensResponse struct {
	InputTokens int `json:"input_tokens"`
}

// countGroupTokens estimates prompt tokens for a group request locally with o200k_base, the
// same estimate CPA uses for providers without a native count endpoint. A group has no single
// upstream to ask, and the host offers no count callback for plugin executors.
func countGroupTokens(raw []byte, cfg pluginConfig) ([]byte, error) {
	var req executorRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if _, ok := cfg.group(req.Model); !ok {
		return nil, fmt.Errorf("model group %s is not configured", req.Model)
	}
	count, errCount := countPromptTokens(req.Payload)
	if errCount != nil {
		return nil, errCount
	}
	payload, errMarshal := json.Marshal(countTokensResponse{InputTokens: count})
	if errMarshal != nil {
		return nil, errMarshal
	}
	return okEnvelope(executorResponse{Payload: payload})
}

func countPromptTokens(payload []byte) (int, error) {
	var body map[string]any
	if errUnmarshal := json.Unmarshal(payload, &body); errUnmarshal != nil {
		return 0, fmt.Errorf("decode count_tokens body: %w", errUnmarshal)
	}
	var segments []string
	for _, key := range promptKeys {
		if value, ok := body[key]; ok {
			collectText(value, &segments)
		}
	}
	text := strings.TrimSpace(strings.Join(segments, "\n"))
	if text == "" {
		return 0, nil
	}
	tokenCodecOnce.Do(func() {
		tokenCodec, tokenCodecErr = tokenizer.Get(tokenizer.O200kBase)
	})
	if tokenCodecErr != nil {
		return 0, fmt.Errorf("load o200k_base tokenizer: %w", tokenCodecErr)
	}
	count, errCount := tokenCodec.Count(text)
	if errCount != nil {
		return 0, fmt.Errorf("count tokens: %w", errCount)
	}
	return count, nil
}

// collectText gathers every string in a JSON value, keys included for objects (tool schemas
// and argument names are part of the prompt), in a stable order.
func collectText(value any, segments *[]string) {
	switch typed := value.(type) {
	case string:
		if typed != "" && !hasNoticePrefix(typed, activeNoticePrefixes(currentConfig())) {
			*segments = append(*segments, typed)
		}
	case []any:
		for _, item := range typed {
			collectText(item, segments)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			switch key {
			case "type", "role", "id", "tool_use_id", "tool_call_id", "cache_control", "signature":
				// Protocol bookkeeping, not prompt content.
				continue
			case "content", "text", "parts":
				// Containers: their values are the prompt, the key itself is not.
			default:
				*segments = append(*segments, key)
			}
			collectText(typed[key], segments)
		}
	}
}
