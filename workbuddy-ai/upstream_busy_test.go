// upstream_busy_test.go 锁定「上游拥堵不罚号」的端到端语义：
// 渠道级容量满载（6021 / queue.full）与模型级限流（6004）都不得把账号写盘
// 禁用。这是高峰期可用性的核心防线——一罚号就会缩小可用池并放大拥堵。
//
// 生产依据（参考实现 linguo2625469/workbuddy2api-panel 记录的 2026-10-15
// 实案）：把渠道满载当普通限流逐号冷却，2 秒内即可绞杀整池 DeepSeek 号。
package main

import "testing"

// TestQueueFullDoesNotDisableAccount 验证渠道满载在记账落点上零副作用：
// 不写盘、不入冷却，账号保持原状。
// 最近修改时间：2026-10-31；改动原因：新增，锁定拥堵免罚。
func TestQueueFullDoesNotDisableAccount(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("busy-1", "idx-busy-1", map[string]any{"note": "healthy"})

	body := `{"code":6021,"msg":"queue.full.title"}`
	if recordAccountFailureForModel("busy-1", "glm-4.6", 429, body) {
		t.Fatal("渠道满载不得计入冷却")
	}
	// 1. 绝不写盘：禁用会让健康号退出路由。
	if got := h.writeCount(); got != 0 {
		t.Fatalf("渠道满载写盘次数 = %d, want 0", got)
	}
	// 2. 盘上 disabled 保持原值（fake host 初始为 false）。
	if doc := h.docOf("idx-busy-1"); doc["disabled"] == true {
		t.Fatalf("渠道满载不得把账号置为 disabled: %v", doc)
	}
	// 3. 内存也不得留下冷却状态，否则路由层仍会跳过该号。
	if _, _, ok := failoverStateSnapshot("busy-1"); ok {
		t.Fatal("渠道满载不得留下冷却状态")
	}
	if isAccountCoolingDown("busy-1") {
		t.Fatal("渠道满载后账号不应处于冷却")
	}
}

// TestModelRateLimitCoolsModelButKeepsAccount 验证 6004 的「只隔离模型」语义：
// 该模型进入账号×模型冷却，但账号本身不被禁用、账号级冷却也不被污染。
// 最近修改时间：2026-10-31；改动原因：新增，锁定模型级限流的禁用边界。
func TestModelRateLimitCoolsModelButKeepsAccount(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("mr-1", "idx-mr-1", map[string]any{"note": "healthy"})

	body := `{"code":6004,"message":"usage exceeds frequency limit, will reset at 2026-10-31 20:00:00"}`
	if !recordAccountFailureForModel("mr-1", "glm-4.6", 429, body) {
		t.Fatal("模型级限流应冷却该模型")
	}
	// 1. 账号健康，绝不写盘禁用。
	if got := h.writeCount(); got != 0 {
		t.Fatalf("模型级限流写盘次数 = %d, want 0", got)
	}
	if doc := h.docOf("idx-mr-1"); doc["disabled"] == true {
		t.Fatalf("模型级限流不得禁用账号: %v", doc)
	}
	// 2. 该模型必须被隔离，否则同一模型会被反复撞。
	if !isAuthModelCoolingDown("mr-1", "glm-4.6") {
		t.Fatal("模型级限流必须把该模型置入账号×模型冷却")
	}
	// 3. 同账号的其它模型必须不受影响——这正是「切模型即可用」的含义。
	if isAuthModelCoolingDown("mr-1", "deepseek-v4.1-flash") {
		t.Fatal("模型级限流不得牵连同账号的其它模型")
	}
	// 4. 账号级冷却不得被污染：否则该号会从所有模型上被摘掉。
	if count, _, ok := failoverStateSnapshot("mr-1"); ok && count > 0 {
		t.Fatalf("账号级冷却计数 = %d, want 0", count)
	}
}

// TestDisableThresholdRequiresConsecutiveFailures 锁定写盘禁用门限：
// 单次失败只走内存冷却，连续达到门限才写盘禁用。
//
// 背景（2026-10-29 生产事故）：主力快号在一条 429 后立刻被写盘禁用，
// 且因标记缺失导致探活永久跳过，8 小时零流量。门限与探活兜底共同堵住
// 这条「一次抖动即失联」的路径。
// 最近修改时间：2026-10-31；改动原因：新增，锁定禁用门限语义。
func TestDisableThresholdRequiresConsecutiveFailures(t *testing.T) {
	resetFailover(t)
	// 门限默认 3；显式设置避免依赖外部配置。
	setFailoverDisableMinCount(3)
	t.Cleanup(func() { setFailoverDisableMinCount(defaultFailoverDisableMinCount) })

	h := newFailoverFakeHost(t)
	h.addAccount("th-1", "idx-th-1", nil)

	// 1. 第 1 次失败：只冷却，不写盘。
	if !recordAccountFailureForModel("th-1", "glm-4.6", 500, "upstream 500: internal error") {
		t.Fatal("5xx 应计入冷却")
	}
	if got := h.writeCount(); got != 0 {
		t.Fatalf("第 1 次失败写盘次数 = %d, want 0（未达门限）", got)
	}
	// 2. 第 2 次失败：仍未达门限。
	recordAccountFailureForModel("th-1", "glm-4.6", 500, "upstream 500: internal error")
	if got := h.writeCount(); got != 0 {
		t.Fatalf("第 2 次失败写盘次数 = %d, want 0（未达门限）", got)
	}
	// 3. 第 3 次失败：达到门限，必须写盘禁用。
	recordAccountFailureForModel("th-1", "glm-4.6", 500, "upstream 500: internal error")
	if got := h.writeCount(); got != 1 {
		t.Fatalf("第 3 次失败写盘次数 = %d, want 1（应达门限）", got)
	}
	if doc := h.docOf("idx-th-1"); doc["disabled"] != true {
		t.Fatalf("达门限后应写盘禁用: %v", doc)
	}
}

