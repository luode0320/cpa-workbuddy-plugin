package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_LineageWithAssistant_matches_full_parse_and_preserves_request(t *testing.T) {
	raw := `{"model":"auto","tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}],"messages":[{"role":"system","content":"system"},{"role":"user","content":"question"}]}`
	request, err := ParseChatRequest([]byte(raw))
	require.NoError(t, err)
	original := append([]Digest(nil), request.Lineage.PrefixDigests...)
	for _, response := range []string{"answer", "another\nanswer", "中文结果"} {
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		extended := strings.TrimSuffix(raw, "]}") + `,{"role":"assistant","content":` + string(encoded) + `}]}`
		expected, err := ParseChatRequest([]byte(extended))
		require.NoError(t, err)

		actual := request.LineageWithAssistant(response)

		require.Equal(t, expected.Lineage, actual)
		require.Equal(t, original, request.Lineage.PrefixDigests)
		require.Len(t, request.Transcript, 2)
	}
}

func Benchmark_LineageWithAssistant_long_cached_prefix(b *testing.B) {
	prompt, err := json.Marshal(strings.Repeat("synthetic context\n", 65536))
	if err != nil {
		b.Fatal(err)
	}
	request, err := ParseChatRequest([]byte(`{"model":"auto","messages":[{"role":"user","content":` + string(prompt) + `}]}`))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if len(request.LineageWithAssistant("answer").PrefixDigests) != 2 {
			b.Fatal("missing assistant lineage")
		}
	}
}
