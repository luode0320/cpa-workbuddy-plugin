// usage_rotation_test.go 锁死轮换机制的四个核心契约：
//
//  1. 连续使用满阈值才让位，未满不冷却；
//  2. 空闲超过 idle 阈值视为中断，重新计时（零散流量不误伤）；
//  3. 全池将冷时先放开一半，避免调度无号可选；
//  4. 轮换冷却与失败冷却分表存储，成功请求不会把它抹掉。
//
// 契约 4 是这次移植的关键：失败冷却在每次成功时被 resetAccountFailover 清零，
// 若共用一个状态表，「用满 1 小时让位」会被第一次成功立刻取消，机制形同虚设。
package main

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// setupRotation 用一组确定的配置初始化轮换，并在用例结束后还原。
func setupRotation(t *testing.T, serve, cooldown, idle int) {
	t.Helper()
	oldEnabled := rotationEnabled
	oldServe := rotationServeMinutes
	oldCooldown := rotationCooldownMinutes
	oldIdle := rotationIdleResetMinutes
	oldPool := rotationPoolFn
	t.Cleanup(func() {
		rotationEnabledMu.Lock()
		rotationEnabled = oldEnabled
		rotationEnabledMu.Unlock()
		rotationConfigMu.Lock()
		rotationServeMinutes = oldServe
		rotationCooldownMinutes = oldCooldown
		rotationIdleResetMinutes = oldIdle
		rotationConfigMu.Unlock()
		rotationPoolFn = oldPool
		clearRotationStates()
	})
	setLoadedRotationConfig(true, serve, cooldown, idle)
	clearRotationStates()
}

// TestRotation_CoolsDownAfterServeWindow 覆盖契约 1。
func TestRotation_CoolsDownAfterServeWindow(t *testing.T) {
	setupRotation(t, 60, 60, 10)
	base := time.Now()

	// 首次使用只是打点，远未到阈值。
	if serving := markAuthUsed("acc-a", base); serving != 0 {
		t.Fatalf("首次打点连续使用时长 = %v, want 0", serving)
	}
	if rotationCoolingDown("acc-a", base) {
		t.Fatal("刚打点不应进入冷却")
	}

	// 持续承流：每隔 5 分钟有一次请求（未超过 idle=10，计时连续）。
	at59 := base
	for m := 5; m <= 55; m += 5 {
		at59 = base.Add(time.Duration(m) * time.Minute)
		markAuthUsed("acc-a", at59)
	}
	if evaluateRotation("acc-a", []string{"acc-a", "acc-b"}, at59) {
		t.Fatalf("连续使用仅 %v，未满 60 分钟不应让位", at59.Sub(base))
	}

	// 推进到 61 分钟：达到阈值，应冷却。
	at61 := base.Add(61 * time.Minute)
	markAuthUsed("acc-a", at61)
	if !evaluateRotation("acc-a", []string{"acc-a", "acc-b"}, at61) {
		t.Fatal("连续使用超过 60 分钟应让位")
	}
	if !rotationCoolingDown("acc-a", at61) {
		t.Fatal("让位后应处于轮换冷却中")
	}
}

// TestRotation_IdleResetsTimer 覆盖契约 2：零散流量不触发轮换。
func TestRotation_IdleResetsTimer(t *testing.T) {
	setupRotation(t, 60, 60, 10)
	base := time.Now()

	markAuthUsed("acc-a", base)
	// 空闲 30 分钟（超过 idle=10）后再次使用：计时应重置。
	serving := markAuthUsed("acc-a", base.Add(30*time.Minute))
	if serving != 0 {
		t.Fatalf("空闲超过 idle 后应重新计时, got serving=%v", serving)
	}
	// 再过 30 分钟（相对重置点仅 30 分钟）仍不应达到 60 分钟阈值。
	if evaluateRotation("acc-a", []string{"acc-a", "acc-b"}, base.Add(60*time.Minute)) {
		t.Fatal("计时已重置，累计实际承流仅 30 分钟，不应让位")
	}
}

// TestRotation_ReleasesHalfWhenPoolWouldStarve 覆盖契约 3。
func TestRotation_ReleasesHalfWhenPoolWouldStarve(t *testing.T) {
	setupRotation(t, 60, 60, 10)
	base := time.Now()
	pool := []string{"acc-a", "acc-b", "acc-c"}

	// 先让 b、c 进入轮换冷却。
	for _, id := range []string{"acc-b", "acc-c"} {
		markAuthUsed(id, base)
		if !evaluateRotation(id, pool, base.Add(61*time.Minute)) {
			t.Fatalf("%s 应被让位", id)
		}
	}
	// 此时若 a 也让位，全池冷却无号可选；应放开一半。
	markAuthUsed("acc-a", base)
	if !evaluateRotation("acc-a", pool, base.Add(61*time.Minute)) {
		t.Fatal("acc-a 达到阈值应让位")
	}

	cooling := 0
	for _, id := range pool {
		if rotationCoolingDown(id, base.Add(61*time.Minute)) {
			cooling++
		}
	}
	if cooling == len(pool) {
		t.Fatal("全池同时冷却，防抽干策略未生效，调度将无号可选")
	}
}

