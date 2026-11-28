package main

import (
	"encoding/json"
	"testing"
	"time"
)

// TestInspectClaudeContentShapes 覆盖 Claude 内容块的三种形态：纯字符串、text+tool、refusal。
// 这条检测直接决定「要不要换模型」，误判会让本该重试的请求直接返回空内容。
func TestInspectClaudeContentShapes(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantRefus string
		wantText  string
		wantTool  bool
	}{
		{"纯字符串", `"  hello  "`, "", "hello", false},
		{"text 与 tool_use", `[{"type":"text","text":"a"},{"type":"tool_use","text":""}]`, "", "a", true},
		{"命中断言块", `[{"type":"text","text":"before"},{"type":"refusal"}]`, "refusal", "", false},
		{"非 JSON", `not json`, "", "", false},
		{"空数组", `[]`, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refusal, text, tool := inspectClaudeContent(json.RawMessage(tc.raw))
			if refusal != tc.wantRefus || text != tc.wantText || tool != tc.wantTool {
				t.Fatalf("inspectClaudeContent(%s) = (%q, %q, %v), want (%q, %q, %v)",
					tc.raw, refusal, text, tool, tc.wantRefus, tc.wantText, tc.wantTool)
			}
		})
	}
}

// TestIsGeminiRefusalFinishReason 校验 Gemini 的拒答 finishReason 判定：
// 只认安全类原因，正常的 STOP / MAX_TOKENS 不能被当成拒答。
func TestIsGeminiRefusalFinishReason(t *testing.T) {
	for _, reason := range []string{"SAFETY", "RECITATION", "PROHIBITED_CONTENT", "SPII", "BLOCKLIST", "IMAGE_SAFETY"} {
		if !isGeminiRefusalFinishReason(reason) {
			t.Fatalf("%s: want refusal", reason)
		}
	}
	for _, reason := range []string{"", "STOP", "MAX_TOKENS", "OTHER", "safety"} {
		if isGeminiRefusalFinishReason(reason) {
			t.Fatalf("%s: want not refusal", reason)
		}
	}
}

// TestAdvanceGroupSessionOnRefusal 校验流中拒答后把会话粘性推进到组内下一个模型。
func TestAdvanceGroupSessionOnRefusal(t *testing.T) {
	stubHost(t, noOtherHost(t))
	group := modelGroup{ID: "g", Name: "G", Models: []string{"m1", "m2", "m3"}}
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	advanceGroupSessionOnRefusal("sess", group, "m1", now)

	binding, ok := groupSessions["sess\x00g"]
	if !ok {
		t.Fatal("binding missing after advance")
	}
	if binding.AuthID != "m2" {
		t.Fatalf("auth = %q, want m2", binding.AuthID)
	}
	if want := now.Add(groupCacheTTL); !binding.IdleExpiresAt.Equal(want) {
		t.Fatalf("idle expires = %v, want %v", binding.IdleExpiresAt, want)
	}

	// 已经是组内最后一个模型时不再推进。
	advanceGroupSessionOnRefusal("sess-last", group, "m3", now)
	if _, ok := groupSessions["sess-last\x00g"]; ok {
		t.Fatal("binding created for the last model")
	}

	// 单模型组没有可推进的目标。
	solo := modelGroup{ID: "solo", Name: "Solo", Models: []string{"only"}}
	advanceGroupSessionOnRefusal("sess-solo", solo, "only", now)
	if _, ok := groupSessions["sess-solo\x00solo"]; ok {
		t.Fatal("binding created for a single-model group")
	}

	// 空会话标识不建绑定。
	advanceGroupSessionOnRefusal("", group, "m1", now)
	if len(groupSessions) != 1 {
		t.Fatalf("groupSessions = %d entries, want 1", len(groupSessions))
	}
}