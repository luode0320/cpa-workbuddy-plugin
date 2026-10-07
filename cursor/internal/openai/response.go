package openai

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type Turn struct {
	id            string
	model         string
	created       int64
	text          string
	tools         []responseToolCall
	images        []responseImage
	toolIDOwners  map[string]string
	responseErr   error
	lengthLimited bool
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatCompletion struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   Usage              `json:"usage"`
}

type completionChoice struct {
	Index        int               `json:"index"`
	Message      *assistantMessage `json:"message,omitempty"`
	Delta        *assistantMessage `json:"delta,omitempty"`
	FinishReason *string           `json:"finish_reason"`
}

type assistantMessage struct {
	Role      string             `json:"role,omitempty"`
	Content   string             `json:"content,omitempty"`
	ToolCalls []responseToolCall `json:"tool_calls,omitempty"`
	Images    []responseImage    `json:"images,omitempty"`
}

type responseImage struct {
	Index    int              `json:"index"`
	Type     string           `json:"type"`
	ImageURL responseImageURL `json:"image_url"`
}

type responseImageURL struct {
	URL string `json:"url"`
}

type responseToolCall struct {
	Index    *int                 `json:"index,omitempty"`
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function responseToolFunction `json:"function"`
}

type responseToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func NewTurn(model string) *Turn {
	return &Turn{id: "chatcmpl-" + randomHex(12), model: model, created: time.Now().Unix()}
}

func (turn *Turn) MarkLengthLimited() { turn.lengthLimited = true }

func (turn *Turn) StreamChunk(text string) ([]byte, error) {
	turn.text += text
	payload := chatCompletion{
		ID:      turn.id,
		Object:  "chat.completion.chunk",
		Created: turn.created,
		Model:   turn.model,
		Choices: []completionChoice{{
			Index: 0,
			Delta: &assistantMessage{Role: "assistant", Content: text},
		}},
	}
	return marshalStreamPayload(payload)
}

func (turn *Turn) StreamToolCall(id, name, arguments string) ([]byte, error) {
	id, err := turn.reserveToolCallID(id)
	if err != nil {
		return nil, err
	}
	index := len(turn.tools)
	call := responseToolCall{
		ID: id, Type: "function", Function: responseToolFunction{Name: name, Arguments: arguments},
	}
	turn.tools = append(turn.tools, call)
	streamCall := call
	streamCall.Index = &index
	payload := chatCompletion{
		ID: turn.id, Object: "chat.completion.chunk", Created: turn.created, Model: turn.model,
		Choices: []completionChoice{{Index: 0, Delta: &assistantMessage{Role: "assistant", ToolCalls: []responseToolCall{streamCall}}}},
	}
	return marshalStreamPayload(payload)
}

func (turn *Turn) StreamImage(mimeType string, data []byte) ([]byte, error) {
	image, err := newResponseImage(len(turn.images), mimeType, data)
	if err != nil {
		return nil, err
	}
	turn.images = append(turn.images, image)
	payload := chatCompletion{
		ID: turn.id, Object: "chat.completion.chunk", Created: turn.created, Model: turn.model,
		Choices: []completionChoice{{Index: 0, Delta: &assistantMessage{Role: "assistant", Images: []responseImage{image}}}},
	}
	return marshalStreamPayload(payload)
}

func (turn *Turn) FinalChunk(prompt string) ([]byte, error) {
	if err := turn.validateOutput(); err != nil {
		return nil, err
	}
	finish := turn.finishReason()
	payload := chatCompletion{
		ID:      turn.id,
		Object:  "chat.completion.chunk",
		Created: turn.created,
		Model:   turn.model,
		Choices: []completionChoice{{Index: 0, Delta: &assistantMessage{}, FinishReason: &finish}},
		Usage:   turn.EstimatedUsage(prompt),
	}
	return marshalStreamPayload(payload)
}

func (turn *Turn) Completion(prompt string) ([]byte, error) {
	if err := turn.validateOutput(); err != nil {
		return nil, err
	}
	finish := turn.finishReason()
	payload := chatCompletion{
		ID:      turn.id,
		Object:  "chat.completion",
		Created: turn.created,
		Model:   turn.model,
		Choices: []completionChoice{{
			Index:        0,
			Message:      &assistantMessage{Role: "assistant", Content: turn.text, ToolCalls: turn.tools, Images: turn.images},
			FinishReason: &finish,
		}},
		Usage: turn.EstimatedUsage(prompt),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode OpenAI completion: %w", err)
	}
	return raw, nil
}

func (turn *Turn) AddText(text string) {
	turn.text += text
}

func (turn *Turn) AddToolCall(id, name, arguments string) {
	id, err := turn.reserveToolCallID(id)
	if err != nil {
		if turn.responseErr == nil {
			turn.responseErr = err
		}
		return
	}
	turn.tools = append(turn.tools, responseToolCall{
		ID: id, Type: "function", Function: responseToolFunction{Name: name, Arguments: arguments},
	})
}

func (turn *Turn) AddImage(mimeType string, data []byte) {
	image, err := newResponseImage(len(turn.images), mimeType, data)
	if err != nil {
		if turn.responseErr == nil {
			turn.responseErr = err
		}
		return
	}
	turn.images = append(turn.images, image)
}

func (turn *Turn) validateOutput() error {
	if turn.responseErr != nil {
		return turn.responseErr
	}
	if !turn.lengthLimited && strings.TrimSpace(turn.text) == "" && len(turn.tools) == 0 && len(turn.images) == 0 {
		return fmt.Errorf("Cursor response has no text or tool calls (and no images)")
	}
	return nil
}

func newResponseImage(index int, mimeType string, data []byte) (responseImage, error) {
	mimeType = strings.TrimSpace(mimeType)
	if !strings.HasPrefix(mimeType, "image/") || len(data) == 0 {
		return responseImage{}, fmt.Errorf("Cursor generated image requires an image MIME type and data")
	}
	return responseImage{
		Index: index,
		Type:  "image_url",
		ImageURL: responseImageURL{
			URL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data),
		},
	}, nil
}

func (turn *Turn) EstimatedUsage(prompt string) Usage {
	var completion strings.Builder
	completion.WriteString(turn.text)
	for _, call := range turn.tools {
		completion.WriteString(call.ID)
		completion.WriteString(call.Function.Name)
		completion.WriteString(call.Function.Arguments)
	}
	return estimatedUsage(prompt, completion.String())
}

func (turn *Turn) finishReason() string {
	if turn.lengthLimited {
		return "length"
	}
	if len(turn.tools) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func marshalStreamPayload(payload chatCompletion) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode OpenAI stream chunk: %w", err)
	}
	return raw, nil
}

func estimatedUsage(prompt, completion string) Usage {
	promptTokens := max(1, utf8.RuneCountInString(prompt)/4)
	completionTokens := max(1, utf8.RuneCountInString(completion)/4)
	return Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
}

func randomHex(bytesCount int) string {
	buffer := make([]byte, bytesCount)
	if _, err := rand.Read(buffer); err != nil {
		return "cursor"
	}
	return hex.EncodeToString(buffer)
}
