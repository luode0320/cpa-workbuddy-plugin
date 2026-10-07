package plugin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"

	"github.com/stretchr/testify/require"
)

func Test_Handler_ExecuteStream_emits_openai_chunks_and_closes(t *testing.T) {
	// Given
	emitter := &captureEmitter{done: make(chan struct{})}
	handler := NewHandler(Dependencies{Cursor: fakeCursorClient{}, Emitter: emitter})
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken:  "access",
		RefreshToken: "refresh",
		Type:         "cursor-provider",
	})
	require.NoError(t, err)
	request := executorRequest{
		AuthID:      "cursor-auth-id",
		StreamID:    "stream-1",
		StorageJSON: credentials,
		Payload:     []byte(`{"model":"cursor/auto","stream":true,"messages":[{"role":"user","content":"say ok"}]}`),
	}
	rawRequest, err := json.Marshal(request)
	require.NoError(t, err)

	// When
	observeRequestAuth(t, handler, "request-stream", "cursor-auth-id")
	raw := handler.Call(context.Background(), "executor.execute_stream", rawRequest)
	<-emitter.done
	completeCursorRequest(t, handler, "request-stream", "succeeded")

	// Then
	var response envelope
	require.NoError(t, json.Unmarshal(raw, &response))
	require.True(t, response.OK)
	require.NoError(t, emitter.closeError)
	require.Len(t, emitter.payloads, 3)
	require.Contains(t, string(emitter.payloads[0]), "cursor-plugin-ok")
	require.Equal(t, "[DONE]", string(emitter.payloads[2]))
	usage := handler.usage.snapshot("cursor-auth-id")
	require.EqualValues(t, 1, usage.Requests)
	require.EqualValues(t, 1, usage.Succeeded)
	require.Zero(t, usage.Failed)
}

func Test_Handler_Execute_returns_tool_call_and_forwards_tool_catalog(t *testing.T) {
	// Given
	cursor := &toolCursorClient{id: joinedToolCallID}
	handler := NewHandler(Dependencies{Cursor: cursor})
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access", RefreshToken: "refresh", Type: "cursor-provider",
	})
	require.NoError(t, err)
	request := executorRequest{
		StorageJSON: credentials,
		Payload:     []byte(`{"model":"cursor/auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"text","text":"call a tool"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`),
	}
	rawRequest, err := json.Marshal(request)
	require.NoError(t, err)

	// When
	raw := handler.Call(context.Background(), "executor.execute", rawRequest)

	// Then
	var response envelope
	require.NoError(t, json.Unmarshal(raw, &response))
	require.True(t, response.OK)
	var result executorResponse
	require.NoError(t, json.Unmarshal(response.Result, &result))
	require.Contains(t, string(result.Payload), `tool_calls`)
	var completion struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(result.Payload, &completion))
	require.Equal(t, normalizedJoinedToolCallID, completion.Choices[0].Message.ToolCalls[0].ID)
	require.Len(t, cursor.input.Tools, 1)
	require.Len(t, cursor.input.Images, 1)
}

func Test_Handler_ExecuteStream_emits_tool_call_and_tool_finish_reason(t *testing.T) {
	emitter := &captureEmitter{done: make(chan struct{})}
	handler := NewHandler(Dependencies{Cursor: &toolCursorClient{id: joinedToolCallID}, Emitter: emitter})
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access", RefreshToken: "refresh", Type: "cursor-provider",
	})
	require.NoError(t, err)
	request := executorRequest{
		StreamID: "stream-tool", StorageJSON: credentials,
		Payload: []byte(`{"model":"cursor/auto","stream":true,"messages":[{"role":"user","content":"call a tool"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`),
	}
	rawRequest, err := json.Marshal(request)
	require.NoError(t, err)

	raw := handler.Call(context.Background(), "executor.execute_stream", rawRequest)
	<-emitter.done

	var response envelope
	require.NoError(t, json.Unmarshal(raw, &response))
	require.True(t, response.OK)
	require.NoError(t, emitter.closeError)
	require.Len(t, emitter.payloads, 3)
	require.Contains(t, string(emitter.payloads[0]), `"tool_calls"`)
	require.Contains(t, string(emitter.payloads[0]), `"id":"`+normalizedJoinedToolCallID+`"`)
	require.NotContains(t, string(emitter.payloads[0]), `fc_`)
	require.Contains(t, string(emitter.payloads[1]), `"finish_reason":"tool_calls"`)
	require.Equal(t, "[DONE]", string(emitter.payloads[2]))
}

