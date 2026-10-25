package main

import (
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 轮换冷却（2026-10-25）。
//
// 核心约束：
//  1. 轮换状态必须独立于失败冷却——resetAccountFailover 每次成功都清零失败计数，
//     复用同一张表会让 1 小时让位冷却被第一次成功立刻抹掉；
//  2. 调度层必须真的跳过冷却账号——只测纯函数会漏掉「过滤行没接上」的变异；
//  3. serve=0 / enabled=false 必须等于关闭，不能退化成「立即让位」；
//  4. 四处成功打点缺一不可，异步流式路径尤其容易漏。

// setupRotationFixture 清空轮换状态并恢复默认配置。
func setupRotationFixture(t *testing.T) {
	t.Helper()
	clearRotationStates()
	setLoadedRotationConfig(defaultRotationEnabled, defaultRotationServeMinutes,
		defaultRotationCooldownMinutes, defaultRotationIdleResetMinutes)
	clearAccountCacheForTest(t)
	t.Cleanup(func() {
		clearRotationStates()
		setLoadedRotationConfig(defaultRotationEnabled, defaultRotationServeMinutes,
			defaultRotationCooldownMinutes, defaultRotationIdleResetMinutes)
	})
}

// TestRotation_ServesThenCoolsDown 锁定承流阈值：够时长才让位。
func TestRotation_ServesThenCoolsDown(t *testing.T) {
	setupRotationFixture(t)

	const authID = "rot-acc-1"
	now := time.Now()

	if served := markAuthUsed(authID, now); served != 0 {
		t.Fatalf("首次使用连续时长应为 0，got %v", served)
	}
	// 未达阈值不冷却。
	if evaluateRotation(authID, []string{authID, "rot-other"}, now.Add(59*time.Minute)) {
		t.Fatal("未满 60 分钟不得让位")
	}
	if !evaluateRotation(authID, []string{authID, "rot-other"}, now.Add(61*time.Minute)) {
		t.Fatal("满 60 分钟必须让位")
	}
	if !rotationCoolingDown(authID, now.Add(62*time.Minute)) {
		t.Fatal("让位后必须处于轮换冷却")
	}
	// 冷却结束后恢复可路由。
	if rotationCoolingDown(authID, now.Add(61*time.Minute+61*time.Minute)) {
		t.Fatal("冷却 60 分钟后必须恢复")
	}
}

// TestRotation_IdleResetsTimer 锁定「零散流量不误伤」：中断超过 idle 阈值重新计时。
func TestRotation_IdleResetsTimer(t *testing.T) {
	setupRotationFixture(t)

	const authID = "rot-acc-idle"
	now := time.Now()
	markAuthUsed(authID, now)

	// 中断 11 分钟（超过 idle=10）后再来一次：本轮从此刻重新开始。
	served := markAuthUsed(authID, now.Add(11*time.Minute))
	if served != 0 {
		t.Fatalf("中断后应重新计时，连续时长应为 0，got %v", served)
	}
	// 于是 total 71 分钟时实际只连续了 60 分钟中的 0 分钟起步。
	if evaluateRotation(authID, []string{authID, "rot-other"}, now.Add(11*time.Minute+10*time.Minute)) {
		t.Fatal("中断重新计时后，10 分钟不该触发让位")
	}
	// 续期在 idle 窗口内：不重置。
	markAuthUsed(authID, now.Add(11*time.Minute+10*time.Minute))
	if served := markAuthUsed(authID, now.Add(11*time.Minute+15*time.Minute)); served < 14*time.Minute {
		t.Fatalf("idle 窗口内续期应累计连续时长，got %v", served)
	}
}

// TestRotation_AvoidsDrainingPool 锁定防抽干：全池已冷却时先放开一半。
func TestRotation_AvoidsDrainingPool(t *testing.T) {
	setupRotationFixture(t)

	now := time.Now()
	pool := []string{"drain-a", "drain-b", "drain-c", "drain-d"}
	for _, id := range pool {
		markAuthUsed(id, now)
	}
	// 前三个先冷却。
	for _, id := range pool[:3] {
		if !evaluateRotation(id, pool, now.Add(61*time.Minute)) {
			t.Fatalf("%s 应被冷却", id)
		}
	}
	// 第四个是最后一个可用号：冷却它必须先放开一半，否则全池死锁。
	if !evaluateRotation(pool[3], pool, now.Add(61*time.Minute)) {
		t.Fatal("最后一个号达到阈值也应让位（先放开一半再冷却）")
	}
	cooling := 0
	for _, id := range pool {
		if rotationCoolingDown(id, now.Add(62*time.Minute)) {
			cooling++
		}
	}
	if cooling == len(pool) {
		t.Fatalf("防抽干失效：全池 %d 个号同时冷却，调度将无号可选", cooling)
	}
}

// TestRotation_IndependentFromFailoverReset 锁定轮换独立于失败复位。
//
// 这是计划 D3 的硬约束：若把轮换状态塞进 failoverStates，resetAccountFailover
// 会在每次成功时把它一起清掉，1 小时让位冷却被第一次成功立刻抹掉。
func TestRotation_IndependentFromFailoverReset(t *testing.T) {
	setupRotationFixture(t)

	const authID = "rot-independent"
	now := time.Now()
	markAuthUsed(authID, now)
	if !evaluateRotation(authID, []string{authID, "rot-other"}, now.Add(61*time.Minute)) {
		t.Fatal("precondition: 应已让位")
	}

	// 模拟成功请求复位失败状态：轮换冷却必须原样保留。
	resetAccountFailover(authID)

	if !rotationCoolingDown(authID, now.Add(62*time.Minute)) {
		t.Fatal("失败冷却复位不得清掉轮换冷却（两者是独立状态）")
	}
}

// TestRotation_SchedulerSkipsRotationCoolingAccount 锁定调度层排除链路。
//
// 必须调 handleSchedulerPick 这个生产入口：只测 rotationCoolingDown 纯函数
// 无法证明过滤行真的接上了调度器（qoderwork 0.11.0 的变异存活教训）。
func TestRotation_SchedulerSkipsRotationCoolingAccount(t *testing.T) {
	setupRotationFixture(t)
	clearAccountCacheForTest(t)

	const badID = "rot-sched-bad"
	const goodID = "rot-sched-good"
	now := time.Now()
	markAuthUsed(badID, now)
	if !evaluateRotation(badID, []string{badID, goodID}, now.Add(61*time.Minute)) {
		t.Fatal("precondition: 应已进入轮换冷却")
	}

	oldMode := setSchedulerMode(schedulerModeCredits)
	t.Cleanup(oldMode)
	oldActive := getActiveAuthID()
	setActiveAuthID("")
	t.Cleanup(func() { setActiveAuthID(oldActive) })

	req := pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Model:    "qwen3.8-max",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: badID, Provider: providerName},
			{ID: goodID, Provider: providerName},
		},
	}
	raw, err := handleSchedulerPick(mustMarshal(t, req))
	if err != nil {
		t.Fatalf("handleSchedulerPick: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled {
		t.Fatal("应至少选中健康账号，got 未处理")
	}
	if resp.AuthID == badID {
		t.Fatal("轮换冷却中的账号不得被调度选中（过滤行没接上调度器）")
	}
	if resp.AuthID != goodID {
		t.Fatalf("应选中健康账号 %s，got %q", goodID, resp.AuthID)
	}
}

