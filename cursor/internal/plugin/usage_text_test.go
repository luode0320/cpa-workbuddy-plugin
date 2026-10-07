package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/openai"

	"github.com/stretchr/testify/require"
)

func Test_UsageText_preserves_content_order_and_unicode(t *testing.T) {
	chat := openai.ChatRequest{
		System: "系统", Prompt: "请求",
		Attachments: []openai.Attachment{{Content: "附件1"}, {Content: "附件2"}},
		Tools:       []openai.Tool{{Name: "read", Description: "读取", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	require.Equal(t, `系统请求附件1附件2read读取{"type":"object"}`, usageText(chat))
}

func Benchmark_UsageText_large_context_with_84_tools(b *testing.B) {
	chat := openai.ChatRequest{Prompt: strings.Repeat("x", 900_000)}
	for range 84 {
		chat.Tools = append(chat.Tools, openai.Tool{Name: "read", Description: strings.Repeat("d", 400), Parameters: json.RawMessage(`{"type":"object"}`)})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if len(usageText(chat)) < len(chat.Prompt) {
			b.Fatal("lost prompt")
		}
	}
}
