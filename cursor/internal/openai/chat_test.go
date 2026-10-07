package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_ParseChatRequest_flattens_supported_history(t *testing.T) {
	// Given
	raw := []byte(`{"model":"cursor/auto","stream":true,"messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":[{"type":"text","text":"second"}]}]}`)

	// When
	request, err := ParseChatRequest(raw)

	// Then
	require.NoError(t, err)
	require.Equal(t, "auto", request.Model)
	require.Equal(t, "Be brief.", request.System)
	require.Equal(t, "User: first\nAssistant: answer\nUser: second", request.Prompt)
	require.True(t, request.Stream)
}

func Test_ParseChatRequest_accepts_opencode_options_tools_and_attachments(t *testing.T) {
	raw := []byte(`{
		"model":"cursor/claude-fable-5-xhigh",
		"stream":true,
		"max_tokens":32000,
		"stream_options":{"include_usage":true},
		"parallel_tool_calls":true,
		"tools":[{"type":"function","function":{"name":"read_file","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}],
		"messages":[
			{"role":"user","content":[
				{"type":"text","text":"inspect these"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}},
				{"type":"file","file":{"filename":"notes.txt","file_data":"data:text/plain;base64,bm90ZXM="}}
			]},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":[
				{"type":"text","text":"contents"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,dG9vbGltYWdl"}}
			]}
		]
	}`)

	request, err := ParseChatRequest(raw)

	require.NoError(t, err)
	require.Equal(t, "claude-fable-5-xhigh", request.Model)
	require.Len(t, request.Tools, 1)
	require.Equal(t, "read_file", request.Tools[0].Name)
	require.Len(t, request.Images, 2)
	require.Equal(t, "image/png", request.Images[0].MIMEType)
	require.Equal(t, []byte("image"), request.Images[0].Data)
	require.Equal(t, []byte("toolimage"), request.Images[1].Data)
	require.Len(t, request.Attachments, 1)
	require.Equal(t, "notes.txt", request.Attachments[0].Name)
	require.Equal(t, "notes", request.Attachments[0].Content)
	require.Contains(t, request.Prompt, "read_file")
	require.Contains(t, request.Prompt, "call_1")
	require.Contains(t, request.Prompt, "contents")
}

func Test_ParseChatRequest_ignores_truly_empty_assistant_history(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "missing content"},
		{name: "null content", content: `,"content":null`},
		{name: "empty string", content: `,"content":""`},
		{name: "blank string", content: `,"content":"  "`},
		{name: "empty parts", content: `,"content":[]`},
		{name: "empty text part", content: `,"content":[{"type":"text","text":""}]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			raw := []byte(`{"model":"cursor/auto","messages":[{"role":"user","content":"first"},{"role":"assistant"` + test.content + `},{"role":"user","content":"second"}]}`)

			// When
			request, err := ParseChatRequest(raw)

			// Then
			require.NoError(t, err)
			require.Equal(t, "User: first\nUser: second", request.Prompt)
			require.Len(t, request.Transcript, 2)
			require.Equal(t, []Role{RoleUser, RoleUser}, transcriptRoles(request.Transcript))
		})
	}
}

func Test_StreamChunk_emits_openai_json_payload_for_host_sse_wrapper(t *testing.T) {
	// Given
	turn := NewTurn("cursor/auto")

	// When
	chunk, err := turn.StreamChunk("hello")

	// Then
	require.NoError(t, err)
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(chunk, &payload))
	require.Contains(t, string(payload["choices"]), "hello")
}

func Test_Turn_emits_openai_tool_calls_for_stream_and_completion(t *testing.T) {
	turn := NewTurn("cursor/auto")

	chunk, err := turn.StreamToolCall("call_1", "read_file", `{"path":"a.txt"}`)
	require.NoError(t, err)
	require.Contains(t, string(chunk), `"tool_calls"`)
	require.Contains(t, string(chunk), `"read_file"`)

	final, err := turn.FinalChunk("prompt")
	require.NoError(t, err)
	require.Contains(t, string(final), `"finish_reason":"tool_calls"`)

	completion, err := turn.Completion("prompt")
	require.NoError(t, err)
	require.Contains(t, string(completion), `"tool_calls"`)
	require.Contains(t, string(completion), `"finish_reason":"tool_calls"`)
}
