// 校验测试提示词按能力分组且能轮换。
//
// 依据（2026-11-18）：本插件只做生视频，原来那套「你好 / 今天几号」的对话提示词会
// 让上游用文字作答，账号看似活跃、实际一段片子也出不来。
//
// 依据（2026-11-16）：用户明确要求「不要频繁用相同的提示词测试新会话，会出现风控的」，
// 面板测试与探活每次都发同一句「hi」会被上游按内容重复度判定异常。
package main

import (
	"strings"
	"testing"
)

// TestTestPingPromptRotates 校验提示词池非空、不重复、能轮换。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:10:00；改动原因：图片能力删除，只剩生视频一个池。
func TestTestPingPromptRotates(t *testing.T) {
	if len(testPingPromptsByKind) != 1 {
		t.Fatalf("提示词池只应有生视频一组，实际 %d 组", len(testPingPromptsByKind))
	}
	for kind, prompts := range testPingPromptsByKind {
		if len(prompts) < 2 {
			t.Fatalf("%s 的提示词池至少要有两条，实际 %d 条", kind, len(prompts))
		}
		seen := map[string]bool{}
		for _, prompt := range prompts {
			if prompt == "" {
				t.Errorf("%s 的提示词池里不能出现空串", kind)
			}
			if seen[prompt] {
				t.Errorf("%s 的提示词池里出现重复项 %q", kind, prompt)
			}
			seen[prompt] = true
		}

		// 1. 连续取若干次必须覆盖多条：固定池 + 原子计数，重复概率可忽略。
		picked := map[string]bool{}
		for i := 0; i < 32; i++ {
			picked[testPingPromptFor(kind)] = true
		}
		if len(picked) < 2 {
			t.Errorf("%s 连续 32 次取提示词只出现 %d 条，轮换未生效", kind, len(picked))
		}
	}

	// 2. 提示词必须是「生成一段…」，不能用对话式提问：后者只会换来一段文字。
	for _, prompt := range testPingPromptsByKind[testPingFallbackKind] {
		if !strings.HasPrefix(prompt, "生成") {
			t.Errorf("生视频提示词必须描述生成任务，实际 %q", prompt)
		}
	}

	// 3. 能力未知时回落到生视频池，绝不能返回空串。
	if got := testPingPromptFor("unknown-kind"); got == "" {
		t.Error("未知能力必须回落到可用提示词，实际空串")
	}
}
