package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// testFileReferencePrompt 是 DSH 内核 @deepseek-ai/dsh-file-reference 注入的
// 文件引用说明段原文（2026-11-10 从内核产物逐字节抄录）。
const testFileReferencePrompt = "Tokens prefixed with @ are paths the user explicitly referenced. " +
	"Relative paths resolve from the workspace root; absolute paths identify files or directories on the host. " +
	"A trailing slash marks a directory: list it when its contents matter. " +
	"Anything else is a file: use the read tool when its contents are needed, " +
	"and do not claim to have inspected it before reading. " +
	`@"..." quotes a path containing spaces.`

// TestNeutralizeDSHFileReference_AnchorOnly 锁定最小介入面：
// 上游匹配的是「Tokens prefixed with @」这个词组 + @ 字形，把 @ 换成 (at) 即可。
// 整段其余内容（含 @"..." 那句）必须逐字节保留，否则会改变模型对文件引用的理解。
func TestNeutralizeDSHFileReference_AnchorOnly(t *testing.T) {
	got := neutralizeDSHFileReference(testFileReferencePrompt)
	if strings.Contains(got, "prefixed with @") {
		t.Fatalf("anchor must be broken: %q", got)
	}
	if !strings.HasPrefix(got, "Tokens prefixed with (at) are paths the user explicitly referenced.") {
		t.Fatalf("unexpected anchor rewrite: %q", got[:80])
	}
	// 段落尾部原样保留：只动锚点这一个 @。
	if !strings.Contains(got, `@"..." quotes a path containing spaces.`) {
		t.Fatalf("tail must stay untouched: %q", got)
	}
	if len(got) != len(testFileReferencePrompt)+3 {
		t.Fatalf("only the anchor should change: got len %d want %d", len(got), len(testFileReferencePrompt)+3)
	}
}

// TestNeutralizeDSHFileReference_Idempotent 保证重复处理不会层层叠加，
// 也保证「已改写的文本」再次经过时不再变化（会话历史会被反复扫描）。
func TestNeutralizeDSHFileReference_Idempotent(t *testing.T) {
	first := neutralizeDSHFileReference(testFileReferencePrompt)
	second := neutralizeDSHFileReference(first)
	if first != second {
		t.Fatalf("must be idempotent:\n first=%q\nsecond=%q", first, second)
	}
}

// TestNeutralizeDSHFileReference_ToleratesWhitespaceAndCase 覆盖内核改排版：
// 多空格、换行、大小写漂移都必须仍能命中锚点。
func TestNeutralizeDSHFileReference_ToleratesWhitespaceAndCase(t *testing.T) {
	for _, in := range []string{
		"Tokens  prefixed  with  @ are paths",
		"Tokens\tprefixed\nwith\t@ are paths",
		"tokens prefixed with @ are paths",
		"TOKENS PREFIXED WITH @ are paths",
	} {
		if got := neutralizeDSHFileReference(in); strings.Contains(got, "with @") {
			t.Errorf("anchor not broken: in %q got %q", in, got)
		}
	}
}

// TestNeutralizeDSHFileReference_LeavesUnrelatedAt 是反向护栏：
// 裸 @、邮箱、代码装饰器、PowerShell here-string 都不能被改。
// 社区早期版本有「见到 @" 就换」的兜底，实测上游不拦这些形态，留着只会改坏代码。
func TestNeutralizeDSHFileReference_LeavesUnrelatedAt(t *testing.T) {
	for _, in := range []string{
		"联系我 a@b.com",
		"@someone 看一下",
		"var s = @\"C:\\tmp\";",
		"$c = @\"\nGet-ChildItem\n\"@",
		"@decorator\ndef f(): pass",
		"npm i @scope/pkg",
		"路径 D:\\DeepSeek Harness\\bin",
	} {
		if got := neutralizeDSHFileReference(in); got != in {
			t.Errorf("must stay untouched: in %q got %q", in, got)
		}
	}
}

