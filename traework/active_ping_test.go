package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPickRandomTraeModel(t *testing.T) {
	storeTraeDynamicModels(nil)
	saEmpty := &traeAuth{}
	mDefault := pickRandomTraeModel(saEmpty)
	if mDefault != "claude-3-5-sonnet" {
		t.Fatalf("expected fallback model claude-3-5-sonnet, got %s", mDefault)
	}

	fakeModels := []pluginapi.ModelInfo{
		{ID: "trae-fake-1"},
		{ID: "trae-fake-2"},
	}
	storeTraeDynamicModels(fakeModels)
	defer storeTraeDynamicModels(nil)

	picked := pickRandomTraeModel(saEmpty)
	if picked != "trae-fake-1" && picked != "trae-fake-2" {
		t.Fatalf("expected picked model from fakeModels, got %s", picked)
	}
}

func TestShouldActivePingThrottle(t *testing.T) {
	resetActivePingTimes()
	defer resetActivePingTimes()

	authKey := "trae-user-123"

	// 1. 首次应该允许
	if !shouldActivePing(authKey) {
		t.Fatalf("first ping should be allowed")
	}

	// 记录活跃时间
	recordActivePing(authKey)

	// 2. 立即再次检查应该被节流拦截
	if shouldActivePing(authKey) {
		t.Fatalf("immediate next ping should be throttled")
	}

	// 3. 模拟时间超过 30 分钟后应该再次允许
	activePingMu.Lock()
	lastActivePingTimes[authKey] = time.Now().Add(-31 * time.Minute)
	activePingMu.Unlock()
	if !shouldActivePing(authKey) {
		t.Fatalf("ping after 31 minutes should be allowed")
	}
}

func TestDoFetchOneWithActivePing(t *testing.T) {
	origPingFn := triggerActivePingFn
	defer func() { triggerActivePingFn = origPingFn }()

	pingCount := 0
	var lastAuthID string
	triggerActivePingFn = func(authIndex, authID string, sa *traeAuth) error {
		pingCount++
		lastAuthID = authID
		return errors.New("upstream transient ping error")
	}

	sa := &traeAuth{
		UserID: "test-uid",
	}
	err := triggerActivePing("idx-1", "test-auth", sa)
	if err == nil || err.Error() != "upstream transient ping error" {
		t.Fatalf("expected mock ping error, got %v", err)
	}
	if pingCount != 1 || lastAuthID != "test-auth" {
		t.Fatalf("expected ping recorded for test-auth, count=%d", pingCount)
	}
}

func TestHandleTestActiveTrae(t *testing.T) {
	origPingFn := sendActivePingTraeFn
	defer func() { sendActivePingTraeFn = origPingFn }()

	// 1. 缺少 auth_index 校验
	resMissing := handleTestActive(pluginapi.ManagementRequest{
		Body: []byte(`{}`),
	})
	if resMissing["error"] != "auth_index is required" {
		t.Fatalf("expected error for missing auth_index, got %v", resMissing)
	}

	// 2. host API 缺省时的凭据获取错误
	resNoHost := handleTestActive(pluginapi.ManagementRequest{
		Body: []byte(`{"auth_index": "test-index"}`),
	})
	if resNoHost["error"] == nil {
		t.Fatalf("expected error for hostAuthGet under test shim, got %v", resNoHost)
	}

	// 3. 模拟凭据成功调用 handleTestActiveWithAuth
	sa := &traeAuth{
		UserID: "trae-user-001",
	}
	var calledModel string
	sendActivePingTraeFn = func(sa *traeAuth, chosenModel string) error {
		calledModel = chosenModel
		return nil
	}

	resOk := handleTestActiveWithAuth(sa, "test-index", "")
	if resOk["ok"] != true || calledModel == "" {
		t.Fatalf("expected active ping test ok, got %v", resOk)
	}

	// 4. 模拟上游报错
	sendActivePingTraeFn = func(sa *traeAuth, chosenModel string) error {
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
	origPingFn := sendActivePingTraeFn
	defer func() { sendActivePingTraeFn = origPingFn }()

	sa := &traeAuth{UserID: "trae-user-spec"}
	var calledModel string
	sendActivePingTraeFn = func(sa *traeAuth, chosenModel string) error {
		calledModel = chosenModel
		return nil
	}

	// 1. 指定模型必须原样透传给上游发送函数并回显在结果里
	resSpec := handleTestActiveWithAuth(sa, "test-index", "gemini-2.5-pro")
	if resSpec["ok"] != true || calledModel != "gemini-2.5-pro" {
		t.Fatalf("expected specified model passthrough, got res=%v called=%q", resSpec, calledModel)
	}
	if resSpec["model"] != "gemini-2.5-pro" {
		t.Fatalf("expected echoed model gemini-2.5-pro, got %v", resSpec["model"])
	}

	// 2. 模型为空时回退随机模型（缓存清空 → 兜底默认值），兼容旧面板
	storeTraeDynamicModels(nil)
	resAuto := handleTestActiveWithAuth(sa, "test-index", "")
	if resAuto["ok"] != true || calledModel != "claude-3-5-sonnet" {
		t.Fatalf("expected fallback random model, got res=%v called=%q", resAuto, calledModel)
	}
}
