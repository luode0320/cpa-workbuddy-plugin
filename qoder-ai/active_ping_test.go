package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestPickRandomQoderModel 验证缓存缺失时回退兜底模型、缓存命中时从候选集中选取。
func TestPickRandomQoderModel(t *testing.T) {
	storeDynamicModels(nil)
	saEmpty := &storedAuth{}
	if m := pickRandomQoderModel(saEmpty); m != "auto" {
		t.Fatalf("expected fallback model auto, got %s", m)
	}

	fakeModels := []pluginapi.ModelInfo{
		{ID: "fake-model-1"},
		{ID: "fake-model-2"},
	}
	storeDynamicModels(fakeModels)
	defer storeDynamicModels(nil)

	picked := pickRandomQoderModel(saEmpty)
	if picked != "fake-model-1" && picked != "fake-model-2" {
		t.Fatalf("expected picked model from fakeModels, got %s", picked)
	}
}

// TestPickRandomQoderModels 验证多模型随机挑选的上限、去重与 fallback 行为。
func TestPickRandomQoderModels(t *testing.T) {
	storeDynamicModels(nil)
	saEmpty := &storedAuth{}
	resFallback := pickRandomQoderModels(saEmpty, 5)
	if len(resFallback) != 1 || resFallback[0] != "auto" {
		t.Fatalf("expected fallback model, got %v", resFallback)
	}

	fakeModels := []pluginapi.ModelInfo{
		{ID: "m1"}, {ID: "m2"}, {ID: "m3"}, {ID: "m4"}, {ID: "m5"}, {ID: "m6"}, {ID: "m1"},
	}
	storeDynamicModels(fakeModels)
	defer storeDynamicModels(nil)

	res5 := pickRandomQoderModels(saEmpty, 5)
	if len(res5) != 5 {
		t.Fatalf("expected 5 models, got %d (%v)", len(res5), res5)
	}
	seen := make(map[string]bool)
	for _, m := range res5 {
		if seen[m] {
			t.Fatalf("unexpected duplicate model %s in %v", m, res5)
		}
		seen[m] = true
	}

	res2 := pickRandomQoderModels(saEmpty, 2)
	if len(res2) != 2 {
		t.Fatalf("expected 2 models, got %d (%v)", len(res2), res2)
	}
}

// TestHandleTestActiveQoder 验证缺参、凭据获取失败与成功/失败路径。
func TestHandleTestActiveQoder(t *testing.T) {
	origPingFn := sendActivePingQoderFn
	defer func() { sendActivePingQoderFn = origPingFn }()

	// 1. 缺少 auth_index 校验
	resMissing := handleTestActive(pluginapi.ManagementRequest{Body: []byte(`{}`)})
	if resMissing["error"] != "auth_index is required" {
		t.Fatalf("expected error for missing auth_index, got %v", resMissing)
	}

	// 2. host API 缺省时的凭据获取错误
	resNoHost := handleTestActive(pluginapi.ManagementRequest{Body: []byte(`{"auth_index": "test-index"}`)})
	if resNoHost["error"] == nil {
		t.Fatalf("expected error for hostAuthGet under test shim, got %v", resNoHost)
	}

	// 3. 模拟凭据成功调用 handleTestActiveWithAuth
	sa := &storedAuth{Account: storedAccount{UID: "qw-user-001"}}
	var calledModel string
	sendActivePingQoderFn = func(sa *storedAuth, chosenModel string) error {
		calledModel = chosenModel
		return nil
	}
	resOk := handleTestActiveWithAuth(sa, "test-index", "")
	if resOk["ok"] != true || calledModel == "" {
		t.Fatalf("expected active ping test ok, got %v", resOk)
	}

	// 4. 模拟上游报错
	sendActivePingQoderFn = func(sa *storedAuth, chosenModel string) error {
		return errors.New("upstream rate limited")
	}
	resErr := handleTestActiveWithAuth(sa, "test-index", "")
	if resErr["error"] == nil || !strings.Contains(fmt.Sprint(resErr["error"]), "upstream rate limited") {
		t.Fatalf("expected rate limit error, got %v", resErr)
	}
}

