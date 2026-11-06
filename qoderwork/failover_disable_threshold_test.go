// failover_disable_threshold_test.go 锁定「写盘禁用门限」的三条契约：
//
//  1. 瞬时失败（5xx / 传输层错误 / 软限流 / 零字节断流）必须连续达到门限才写盘
//     禁用。qoderwork 生产池只有个位数账号，一次抖动就禁用等于直接砍掉可用容量；
//  2. 确定性失败（401/403/404/405、402 额度耗尽、110 日请求上限）首次即禁用：
//     它们在同一个账号上必然重演，等两次只会让用户多失败两次；
//  3. HTTP 200 承载的业务错误帧不得再伪造成 403 —— 伪造会让第 2 条把瞬时失败
//     当确定性处理，等于门限失效（历史缺陷，2026-11-07 修复）。
//
// 既有红线同时纳入回归：排队帧（isQueued / 10605）与模型级故障
// （all backends failed）始终不罚号，不受门限影响。
package main

import (
	"testing"
)

// withDisableThreshold 设置门限并在用例结束后恢复原值。
// 门限是包级状态，用例之间必须隔离，否则先跑的用例会把后跑的判定带偏。
func withDisableThreshold(t *testing.T, n int) {
	t.Helper()
	old := failoverDisableThreshold()
	t.Cleanup(func() { setFailoverDisableMinCount(old) })
	setFailoverDisableMinCount(n)
}

// stubDisableCalls 把禁用侧的账号解析替换为计数桩。
//
// 返回空 auth_index 会让 disableAccountForFailover 在写盘前退出（不碰宿主 RPC、
// 不改盘上文档），但调用本身已被计入 —— 这正是我们要观测的「门限是否放行」。
func stubDisableCalls(t *testing.T) *int {
	t.Helper()
	orig := failoverResolveIndexFn
	calls := 0
	failoverResolveIndexFn = func(authID string) (string, string) {
		calls++
		return "", authID
	}
	t.Cleanup(func() { failoverResolveIndexFn = orig })
	return &calls
}

// TestFailureShouldDisableAccount_TransientNeedsThreshold 锁定瞬时失败的门限语义。
func TestFailureShouldDisableAccount_TransientNeedsThreshold(t *testing.T) {
	withDisableThreshold(t, 3)
	cases := []struct {
		name   string
		status int
		body   string
		count  int
		want   bool
	}{
		{"5xx 第1次", 500, "", 1, false},
		{"5xx 第2次", 500, "", 2, false},
		{"5xx 第3次", 500, "", 3, true},
		{"502 第2次", 502, "bad gateway", 2, false},
		{"传输错误 第1次", 0, "connection reset by peer", 1, false},
		{"传输错误 第3次", 0, "connection reset by peer", 3, true},
		{"429 第1次", 429, "", 1, false},
		{"429 第3次", 429, "", 3, true},
		{"200 软限流 第2次", 200, "rate limit exceeded", 2, false},
		{"零字节断流 第1次", 200, "upstream stream closed before first payload", 1, false},
		{"零字节断流 第3次", 200, "upstream stream closed before first payload", 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := failureShouldDisableAccount(tc.status, tc.body, tc.count); got != tc.want {
				t.Fatalf("failureShouldDisableAccount(%d, %q, %d) = %v, want %v",
					tc.status, tc.body, tc.count, got, tc.want)
			}
		})
	}
}

