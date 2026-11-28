// observability_test.go 锁定「持续监控」三件套：前缀缓存字段提取、首字慢埋点阈值、
// 票据签发频次留痕。
//
// 背景（2026-11-28 生产取证）：上游对 minimax 回报的前缀缓存命中数放在
// `usage.prompt_tokens_details.cached_tokens`，而插件原先只读顶层 `cached_tokens`，
// 于是 feed 与面板的缓存命中恒为 0——监控口径直接失真，连「首字慢是缓存没命中
// 还是上游慢」都分不出来。
package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// TestUsageDetailFromMap_ReadsPromptTokensDetails 锁定 OpenAI 形态的缓存字段。
func TestUsageDetailFromMap_ReadsPromptTokensDetails(t *testing.T) {
	raw := `{"prompt_tokens":620000,"completion_tokens":800,"total_tokens":620800,
	"prompt_tokens_details":{"cached_tokens":617568},
	"completion_tokens_details":{"reasoning_tokens":321}}`
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	d := usageDetailFromMap(m)
	if d.InputTokens != 620000 {
		t.Fatalf("InputTokens = %d, want 620000", d.InputTokens)
	}
	if d.OutputTokens != 800 {
		t.Fatalf("OutputTokens = %d, want 800", d.OutputTokens)
	}
	if d.CachedTokens != 617568 {
		t.Fatalf("CachedTokens = %d, want 617568（prompt_tokens_details.cached_tokens 必须被解析）", d.CachedTokens)
	}
	if d.ReasoningTokens != 321 {
		t.Fatalf("ReasoningTokens = %d, want 321", d.ReasoningTokens)
	}
}

