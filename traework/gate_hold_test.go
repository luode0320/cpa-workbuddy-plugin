// gate_hold_test.go 锁定「首字闸门的双闸门语义」。
//
// 生产背景（2026-11-09）：闸门按字符数攒够 pseudoCompletionMaxChars(600) 才放行，
// 思考型模型在 reasoning 阶段可能数十秒不满 600 字符，用户侧首字被整段拖后。
// 实测 99.1% 的 traework 请求走闸门（input ≥ 5000 token），闸门组 TTFT p50 5630ms、
// 直通组仅 1866ms。修复后加时间上限：持有超过 pseudoGateMaxHold 即无条件放行。
package main

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// gateSSEFrame 把一段正文包成上游 output 事件的 SSE 帧。
// [参数] text：正文片段。
// [返回] 完整 SSE 帧文本。
// 最近修改时间：2026-11-09 23:00:00；改动原因：新增，测试辅助。
func gateSSEFrame(text string) string {
	return fmt.Sprintf("event: output\ndata: {\"response\":%q}\n\n", text)
}

// TestPseudoGateMaxHoldIsBounded 验证闸门时间上限的取值合理。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-09 22:55:00；改动原因：新增，锁住闸门上限。
func TestPseudoGateMaxHoldIsBounded(t *testing.T) {
	if pseudoGateMaxHold <= 0 {
		t.Fatalf("闸门上限必须为正，实际 %v", pseudoGateMaxHold)
	}
	// 上限必须显著小于上游 TTFT 的 p50（生产 5630ms），否则起不到保首字的作用。
	if pseudoGateMaxHold > 2*time.Second {
		t.Fatalf("闸门上限 %v 过大，无法改善首字体感", pseudoGateMaxHold)
	}
	// 也不能过小：太小会让闸门几乎立即放行，伪完成防护形同虚设。
	if pseudoGateMaxHold < 500*time.Millisecond {
		t.Fatalf("闸门上限 %v 过小，伪完成防护失效", pseudoGateMaxHold)
	}
}

// TestPumpGateOpensOnHoldTimeout 验证持有超时后即使字符不足也会放行。
//
// 构造方式：用 io.Pipe 让两帧之间间隔超过上限，模拟上游 reasoning 阶段
// 长时间不出满 600 字符。第二帧到达时持有时间已超上限，闸门应打开并
// 下发此前缓冲的全部分片。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-09 23:00:00；改动原因：新增，锁住时间上限放行路径。
func TestPumpGateOpensOnHoldTimeout(t *testing.T) {
	ctx := traeStreamPumpContext{Model: "glm-5.3-flashx", StatusCode: 200, InputChars: 30000}
	var emitted int
	emit := func(payload []byte) error {
		emitted++
		return nil
	}

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		_, _ = io.WriteString(pw, gateSSEFrame("短"))
		time.Sleep(pseudoGateMaxHold + 300*time.Millisecond)
		_, _ = io.WriteString(pw, gateSSEFrame("第二帧"))
		_, _ = io.WriteString(pw, "event: done\ndata: {}\n\n")
	}()

	result := pumpTraeStreamAttempt(pr, ctx, "gate-hold", emit)
	if result.GateOpenedAt.IsZero() {
		t.Fatal("持有超时后闸门必须打开")
	}
	if emitted == 0 {
		t.Fatal("持有超时后必须下发已缓冲分片，实际一帧都没发")
	}
}

// TestPumpGateStaysClosedForShortHold 验证持有未超时时闸门仍按字符数判定。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-09 23:00:00；改动原因：新增，反证时间上限没有让闸门失效。
func TestPumpGateStaysClosedForShortHold(t *testing.T) {
	ctx := traeStreamPumpContext{Model: "glm-5.3-flashx", StatusCode: 200, InputChars: 30000}
	var emitted int
	emit := func(payload []byte) error {
		emitted++
		return nil
	}

	// 单帧 2 字符 + 立即 done：持有时间远未超时、字符也不足，闸门全程不开，
	// 且该流被判伪完成 —— pending 必须整体丢弃，一帧都不能下发（防泄漏）。
	sse := gateSSEFrame("短") + "event: done\ndata: {}\n\n"
	result := pumpTraeStreamAttempt(strings.NewReader(sse), ctx, "gate-short", emit)
	if !result.GateOpenedAt.IsZero() {
		t.Fatal("持有未超时且字符不足时闸门不应打开")
	}
	if !result.Pseudo {
		t.Fatal("双轴都短的流应判伪完成")
	}
	if emitted != 0 {
		t.Fatalf("伪完成流不得下发任何分片，实际 %d 帧", emitted)
	}
}
