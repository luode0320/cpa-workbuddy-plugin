// slow_pipeline_test.go 锁定「成功打点 → 样本累积 → 判慢」这条串联链路。
//
// 打点调用点本身位于执行链上（需要宿主 RPC，测试环境无法打桩），
// 因此这里验证打点入口 noteSlowObservation 的整体语义：
// 它必须同时完成「记样本」与「达标即判定」，任一环断开都会让机制静默失效。
package main

import (
	"testing"
	"time"
)

// TestNoteSlowObservation_RecordsAndEvaluates 守住打点入口的双重职责。
//
// 只记样本不判定 → 机制永不触发；只判定不记样本 → 永远样本不足。
func TestNoteSlowObservation_RecordsAndEvaluates(t *testing.T) {
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	const model = "glm-5.3"
	for i := 0; i < 6; i++ {
		noteSlowObservation("fast-1", model, uint64(4*time.Second))
	}
	for i := 0; i < 5; i++ {
		noteSlowObservation("slow-1", model, uint64(20*time.Second))
	}

	if !slowCoolingDown("slow-1", model, time.Now()) {
		t.Fatal("noteSlowObservation 必须在样本达标后把账号置入慢速冷却（只记样本不判定等于机制失效）")
	}
	if slowCoolingDown("fast-1", model, time.Now()) {
		t.Fatal("同池快号不得被判慢")
	}
}

// TestNoteSlowObservation_IgnoresZeroTTFT 守住不可观测时不留样本。
func TestNoteSlowObservation_IgnoresZeroTTFT(t *testing.T) {
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	const model = "glm-5.3"
	for i := 0; i < 10; i++ {
		noteSlowObservation("zero-1", model, 0)
	}
	if slowCoolingDown("zero-1", model, time.Now()) {
		t.Fatal("TTFT 为 0 表示未观测到首字节，不得据此判慢")
	}
}

// TestNoteSlowObservation_DisabledDoesNothing 守住总开关。
func TestNoteSlowObservation_DisabledDoesNothing(t *testing.T) {
	withSlowConfig(t, false, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	const model = "glm-5.3"
	for i := 0; i < 2; i++ {
		noteSlowObservation("fast-1", model, uint64(4*time.Second))
	}
	for i := 0; i < 10; i++ {
		noteSlowObservation("slow-1", model, uint64(50*time.Second))
	}
	if slowCoolingDown("slow-1", model, time.Now()) {
		t.Fatal("总开关关闭时不得判慢")
	}
}
