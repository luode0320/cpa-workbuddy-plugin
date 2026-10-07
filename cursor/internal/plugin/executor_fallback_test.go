package plugin

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorapi"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
)

func Test_Checkpoint_Stream_and_media_continuation_preserve_suffix(t *testing.T) {
	client := &recordingCursorClient{steps: []cursorRunStep{
		successfulTextStep("seed-answer", "conversation", []byte("checkpoint-seed")),
		successfulTextStep("stream-answer", "conversation", []byte("checkpoint-next")),
	}}
	emitter := &captureEmitter{done: make(chan struct{})}
	handler := NewHandler(Dependencies{Cursor: client, Emitter: emitter})
	_, err := handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{textMessage("user", "seed")}))
	require.NoError(t, err)
	media := map[string]any{"role": "user", "content": []map[string]any{
		{"type": "text", "text": "inspect suffix"},
		{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,aW1hZ2U="}},
		{"type": "file", "file": map[string]any{"filename": "note.txt", "file_data": "data:text/plain;base64,bm90ZQ=="}},
	}}
	raw := executorFixture(t, "session", "account", "auth", "auto", "stream-media", []map[string]any{
		textMessage("user", "seed"), textMessage("assistant", "seed-answer"), media,
	})

	_, err = handler.executeStream(context.Background(), raw)
	require.NoError(t, err)
	<-emitter.done

	require.NoError(t, emitter.closeError)
	inputs := client.Inputs()
	require.Len(t, inputs, 2)
	require.Equal(t, cursorproto.CheckpointSuffix, inputs[1].Mode)
	require.Len(t, inputs[1].Images, 1)
	require.Len(t, inputs[1].Attachments, 1)
	require.Equal(t, "note.txt", inputs[1].Attachments[0].Name)
	require.NotContains(t, inputs[1].Prompt, "seed")
	require.Contains(t, inputs[1].Prompt, "inspect suffix")
}

func Test_Fallback_InvalidArgument_retries_once_only_when_safe(t *testing.T) {
	client := &recordingCursorClient{steps: []cursorRunStep{
		successfulTextStep("seed-answer", "conversation", []byte("checkpoint-seed")),
		{result: cursorapi.RunResult{ConversationID: "conversation"}, err: fmt.Errorf("checkpoint rejected: %w", cursorapi.ErrInvalidArgument)},
		successfulTextStep("fallback-answer", "fresh-conversation", []byte("checkpoint-fresh")),
	}}
	handler := NewHandler(Dependencies{Cursor: client})
	_, err := handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{textMessage("user", "seed")}))
	require.NoError(t, err)
	continuation := executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{
		textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "next"),
	})

	_, err = handler.execute(context.Background(), continuation)

	require.NoError(t, err)
	inputs := client.Inputs()
	require.Len(t, inputs, 3)
	require.Equal(t, cursorproto.CheckpointSuffix, inputs[1].Mode)
	require.Equal(t, cursorproto.FullReplay, inputs[2].Mode)
	require.Empty(t, inputs[2].Checkpoint)
	require.Empty(t, inputs[2].ConversationID)
	require.Contains(t, inputs[2].Prompt, "seed")
}

func Test_Fallback_EmptyCompletion_retries_fresh_when_checkpoint_suffix(t *testing.T) {
	// Given
	client := &recordingCursorClient{steps: []cursorRunStep{
		successfulTextStep("seed-answer", "conversation", []byte("checkpoint-seed")),
		{result: cursorapi.RunResult{ConversationID: "conversation"}, err: cursorapi.ErrEmptyCompletion},
		successfulTextStep("fallback-answer", "fresh-conversation", []byte("checkpoint-fresh")),
	}}
	handler := NewHandler(Dependencies{Cursor: client})
	_, err := handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{textMessage("user", "seed")}))
	require.NoError(t, err)
	continuation := executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{
		textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "next"),
	})

	// When
	_, err = handler.execute(context.Background(), continuation)

	// Then
	require.NoError(t, err)
	inputs := client.Inputs()
	require.Len(t, inputs, 3)
	require.Equal(t, cursorproto.CheckpointSuffix, inputs[1].Mode)
	require.Equal(t, cursorproto.FullReplay, inputs[2].Mode)
}

