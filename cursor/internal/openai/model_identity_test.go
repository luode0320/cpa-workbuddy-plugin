package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Model_identity_normalizes_prefix_whitespace_before_session_keying(t *testing.T) {
	for _, model := range []string{"auto", "cursor/auto", "cursor/ auto", " cursor/ auto "} {
		raw, err := json.Marshal(map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": "hello"}}})
		require.NoError(t, err)

		chat, err := ParseChatRequest(raw)

		require.NoError(t, err)
		require.Equal(t, "auto", chat.Model)
	}
}