// TestDisableThresholdConfigurableToOne 锁定门限可配置为 1（恢复旧行为），
// 并确认该配置确实生效——避免「配置读了但没接上」的经典漏接。
// 最近修改时间：2026-10-31；改动原因：新增，锁定门限开关接通。
func TestDisableThresholdConfigurableToOne(t *testing.T) {
	resetFailover(t)
	setFailoverDisableMinCount(1)
	t.Cleanup(func() { setFailoverDisableMinCount(defaultFailoverDisableMinCount) })

	h := newFailoverFakeHost(t)
	h.addAccount("th-1x", "idx-th-1x", nil)

	if !recordAccountFailureForModel("th-1x", "glm-4.6", 500, "upstream 500: internal error") {
		t.Fatal("5xx 应计入冷却")
	}
	if got := h.writeCount(); got != 1 {
		t.Fatalf("门限=1 时首次失败写盘次数 = %d, want 1", got)
	}
}

// TestDeterministicFailureIgnoresThreshold 锁定确定性故障不受门限约束：
// 401 在同一账号上必然重演，等满 3 次只会让用户多失败两次。
// 最近修改时间：2026-10-31；改动原因：新增，锁定门限只约束瞬时失败。
func TestDeterministicFailureIgnoresThreshold(t *testing.T) {
	resetFailover(t)
	setFailoverDisableMinCount(3)
	t.Cleanup(func() { setFailoverDisableMinCount(defaultFailoverDisableMinCount) })

	h := newFailoverFakeHost(t)
	h.addAccount("det-1", "idx-det-1", nil)

	if !recordAccountFailureForModel("det-1", "glm-4.6", 401, `{"error":"invalid token"}`) {
		t.Fatal("401 应计入冷却")
	}
	if got := h.writeCount(); got != 1 {
		t.Fatalf("确定性故障首次失败写盘次数 = %d, want 1（不受门限约束）", got)
	}
}

// TestModelRateLimitNeverDisablesEvenAtThreshold 锁定 6004 的禁用豁免优先于门限：
// 即使连续失败远超门限，模型级限流也不得写盘禁用。
// 最近修改时间：2026-10-31；改动原因：新增，锁定禁用豁免与门限的优先关系。
func TestModelRateLimitNeverDisablesEvenAtThreshold(t *testing.T) {
	resetFailover(t)
	setFailoverDisableMinCount(1)
	t.Cleanup(func() { setFailoverDisableMinCount(defaultFailoverDisableMinCount) })

	h := newFailoverFakeHost(t)
	h.addAccount("mr-2", "idx-mr-2", nil)

	body := `{"code":6004,"message":"usage exceeds frequency limit"}`
	for i := 0; i < 5; i++ {
		recordAccountFailureForModel("mr-2", "glm-4.6", 429, body)
	}
	if got := h.writeCount(); got != 0 {
		t.Fatalf("模型级限流写盘次数 = %d, want 0（豁免优先于门限）", got)
	}
}

// TestProbeTargetsFallbackModelWhenMarkerMissing 锁定探活兜底：盘上标记与
// 内存都没记模型时，仍要产出一个可回放的候选，而不是永久跳过。
// 最近修改时间：2026-10-31；改动原因：新增，锁定「禁用号永不失联」。
func TestProbeTargetsFallbackModelWhenMarkerMissing(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	// 只有 disabled 标记、没有模型标记——正是事故里的残缺形态。
	h.addAccount("orphan", "idx-orphan", map[string]any{
		"disabled":           true,
		failoverDisableKey:   true,
		failoverDisableAtKey: "2026-10-29T15:48:53Z",
	})

	targets := failoverProbeTargets()
	var found *failoverProbeCandidate
	for i := range targets {
		if targets[i].authID == "orphan" {
			found = &targets[i]
			break
		}
	}
	if found == nil {
		t.Fatal("标记残缺的禁用账号必须仍出现在探活候选里")
	}
	if found.model == "" {
		t.Fatal("标记残缺时必须兜底出一个可回放模型，否则该号永久失联")
	}
}
