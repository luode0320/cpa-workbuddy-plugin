package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/openai"
)

func (handler *Handler) execute(ctx context.Context, raw []byte) (any, error) {
	request, chat, credentials, err := decodeExecution(raw)
	if err != nil {
		return nil, err
	}
	turn := openai.NewTurn("cursor/" + chat.Model)
	inputText := usageText(chat)
	defer func() {
		handler.usage.recordTokens(request.AuthID, turn.EstimatedUsage(inputText))
	}()
	if handler.cursor == nil {
		return nil, errors.New("Cursor client is unavailable")
	}
	result, err := handler.runCheckpointed(ctx, request, chat, credentials, func(event cursorproto.ServerEvent) error {
		switch event.Kind {
		case cursorproto.EventText:
			turn.AddText(event.Text)
		case cursorproto.EventImage:
			turn.AddImage(event.MIMEType, event.ImageData)
		case cursorproto.EventToolCall:
			turn.AddToolCall(event.ID, event.Name, event.Arguments)
		}
		return nil
	})
	if errors.Is(err, errOutputLimit) {
		turn.MarkLengthLimited()
	} else if err != nil {
		return nil, withExecutionResult(err, result)
	}
	payload, err := turn.Completion(inputText)
	if err != nil {
		return nil, err
	}
	return executorResponse{Payload: payload, Headers: http.Header{"content-type": []string{"application/json"}}}, nil
}

func (handler *Handler) executeStream(ctx context.Context, raw []byte) (any, error) {
	request, chat, credentials, err := decodeExecution(raw)
	if err != nil {
		return nil, err
	}
	if handler.cursor == nil || handler.emitter == nil || request.StreamID == "" {
		turn := openai.NewTurn("cursor/" + chat.Model)
		handler.usage.recordTokens(request.AuthID, turn.EstimatedUsage(usageText(chat)))
		return nil, errors.New("Cursor stream bridge is unavailable")
	}
	go handler.runStream(ctx, request, chat, credentials)
	return struct {
		Headers http.Header `json:"headers"`
	}{Headers: http.Header{"content-type": []string{"text/event-stream"}}}, nil
}

func (handler *Handler) runStream(parent context.Context, request executorRequest, chat openai.ChatRequest, credentials cursorauth.Credentials) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	defer cancel()
	turn := openai.NewTurn("cursor/" + chat.Model)
	done := false
	toolCallSeen := false
	inputText := usageText(chat)
	defer func() {
		handler.usage.recordTokens(request.AuthID, turn.EstimatedUsage(inputText))
	}()
	result, runErr := handler.runCheckpointed(ctx, request, chat, credentials, func(event cursorproto.ServerEvent) error {
		switch event.Kind {
		case cursorproto.EventText:
			chunk, err := turn.StreamChunk(event.Text)
			if err != nil {
				return err
			}
			return handler.emitter.Emit(ctx, request.StreamID, chunk)
		case cursorproto.EventImage:
			chunk, err := turn.StreamImage(event.MIMEType, event.ImageData)
			if err != nil {
				return err
			}
			return handler.emitter.Emit(ctx, request.StreamID, chunk)
		case cursorproto.EventDone:
			done = true
			chunk, err := turn.FinalChunk(inputText)
			if err != nil {
				return err
			}
			if err := handler.emitter.Emit(ctx, request.StreamID, chunk); err != nil {
				return err
			}
			return handler.emitter.Emit(ctx, request.StreamID, []byte("[DONE]"))
		case cursorproto.EventToolCall:
			chunk, err := turn.StreamToolCall(event.ID, event.Name, event.Arguments)
			if err != nil {
				return err
			}
			if err := handler.emitter.Emit(ctx, request.StreamID, chunk); err != nil {
				return err
			}
			toolCallSeen = true
			return nil
		case cursorproto.EventIgnored, cursorproto.EventThinking, cursorproto.EventTokens:
			return nil
		default:
			return nil
		}
	})
	limited := errors.Is(runErr, errOutputLimit)
	if limited {
		turn.MarkLengthLimited()
		runErr = nil
	}
	if runErr == nil && (toolCallSeen || limited) && !done {
		chunk, err := turn.FinalChunk(inputText)
		if err != nil {
			runErr = err
		} else if err = handler.emitter.Emit(ctx, request.StreamID, chunk); err != nil {
			runErr = err
		} else if err = handler.emitter.Emit(ctx, request.StreamID, []byte("[DONE]")); err != nil {
			runErr = err
		} else {
			done = true
		}
	}
	if runErr == nil && !done {
		runErr = errors.New("Cursor stream completed without turn end")
	}
	if closeErr := handler.emitter.Close(request.StreamID, withExecutionResult(runErr, result)); closeErr != nil {
		return
	}
}

