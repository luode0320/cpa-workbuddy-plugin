package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSanitizeBlockedTemplates_DeepSeekHarness 验证 DSH 身份行被中和：
// 原串消失（上游子串匹配不再命中），但去掉零宽空格后与原文完全一致（无损）。
func TestSanitizeBlockedTemplates_DeepSeekHarness(t *testing.T) {
	in := "You are an AI agent powered by DeepSeek Harness."
	out := sanitizeBlockedTemplates(in)
	if out == in {
		t.Fatal("should neutralize DeepSeek Harness")
	}
	if strings.Contains(out, "DeepSeek Harness") {
		t.Fatalf("blocked phrase still intact: %q", out)
	}
	if !strings.Contains(out, zeroWidthSpace) {
		t.Fatalf("expected zero-width separator, got %q", out)
	}
	if got := strings.ReplaceAll(out, zeroWidthSpace, ""); got != in {
		t.Fatalf("neutralization must be lossless:\n got %q\nwant %q", got, in)
	}
}

// TestSanitizeBlockedTemplates_DeepSeekHarnessCaseAndSpacing 验证大小写与
// 非常规空白（含不换行空格 U+00A0）同样能命中，避免上游换写法后失效。
func TestSanitizeBlockedTemplates_DeepSeekHarnessCaseAndSpacing(t *testing.T) {
	for _, in := range []string{
		"powered by deepseek harness",
		"powered by DeepSeek\u00a0Harness",
		"powered by DeepSeek  Harness",
	} {
		out := sanitizeBlockedTemplates(in)
		if out == in {
			t.Fatalf("should neutralize variant %q", in)
		}
		if got := strings.ReplaceAll(out, zeroWidthSpace, ""); got != in {
			t.Fatalf("lossless failed for %q: got %q", in, got)
		}
	}
}

// TestSanitizeBlockedTemplates_KeepsPathContext 验证纯路径上下文完全不动：
// 一旦给路径插入零宽空格，模型会把坏路径交给工具，且肉眼不可见。
func TestSanitizeBlockedTemplates_KeepsPathContext(t *testing.T) {
	in := `D:\10_COD\DeepSeek Harness\resources\app.asar\dsh\`
	out := sanitizeBlockedTemplates(in)
	if out != in {
		t.Fatalf("path must stay byte-identical, got %q", out)
	}
}

// TestSanitizeBlockedTemplates_MixedPathAndProse 验证同一段文本里只中和
// 正文出现的那一次，路径中的那一次保持原样。
func TestSanitizeBlockedTemplates_MixedPathAndProse(t *testing.T) {
	in := "You are DeepSeek Harness running from D:\\10_COD\\DeepSeek Harness\\app."
	out := sanitizeBlockedTemplates(in)
	if n := strings.Count(out, zeroWidthSpace); n != 1 {
		t.Fatalf("want exactly 1 neutralized occurrence, got %d: %q", n, out)
	}
	if !strings.Contains(out, `D:\10_COD\DeepSeek Harness\app`) {
		t.Fatalf("path changed: %q", out)
	}
	if strings.HasPrefix(out, "You are DeepSeek Harness ") {
		t.Fatalf("prose occurrence not neutralized: %q", out)
	}
}

// TestSanitizeBlockedTemplates_ClaudeCode 验证 Claude Code 身份行被中和。
func TestSanitizeBlockedTemplates_ClaudeCode(t *testing.T) {
	in := "You are Claude Code, Anthropic's official CLI tool for Claude."
	out := sanitizeBlockedTemplates(in)
	if out == in {
		t.Fatal("should neutralize Claude Code identity line")
	}
	if got := strings.ReplaceAll(out, zeroWidthSpace, ""); got != in {
		t.Fatalf("lossless failed: got %q want %q", got, in)
	}
}

// TestSanitizeBlockedTemplates_MainBranch 验证 git 注入行被中和。
func TestSanitizeBlockedTemplates_MainBranch(t *testing.T) {
	in := "Default branch (you will usually use this for PRs)"
	out := sanitizeBlockedTemplates(in)
	if out == in {
		t.Fatal("should neutralize Main branch injection")
	}
	if got := strings.ReplaceAll(out, zeroWidthSpace, ""); got != in {
		t.Fatalf("lossless failed: got %q want %q", got, in)
	}
}

// TestSanitizeBlockedTemplates_NoMatch 验证无关文本原样透传。
func TestSanitizeBlockedTemplates_NoMatch(t *testing.T) {
	in := "Hello world"
	out := sanitizeBlockedTemplates(in)
	if out != in {
		t.Fatalf("should pass through unchanged, got %q", out)
	}
}

// TestSanitizeBlockedTemplates_Empty 验证空串不 panic。
func TestSanitizeBlockedTemplates_Empty(t *testing.T) {
	if got := sanitizeBlockedTemplates(""); got != "" {
		t.Fatalf("empty input should stay empty, got %q", got)
	}
}

// TestPrepareUpstreamBody_NeutralizesDSHIdentity 是端到端回归：真实 chat
// completions 请求体经 prepareUpstreamBody 后，system 消息被中和、JSON 仍
// 可解析、其余字段（model / user 消息）不受影响。
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
	if strings.Contains(content, "DeepSeek Harness") {
		t.Fatalf("system prompt still contains blocked phrase: %q", content)
	}
	if got := strings.ReplaceAll(content, zeroWidthSpace, ""); got != "You are an AI agent powered by DeepSeek Harness." {
		t.Fatalf("system prompt not lossless: %q", got)
	}
	if obj["stream"] != true {
		t.Fatal("stream must still be forced to true")
	}
}

// TestPrepareUpstreamBody_PathInSystemPromptIntact 验证系统提示里的真实
// 路径不会被中和逻辑污染（DSH 运行时会注入安装目录）。
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
