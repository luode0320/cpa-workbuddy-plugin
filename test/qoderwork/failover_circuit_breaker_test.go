// failover_circuit_breaker_test.go 锁死整池熔断：一次上游抖动不得把整池打空。
//
// 背景（2026-10-25 生产实证）：单个请求请求 Kimi-K3.1-Pro，连续尝试 9 个账号
// 全部失败，插件把这 9 个号逐个禁用；另一个请求随后又撞上剩余账号。整池几乎
// 被一次抖动清空，服务对用户完全不可用。
//
// 熔断的判据是「本轮禁用数占池子比例」：一旦超过一半就整体回滚。
// 小池子（< failoverDisableBudgetMinPool）不做比例熔断——几个号里禁掉两个
// 可能是真实的部分故障，拦下来等于取消禁用机制本身。
package main

import (
	"testing"
)

// addFailoverPool 给假宿主加 n 个健康账号，返回它们的 authID 列表。
func addFailoverPool(t *testing.T, h *failoverFakeHost, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := "pool-" + string(rune('a'+i))
		h.addAccount(id, "idx-"+id, nil)
		ids = append(ids, id)
	}
	return ids
}

// TestCircuitBreaker_RollsBackWhenMajorityDisabled 是核心用例：
// 池子里过半账号在短时间被判坏时，必须整体回滚，而不是继续禁用。
func TestCircuitBreaker_RollsBackWhenMajorityDisabled(t *testing.T) {
	setupFailoverStateDir(t)
	resetFailover(t)
	resetFailoverRoundForTest()

	h := newFailoverFakeHost(t)
	// 10 个号：budget = 10 * 0.5 = 5，第 6 个开始触发熔断。
	ids := addFailoverPool(t, h, 10)

	// 依次禁用 5 个：都应在预算内成功。
	for i := 0; i < 5; i++ {
		if !disableAccountForFailover(ids[i], "kmodel_latest") {
			t.Fatalf("第 %d 个账号禁用失败，未达预算不应熔断", i+1)
		}
		if !isAccountCoolingDown(ids[i]) {
			t.Fatalf("第 %d 个账号禁用后应被路由排除", i+1)
		}
	}

	// 第 6 个触发熔断：本次禁用被拒绝，且本轮已禁用的 5 个全部回滚。
	if disableAccountForFailover(ids[5], "kmodel_latest") {
		t.Fatal("超过池子 50% 时仍允许禁用，熔断未生效")
	}
	for i := 0; i < 6; i++ {
		if isAccountCoolingDown(ids[i]) {
			t.Fatalf("熔断回滚后 %s 仍被排除，整池未恢复", ids[i])
		}
	}
	if failoverStateIsDisabled(ids[0]) {
		t.Fatal("熔断回滚未清理 sidecar 禁用标记，账号会永久卡在禁用态")
	}
}

// TestCircuitBreaker_SmallPoolNotTripped 反证：小池子不因比例熔断被误伤。
// 4 个号的池子里禁用 2 个是正常的部分故障，必须照常生效。
func TestCircuitBreaker_SmallPoolNotTripped(t *testing.T) {
	setupFailoverStateDir(t)
	resetFailover(t)
	resetFailoverRoundForTest()

	h := newFailoverFakeHost(t)
	ids := addFailoverPool(t, h, 4)

	for i := 0; i < 2; i++ {
		if !disableAccountForFailover(ids[i], "kmodel_latest") {
			t.Fatalf("小池子第 %d 个账号禁用被熔断误拦", i+1)
		}
		if !isAccountCoolingDown(ids[i]) {
			t.Fatalf("小池子第 %d 个账号禁用未生效", i+1)
		}
	}
}

// TestCircuitBreaker_SameAccountNotDoubleCounted 确保重复禁用同一账号不推进熔断计数。
func TestCircuitBreaker_SameAccountNotDoubleCounted(t *testing.T) {
	setupFailoverStateDir(t)
	resetFailover(t)
	resetFailoverRoundForTest()

	h := newFailoverFakeHost(t)
	ids := addFailoverPool(t, h, 10)

	// 同一个账号禁用 5 次：幂等，不应消耗预算。
	for i := 0; i < 5; i++ {
		disableAccountForFailover(ids[0], "kmodel_latest")
	}
	// 还能正常禁用 4 个不同账号（累计 5 个，仍在预算内）。
	for i := 1; i <= 4; i++ {
		if !disableAccountForFailover(ids[i], "kmodel_latest") {
			t.Fatalf("第 %d 个不同账号禁用被误熔断，说明重复禁用被重复计数", i)
		}
	}
}

// TestCircuitBreaker_RoundWindowResets 锁定窗口语义：超出窗口的旧台账不再计入。
func TestCircuitBreaker_RoundWindowResets(t *testing.T) {
	setupFailoverStateDir(t)
	resetFailover(t)
	resetFailoverRoundForTest()

	h := newFailoverFakeHost(t)
	ids := addFailoverPool(t, h, 10)

	for i := 0; i < 5; i++ {
		disableAccountForFailover(ids[i], "kmodel_latest")
	}
	// 把台账起点拨到窗口之外，模拟「上一轮早已结束」。
	expireFailoverRoundForTest()
	if !disableAccountForFailover(ids[5], "kmodel_latest") {
		t.Fatal("窗口已过期，新一轮不应继承上一轮的禁用计数")
	}
}
