package plugin

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorapi"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
)

type cursorRunStep struct {
	events  []cursorproto.ServerEvent
	result  cursorapi.RunResult
	err     error
	entered chan struct{}
	release <-chan struct{}
}

type recordingCursorClient struct {
	mu     sync.Mutex
	inputs []cursorapi.RunInput
	steps  []cursorRunStep
}

func (client *recordingCursorClient) Run(
	ctx context.Context,
	input cursorapi.RunInput,
	emit func(cursorproto.ServerEvent) error,
) (cursorapi.RunResult, error) {
	client.mu.Lock()
	index := len(client.inputs)
	client.inputs = append(client.inputs, cloneRunInput(input))
	step := cursorRunStep{}
	if index < len(client.steps) {
		step = client.steps[index]
	}
	client.mu.Unlock()
	if step.entered != nil {
		close(step.entered)
	}
	if step.release != nil {
		select {
		case <-step.release:
		case <-ctx.Done():
			return step.result, context.Cause(ctx)
		}
	}
	for _, event := range step.events {
		if err := emit(event); err != nil {
			return step.result, err
		}
	}
	return step.result, step.err
}

func (*recordingCursorClient) DiscoverModels(context.Context, string) ([]string, error) {
	return []string{"auto"}, nil
}

func (client *recordingCursorClient) Inputs() []cursorapi.RunInput {
	client.mu.Lock()
	defer client.mu.Unlock()
	inputs := make([]cursorapi.RunInput, len(client.inputs))
	for index, input := range client.inputs {
		inputs[index] = cloneRunInput(input)
	}
	return inputs
}

func cloneRunInput(input cursorapi.RunInput) cursorapi.RunInput {
	input.Checkpoint = append([]byte(nil), input.Checkpoint...)
	input.Tools = append([]cursorproto.ToolDefinition(nil), input.Tools...)
	input.Images = append([]cursorproto.ImageAttachment(nil), input.Images...)
	input.Attachments = append([]cursorproto.FileAttachment(nil), input.Attachments...)
	return input
}

func successfulTextStep(text, conversation string, checkpoint []byte) cursorRunStep {
	return cursorRunStep{
		events: []cursorproto.ServerEvent{
			{Kind: cursorproto.EventText, Text: text},
			{Kind: cursorproto.EventDone},
		},
		result: cursorapi.RunResult{
			ConversationID: conversation,
			Checkpoint:     append([]byte(nil), checkpoint...),
			OutputExposed:  true,
		},
	}
}

func executorFixture(
	t *testing.T,
	session string,
	accountID string,
	authID string,
	model string,
	streamID string,
	messages []map[string]any,
) []byte {
	t.Helper()
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "rotating-access-token", RefreshToken: "refresh-token", AccountID: accountID, Type: "cursor-provider",
	})
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{
		"model": "cursor/" + model, "stream": streamID != "", "messages": messages,
	})
	require.NoError(t, err)
	request := executorRequest{
		AuthID: authID, StreamID: streamID, StorageJSON: credentials, Payload: payload,
		Metadata: executorMetadata{ExecutionSessionID: optionalIdentity(session)},
	}
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	return raw
}

func rotateFixtureAccessToken(t *testing.T, raw []byte, accessToken string) []byte {
	t.Helper()
	var request executorRequest
	require.NoError(t, json.Unmarshal(raw, &request))
	credentials, err := cursorauth.ParseCredentials(request.StorageJSON)
	require.NoError(t, err)
	credentials.AccessToken = accessToken
	request.StorageJSON, err = cursorauth.MarshalCredentials(credentials)
	require.NoError(t, err)
	updated, err := json.Marshal(request)
	require.NoError(t, err)
	return updated
}

func replaceFixtureAuthID(t *testing.T, raw []byte, authID string) []byte {
	t.Helper()
	var request executorRequest
	require.NoError(t, json.Unmarshal(raw, &request))
	request.AuthID = authID
	updated, err := json.Marshal(request)
	require.NoError(t, err)
	return updated
}

func replayBytes(input cursorapi.RunInput) int {
	bytes := len(input.System) + len(input.Prompt)
	for _, image := range input.Images {
		bytes += len(image.Data)
	}
	for _, attachment := range input.Attachments {
		bytes += len(attachment.Content)
	}
	return bytes
}

func textMessage(role, content string) map[string]any {
	return map[string]any{"role": role, "content": content}
}

func observeRequestAuth(t *testing.T, handler *Handler, requestID, authID string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"RequestID": requestID,
		"Metadata":  map[string]any{"selected_auth_id": authID},
	})
	require.NoError(t, err)
	_, err = handler.dispatch(context.Background(), "request.intercept_after", raw)
	require.NoError(t, err)
}

func completeCursorRequest(t *testing.T, handler *Handler, requestID, outcome string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"RequestID": requestID, "Outcome": outcome})
	require.NoError(t, err)
	_, err = handler.dispatch(context.Background(), "request.complete", raw)
	require.NoError(t, err)
}

func completeCursorRequestForAuth(t *testing.T, handler *Handler, requestID, authID, outcome string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"RequestID": requestID,
		"Outcome":   outcome,
		"Metadata":  map[string]any{"selected_auth_id": authID},
	})
	require.NoError(t, err)
	_, err = handler.dispatch(context.Background(), "request.complete", raw)
	require.NoError(t, err)
}
