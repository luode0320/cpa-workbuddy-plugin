// session_seed_test.go 锁定会话身份的两套派生规则：
//  1. 路由会话键剔除低熵标识——生产全量请求都带 X-Session-ID: 1，照收会把所有
//     对话折叠成同一个会话，选号退化成钉死单一账号，全部流量压在一个上游
//     MaaS 会话上排队；
//  2. 上游 MaaS 会话键按请求体逐对话派生——社区一致口径（prompt_cache_key →
//     user → system + 首条 user 消息），避免上游会话与前缀缓存串味。
package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestLowEntropySessionID 锁定弱会话标识判定。
//
// 短值与被同一字符填满的值都不可信；长度足够的纯数字合法标识必须放行，
// 误杀会把正常粘性一起关掉。
func TestLowEntropySessionID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"1", true},
		{"0", true},
		{"s1", true},
		{"abc", true},
		{"aaaa", true},
		{"0000", true},
		{"1234", false},
		{"123456", false},
		{"sess-9f2a", false},
		{"abcd", false},
	}
	for _, tc := range cases {
		if got := lowEntropySessionID(tc.id); got != tc.want {
			t.Fatalf("lowEntropySessionID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// TestExtractSessionKey_SkipsLowEntropyHeader 锁定弱会话头被剔除后回退。
//
// X-Session-ID: 1 必须被丢弃；没有其它强信号时返回空串，由调度层按负载平摊，
// 而不是把全量流量折成一个会话。
func TestExtractSessionKey_SkipsLowEntropyHeader(t *testing.T) {
	headers := map[string][]string{"X-Session-Id": {"1"}}
	if got := extractSessionKeyFromSources(headers, nil); got != "" {
		t.Fatalf("弱会话标识必须剔除并返回空串，got %q", got)
	}
}

// TestExtractSessionKey_KeepsStrongHeader 锁定强会话头正常生效。
func TestExtractSessionKey_KeepsStrongHeader(t *testing.T) {
	headers := map[string][]string{"X-Session-Id": {"conv-9f2a1b"}}
	if got := extractSessionKeyFromSources(headers, nil); got != "header:conv-9f2a1b" {
		t.Fatalf("强会话标识必须保留，got %q", got)
	}
}

// TestExtractSessionKey_FallsBackPastWeakHeader 锁定弱头不影响后续强信号。
//
// 弱标识必须「跳过继续找」，不是「命中即返回空」。
func TestExtractSessionKey_FallsBackPastWeakHeader(t *testing.T) {
	headers := map[string][]string{
		"X-Session-Id":        {"1"},
		"X-Client-Request-Id": {"req-7d3c5e"},
	}
	if got := extractSessionKeyFromSources(headers, nil); got != "clientreq:req-7d3c5e" {
		t.Fatalf("弱头之后必须继续沿优先级链匹配，got %q", got)
	}
}

// TestMaasSessionKeyFromPayload_PromptCacheKey 锁定第一优先级：客户端显式缓存键。
func TestMaasSessionKeyFromPayload_PromptCacheKey(t *testing.T) {
	obj := map[string]any{"prompt_cache_key": "conv-42"}
	if got := maasSessionKeyFromPayload(obj); got != "cache:conv-42" {
		t.Fatalf("prompt_cache_key 优先级最高，got %q", got)
	}
}

// TestMaasSessionKeyFromPayload_UserFallback 锁定第二优先级：客户端用户标识。
func TestMaasSessionKeyFromPayload_UserFallback(t *testing.T) {
	obj := map[string]any{"user": "alice"}
	if got := maasSessionKeyFromPayload(obj); got != "user:alice" {
		t.Fatalf("user 次优先级，got %q", got)
	}
}

// TestMaasSessionKeyFromPayload_MessagesFallback 锁定第三优先级：消息兜底。
//
// 同一对话（system + 首条 user 消息不变）必须恒定；不同对话必须不同——
// 这正是「全部对话撞成同一个上游会话」的修复点。
func TestMaasSessionKeyFromPayload_MessagesFallback(t *testing.T) {
	convA := map[string]any{
		"instructions": "you are helpful",
		"messages": []any{
			map[string]any{"role": "system", "content": "sys"},
			map[string]any{"role": "user", "content": "帮我看看这段代码"},
			map[string]any{"role": "assistant", "content": "好的"},
		},
	}
	convA2 := map[string]any{
		"instructions": "you are helpful",
		"messages": []any{
			map[string]any{"role": "system", "content": "sys"},
			map[string]any{"role": "user", "content": "帮我看看这段代码"},
			map[string]any{"role": "assistant", "content": "好的"},
			map[string]any{"role": "user", "content": "继续"},
		},
	}
	convB := map[string]any{
		"instructions": "you are helpful",
		"messages": []any{
			map[string]any{"role": "system", "content": "sys"},
			map[string]any{"role": "user", "content": "另一个问题"},
		},
	}
	keyA := maasSessionKeyFromPayload(convA)
	keyA2 := maasSessionKeyFromPayload(convA2)
	keyB := maasSessionKeyFromPayload(convB)
	if keyA == "" || keyA2 == "" || keyB == "" {
		t.Fatalf("消息兜底必须派生出非空会话键，got %q/%q/%q", keyA, keyA2, keyB)
	}
	if keyA != keyA2 {
		t.Fatalf("同一对话多轮追加后必须仍是同一个会话键，got %q vs %q", keyA, keyA2)
	}
	if keyA == keyB {
		t.Fatalf("不同对话必须派生不同的会话键，got %q", keyA)
	}
	if len(keyA) != len("msg:")+16 {
		t.Fatalf("会话键形态应为 msg:<16 hex>，got %q", keyA)
	}
}

// TestMaasSessionKeyFromPayload_MultimodalContent 锁定多模态 content 形态。
//
// content 是分片数组时也必须能取到文本，否则多模态客户端的会话键退化为空串。
func TestMaasSessionKeyFromPayload_MultimodalContent(t *testing.T) {
	obj := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "看这张图"},
			}},
		},
	}
	key := maasSessionKeyFromPayload(obj)
	if key == "" {
		t.Fatal("多模态 content 必须能派生出会话键")
	}
}