func cursorTools(tools []openai.Tool) []cursorproto.ToolDefinition {
	result := make([]cursorproto.ToolDefinition, len(tools))
	for index, tool := range tools {
		result[index] = cursorproto.ToolDefinition{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters}
	}
	return result
}

func cursorImages(images []openai.Image) []cursorproto.ImageAttachment {
	result := make([]cursorproto.ImageAttachment, len(images))
	for index, image := range images {
		result[index] = cursorproto.ImageAttachment{Name: image.Name, MIMEType: image.MIMEType, Data: image.Data}
	}
	return result
}

func cursorAttachments(attachments []openai.Attachment) []cursorproto.FileAttachment {
	result := make([]cursorproto.FileAttachment, len(attachments))
	for index, attachment := range attachments {
		result[index] = cursorproto.FileAttachment{Name: attachment.Name, Content: attachment.Content}
	}
	return result
}

func usageText(chat openai.ChatRequest) string {
	size := len(chat.System) + len(chat.Prompt)
	for _, attachment := range chat.Attachments {
		size += len(attachment.Content)
	}
	for _, tool := range chat.Tools {
		size += len(tool.Name) + len(tool.Description) + len(tool.Parameters)
	}
	var text strings.Builder
	text.Grow(size)
	text.WriteString(chat.System)
	text.WriteString(chat.Prompt)
	for _, attachment := range chat.Attachments {
		text.WriteString(attachment.Content)
	}
	for _, tool := range chat.Tools {
		text.WriteString(tool.Name)
		text.WriteString(tool.Description)
		text.Write(tool.Parameters)
	}
	return text.String()
}

func decodeExecution(raw []byte) (executorRequest, openai.ChatRequest, cursorauth.Credentials, error) {
	request, chat, _, err := decodeContextRequest(raw)
	if err != nil {
		return executorRequest{}, openai.ChatRequest{}, cursorauth.Credentials{}, err
	}
	credentials, err := cursorauth.ParseCredentials(request.StorageJSON)
	if err != nil {
		return executorRequest{}, openai.ChatRequest{}, cursorauth.Credentials{}, err
	}
	if _, disabled := normalizedModelSet(credentials.DisabledModels)[chat.Model]; disabled {
		return executorRequest{}, openai.ChatRequest{}, cursorauth.Credentials{}, &openai.InvalidRequestError{
			Message: fmt.Sprintf("Cursor model %q is disabled by plugin configuration", chat.Model),
		}
	}
	if err := chat.ValidateToolProgress(credentials.ToolLoopGuardTools); err != nil {
		return executorRequest{}, openai.ChatRequest{}, cursorauth.Credentials{}, err
	}
	return request, chat, credentials, nil
}

func countTokens(raw []byte) (any, error) {
	_, _, tokens, err := decodeContextRequest(raw)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(struct {
		TotalTokens        int64  `json:"total_tokens"`
		Estimated          bool   `json:"estimated"`
		CountMethod        string `json:"count_method"`
		ClientContextLimit int64  `json:"client_context_limit"`
	}{tokens, true, contextCountMethod, clientContextLimit})
	if err != nil {
		return nil, fmt.Errorf("encode token count: %w", err)
	}
	return executorResponse{Payload: encoded, Headers: http.Header{"content-type": []string{"application/json"}}}, nil
}
