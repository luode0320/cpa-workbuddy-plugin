package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSanitizeBlockedTemplates_DeepSeekHarness 覆盖线上实测的全部词形：
// 空格 / 多空格 / NBSP / 零宽空格 / 零宽非连接符 都必须被收束成可见连字符。
// 上游会归一化剥离不可见字符，只有可见分隔符能真正打断子串匹配。
func TestSanitizeBlockedTemplates_DeepSeekHarness(t *testing.T) {
	cases := []struct{ in, want string }{
		{"You are an AI agent powered by DeepSeek Harness.", "You are an AI agent powered by DeepSeek-Harness."},
		{"powered by deepseek harness", "powered by deepseek-harness"},
		{"powered by DEEPSEEK HARNESS", "powered by DEEPSEEK-HARNESS"},
		{"powered by DeepSeek  Harness", "powered by DeepSeek-Harness"},
		{"powered by DeepSeek\tHarness", "powered by DeepSeek-Harness"},
		{"powered by DeepSeek\u00a0Harness", "powered by DeepSeek-Harness"},
		{"powered by DeepSeek\u200b Harness", "powered by DeepSeek-Harness"},
		{"powered by DeepSeek\u200c Harness", "powered by DeepSeek-Harness"},
		{"powered by DeepSeek\ufeffHarness", "powered by DeepSeek-Harness"},
	}
	for _, c := range cases {
		if got := sanitizeBlockedTemplates(c.in); got != c.want {
			t.Errorf("in %q\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// TestSanitizeBlockedTemplates_NoInvisibleChars 是一条反向护栏：改写结果
// 不得包含任何零宽/不可见字符，否则等于什么都没做（上游会先剥离再匹配）。
func TestSanitizeBlockedTemplates_NoInvisibleChars(t *testing.T) {
	out := sanitizeBlockedTemplates("powered by DeepSeek\u200b Harness")
	for _, r := range []string{"\u200b", "\u200c", "\u200d", "\ufeff", "\u00ad"} {
		if strings.Contains(out, r) {
			t.Fatalf("output must not contain invisible char %q: %q", r, out)
		}
	}
}

// TestSanitizeBlockedTemplates_SingleWordUntouched 验证拆开后不再命中：
// 只出现 DeepSeek 或只出现 Harness 时上游放行，插件也不该改。
func TestSanitizeBlockedTemplates_SingleWordUntouched(t *testing.T) {
	for _, in := range []string{
		"You are an AI agent powered by DeepSeek.",
		"You are an AI agent powered by Harness.",
		"You are an AI agent powered by DSH.",
	} {
		if got := sanitizeBlockedTemplates(in); got != in {
			t.Errorf("should stay unchanged: in %q got %q", in, got)
		}
	}
}

// TestSanitizeBlockedTemplates_KeepsPathContext 验证纯路径上下文完全不动：
// 路径被改成带连字符会直接让工具调用失败，且肉眼极难排查。
func TestSanitizeBlockedTemplates_KeepsPathContext(t *testing.T) {
	in := `D:\10_COD\DeepSeek Harness\resources\app.asar\dsh\`
	if got := sanitizeBlockedTemplates(in); got != in {
		t.Fatalf("path must stay byte-identical, got %q", got)
	}
	if got := sanitizeBlockedTemplates("/opt/DeepSeek Harness/bin"); got != "/opt/DeepSeek Harness/bin" {
		t.Fatalf("posix path must stay byte-identical, got %q", got)
	}
}

// TestSanitizeBlockedTemplates_MixedPathAndProse 验证同一段文本里只改正文，
// 路径保持原样。
func TestSanitizeBlockedTemplates_MixedPathAndProse(t *testing.T) {
	in := "You are DeepSeek Harness running from D:\\10_COD\\DeepSeek Harness\\app."
	want := "You are DeepSeek-Harness running from D:\\10_COD\\DeepSeek Harness\\app."
	if got := sanitizeBlockedTemplates(in); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// TestSanitizeBlockedTemplates_ClaudeCode 验证 Claude Code 身份行被插入
// 可见词 "tool"，整句模板不再逐字命中，语义不变。
func TestSanitizeBlockedTemplates_ClaudeCode(t *testing.T) {
	in := "You are Claude Code, Anthropic's official CLI for Claude."
	want := "You are Claude Code, Anthropic's official CLI tool for Claude."
	if got := sanitizeBlockedTemplates(in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// 已经是带 tool 的形态时不得再次改写（幂等）。
	if got := sanitizeBlockedTemplates(want); got != want {
		t.Fatalf("rewrite must be idempotent, got %q", got)
	}
}

// TestSanitizeBlockedTemplates_Idempotent 验证重复执行不会累积改写。
func TestSanitizeBlockedTemplates_Idempotent(t *testing.T) {
	first := sanitizeBlockedTemplates("powered by DeepSeek Harness")
	if second := sanitizeBlockedTemplates(first); second != first {
		t.Fatalf("not idempotent: %q -> %q", first, second)
	}
}

// TestSanitizeBlockedTemplates_NoMatch 验证无关文本原样透传。
func TestSanitizeBlockedTemplates_NoMatch(t *testing.T) {
	in := "Hello world"
	if got := sanitizeBlockedTemplates(in); got != in {
		t.Fatalf("should pass through unchanged, got %q", got)
	}
}

// TestSanitizeBlockedTemplates_Empty 验证空串不 panic。
func TestSanitizeBlockedTemplates_Empty(t *testing.T) {
	if got := sanitizeBlockedTemplates(""); got != "" {
		t.Fatalf("empty input should stay empty, got %q", got)
	}
}

// TestPrepareUpstreamBody_NeutralizesDSHIdentity 是端到端回归：真实 chat
// completions 请求体经 prepareUpstreamBody 后，system 消息被改写、JSON 仍
// 可解析、其余字段（model / user 消息 / stream）不受影响。
func TestPrepareUpstreamBody_NeutralizesDSHIdentity(t *testing.T) {
	in := []byte(`{"model":"deepseek-v4.1-flash","messages":[` +
		`{"role":"system","content":"You are an AI agent powered by DeepSeek Harness."},` +
		`{"role":"user","content":"hi"}]}`)
	out := prepareUpstreamBody(in, nil, nil, "deepseek-v4.1-flash")

	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("output must stay valid JSON: %v", err)
	}
	msgs, _ := obj["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("message count changed: %d", len(msgs))
	}
	sys, _ := msgs[0].(map[string]any)
	content, _ := sys["content"].(string)
	if strings.Contains(strings.ToLower(content), "deepseek harness") {
		t.Fatalf("system prompt still contains blocked phrase: %q", content)
	}
	if content != "You are an AI agent powered by DeepSeek-Harness." {
		t.Fatalf("unexpected rewrite: %q", content)
	}
	if obj["stream"] != true {
		t.Fatal("stream must still be forced to true")
	}
}

// TestPrepareUpstreamBody_PathInSystemPromptIntact 验证系统提示里的真实
// 路径不会被改写逻辑污染（DSH 运行时会注入安装目录）。
func TestPrepareUpstreamBody_PathInSystemPromptIntact(t *testing.T) {
	path := `D:\10_COD\DeepSeek Harness\resources\app.asar\dsh\`
	in := []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"system","content":"cwd: ` + strings.ReplaceAll(path, `\`, `\\`) + `"}]}`)
	out := prepareUpstreamBody(in, nil, nil, "deepseek-v4.1-flash")

	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	msgs := obj["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].(string)
	if content != "cwd: "+path {
		t.Fatalf("path was rewritten: %q", content)
	}
}

// TestPrepareUpstreamBody_NeutralizesMultimodalParts 验证 OpenAI 多模态
// content（数组形态）里的文本 part 同样被处理。
func TestPrepareUpstreamBody_NeutralizesMultimodalParts(t *testing.T) {
	in := []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"system","content":[` +
		`{"type":"text","text":"powered by DeepSeek Harness"}]}]}`)
	out := prepareUpstreamBody(in, nil, nil, "deepseek-v4.1-flash")

	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	msgs := obj["messages"].([]any)
	parts := msgs[0].(map[string]any)["content"].([]any)
	text := parts[0].(map[string]any)["text"].(string)
	if text != "powered by DeepSeek-Harness" {
		t.Fatalf("part not rewritten: %q", text)
	}
}

func TestForceMaxThinking_Hy3Model(t *testing.T) {
	obj := map[string]any{"model": "hy3-std"}
	changed := forceMaxThinking(obj)
	if !changed {
		t.Fatal("should change hy3 model")
	}
	if obj["reasoning_effort"] != "high" {
		t.Fatal("should set high")
	}
}

func TestForceMaxThinking_NonHy3Model(t *testing.T) {
	obj := map[string]any{"model": "glm-5.2"}
	changed := forceMaxThinking(obj)
	if changed {
		t.Fatal("should not change non-hy3 model")
	}
}

func TestForceMaxThinking_AlreadyHigh(t *testing.T) {
	obj := map[string]any{"model": "hy3-std", "reasoning_effort": "high"}
	changed := forceMaxThinking(obj)
	if changed {
		t.Fatal("should not change when already high")
	}
}

func TestTruncate(t *testing.T) {
	if truncate("hello", 10) != "hello" {
		t.Fatal("short string should be unchanged")
	}
	if truncate("hello world", 5) != "hello" {
		t.Fatal("should truncate to 5 chars")
	}
	if truncate("", 5) != "" {
		t.Fatal("empty string")
	}
}