// TestUsageDetailFromMap_CacheFieldFallbacks 锁定三种缓存字段形态都能读。
func TestUsageDetailFromMap_CacheFieldFallbacks(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int64
	}{
		{"OpenAI prompt_tokens_details", `{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":9}}`, 9},
		{"顶层 cached_tokens", `{"prompt_tokens":10,"cached_tokens":8}`, 8},
		{"Anthropic cache_read_input_tokens", `{"prompt_tokens":10,"cache_read_input_tokens":7}`, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal([]byte(tc.raw), &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := usageDetailFromMap(m).CachedTokens; got != tc.want {
				t.Fatalf("CachedTokens = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestLogSlowRequest_OnlyAboveThreshold 锁定首字慢埋点的阈值行为。
//
// 阈值与用户口径一致：首字大于 10s 才留痕；低于阈值的正常长上下文首字不记。
//
// 断言用返回值而不是时间戳：Windows 系统时钟粒度约 15.6ms，连续两次
// time.Now() 可能取到同一个值，用时间戳比对会漏判节流失效。
func TestLogSlowRequest_OnlyAboveThreshold(t *testing.T) {
	slowRequestLogMu.Lock()
	slowRequestLastLog = make(map[string]time.Time)
	slowRequestLogMu.Unlock()

	detail := usage.Detail{InputTokens: 600000, CachedTokens: 599000, OutputTokens: 120}
	if logSlowRequest("acct-fast", "minimax-m3.1-flash", uint64(5*time.Second), detail, 6*time.Second) {
		t.Fatal("首字 5s 不应触发慢请求留痕")
	}
	if !logSlowRequest("acct-slow", "deepseek-flash", uint64(12*time.Second), detail, 20*time.Second) {
		t.Fatal("首字 12s 必须触发慢请求留痕")
	}
}

// TestLogSlowRequest_ThrottledPerAccount 锁定同账号的慢请求日志按间隔节流。
func TestLogSlowRequest_ThrottledPerAccount(t *testing.T) {
	slowRequestLogMu.Lock()
	slowRequestLastLog = make(map[string]time.Time)
	slowRequestLogMu.Unlock()

	detail := usage.Detail{InputTokens: 100}
	if !logSlowRequest("acct-throttle", "m", uint64(30*time.Second), detail, 31*time.Second) {
		t.Fatal("首次慢请求必须留痕")
	}
	if logSlowRequest("acct-throttle", "m", uint64(31*time.Second), detail, 32*time.Second) {
		t.Fatal("同账号的慢请求日志必须按间隔节流")
	}
	// 换账号不受前一个账号的节流影响。
	if !logSlowRequest("acct-other", "m", uint64(31*time.Second), detail, 32*time.Second) {
		t.Fatal("节流必须按账号独立计数")
	}
}

// TestSlowRequestLogThresholdMatchesUserCriterion 锁定阈值常量与用户口径一致。
func TestSlowRequestLogThresholdMatchesUserCriterion(t *testing.T) {
	if slowRequestLogThreshold != 10*time.Second {
		t.Fatalf("首字慢阈值必须是 10s（用户口径「首字大于 10s」），当前 %v", slowRequestLogThreshold)
	}
	if slowRequestLogInterval <= 0 {
		t.Fatal("慢请求日志节流间隔必须为正")
	}
}

// TestLogMaasTicketMint_Throttled 锁定票据签发日志按间隔节流。
func TestLogMaasTicketMint_Throttled(t *testing.T) {
	maasMintLogMu.Lock()
	maasMintLastLog = time.Now().Add(-2 * maasMintLogInterval)
	maasMintLogMu.Unlock()
	if !logMaasTicketMint("acct-1", "sess-1") {
		t.Fatal("首次票据签发必须留痕")
	}
	if logMaasTicketMint("acct-1", "sess-2") {
		t.Fatal("票据签发日志必须按间隔节流")
	}
}

// TestUsageDetailFromMap_ReadsCacheCreation 锁定「缓存创建量」也被采集。
//
// 只看重命中数无法区分「缓存被复用」与「缓存被整体重建」——后者意味着每次
// 请求都要重付全量 prefill，正是首字延迟的机制。
func TestUsageDetailFromMap_ReadsCacheCreation(t *testing.T) {
	raw := `{"prompt_tokens":1000,"prompt_tokens_details":{"cached_tokens":900,"cache_creation_input_tokens":1200}}`
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	d := usageDetailFromMap(m)
	if d.CachedTokens != 900 {
		t.Fatalf("CachedTokens = %d, want 900", d.CachedTokens)
	}
	if d.CacheCreationTokens != 1200 {
		t.Fatalf("CacheCreationTokens = %d, want 1200（缓存创建量必须被采集）", d.CacheCreationTokens)
	}
}

// TestMaasDefaultBaseURLMatchesOfficialClient 锁定兜底入口与官方客户端一致。
//
// 官方桌面客户端的 DEFAULT_MAAS_BASE_URL 是 https://api9.qwenwork.cn；原先本插件
// 写的是 https://maas.xiaoluozi.cn（另一条 route family，社区实测该兜底值 401）。
// 兜底值写错等于「签发响应缺 base_url 时必然 401」。
func TestMaasDefaultBaseURLMatchesOfficialClient(t *testing.T) {
	if maasDefaultBaseURL != "https://api9.qwenwork.cn" {
		t.Fatalf("兜底 base_url 必须与官方客户端 DEFAULT_MAAS_BASE_URL 一致，当前 %q", maasDefaultBaseURL)
	}
	if strings.Contains(maasDefaultBaseURL, "maas.xiaoluozi.cn") {
		t.Fatalf("兜底 base_url 不得使用实测 401 的 legacy 入口：%q", maasDefaultBaseURL)
	}
}

// TestApplyMaasHeaders_SendsDeviceUA 锁定设备 UA 头（官方出站白名单内）。
func TestApplyMaasHeaders_SendsDeviceUA(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://maas.example/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	applyMaasHeaders(req, &maasSession{AccessKey: "sk-mm-v1.test", SessionID: "sess-1"})
	if got := req.Header.Get("x-mm-device-ua"); got == "" {
		t.Fatal("x-mm-device-ua 必须发出（官方出站头白名单含该字段）")
	}
	if got := req.Header.Get("x-mm-device-type"); got == "" {
		t.Fatal("x-mm-device-type 必须发出")
	}
}

// TestUsageDetailFromMap_EmptyBody 锁定空 usage 不 panic。
func TestUsageDetailFromMap_EmptyBody(t *testing.T) {
	if got := usageDetailFromMap(map[string]any{}); got.InputTokens != 0 {
		t.Fatalf("空 usage 必须返回零值，got %+v", got)
	}
	if got := usageDetailFromMap(nil); got.CachedTokens != 0 {
		t.Fatalf("nil usage 必须返回零值，got %+v", got)
	}
}
