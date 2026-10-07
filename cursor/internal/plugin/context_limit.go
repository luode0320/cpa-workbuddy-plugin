package plugin

import (
	"encoding/json"
	"fmt"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/openai"
)

const (
	clientContextLimit       int64 = 1_000_000
	contextReserve           int64 = 16_384
	contextCountMethod             = "conservative_utf8_bytes_v1"
	maxExecutorEnvelopeBytes       = 4 << 20
	maxContextMessages             = 8192
	maxContextTools                = 256
)

type contextLimitError struct{ reason string }

func (err *contextLimitError) Error() string {
	return "Cursor client context policy (1000000 conservative budget units): " + err.reason
}

func (err *contextLimitError) Is(target error) bool { return target == openai.ErrInvalidRequest }

func contextExceeded(reason string) error { return &contextLimitError{reason: reason} }

func ValidateExecutorRequestSize(method string, size uint64) error {
	switch method {
	case "executor.execute", "executor.execute_stream", "executor.count_tokens":
		if size > maxExecutorEnvelopeBytes {
			return contextExceeded("executor envelope exceeds 4 MiB")
		}
	}
	return nil
}

type contextBudgetWire struct {
	Messages            []json.RawMessage `json:"messages"`
	Tools               []json.RawMessage `json:"tools"`
	MaxTokens           int64             `json:"max_tokens"`
	MaxCompletionTokens int64             `json:"max_completion_tokens"`
}

func decodeContextRequest(raw []byte) (executorRequest, openai.ChatRequest, int64, error) {
	var request executorRequest
	if len(raw) > maxExecutorEnvelopeBytes {
		return request, openai.ChatRequest{}, 0, contextExceeded("executor envelope exceeds 4 MiB")
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return request, openai.ChatRequest{}, 0, &openai.InvalidRequestError{Message: "decode executor request: " + err.Error()}
	}
	payload := request.Payload
	if len(payload) == 0 {
		payload = request.OriginalRequest
	}
	if int64(len(payload)) > clientContextLimit-contextReserve {
		return request, openai.ChatRequest{}, 0, contextExceeded("input JSON exceeds the conservative byte budget; reduce or compact the request")
	}
	var wire contextBudgetWire
	if err := json.Unmarshal(payload, &wire); err != nil {
		return request, openai.ChatRequest{}, 0, &openai.InvalidRequestError{Message: "decode context budget: " + err.Error()}
	}
	if wire.MaxTokens < 0 || wire.MaxCompletionTokens < 0 {
		return request, openai.ChatRequest{}, 0, &openai.InvalidRequestError{Message: "output token limits must be nonnegative integers"}
	}
	outputReserve := max(wire.MaxTokens, wire.MaxCompletionTokens)
	if outputReserve > clientContextLimit-contextReserve {
		return request, openai.ChatRequest{}, 0, contextExceeded("requested output reserve exceeds the client context budget")
	}
	if len(wire.Messages) > maxContextMessages || len(wire.Tools) > maxContextTools {
		return request, openai.ChatRequest{}, 0, contextExceeded("request exceeds 8192 messages or 256 tools")
	}
	if err := validateContextExpansion(wire.Messages, int64(len(payload)), clientContextLimit-contextReserve-outputReserve); err != nil {
		return request, openai.ChatRequest{}, 0, err
	}
	chat, err := openai.ParseChatRequest(payload)
	if err != nil {
		return request, openai.ChatRequest{}, 0, err
	}
	units := max(int64(len(payload)), contextInputBytes(chat)) + contextReserve
	if units > clientContextLimit-outputReserve {
		return request, openai.ChatRequest{}, 0, contextExceeded(fmt.Sprintf("input budget %d plus output reserve %d exceeds %d; reduce or compact the request", units, outputReserve, clientContextLimit))
	}
	chat.OutputByteLimit = outputReserve
	if wire.MaxTokens > 0 && wire.MaxCompletionTokens > 0 {
		chat.OutputByteLimit = min(wire.MaxTokens, wire.MaxCompletionTokens)
	}
	return request, chat, units, nil
}

// This admission budget is deliberately conservative, not an upstream tokenizer.
func contextInputBytes(chat openai.ChatRequest) int64 {
	units := int64(len(chat.System)) + int64(len(chat.Prompt)) + int64(len(chat.Transcript))*32
	for _, tool := range chat.Tools {
		units += int64(len(tool.Name)) + int64(len(tool.Description)) + int64(len(tool.Parameters)) + 256
	}
	for _, attachment := range chat.Attachments {
		units += int64(len(attachment.Name)) + int64(len(attachment.Content)) + 256
	}
	for _, image := range chat.Images {
		units += int64(len(image.Name)) + int64(len(image.MIMEType)) + max(int64(len(image.Data)), 65_536)
	}
	return units
}

func effectiveContextLength(native int64) int64 {
	if native <= 0 {
		return 0
	}
	return min(native, clientContextLimit)
}