// TestMaasSessionKeyFromPayload_Nil 锁定请求体不可解析时返回空串。
func TestMaasSessionKeyFromPayload_Nil(t *testing.T) {
	if got := maasSessionKeyFromPayload(nil); got != "" {
		t.Fatalf("请求体不可解析时必须返回空串，got %q", got)
	}
}

// TestPrepareUpstreamBody_ReturnsParsedObject 锁定请求体只解析一次。
//
// 解析结果要同时服务于「请求体改写」与「MaaS 会话键派生」，避免对 1MB+
// 的生产请求体做第二次全量反序列化。
func TestPrepareUpstreamBody_ReturnsParsedObject(t *testing.T) {
	raw := []byte(`{"model":"m","prompt_cache_key":"conv-7","messages":[{"role":"user","content":"hi"}]}`)
	body, obj := prepareUpstreamBody(raw, "deepseek-flash")
	if obj == nil {
		t.Fatal("必须返回解析后的请求体对象")
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if out["model"] != "deepseek-flash" {
		t.Fatalf("model 必须改写为上游真实 ID，got %v", out["model"])
	}
	if got := maasSessionKeyFromPayload(obj); got != "cache:conv-7" {
		t.Fatalf("会话键必须能从同一份解析结果派生，got %q", got)
	}
}

// TestExecutors_UsePayloadDerivedMaasSessionKey 锁定两个执行入口都用请求体
// 派生的 MaaS 会话键签发票据。
//
// 这类「装配层」缺陷只有接线断言能覆盖：派生函数本身测对了，调用点仍可能
// 传错键——本插件历史上就在 pumpUpstreamStream 把客户端别名当上游模型传下去。
func TestExecutors_UsePayloadDerivedMaasSessionKey(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "maasSessionKey := maasSessionKeyFromPayload(bodyObj)") {
		t.Error("执行入口必须用请求体派生的 MaaS 会话键")
	}
	if strings.Contains(text, "ensureMaasSession(sa, sessionKey)") {
		t.Error("执行入口不得再把路由会话键当 MaaS 会话键用")
	}
	if !strings.Contains(text, "ensureMaasSession(sa, maasSessionKey)") {
		t.Error("执行入口必须把 MaaS 会话键传给 ensureMaasSession")
	}
	if !strings.Contains(text, "pumpUpstreamStream(httpReq, cancel, req.StreamID, sseFramed, req.Model, upstreamModel,") {
		t.Error("异步流入口必须把解析后的上游模型传给 pumpUpstreamStream")
	}
}

// TestMaasSessionID_StablePerSessionKey 锁定上游 session_id 逐会话稳定。
//
// 同一上游会话键必须恒得同一 UUID（上游对 session_id 强校验 UUID，且票据与
// session 绑定），不同会话键必须不同。
func TestMaasSessionID_StablePerSessionKey(t *testing.T) {
	a1 := maasSessionID("cache:conv-1")
	a2 := maasSessionID("cache:conv-1")
	b := maasSessionID("cache:conv-2")
	if a1 != a2 {
		t.Fatalf("同一会话键必须派生同一 UUID，got %q vs %q", a1, a2)
	}
	if a1 == b {
		t.Fatalf("不同会话键必须派生不同 UUID，got %q", a1)
	}
	if len(a1) != 36 {
		t.Fatalf("派生结果必须是 UUID 形态，got %q", a1)
	}
}