func Test_Fallback_InternalStream_retries_fresh_when_checkpoint_suffix(t *testing.T) {
	client := &recordingCursorClient{steps: []cursorRunStep{
		successfulTextStep("seed-answer", "conversation", []byte("checkpoint-seed")),
		{result: cursorapi.RunResult{ConversationID: "conversation"}, err: cursorapi.ErrInternalStream},
		successfulTextStep("fallback-answer", "fresh-conversation", []byte("checkpoint-fresh")),
	}}
	handler := NewHandler(Dependencies{Cursor: client})
	_, err := handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{textMessage("user", "seed")}))
	require.NoError(t, err)
	continuation := executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{
		textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "next"),
	})

	_, err = handler.execute(context.Background(), continuation)

	require.NoError(t, err)
	inputs := client.Inputs()
	require.Len(t, inputs, 3)
	require.Equal(t, cursorproto.CheckpointSuffix, inputs[1].Mode)
	require.Equal(t, cursorproto.FullReplay, inputs[2].Mode)
}

func Test_Fallback_FailedPrecondition_retries_fresh_when_checkpoint_suffix_has_no_output(t *testing.T) {
	// Given
	client := &recordingCursorClient{steps: []cursorRunStep{
		successfulTextStep("seed-answer", "conversation", []byte("checkpoint-seed")),
		{result: cursorapi.RunResult{ConversationID: "conversation"}, err: cursorapi.ErrFailedPrecondition},
		successfulTextStep("fallback-answer", "fresh-conversation", []byte("checkpoint-fresh")),
	}}
	handler := NewHandler(Dependencies{Cursor: client})
	_, err := handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{textMessage("user", "seed")}))
	require.NoError(t, err)
	continuation := executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{
		textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "next"),
	})

	// When
	_, err = handler.execute(context.Background(), continuation)

	// Then
	require.NoError(t, err)
	inputs := client.Inputs()
	require.Len(t, inputs, 3)
	require.Equal(t, cursorproto.CheckpointSuffix, inputs[1].Mode)
	require.Equal(t, cursorproto.FullReplay, inputs[2].Mode)
}

func Test_Fallback_ProgressTimeout_retries_fresh_once_when_checkpoint_suffix_has_no_output(t *testing.T) {
	// Given
	client := &recordingCursorClient{steps: []cursorRunStep{
		successfulTextStep("seed-answer", "conversation", []byte("checkpoint-seed")),
		{result: cursorapi.RunResult{ConversationID: "conversation"}, err: cursorapi.ErrProgressTimeout},
		successfulTextStep("fallback-answer", "fresh-conversation", []byte("checkpoint-fresh")),
	}}
	handler := NewHandler(Dependencies{Cursor: client})
	_, err := handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{textMessage("user", "seed")}))
	require.NoError(t, err)
	continuation := executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{
		textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "next"),
	})

	// When
	_, err = handler.execute(context.Background(), continuation)

	// Then
	require.NoError(t, err)
	inputs := client.Inputs()
	require.Len(t, inputs, 3)
	require.Equal(t, cursorproto.CheckpointSuffix, inputs[1].Mode)
	require.Equal(t, cursorproto.FullReplay, inputs[2].Mode)
}

func Test_Fallback_InvalidArgument_does_not_retry_the_fresh_replay(t *testing.T) {
	client := &recordingCursorClient{steps: []cursorRunStep{
		successfulTextStep("seed-answer", "conversation", []byte("checkpoint-seed")),
		{err: fmt.Errorf("checkpoint rejected: %w", cursorapi.ErrInvalidArgument)},
		{err: fmt.Errorf("fresh replay rejected: %w", cursorapi.ErrInvalidArgument)},
	}}
	handler := NewHandler(Dependencies{Cursor: client})
	_, err := handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{textMessage("user", "seed")}))
	require.NoError(t, err)
	continuation := executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{
		textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "next"),
	})

	_, err = handler.execute(context.Background(), continuation)

	require.Error(t, err)
	require.Len(t, client.Inputs(), 3)
}

func Test_Fallback_does_not_retry_message_lookalike(t *testing.T) {
	// Given
	client := &recordingCursorClient{steps: []cursorRunStep{
		successfulTextStep("seed-answer", "conversation", []byte("checkpoint-seed")),
		{err: errors.New("not_invalid_argument: checkpoint rejected")},
	}}
	handler := NewHandler(Dependencies{Cursor: client})
	_, err := handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{textMessage("user", "seed")}))
	require.NoError(t, err)
	continuation := executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{
		textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "next"),
	})

	// When
	_, err = handler.execute(context.Background(), continuation)

	// Then
	require.Error(t, err)
	require.Len(t, client.Inputs(), 2)
}