// TestRotation_DisabledSwitchStopsEverything 锁定总开关关闭后完全不参与。
func TestRotation_DisabledSwitchStopsEverything(t *testing.T) {
	setupRotationFixture(t)
	setLoadedRotationConfig(false, 60, 60, 10)
	now := time.Now()

	markAuthUsed("rot-off", now)
	if rotationCoolingDown("rot-off", now) {
		t.Fatal("关闭后不得产生冷却")
	}
	if evaluateRotation("rot-off", []string{"rot-off", "rot-other"}, now.Add(120*time.Minute)) {
		t.Fatal("关闭后不得让位")
	}
	if got := rotationServingMinutes("rot-off", now.Add(time.Hour)); got != 0 {
		t.Fatalf("关闭时连续使用时长应恒为 0，got %d", got)
	}
}

// TestRotation_ZeroServeDisables 锁定 serve=0 等于关闭，而不是「立即让位」。
func TestRotation_ZeroServeDisables(t *testing.T) {
	setupRotationFixture(t)
	setLoadedRotationConfig(true, 0, 60, 10)
	now := time.Now()

	markAuthUsed("rot-serve0", now)
	if evaluateRotation("rot-serve0", []string{"rot-serve0", "rot-other"}, now.Add(time.Second)) {
		t.Fatal("serve=0 必须等同关闭，不得退化成「立刻让位」")
	}
	if rotationCoolingDown("rot-serve0", now) {
		t.Fatal("serve=0 时不得产生冷却")
	}
}

// TestRotation_AsyncStreamMarksUsage 锁定流式成功路径的打点接入（风险 R7）。
//
// 异步流、同步 collect、直通 stream 是三个互斥的成功返回点；漏接任何一处，
// 该模式的全部流量都不计入连续承流，轮换在流式场景永久失效。
func TestRotation_AsyncStreamMarksUsage(t *testing.T) {
	setupRotationFixture(t)
	disableUsageOutputs(t)

	const authID = "rot-async-acc"
	authA := &traeAuth{Token: "token-a", UserID: "uid-a"}
	deps := traeAsyncStreamDeps{
		Open: func(_ *traeAuth, _ map[string]any, _ string, _ string) (traeAsyncUpstream, int, error) {
			return traeAsyncUpstream{
				// 正文必须超过 pseudoCompletionMaxChars（600 字符）：过短的 done
				// 输出会被判成伪完成并触发换号，那样就测不到成功打点了。
				Reader: strings.NewReader(streamTestResponse(strings.Repeat("healthy-output-", 60)).BodyString()),
				Close:  func() {},
			}, 200, nil
		},
		// 不注入候选选择：本用例只验证成功打点，不应发生换号。
		PickNextAuth: func(string) (string, *traeAuth, bool) {
			t.Fatal("成功路径不得换号")
			return "", nil, false
		},
		Emit:      func(string, []byte) error { return nil },
		EmitError: func(string, string) error { return nil },
		Close:     func(string) {},
	}
	runTraeAsyncStream(authA, authID, traeAsyncStreamContext{
		StreamID: "rot-stream", Model: "qwen-max-latest", UpstreamModel: "qwen3.8-max",
		Started: time.Now(), InputChars: 3000, Budget: 1,
	}, deps)

	// 打点已推进计时：立即读应得到 0 分钟（首帧），但状态条目必须存在。
	if _, ok := rotationStates[authID]; !ok {
		t.Fatal("异步流成功必须推进轮换计时，否则流式会话永远不会被轮换")
	}
}
