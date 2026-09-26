package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPickRandomWorkbuddyModel(t *testing.T) {
	storeDynamicModels(nil)
	saEmpty := &storedAuth{}
	mDefault := pickRandomWorkbuddyModel(saEmpty)
	if mDefault != "deepseek-v4.1-flash" {
		t.Fatalf("expected fallback model deepseek-v4.1-flash, got %s", mDefault)
	}

	fakeModels := []pluginapi.ModelInfo{
		{ID: "fake-model-1"},
		{ID: "fake-model-2"},
	}
	storeDynamicModels(fakeModels)
	defer storeDynamicModels(nil)

	picked := pickRandomWorkbuddyModel(saEmpty)
	if picked != "fake-model-1" && picked != "fake-model-2" {
		t.Fatalf("expected picked model from fakeModels, got %s", picked)
	}
}

func TestShouldActivePingThrottle(t *testing.T) {
	resetActivePingTimes()
	defer resetActivePingTimes()

	authKey := "test-user-123"

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
	triggerActivePingFn = func(authIndex, authID string, sa *storedAuth) error {
		pingCount++
		lastAuthID = authID
		return errors.New("upstream transient ping error")
	}

	sa := &storedAuth{
		Account: storedAccount{UID: "test-uid"},
	}
	err := triggerActivePing("idx-1", "test-auth", sa)
	if err == nil || err.Error() != "upstream transient ping error" {
		t.Fatalf("expected mock ping error, got %v", err)
	}
	if pingCount != 1 || lastAuthID != "test-auth" {
		t.Fatalf("expected ping recorded for test-auth, count=%d", pingCount)
	}
}

func TestHandleTestActiveWorkbuddy(t *testing.T) {
	origPingFn := sendActivePingWorkbuddyFn
	defer func() { sendActivePingWorkbuddyFn = origPingFn }()

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
	sa := &storedAuth{
		Account: storedAccount{UID: "wb-user-001"},
	}
	var calledModel string
	sendActivePingWorkbuddyFn = func(sa *storedAuth, chosenModel string) error {
		calledModel = chosenModel
		return nil
	}

	resOk := handleTestActiveWithAuth(sa, "test-index")
	if resOk["ok"] != true || calledModel == "" {
		t.Fatalf("expected active ping test ok, got %v", resOk)
	}

	// 4. 模拟上游报错
	sendActivePingWorkbuddyFn = func(sa *storedAuth, chosenModel string) error {
		return errors.New("upstream rate limited")
	}
	resErr := handleTestActiveWithAuth(sa, "test-index")
	if resErr["error"] == nil || !strings.Contains(fmt.Sprint(resErr["error"]), "upstream rate limited") {
		t.Fatalf("expected rate limit error, got %v", resErr)
	}
}