// TestFailureShouldDisableAccount_DeterministicIgnoresThreshold 锁定确定性失败首次即禁用。
func TestFailureShouldDisableAccount_DeterministicIgnoresThreshold(t *testing.T) {
	withDisableThreshold(t, 3)
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"401 凭据失效", 401, ""},
		{"403 权限不足", 403, "forbidden"},
		{"404 端点缺失", 404, ""},
		{"405 方法不允许", 405, ""},
		{"402 额度耗尽", 402, ""},
		{"110 日请求上限", 200, "billing daily count exceeded"},
		{"110 数字形态", 200, `{"code":110}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !failureShouldDisableAccount(tc.status, tc.body, 1) {
				t.Fatalf("确定性失败 %s 首次即应禁用，实际为 false", tc.name)
			}
		})
	}
}

// TestFailureShouldDisableAccount_ThresholdOneRestoresLegacy 锁定填 1 恢复旧行为。
func TestFailureShouldDisableAccount_ThresholdOneRestoresLegacy(t *testing.T) {
	withDisableThreshold(t, 1)
	if !failureShouldDisableAccount(500, "", 1) {
		t.Fatal("门限=1 时首次瞬时失败应禁用，实际为 false")
	}
}

// TestRecordAccountFailure_DisableHonorsThreshold 端到端锁门限：
// 前两次只冷却不禁用，第三次才调用禁用侧。
func TestRecordAccountFailure_DisableHonorsThreshold(t *testing.T) {
	resetFailover(t)
	withDisableThreshold(t, 3)
	calls := stubDisableCalls(t)

	for i := 1; i <= 2; i++ {
		if !recordAccountFailure("acc-threshold", 500, "upstream 500") {
			t.Fatalf("第 %d 次失败应计入冷却", i)
		}
		if *calls != 0 {
			t.Fatalf("第 %d 次瞬时失败不应写盘禁用，实际调用禁用 %d 次", i, *calls)
		}
	}
	if !recordAccountFailure("acc-threshold", 500, "upstream 500") {
		t.Fatal("第 3 次失败应计入冷却")
	}
	if *calls != 1 {
		t.Fatalf("第 3 次瞬时失败应触发一次写盘禁用，实际 %d 次", *calls)
	}
}

// TestRecordAccountFailure_DeterministicDisablesFirst 端到端锁确定性失败首败即禁。
func TestRecordAccountFailure_DeterministicDisablesFirst(t *testing.T) {
	resetFailover(t)
	withDisableThreshold(t, 3)
	calls := stubDisableCalls(t)

	if !recordAccountFailure("acc-det", 401, "invalid token") {
		t.Fatal("401 应计入冷却")
	}
	if *calls != 1 {
		t.Fatalf("401 首次即应写盘禁用，实际调用 %d 次", *calls)
	}
}

// TestRecordAccountFailure_QueueAndModelLevelStillExempt 回归既有红线：
// 排队帧与模型级故障不罚号，不受门限改动影响。
func TestRecordAccountFailure_QueueAndModelLevelStillExempt(t *testing.T) {
	resetFailover(t)
	withDisableThreshold(t, 1)
	calls := stubDisableCalls(t)

	if recordAccountFailure("acc-queue", 403, `{"isQueued":true,"queueCount":14250,"serviceAvailable":true}`) {
		t.Fatal("排队帧不应记账")
	}
	if recordAccountFailure("acc-queue2", 403, "code 10605: capacity limited") {
		t.Fatal("10605 排队业务码不应记账")
	}
	if recordAccountFailure("acc-model", 500, "all backends failed") {
		t.Fatal("模型级故障不应记账")
	}
	if *calls != 0 {
		t.Fatalf("红线场景不应调用禁用侧，实际 %d 次", *calls)
	}
}

// TestNoteUpstreamErrFrame_DoesNotFake403 是本次修复的核心回归：
// 200 错误帧若仍被伪造成 403，软限流会在首败就被写盘禁用。
func TestNoteUpstreamErrFrame_DoesNotFake403(t *testing.T) {
	resetFailover(t)
	withDisableThreshold(t, 3)
	calls := stubDisableCalls(t)

	// 软限流错误帧：瞬时，首次不禁用
	if !noteUpstreamErrFrameForModel("acc-sse-rl", "kmodel_latest", "upstream 200: rate limit exceeded") {
		t.Fatal("软限流错误帧应计入冷却")
	}
	if *calls != 0 {
		t.Fatalf("软限流错误帧首次不应禁用（伪造 403 的旧实现会在此禁用），实际 %d 次", *calls)
	}

	// 额度错误帧：确定性，首次即禁用
	if !noteUpstreamErrFrameForModel("acc-sse-credit", "kmodel_latest", "insufficient credits") {
		t.Fatal("额度错误帧应计入冷却")
	}
	if *calls != 1 {
		t.Fatalf("额度错误帧首次即应禁用，实际调用 %d 次", *calls)
	}
}

// TestRecordCooldownState_ReturnsCount 锁住返回值契约：门限判定依赖它，
// 少返回一次计数会让「第 N 次才禁用」永久延迟一拍。
func TestRecordCooldownState_ReturnsCount(t *testing.T) {
	resetFailover(t)
	for want := 1; want <= 3; want++ {
		ok, count := recordCooldownState("acc-count")
		if !ok {
			t.Fatalf("第 %d 次写入应成功", want)
		}
		if count != want {
			t.Fatalf("第 %d 次写入返回 count=%d，期望 %d", want, count, want)
		}
	}
}
