package plugin

import (
	"context"
	"strings"
	"testing"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
)

func Test_Checkpoint_reuses_equivalent_text_parts_without_replaying_history(t *testing.T) {
	client := &recordingCursorClient{steps: []cursorRunStep{
		successfulTextStep("first line\nsecond line", "conversation", []byte("checkpoint")),
		successfulTextStep("continued", "conversation", []byte("next-checkpoint")),
	}}
	handler := NewHandler(Dependencies{Cursor: client})
	seed := strings.Repeat("synthetic long context\n", 4096)
	_, ok := handler.CallWithStatus(context.Background(), "executor.execute", executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{
		textMessage("user", seed),
	}))
	require.True(t, ok)
	continuation := executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{
		textMessage("user", seed),
		{"role": "assistant", "content": []map[string]string{
			{"type": "text", "text": "first line"}, {"type": "text", "text": "second line"},
		}},
		textMessage("user", "continue"),
	})

	response, ok := handler.CallWithStatus(context.Background(), "executor.execute", continuation)

	require.True(t, ok, "%s", response)
	inputs := client.Inputs()
	require.Len(t, inputs, 2)
	require.Equal(t, cursorproto.CheckpointSuffix, inputs[1].Mode)
	require.Equal(t, "User: continue", inputs[1].Prompt)
	require.Equal(t, []byte("checkpoint"), inputs[1].Checkpoint)
	require.Less(t, replayBytes(inputs[1]), replayBytes(inputs[0])/100)
	t.Logf("cached prefix preserved: initial replay=%d bytes; continuation=%d bytes", replayBytes(inputs[0]), replayBytes(inputs[1]))
}
