package main

import (
	"testing"
)

// TestHandleTestActiveWithAuthSpecifiedModel 验证「测试」弹窗点选模型后的
// 指定模型透传，以及未指定时回退随机模型的向后兼容行为。
func TestHandleTestActiveWithAuthSpecifiedModel(t *testing.T) {
	origPingFn := sendActivePingWorkbuddyFn
	defer func() { sendActivePingWorkbuddyFn = origPingFn }()

	sa := &storedAuth{Account: storedAccount{UID: "wbai-user-spec"}}
	var calledModel string
	sendActivePingWorkbuddyFn = func(sa *storedAuth, chosenModel string) error {
		calledModel = chosenModel
		return nil
	}

	// 1. 指定模型必须原样透传给上游发送函数并回显在结果里
	resSpec := handleTestActiveWithAuth(sa, "test-index", "gpt-6-astra")
	if resSpec["ok"] != true || calledModel != "gpt-6-astra" {
		t.Fatalf("expected specified model passthrough, got res=%v called=%q", resSpec, calledModel)
	}
	if resSpec["model"] != "gpt-6-astra" {
		t.Fatalf("expected echoed model gpt-6-astra, got %v", resSpec["model"])
	}

	// 2. 模型为空时回退随机模型（缓存清空 + 无 token → 兜底默认值），兼容旧面板
	storeDynamicModels(nil)
	resAuto := handleTestActiveWithAuth(sa, "test-index", "")
	if resAuto["ok"] != true || calledModel != "deepseek-v4.1-flash" {
		t.Fatalf("expected fallback random model, got res=%v called=%q", resAuto, calledModel)
	}
}