// TestRotation_IndependentFromFailoverReset 覆盖契约 4：
// 成功请求清失败冷却，但不得清掉轮换冷却。
func TestRotation_IndependentFromFailoverReset(t *testing.T) {
	setupFailoverStateDir(t)
	resetFailover(t)
	setupRotation(t, 60, 60, 10)
	base := time.Now()

	const authID = "acc-rotation-independent"
	markAuthUsed(authID, base)
	if !evaluateRotation(authID, []string{authID, "acc-other"}, base.Add(61*time.Minute)) {
		t.Fatal("前置条件不成立：应已轮换冷却")
	}
	if !rotationCoolingDown(authID, base.Add(61*time.Minute)) {
		t.Fatal("前置条件不成立：轮换冷却未生效")
	}

	// 模拟一次成功请求的清理路径。
	resetAccountFailover(authID)

	if !rotationCoolingDown(authID, base.Add(61*time.Minute)) {
		t.Fatal("成功请求把轮换冷却抹掉了，轮换机制会被第一次成功立即取消")
	}
}

// TestRotation_SchedulerSkipsRotationCoolingAccount 验证 scheduler.pick 这一层
// 真的会跳过轮换冷却中的账号——只测纯逻辑函数无法证明排除链路被接上。
func TestRotation_SchedulerSkipsRotationCoolingAccount(t *testing.T) {
	setupFailoverStateDir(t)
	resetFailover(t)
	setupRotation(t, 60, 60, 10)
	resetActiveAuth(t)
	base := time.Now()

	// qo-rot 连续承流到阈值进入轮换冷却；qo-fresh 保持健康。
	markAuthUsed("qo-rot", base)
	if !evaluateRotation("qo-rot", []string{"qo-rot", "qo-fresh"}, base.Add(61*time.Minute)) {
		t.Fatal("前置条件不成立：qo-rot 应已进入轮换冷却")
	}
	// scheduler 用的是真实时间，因此把冷却窗口设到未来。
	rotationMu.Lock()
	if st := rotationStateLocked("qo-rot", false); st != nil {
		st.cooldownUntil = time.Now().Add(30 * time.Minute)
	}
	rotationMu.Unlock()

	accountCache.Store("qo-rot", &accountCacheEntry{credits: &creditsSummary{TotalRemain: 500, TotalSize: 500}})
	accountCache.Store("qo-fresh", &accountCacheEntry{credits: &creditsSummary{TotalRemain: 500, TotalSize: 500}})
	t.Cleanup(func() {
		accountCache.Delete("qo-rot")
		accountCache.Delete("qo-fresh")
	})
	setActiveAuthID("qo-rot")

	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "qo-rot", Provider: providerName},
			{ID: "qo-fresh", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "qo-fresh" {
		t.Fatalf("轮换冷却中的账号仍被调度命中: got %+v, want qo-fresh", resp)
	}
}

// TestRotation_DisabledSkipsEverything 确认总开关关闭后所有判据都失效。
func TestRotation_DisabledSkipsEverything(t *testing.T) {
	setupRotation(t, 60, 60, 10)
	base := time.Now()
	markAuthUsed("acc-a", base)

	setLoadedRotationConfig(false, 60, 60, 10)

	if rotationCoolingDown("acc-a", base.Add(120*time.Minute)) {
		t.Fatal("关闭轮换后不应判定为冷却")
	}
	if evaluateRotation("acc-a", []string{"acc-a", "acc-b"}, base.Add(120*time.Minute)) {
		t.Fatal("关闭轮换后不应让位")
	}
}

// TestRotation_ZeroServeDisables 锁定「填 0 等同关闭」的配置语义。
func TestRotation_ZeroServeDisables(t *testing.T) {
	setupRotation(t, 0, 60, 10)
	base := time.Now()
	markAuthUsed("acc-a", base)
	if evaluateRotation("acc-a", []string{"acc-a", "acc-b"}, base.Add(600*time.Minute)) {
		t.Fatal("serve=0 应等同关闭轮换")
	}
}
