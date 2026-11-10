package main

import (
	"testing"
	"time"
)

// TestModelCircuitOpensAfterCrossAuthHits 锁定模型级跨账号熔断的开启条件。
//
// 生产背景（2026-11-11）：content moderation 等确定性失败与账号健康无关，
// 只写「账号×模型」冷却时请求仍会逐个换号，每个账号各付一次上游耗时再失败。
// glm-5.3-flashx 部署后实测单请求 conductor duration p50=29.8s、p90=62.8s、
// max=153.0s，一个请求轮询 10 个不同账号。
//
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-11；改动原因：新增，锁定跨账号熔断门限与去重语义。
func TestModelCircuitOpensAfterCrossAuthHits(t *testing.T) {
	defer resetModelCircuit("mc-model")

	// 同一账号重复命中不得累计：否则单个坏号就能把整个模型熔断。
	for i := 0; i < modelCircuitCrossAuthThreshold+2; i++ {
		noteModelCircuitHit("mc-auth-1", "mc-model")
	}
	if isModelCircuitOpen("mc-model") {
		t.Fatalf("同一账号重复命中不得开闸（门限=%d）", modelCircuitCrossAuthThreshold)
	}

	// 跨账号达到门限后开闸。
	for i := 2; i <= modelCircuitCrossAuthThreshold; i++ {
		noteModelCircuitHit("mc-auth-"+string(rune('0'+i)), "mc-model")
	}
	if !isModelCircuitOpen("mc-model") {
		t.Fatalf("跨 %d 个账号命中后应开闸", modelCircuitCrossAuthThreshold)
	}

	// 熔断只作用于该模型，其它模型不受影响。
	if isModelCircuitOpen("mc-other-model") {
		t.Fatal("熔断不得跨模型泄漏")
	}

	// 成功即清账。
	resetModelCircuit("mc-model")
	if isModelCircuitOpen("mc-model") {
		t.Fatal("resetModelCircuit 后应关闭")
	}
}

// TestModelCircuitWindowBacksOff 锁定熔断窗口的指数退避。
//
// 生产背景（2026-11-11）：固定 60s 窗口下，glm-5.3-flashx 持续 100% 失败时
// 窗口一到期流量就回来撞一轮——实测开闸 8 次、间隔 60~90s、cross_auth_hits
// 从 3 涨到 10，15 分钟内 done=0 / attempt error=136。窗口必须随连续失败增长。
//
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-11；改动原因：新增，锁定退避阶梯与上限。
func TestModelCircuitWindowBacksOff(t *testing.T) {
	cases := []struct {
		streak int
		want   time.Duration
	}{
		{0, 60 * time.Second},
		{1, 120 * time.Second},
		{2, 240 * time.Second},
		{3, 480 * time.Second},
		{4, 960 * time.Second},
		{10, modelCircuitMaxWindow}, // 远超上限时必须封顶
	}
	for _, tc := range cases {
		if got := modelCircuitWindowFor(tc.streak); got != tc.want {
			t.Fatalf("modelCircuitWindowFor(%d) = %v, want %v", tc.streak, got, tc.want)
		}
	}
}

// TestModelCircuitWindowSurvivesReopen 锁定「窗口到期不清零 streak」。
//
// 若到期时把 streak 一起清掉，退避永远停在 60s，等于没做退避。
//
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-11；改动原因：新增，防止退避被窗口重置抹掉。
func TestModelCircuitWindowSurvivesReopen(t *testing.T) {
	const model = "mc-backoff-model"
	defer resetModelCircuit(model)

	openOnce := func(prefix string) {
		for i := 1; i <= modelCircuitCrossAuthThreshold; i++ {
			noteModelCircuitHit(prefix+"-"+string(rune('a'+i)), model)
		}
	}

	openOnce("first")
	if got := modelCircuitStreak[model]; got != 1 {
		t.Fatalf("首次开闸后 streak 应为 1，实际 %d", got)
	}
	// 模拟窗口到期：hits 回落到门限以下，但 streak 必须保留。
	modelCircuitMu.Lock()
	modelCircuitUntil[model] = time.Now().Add(-time.Second)
	modelCircuitMu.Unlock()
	if isModelCircuitOpen(model) {
		t.Fatal("过期窗口不得仍判为开闸")
	}
	if got := modelCircuitStreak[model]; got != 1 {
		t.Fatalf("窗口过期不得清零 streak，实际 %d", got)
	}

	// 再次开闸必须用退避后的窗口（streak=1 -> 120s）。
	openOnce("second")
	modelCircuitMu.Lock()
	until := modelCircuitUntil[model]
	streak := modelCircuitStreak[model]
	modelCircuitMu.Unlock()
	if streak != 2 {
		t.Fatalf("第二次开闸后 streak 应为 2，实际 %d", streak)
	}
	if remain := time.Until(until); remain < 100*time.Second {
		t.Fatalf("第二次开闸窗口应约 120s，实际剩余 %v", remain)
	}

	// 一次成功必须把退避惩罚彻底清零。
	resetModelCircuit(model)
	modelCircuitMu.Lock()
	_, hasStreak := modelCircuitStreak[model]
	modelCircuitMu.Unlock()
	if hasStreak {
		t.Fatal("resetModelCircuit 必须清掉 streak")
	}
}

// TestContentModerationFeedsModelCircuit 锁定审核拦截真的会喂给跨账号熔断。
//
// 这是「分类」与「熔断」之间的接线断言：classify 归入 model_rate_limit 后，
// recordAccountFailureForModel 必须同时记一次跨账号命中，否则熔断永远不会开。
//
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-11；改动原因：新增，防止两处接线被重构掉。
func TestContentModerationFeedsModelCircuit(t *testing.T) {
	const model = "mc-wiring-model"
	defer resetModelCircuit(model)
	clearFailoverStates()

	body := "upstream 200: sensitive content error finish reason: sensitive"
	for i := 1; i <= modelCircuitCrossAuthThreshold; i++ {
		recordAccountFailureForModel("mc-wiring-"+string(rune('a'+i)), model, 200, body)
	}
	if !isModelCircuitOpen(model) {
		t.Fatal("审核拦截必须喂给跨账号熔断，否则 100% 失败模型仍会被无限重派")
	}
}
