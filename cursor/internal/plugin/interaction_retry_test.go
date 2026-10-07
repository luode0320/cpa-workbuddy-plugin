package plugin

import (
	"context"
	"testing"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorapi"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
)

func Test_Fallback_stops_after_interaction_response_for_each_retryable_error(t *testing.T) {
	for _, failure := range []error{
		cursorapi.ErrProgressTimeout, cursorapi.ErrInvalidArgument, cursorapi.ErrInternalStream,
		cursorapi.ErrEmptyCompletion, cursorapi.ErrFailedPrecondition, cursorproto.ErrUnsupportedInteraction,
	} {
		t.Run(failure.Error(), func(t *testing.T) {
			client := &recordingCursorClient{steps: []cursorRunStep{
				successfulTextStep("seed-answer", "conversation", []byte("checkpoint")),
				{result: cursorapi.RunResult{InteractionResponded: true}, err: failure},
			}}
			handler := NewHandler(Dependencies{Cursor: client})
			_, err := handler.execute(context.Background(), executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{textMessage("user", "seed")}))
			require.NoError(t, err)
			continuation := executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{
				textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "next"),
			})
			_, err = handler.execute(context.Background(), continuation)
			require.ErrorIs(t, err, failure)
			inputs := client.Inputs()
			require.Len(t, inputs, 2, "interaction response must block automatic replay even without output")
			require.Equal(t, cursorproto.CheckpointSuffix, inputs[1].Mode)
		})
	}
}
