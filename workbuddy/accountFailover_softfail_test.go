package main

import (
	"strconv"
	"testing"
	"time"
)

// 软失败并入阶梯（2026-10-16）：瞬时过载类失败（429 非 credit / soft rate
// limit 文案 / 上游零字节断流）与硬失败共用同一条计数与阶梯冷却路径，不再
// 豁免计数。历史沿革：2026-09-06 生产实证 Trae 网关瞬时故障窗口内 4 个健康
// 账号相继零字节断流，当时为避免异常池误隔离整池而拆出「只冷却不计数」软
// 通道；异常池已于 2026-09-08 移除，计数不再触发任何冻结，软失败重新并入
// 阶梯的代价（抖动期间冷却更久）由「一次成功即清零」兜底。

// TestTransientThrottle_Soft429LadderCooldown 锁定软失败新语义：
// 连续 12 次 429 计数逐次递增，冷却沿 10min×count 阶梯增长。
func TestTransientThrottle_Soft429LadderCooldown(t *testing.T) {
	resetFailover(t)
	for i := 0; i < 12; i++ {
		if !recordAccountFailure("acc-soft", 429, "rate limit exceeded") {
			t.Fatalf("429 iteration %d must count as a (soft) failure", i)
		}
		count, _, ok := failoverStateSnapshot("acc-soft")
		if !ok {
			t.Fatalf("iteration %d: failover state must exist", i)
		}
		if count != i+1 {
			t.Fatalf("iteration %d: soft failure must advance count, want %d got %d", i, i+1, count)
		}
		assertCooldownNear(t, "acc-soft", failoverCooldownFor(i+1))
	}
}

// TestTransientThrottle_ZeroByteStreamCountsOnLadder 锁定零字节断流文案走
// 可记账通道并进入阶梯：泵的固定文案与 traework collect 错误文案都命中；
// 200 无 marker 普通文本仍不记账（门禁未放宽到任意 200 body）。
func TestTransientThrottle_ZeroByteStreamCountsOnLadder(t *testing.T) {
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
		if !ok || count != 1 {
			t.Fatalf("zero-byte stream must advance count to 1 (got count=%d ok=%v)", count, ok)
		}
		assertCooldownNear(t, id, 10*time.Minute)
		if !isAccountCoolingDown(id) {
			t.Fatalf("zero-byte stream must trigger cooldown: %q", body)
		}
	}
	if recordAccountFailure("acc-normal-200", 200, "some other 200 body text") {
		t.Fatal("200 with no marker and no empty-stream text must not count")
	}
}

// TestTransientThrottle_429WithCreditMarkerStaysHard 锁定硬通道优先：
// 429 + credit marker 是账号耗尽，仍推进连续失败计数（此处只断言计数推进）。
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
