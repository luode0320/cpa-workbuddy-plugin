package main

import (
	"strconv"
	"testing"
	"time"
)

// 软失败拆分（0.9.11）：瞬时过载类失败（429 非 credit / soft rate limit 文案 /
// 上游零字节断流）只做固定冷却 + 换号，不推进连续失败计数、不冻结异常池；
// 硬失败（credit / 401/403/404/405 / 5xx / transport）维持原语义。
// 背景：2026-09-06 生产实证，Trae 网关瞬时故障窗口内 4 个健康账号相继零字节
// 断流，若按硬失败计数会把整池误冻结进异常池。

// TestTransientThrottle_Soft429DoesNotAdvanceCount 锁定软失败核心语义：
// 连续 12 次 429 计数恒为 0、只有冷却生效。（qoderwork 包内无 anomaly
// 测试 helper；冻结翻转语义由 workbuddy 同构测试覆盖。）
func TestTransientThrottle_Soft429DoesNotAdvanceCount(t *testing.T) {
	resetFailover(t)
	for i := 0; i < 12; i++ {
		if !recordAccountFailure("acc-soft", 429, "rate limit exceeded") {
			t.Fatalf("429 iteration %d must count as a (soft) failure", i)
		}
		count, until, ok := failoverStateSnapshot("acc-soft")
		if !ok {
			t.Fatalf("iteration %d: failover state must exist", i)
		}
		if count != 0 {
			t.Fatalf("iteration %d: soft failure must not advance count, got %d", i, count)
		}
		if remain := time.Until(until); remain <= 0 || remain > 15*time.Second {
			t.Fatalf("iteration %d: cooldown remain = %v, want (0, 15s]", i, remain)
		}
	}
}

// TestTransientThrottle_ZeroByteStreamIsSoft 锁定零字节断流文案走软通道：
// 泵的固定文案与 collect 错误文案都命中；200 无 marker 普通文本仍不记账
// （门禁未放宽到任意 200 body）。
func TestTransientThrottle_ZeroByteStreamIsSoft(t *testing.T) {
	resetFailover(t)
	bodies := []string{
		"upstream stream closed before first payload",
		"upstream 200: invalid SSE response: missing output and done event",
	}
	for i, body := range bodies {
		id := "acc-empty-" + strconv.Itoa(i)
		if !recordAccountFailure(id, 200, body) {
			t.Fatalf("zero-byte stream text must count as a (soft) failure: %q", body)
		}
		count, _, ok := failoverStateSnapshot(id)
		if !ok || count != 0 {
			t.Fatalf("zero-byte stream must not advance count (got count=%d ok=%v)", count, ok)
		}
		if !isAccountCoolingDown(id) {
			t.Fatalf("zero-byte stream must trigger cooldown: %q", body)
		}
	}
	if recordAccountFailure("acc-normal-200", 200, "some other 200 body text") {
		t.Fatal("200 with no marker and no empty-stream text must not count")
	}
}

// TestTransientThrottle_429WithCreditMarkerStaysHard 锁定硬通道优先：
// 429 + credit marker 是账号耗尽，仍推进连续失败计数（达到阈值后由既有
// 计数冻结逻辑处理，此处只断言计数推进）。
func TestTransientThrottle_429WithCreditMarkerStaysHard(t *testing.T) {
	resetFailover(t)
	for i := 0; i < 5; i++ {
		if !recordAccountFailure("acc-hard", 429, `{"error":"insufficient credit"}`) {
			t.Fatalf("429+credit iteration %d must count as a hard failure", i)
		}
	}
	count, _, ok := failoverStateSnapshot("acc-hard")
	if !ok || count < 5 {
		t.Fatalf("429+credit must advance count, got count=%d ok=%v", count, ok)
	}
}

// TestTransientThrottle_HardFailuresUnchanged 回归锁定：transport 0 / 5xx /
// 403 的硬计数语义不受软失败拆分影响。
func TestTransientThrottle_HardFailuresUnchanged(t *testing.T) {
	resetFailover(t)
	for i := 0; i < 3; i++ {
		recordAccountFailure("acc-transport", 0, "connection refused")
	}
	if count, _, _ := failoverStateSnapshot("acc-transport"); count != 3 {
		t.Fatalf("transport failures must keep advancing count, got %d", count)
	}
	recordAccountFailure("acc-5xx", 502, "upstream gateway error")
	if count, _, _ := failoverStateSnapshot("acc-5xx"); count != 1 {
		t.Fatalf("5xx must keep advancing count, got %d", count)
	}
	recordAccountFailure("acc-403", 403, "forbidden")
	if count, _, _ := failoverStateSnapshot("acc-403"); count != 1 {
		t.Fatalf("403 must keep advancing count, got %d", count)
	}
}

// TestIsTransientThrottle_Classification 表驱动锁定分类边界。
func TestIsTransientThrottle_Classification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"429 plain", 429, "", true},
		{"429 rate limit text", 200, "rate limit exceeded", true},
		{"429 too many requests", 200, "Too Many Requests", true},
		{"soft throttle wording", 200, "upstream is throttling requests", true},
		{"zero-byte host wording", 200, "upstream stream closed before first payload", true},
		{"zero-byte collect wording", 200, "upstream 200: invalid SSE response: missing output and done event", true},
		{"429 with credit marker", 429, `{"error":"insufficient credit"}`, false},
		{"402 hard credit", 402, "payment required", false},
		{"200 credit marker", 200, `{"error":"insufficient credit"}`, false},
		{"401 account-level", 401, "token expired", false},
		{"5xx", 503, "service unavailable", false},
		{"transport", 0, "dial tcp: connection refused", false},
		{"business 400", 400, "bad request", false},
		{"success 200", 200, "ok", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientThrottle(tc.status, tc.body); got != tc.want {
				t.Fatalf("isTransientThrottle(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}
