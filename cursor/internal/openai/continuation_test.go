package openai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_ToolResultContinuation_marks_the_completed_call_for_checkpoint_and_full_replay(t *testing.T) {
	raw := []byte(`{
		"model":"cursor/grok-4.6",
		"messages":[
			{"role":"user","content":"inspect the project"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"read","arguments":"{\"path\":\"README.md\"}"}}]},
			{"role":"tool","tool_call_id":"call-1","content":"project contents"}
		]
	}`)
	request, err := ParseChatRequest(raw)
	require.NoError(t, err)

	continuation, ok := request.ContinuationFrom(1)

	require.True(t, ok)
	require.True(t, continuation.HasToolResult)
	require.True(t, strings.HasSuffix(continuation.Prompt, toolResultContinuationPrompt))
	require.True(t, strings.HasSuffix(request.Prompt, toolResultContinuationPrompt))
}

func Test_Continuation_without_trailing_tool_result_is_unchanged(t *testing.T) {
	request := ChatRequest{Transcript: []Message{{
		Role: RoleUser, Content: []ContentPart{{Kind: ContentText, Text: "next"}},
	}}}

	continuation, ok := request.ContinuationFrom(0)

	require.True(t, ok)
	require.Equal(t, "User: next", continuation.Prompt)
}