// TestNeutralizeForRole_RoleBoundary 锁定角色边界（2026-11-10 真机消融）：
// 上游只扫 system / developer / assistant。assistant 必须处理——多轮里每一条
// 都会被查，只改第一条无效；user / tool 塞完整指纹仍 200，动了只会破坏用户内容。
func TestNeutralizeForRole_RoleBoundary(t *testing.T) {
	scanned := []string{"system", "developer", "assistant", "System", " ASSISTANT "}
	for _, role := range scanned {
		got := neutralizeForRole(testFileReferencePrompt, role)
		if strings.Contains(got, "prefixed with @") {
			t.Errorf("role %q must be sanitized, got %q", role, got[:60])
		}
	}
	for _, role := range []string{"user", "tool", "", "unknown"} {
		if got := neutralizeForRole(testFileReferencePrompt, role); got != testFileReferencePrompt {
			t.Errorf("role %q must stay untouched, got %q", role, got[:60])
		}
	}
}

// TestNeutralizeForRole_BrandPhraseAllRoles 锁定历史行为不变：
// 品牌词改写原本就作用于全部角色，本轮不收紧，避免回归。
func TestNeutralizeForRole_BrandPhraseAllRoles(t *testing.T) {
	const in = "powered by DeepSeek\u200b Harness"
	const want = "powered by DeepSeek-Harness"
	for _, role := range []string{"system", "assistant", "user", "tool"} {
		if got := neutralizeForRole(in, role); got != want {
			t.Errorf("role %q got %q want %q", role, got, want)
		}
	}
}

// TestNeutralizeForRole_SwitchOff 验证开关能真正回退到 0.21.20 行为。
func TestNeutralizeForRole_SwitchOff(t *testing.T) {
	setDSHFingerprintNeutralize(false)
	t.Cleanup(func() { setDSHFingerprintNeutralize(true) })

	if got := neutralizeForRole(testFileReferencePrompt, "system"); got != testFileReferencePrompt {
		t.Fatalf("switch off must disable file-reference rewrite: %q", got[:60])
	}
	// 品牌词规则不受该开关影响，仍按原逻辑生效。
	if got := neutralizeForRole("DeepSeek\u200b Harness", "system"); got != "DeepSeek-Harness" {
		t.Fatalf("brand rule must survive switch off: %q", got)
	}
}

// TestPrepareUpstreamBody_NeutralizesDSHFileReference 是端到端回归：
// 真实 chat-completions 请求体经 prepareUpstreamBody 后，system 与 assistant
// 的文件引用段都被打断，user 消息原样保留，JSON 仍可解析。
func TestPrepareUpstreamBody_NeutralizesDSHFileReference(t *testing.T) {
	in := []byte(`{"model":"deepseek-v4.1-flash","messages":[` +
		`{"role":"system","content":` + testJSONLiteral(t, testFileReferencePrompt) + `},` +
		`{"role":"user","content":` + testJSONLiteral(t, testFileReferencePrompt) + `},` +
		`{"role":"assistant","content":` + testJSONLiteral(t, testFileReferencePrompt) + `}]}`)

	out := prepareUpstreamBody(in, nil, nil, "deepseek-v4.1-flash")

	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("output must stay valid JSON: %v", err)
	}
	msgs, _ := obj["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages count = %d, want 3", len(msgs))
	}

	byRole := map[string]string{}
	for _, m := range msgs {
		msg, _ := m.(map[string]any)
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		byRole[role] = content
	}
	if strings.Contains(byRole["system"], "prefixed with @") {
		t.Errorf("system not sanitized: %q", byRole["system"][:60])
	}
	if strings.Contains(byRole["assistant"], "prefixed with @") {
		t.Errorf("assistant not sanitized: %q", byRole["assistant"][:60])
	}
	if byRole["user"] != testFileReferencePrompt {
		t.Errorf("user must stay untouched")
	}
}

// TestPrepareUpstreamBody_DSHInMultimodalParts 覆盖数组形态 content：
// DSH 的部分消息把文本放在 parts 里，锚点必须同样被打断。
func TestPrepareUpstreamBody_DSHInMultimodalParts(t *testing.T) {
	in := []byte(`{"model":"deepseek-v4.1-flash","messages":[` +
		`{"role":"system","content":[{"type":"text","text":` + testJSONLiteral(t, testFileReferencePrompt) + `}]}]}`)

	out := prepareUpstreamBody(in, nil, nil, "deepseek-v4.1-flash")
	if strings.Contains(string(out), "prefixed with @") {
		t.Fatalf("multimodal text part not sanitized: %s", out)
	}
}

// testJSONLiteral 把字符串编成 JSON 字面量，供端到端用例拼请求体。
func testJSONLiteral(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
