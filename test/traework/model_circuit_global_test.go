package main

import (
	"testing"
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
