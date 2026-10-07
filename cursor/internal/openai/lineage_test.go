package openai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLineage_preserves_roles_tool_pairing_and_media_ownership_order(t *testing.T) {
	raw := []byte(`{
		"model":"cursor/auto",
		"tools":[{"type":"function","function":{"name":"inspect","parameters":{"type":"object"}}}],
		"messages":[
			{"role":"system","content":"system text"},
			{"role":"developer","content":"developer text"},
			{"role":"user","content":[
				{"type":"text","text":"first"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,dXNlcmltYWdl"}},
				{"type":"file","file":{"filename":"notes.txt","file_data":"data:text/plain;base64,bm90ZXM="}}
			]},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"inspect","arguments":"{\"path\":\"a.txt\"}"}}]},
			{"role":"tool","tool_call_id":"call-1","content":[
				{"type":"text","text":"result"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,dG9vbGltYWdl"}}
			]}
		]
	}`)

	request, err := ParseChatRequest(raw)
	require.NoError(t, err)
	require.Len(t, request.Transcript, 5)
	require.Equal(t, []Role{RoleSystem, RoleDeveloper, RoleUser, RoleAssistant, RoleTool}, transcriptRoles(request.Transcript))
	require.Equal(t, []ContentKind{ContentText, ContentImage, ContentAttachment}, contentKinds(request.Transcript[2].Content))
	require.Equal(t, "notes.txt", request.Transcript[2].Content[2].Attachment.Name)
	require.Equal(t, "call-1", request.Transcript[3].ToolCalls[0].ID)
	require.Equal(t, "call-1", request.Transcript[4].ToolCallID)
	require.Equal(t, []ContentKind{ContentText, ContentImage}, contentKinds(request.Transcript[4].Content))
	require.Len(t, request.Lineage.PrefixDigests, len(request.Transcript))
	require.NotEmpty(t, request.Lineage.SystemDigest)
}

func TestLineage_is_deterministic_for_canonical_equivalents_and_strict_append(t *testing.T) {
	base := []byte(`{
		"model":"cursor/auto",
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"b":{"type":"number"},"a":{"type":"string"}}}}}],
		"messages":[
			{"role":"system","content":"be exact"},
			{"role":"user","content":"question"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"b\":2,\"a\":\"x\"}"}}]},
			{"role":"tool","tool_call_id":"call-1","content":"answer"}
		]
	}`)
	equivalent := []byte(`{"messages":[
		{"content":"be exact","role":"system"},
		{"content":"question","role":"user"},
		{"tool_calls":[{"function":{"arguments":"{ \"a\" : \"x\", \"b\" : 2 }","name":"lookup"},"type":"function","id":"call-1"}],"content":null,"role":"assistant"},
		{"content":"answer","tool_call_id":"call-1","role":"tool"}
	],"tools":[{"function":{"parameters":{"properties":{"a":{"type":"string"},"b":{"type":"number"}},"type":"object"},"name":"lookup"},"type":"function"}],"model":"cursor/auto"}`)
	extended := []byte(`{
		"model":"cursor/auto",
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"b":{"type":"number"},"a":{"type":"string"}}}}}],
		"messages":[
			{"role":"system","content":"be exact"},
			{"role":"user","content":"question"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"b\":2,\"a\":\"x\"}"}}]},
			{"role":"tool","tool_call_id":"call-1","content":"answer"},
			{"role":"user","content":"follow up"}
		]
	}`)

	first, err := ParseChatRequest(base)
	require.NoError(t, err)
	second, err := ParseChatRequest(equivalent)
	require.NoError(t, err)
	continuation, err := ParseChatRequest(extended)
	require.NoError(t, err)

	require.Equal(t, first.Lineage, second.Lineage)
	require.Equal(t, first.Lineage.PrefixDigests, continuation.Lineage.PrefixDigests[:len(first.Lineage.PrefixDigests)])
	require.Len(t, continuation.Lineage.PrefixDigests, len(first.Lineage.PrefixDigests)+1)
}

func TestLineage_invalidates_on_model_system_tool_media_or_order_change(t *testing.T) {
	base := `{
		"model":"cursor/auto",
		"tools":[{"type":"function","function":{"name":"inspect","parameters":{"type":"object"}}}],
		"messages":[
			{"role":"system","content":"system-a"},
			{"role":"user","content":[{"type":"text","text":"first"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2UtYQ=="}}]},
			{"role":"assistant","content":"answer"}
		]
	}`
	mutations := map[string]string{
		"model":  strings.Replace(base, `cursor/auto`, `cursor/other`, 1),
		"system": strings.Replace(base, `system-a`, `system-b`, 1),
		"tool":   strings.Replace(base, `inspect`, `inspect_other`, 1),
		"media":  strings.Replace(base, `aW1hZ2UtYQ==`, `aW1hZ2UtYg==`, 1),
		"order":  strings.Replace(base, `{"type":"text","text":"first"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2UtYQ=="}}`, `{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2UtYQ=="}},{"type":"text","text":"first"}`, 1),
	}

	original, err := ParseChatRequest([]byte(base))
	require.NoError(t, err)
	for name, raw := range mutations {
		t.Run(name, func(t *testing.T) {
			changed, parseErr := ParseChatRequest([]byte(raw))
			require.NoError(t, parseErr)
			require.NotEqual(t, original.Lineage.PrefixDigests, changed.Lineage.PrefixDigests)
		})
	}
}

func TestLineage_invalidates_when_tool_argument_number_changes(t *testing.T) {
	tests := []struct {
		name     string
		original string
		changed  string
	}{
		{
			name:     "large integer",
			original: `9007199254740992`,
			changed:  `9007199254740993`,
		},
		{
			name:     "decimal",
			original: `0.1`,
			changed:  `0.10000000000000001`,
		},
		{
			name:     "exponent",
			original: `1e20`,
			changed:  `100000000000000000001`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			original := []byte(`{"model":"cursor/auto","messages":[{"role":"user","content":"question"},{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"inspect","arguments":"{\"id\":` + test.original + `}"}}]}]}`)
			changed := []byte(`{"model":"cursor/auto","messages":[{"role":"user","content":"question"},{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"inspect","arguments":"{\"id\":` + test.changed + `}"}}]}]}`)

			// When
			first, err := ParseChatRequest(original)
			require.NoError(t, err)
			second, err := ParseChatRequest(changed)
			require.NoError(t, err)

			// Then
			require.NotEqual(t, first.Lineage.PrefixDigests, second.Lineage.PrefixDigests)
		})
	}
}

func TestCanonicalJSON_preserves_number_lexemes_and_sorts_object_keys(t *testing.T) {
	// Given
	raw := []byte(`{"z":1e+03,"a":0.0100}`)

	// When
	canonical := canonicalJSON(raw)

	// Then
	require.Equal(t, `{"a":0.0100,"z":1e+03}`, canonical)
}

func transcriptRoles(messages []Message) []Role {
	roles := make([]Role, len(messages))
	for index, message := range messages {
		roles[index] = message.Role
	}
	return roles
}

func contentKinds(parts []ContentPart) []ContentKind {
	kinds := make([]ContentKind, len(parts))
	for index, part := range parts {
		kinds[index] = part.Kind
	}
	return kinds
}