func Test_Checkpoint_DecodeFailure_invalidates_without_retry(t *testing.T) {
	client := &recordingCursorClient{steps: []cursorRunStep{
		successfulTextStep("seed-answer", "conversation", []byte("checkpoint-seed")),
		{err: errors.New("decode Cursor conversation checkpoint: corrupt state")},
		successfulTextStep("recovered", "fresh", []byte("checkpoint-fresh")),
	}}
	handler := NewHandler(Dependencies{Cursor: client})
	_, err := handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{textMessage("user", "seed")}))
	require.NoError(t, err)
	continuation := executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{
		textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "next"),
	})
	_, err = handler.execute(context.Background(), continuation)
	require.Error(t, err)

	_, err = handler.execute(context.Background(), continuation)

	require.NoError(t, err)
	inputs := client.Inputs()
	require.Len(t, inputs, 3)
	require.Equal(t, cursorproto.CheckpointSuffix, inputs[1].Mode)
	require.Equal(t, cursorproto.FullReplay, inputs[2].Mode)
}

func Test_Fallback_DoesNotRetry_after_stream_output_or_for_tool_result(t *testing.T) {
	t.Run("stream output", func(t *testing.T) {
		client := &recordingCursorClient{steps: []cursorRunStep{
			successfulTextStep("seed-answer", "conversation", []byte("checkpoint-seed")),
			{events: []cursorproto.ServerEvent{{Kind: cursorproto.EventText, Text: "partial"}}, result: cursorapi.RunResult{ConversationID: "conversation", OutputExposed: true}, err: fmt.Errorf("after output: %w", cursorapi.ErrInvalidArgument)},
		}}
		emitter := &captureEmitter{done: make(chan struct{})}
		handler := NewHandler(Dependencies{Cursor: client, Emitter: emitter})
		_, err := handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{textMessage("user", "seed")}))
		require.NoError(t, err)
		raw := executorFixture(t, "session", "account", "auth", "auto", "stream", []map[string]any{
			textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "next"),
		})

		_, err = handler.executeStream(context.Background(), raw)
		require.NoError(t, err)
		<-emitter.done

		require.Error(t, emitter.closeError)
		require.Len(t, client.Inputs(), 2)
		require.Len(t, emitter.payloads, 1)
	})

	t.Run("tool result", func(t *testing.T) {
		client := &recordingCursorClient{steps: []cursorRunStep{
			successfulTextStep("seed-answer", "conversation", []byte("checkpoint-seed")),
			{result: cursorapi.RunResult{ConversationID: "conversation"}, err: fmt.Errorf("tool resume rejected: %w", cursorapi.ErrInvalidArgument)},
		}}
		handler := NewHandler(Dependencies{Cursor: client})
		_, err := handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{textMessage("user", "seed")}))
		require.NoError(t, err)
		toolHistory := []map[string]any{
			textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "use tool"),
			{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "call-1", "type": "function", "function": map[string]any{"name": "lookup", "arguments": `{"q":"x"}`}}}},
			{"role": "tool", "tool_call_id": "call-1", "content": "result"},
		}

		_, err = handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", toolHistory))

		require.Error(t, err)
		require.Len(t, client.Inputs(), 2)
	})
}

func Test_ToolContinuation_retains_confirmed_checkpoint_and_exposes_call_once(t *testing.T) {
	toolStep := cursorRunStep{
		events: []cursorproto.ServerEvent{{Kind: cursorproto.EventToolCall, ID: "call-1", Name: "lookup", Arguments: `{"q":"x"}`}},
		result: cursorapi.RunResult{ConversationID: "conversation", Checkpoint: []byte("unconfirmed-tool-checkpoint"), ToolExposed: true},
	}
	client := &recordingCursorClient{steps: []cursorRunStep{
		successfulTextStep("seed-answer", "conversation", []byte("checkpoint-seed")), toolStep,
		successfulTextStep("final-answer", "conversation", []byte("checkpoint-final")),
	}}
	handler := NewHandler(Dependencies{Cursor: client})
	_, err := handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{textMessage("user", "seed")}))
	require.NoError(t, err)
	toolRequest := []map[string]any{textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "use tool")}
	_, err = handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", toolRequest))
	require.NoError(t, err)
	toolResult := append(append([]map[string]any{}, toolRequest...),
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "call-1", "type": "function", "function": map[string]any{"name": "lookup", "arguments": `{"q":"x"}`}}}},
		map[string]any{"role": "tool", "tool_call_id": "call-1", "content": "result"},
	)

	_, err = handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", toolResult))

	require.NoError(t, err)
	inputs := client.Inputs()
	require.Len(t, inputs, 3)
	require.Equal(t, []byte("checkpoint-seed"), inputs[1].Checkpoint)
	require.Equal(t, []byte("checkpoint-seed"), inputs[2].Checkpoint)
	require.NotEqual(t, []byte("unconfirmed-tool-checkpoint"), inputs[2].Checkpoint)
	require.Contains(t, inputs[2].Prompt, "Tool result lookup (call-1): result")
	require.Contains(t, inputs[2].Prompt, "Do not repeat a completed tool call")
}