// TestHandleTestActiveWithAuthSpecifiedModel 验证「测试」弹窗点选模型后的
// 指定模型透传，以及未指定时回退随机模型的向后兼容行为。
func TestHandleTestActiveWithAuthSpecifiedModel(t *testing.T) {
	origPingFn := sendActivePingQoderFn
	defer func() { sendActivePingQoderFn = origPingFn }()

	sa := &storedAuth{Account: storedAccount{UID: "qw-user-spec"}}
	var calledModel string
	sendActivePingQoderFn = func(sa *storedAuth, chosenModel string) error {
		calledModel = chosenModel
		return nil
	}

	// 1. 指定模型必须原样透传给上游发送函数并回显在结果里
	resSpec := handleTestActiveWithAuth(sa, "test-index", "qmodel_latest")
	if resSpec["ok"] != true || calledModel != "qmodel_latest" {
		t.Fatalf("expected specified model passthrough, got res=%v called=%q", resSpec, calledModel)
	}
	if resSpec["model"] != "qmodel_latest" {
		t.Fatalf("expected echoed model qmodel_latest, got %v", resSpec["model"])
	}

	// 2. 模型为空时回退随机模型（缓存清空 + 无 token → 兜底默认值），兼容旧面板
	storeDynamicModels(nil)
	resAuto := handleTestActiveWithAuth(sa, "test-index", "")
	if resAuto["ok"] != true || calledModel != "auto" {
		t.Fatalf("expected fallback random model, got res=%v called=%q", resAuto, calledModel)
	}
}


// TestDoActivePingMultiModelAnySuccess 验证多模型轮测中任意一个成功即判定健康且立即终止后续测试。
func TestDoActivePingMultiModelAnySuccess(t *testing.T) {
	resetActivePingTimes()
	defer resetActivePingTimes()

	fakeModels := []pluginapi.ModelInfo{
		{ID: "fail-1"}, {ID: "fail-2"}, {ID: "succ-3"}, {ID: "fail-4"}, {ID: "fail-5"},
	}
	storeDynamicModels(fakeModels)
	defer storeDynamicModels(nil)

	origPingFn := sendActivePingQoderFn
	defer func() { sendActivePingQoderFn = origPingFn }()

	var attempted []string
	sendActivePingQoderFn = func(sa *storedAuth, chosenModel string) error {
		attempted = append(attempted, chosenModel)
		if chosenModel == "succ-3" {
			return nil
		}
		return errors.New("upstream failed for " + chosenModel)
	}

	sa := &storedAuth{Account: storedAccount{UID: "qw-user-any-succ"}}
	err := doActivePing("idx-succ", "qw-user-any-succ", sa)
	if err != nil {
		t.Fatalf("expected any-success to return nil, got %v", err)
	}

	foundSucc := false
	for _, m := range attempted {
		if m == "succ-3" {
			foundSucc = true
			break
		}
	}
	if !foundSucc {
		t.Fatalf("expected succ-3 to be attempted, got attempts: %v", attempted)
	}

	// 再次调用应该被 30 分钟节流直接命中
	if shouldActivePing("qw-user-any-succ") {
		t.Fatalf("expected throttle to trigger after successful active ping")
	}
}

// TestDoActivePingMultiModelAllFail 验证所有候选模型均失败时返回错误。
func TestDoActivePingMultiModelAllFail(t *testing.T) {
	resetActivePingTimes()
	defer resetActivePingTimes()

	fakeModels := []pluginapi.ModelInfo{
		{ID: "fail-1"}, {ID: "fail-2"},
	}
	storeDynamicModels(fakeModels)
	defer storeDynamicModels(nil)

	origPingFn := sendActivePingQoderFn
	defer func() { sendActivePingQoderFn = origPingFn }()

	attemptCount := 0
	sendActivePingQoderFn = func(sa *storedAuth, chosenModel string) error {
		attemptCount++
		return errors.New("all models down")
	}

	sa := &storedAuth{Account: storedAccount{UID: "qw-user-all-fail"}}
	err := doActivePing("idx-fail", "qw-user-all-fail", sa)
	if err == nil {
		t.Fatalf("expected error when all models fail")
	}
	if attemptCount != 2 {
		t.Fatalf("expected 2 attempts for 2 available models, got %d", attemptCount)
	}
}
