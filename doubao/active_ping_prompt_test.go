// 校验测试提示词轮换：多次调用不能总取到同一条。
//
// 依据（2026-11-16）：用户明确要求「不要频繁用相同的提示词测试新会话，会出现风控的」，
// 面板测试与探活每次都发同一句「hi」会被上游按内容重复度判定异常。
package main

import (
	"testing"
)

// TestTestPingPromptRotates 校验提示词池非空且能取到多条。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-16 05:06:00；改动原因：新增，测试提示词轮换回归。
func TestTestPingPromptRotates(t *testing.T) {
	if len(testPingPrompts) < 2 {
		t.Fatalf("测试提示词池至少要有两条，实际 %d 条", len(testPingPrompts))
	}
	for _, prompt := range testPingPrompts {
		if prompt == "" {
			t.Error("提示词池里不能出现空串")
		}
	}

	// 连续取若干次必须至少覆盖两条：固定池 + 纳秒取模，重复概率可忽略。
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		seen[testPingPrompt()] = true
	}
	if len(seen) < 2 {
		t.Errorf("连续 32 次取提示词只出现 %d 条，轮换未生效", len(seen))
	}
}