func Test_Handler_Execute_returns_generated_image(t *testing.T) {
	handler := NewHandler(Dependencies{Cursor: imageCursorClient{}})
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access", RefreshToken: "refresh", Type: "cursor-provider",
	})
	require.NoError(t, err)
	request := executorRequest{
		StorageJSON: credentials,
		Payload:     []byte(`{"model":"cursor/grok-4.6","messages":[{"role":"user","content":"draw a fox"}]}`),
	}
	rawRequest, err := json.Marshal(request)
	require.NoError(t, err)

	raw := handler.Call(context.Background(), "executor.execute", rawRequest)

	var response envelope
	require.NoError(t, json.Unmarshal(raw, &response))
	require.True(t, response.OK)
	var result executorResponse
	require.NoError(t, json.Unmarshal(response.Result, &result))
	var completion struct {
		Choices []struct {
			Message struct {
				Images []struct {
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"images"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(result.Payload, &completion))
	require.Len(t, completion.Choices, 1)
	require.Len(t, completion.Choices[0].Message.Images, 1)
	require.Equal(t, "data:image/png;base64,aW1hZ2U=", completion.Choices[0].Message.Images[0].ImageURL.URL)
}

func Test_Handler_ExecuteStream_emits_generated_image_and_done(t *testing.T) {
	emitter := &captureEmitter{done: make(chan struct{})}
	handler := NewHandler(Dependencies{Cursor: imageCursorClient{}, Emitter: emitter})
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access", RefreshToken: "refresh", Type: "cursor-provider",
	})
	require.NoError(t, err)
	request := executorRequest{
		StreamID: "stream-image", StorageJSON: credentials,
		Payload: []byte(`{"model":"cursor/grok-4.6","stream":true,"messages":[{"role":"user","content":"draw a fox"}]}`),
	}
	rawRequest, err := json.Marshal(request)
	require.NoError(t, err)

	raw := handler.Call(context.Background(), "executor.execute_stream", rawRequest)
	<-emitter.done

	var response envelope
	require.NoError(t, json.Unmarshal(raw, &response))
	require.True(t, response.OK)
	require.NoError(t, emitter.closeError)
	require.Len(t, emitter.payloads, 3)
	require.Contains(t, string(emitter.payloads[0]), `"images"`)
	require.Contains(t, string(emitter.payloads[0]), `data:image/png;base64,aW1hZ2U=`)
	require.Contains(t, string(emitter.payloads[1]), `"finish_reason":"stop"`)
	require.Equal(t, "[DONE]", string(emitter.payloads[2]))
}

func Test_Handler_Execute_rejects_empty_cursor_completion(t *testing.T) {
	handler := NewHandler(Dependencies{Cursor: emptyCursorClient{}})
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access", RefreshToken: "refresh", Type: "cursor-provider",
	})
	require.NoError(t, err)
	request := executorRequest{
		StorageJSON: credentials,
		Payload:     []byte(`{"model":"cursor/auto","messages":[{"role":"user","content":"hello"}]}`),
	}
	rawRequest, err := json.Marshal(request)
	require.NoError(t, err)

	raw := handler.Call(context.Background(), "executor.execute", rawRequest)

	var response envelope
	require.NoError(t, json.Unmarshal(raw, &response))
	require.False(t, response.OK)
	require.Contains(t, response.Error.Message, "no text or tool calls")
}

func Test_Handler_ExecuteStream_rejects_empty_cursor_completion(t *testing.T) {
	emitter := &captureEmitter{done: make(chan struct{})}
	handler := NewHandler(Dependencies{Cursor: emptyCursorClient{}, Emitter: emitter})
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access", RefreshToken: "refresh", Type: "cursor-provider",
	})
	require.NoError(t, err)
	request := executorRequest{
		StreamID: "stream-empty", StorageJSON: credentials,
		Payload: []byte(`{"model":"cursor/auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
	}
	rawRequest, err := json.Marshal(request)
	require.NoError(t, err)

	raw := handler.Call(context.Background(), "executor.execute_stream", rawRequest)
	<-emitter.done

	var response envelope
	require.NoError(t, json.Unmarshal(raw, &response))
	require.True(t, response.OK)
	require.ErrorContains(t, emitter.closeError, "no text or tool calls")
	require.Empty(t, emitter.payloads)
}

func Test_Handler_Execute_rejects_model_disabled_by_cursor_plugin(t *testing.T) {
	handler := NewHandler(Dependencies{Cursor: fakeCursorClient{}})
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken:    "access",
		RefreshToken:   "refresh",
		Type:           "cursor-provider",
		DisabledModels: []string{"gpt-5"},
	})
	require.NoError(t, err)
	request := executorRequest{
		StorageJSON: credentials,
		Payload:     []byte(`{"model":"cursor/gpt-5","messages":[{"role":"user","content":"hello"}]}`),
	}
	rawRequest, err := json.Marshal(request)
	require.NoError(t, err)

	raw := handler.Call(context.Background(), "executor.execute", rawRequest)

	var response envelope
	require.NoError(t, json.Unmarshal(raw, &response))
	require.False(t, response.OK)
	require.Equal(t, 400, response.Error.HTTPStatus)
	require.Contains(t, response.Error.Message, "disabled")
}
