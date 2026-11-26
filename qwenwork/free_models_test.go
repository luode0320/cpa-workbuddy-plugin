// free_models_test.go 验证免费模型判据与「账号×模型」路由准入。
package main

import (
	"testing"
)

func resetFreeModels(t *testing.T) {
	t.Helper()
	old := freeModelRegistry
	t.Cleanup(func() { freeModelRegistry = old })
	freeModelRegistry.ids = nil
	freeModelRegistry.discovered = false
}

// TestIsFreeModel_FallbackAndDynamic 验证静态兜底与动态注册两种来源。
func TestIsFreeModel_FallbackAndDynamic(t *testing.T) {
	resetFreeModels(t)
	if !isFreeModel("minimax-m3.1-flash") {
		t.Fatal("fallback free model must be recognized")
	}
	if isFreeModel("qwen4-max") {
		t.Fatal("paid model must not be free")
	}
	// 动态注册后以动态结果为准：新增 free-x，移除 minimax。
	setFreeModels([]string{"free-x", " qwen4-max "})
	if !isFreeModel("free-x") || !isFreeModel("qwen4-max") {
		t.Fatal("dynamically registered free models must be recognized")
	}
	if isFreeModel("minimax-m3.1-flash") {
		t.Fatal("fallback must yield to discovered set")
	}
}

// TestParseFreeModelIDs 验证 price_factor=0 且 enable 才是免费模型。
func TestParseFreeModelIDs(t *testing.T) {
	raw := []byte(`{"schema_version":2,"catalog":[` +
		`{"key":"free","enable":true,"price_factor":0},` +
		`{"key":"paid","enable":true,"price_factor":0.1},` +
		`{"key":"disabled-free","enable":false,"price_factor":0},` +
		`{"key":"","enable":true,"price_factor":0}` +
		`]}`)
	ids := parseFreeModelIDs(raw)
	if len(ids) != 1 || ids[0] != "free" {
		t.Fatalf("parseFreeModelIDs = %v, want [free]", ids)
	}
}

// TestAccountUsableForModel 验证 0 积分账号仅免费模型放行。
func TestAccountUsableForModel(t *testing.T) {
	resetFreeModels(t)
	restoreMin := setMinRoutableCredits(20)
	t.Cleanup(restoreMin)
	accountCache.Store("zero-account", &accountCacheEntry{credits: &creditsSummary{TotalRemain: 0, TotalUsed: 100, TotalSize: 100}})
	t.Cleanup(func() { accountCache.Delete("zero-account") })

	if !accountUsableForModel("zero-account", "minimax-m3.1-flash") {
		t.Fatal("free model must be routable for a zero-credit account")
	}
	if accountUsableForModel("zero-account", "qwen4-max") {
		t.Fatal("paid model must be blocked for a zero-credit account")
	}
	// 未知积分账号两样都放行（不做误判）。
	if !accountUsableForModel("unknown-account", "qwen4-max") {
		t.Fatal("unknown credits must stay routable")
	}
}

// TestHasFreeModels 验证 discover 后空集会让 hasFreeModels 返回 false。
func TestHasFreeModels(t *testing.T) {
	resetFreeModels(t)
	if !hasFreeModels() {
		t.Fatal("fallback set must report free models available")
	}
	setFreeModels(nil)
	if hasFreeModels() {
		t.Fatal("empty discovered set must report no free models")
	}
}
