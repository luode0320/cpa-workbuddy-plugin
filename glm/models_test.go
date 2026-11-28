package main

import (
	"strings"
	"testing"
)

// TestResolveAssistantID 验证智能体 ID 与思考模式映射。
func TestResolveAssistantID(t *testing.T) {
	id, mode := resolveAssistantID("glm/glm-think")
	if id != defaultAssistantID || mode != "zero" {
		t.Fatalf("think model mismatch: %s %s", id, mode)
	}
	id, mode = resolveAssistantID("glm/glm-deepresearch")
	if mode != "deep_research" {
		t.Fatalf("deepresearch mode mismatch: %s", mode)
	}
	agent := "abcdefghijklmnopqrstuvwx"
	id, _ = resolveAssistantID(agent)
	if id != agent {
		t.Fatalf("24-char agent id not honored: %s", id)
	}
}

// TestModelAliasPrefixIsolated 验证所有默认模型都带 glm/ 前缀，避免与 zcode 冲突。
func TestModelAliasPrefixIsolated(t *testing.T) {
	for _, m := range defaultModels {
		if !strings.HasPrefix(m, "glm/") {
			t.Fatalf("model %q missing glm/ prefix", m)
		}
	}
}

// TestMergeMessages 验证多轮消息合并带角色标记。
func TestMergeMessages(t *testing.T) {
	msgs := []completionMessage{
		{Role: "system", Content: "你是助手"},
		{Role: "user", Content: "你好"},
	}
	got := mergeMessages(msgs)
	if !strings.Contains(got, "你好") || !strings.Contains(got, "<|user|>") {
		t.Fatalf("merge result wrong: %q", got)
	}
}
