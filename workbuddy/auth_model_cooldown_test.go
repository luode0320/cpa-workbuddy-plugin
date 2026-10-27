package main

import (
	"testing"
	"time"
)

// TestModelCooldownOnlyBlocksSameModel 锁定账号×模型维度的核心语义：
// 限流类失败只冷却出问题的模型，该账号在其它模型上仍可正常路由。
func TestModelCooldownOnlyBlocksSameModel(t *testing.T) {
	resetFailover(t)

	const authID = "auth-model-scope"
	const badModel = "deepseek-v4.1-flash"
	const goodModel = "glm-4.6"

	// 429 属账号×模型级。
	if !recordAccountFailureForModel(authID, badModel, 429, "rate limit exceeded") {
		t.Fatal("429 应计入冷却")
	}

	if !isAuthModelCoolingDown(authID, badModel) {
		t.Fatal("失败模型应处于冷却")
	}
	if isAuthModelCoolingDown(authID, goodModel) {
		t.Fatal("其它模型不应被账号×模型级冷却挡住")
	}
	// 账号级判定必须为假：模型维度不得把整个账号摘掉。
	if isAccountCoolingDown(authID) {
		t.Fatal("账号×模型级冷却不应污染账号维度")
	}

	// 模型维度计数从 1 开始，阶梯与账号层一致（10 分钟）。
	count, until, ok := authModelFailoverSnapshot(authID, badModel)
	if !ok || count != 1 {
		t.Fatalf("模型维度计数 = %d, ok = %v; want 1", count, ok)
	}
	if remain := time.Until(until); remain <= 10*time.Minute-5*time.Second || remain > 10*time.Minute+5*time.Second {
		t.Fatalf("模型维度冷却剩余 = %v, want ~10m", remain)
	}
}

// TestAccountFailureStillBlocksAllModels 锁定账号级失败语义不变：
// 401/403/传输错误等仍会让账号在所有模型上让位。
func TestAccountFailureStillBlocksAllModels(t *testing.T) {
	resetFailover(t)

	const authID = "auth-account-scope"
	if !recordAccountFailureForModel(authID, "glm-4.6", 403, "forbidden") {
		t.Fatal("403 应计入冷却")
	}
	if !isAccountCoolingDown(authID) {
		t.Fatal("账号级失败必须让账号整体冷却")
	}
	// 账号级冷却下，任何模型都应被挡住。
	if !isAuthModelCoolingDown(authID, "glm-4.6") {
		t.Fatal("账号级冷却应挡住原模型")
	}
	if !isAuthModelCoolingDown(authID, "other-model") {
		t.Fatal("账号级冷却应挡住其它模型")
	}
}

// TestModelCooldownLadderIncrements 验证模型维度同样走阶梯：重复失败递增，
// 且与账号层计数彼此独立。
func TestModelCooldownLadderIncrements(t *testing.T) {
	resetFailover(t)

	const authID = "auth-model-ladder"
	const model = "deepseek-v4.1-flash"

	wants := []time.Duration{10 * time.Minute, 20 * time.Minute, 30 * time.Minute}
	for i, want := range wants {
		recordAccountFailureForModel(authID, model, 429, "rate limit")
		count, until, ok := authModelFailoverSnapshot(authID, model)
		if !ok || count != i+1 {
			t.Fatalf("第 %d 次失败后计数 = %d, ok = %v; want %d", i+1, count, ok, i+1)
		}
		remain := time.Until(until)
		if remain <= want-5*time.Second || remain > want+5*time.Second {
			t.Fatalf("第 %d 次失败后冷却剩余 = %v, want ~%v", i+1, remain, want)
		}
		// 账号层计数不应被模型维度失败推动。
		if count, _, ok := failoverStateSnapshot(authID); ok && count != 0 {
			t.Fatalf("模型维度失败不应推动账号层计数，got %d", count)
		}
	}
}

// TestModelCooldownClearedOnSuccess 验证成功请求会清掉模型维度残留，
// 已恢复的账号不会被历史模型冷却继续挡住。
func TestModelCooldownClearedOnSuccess(t *testing.T) {
	resetFailover(t)

	const authID = "auth-model-reset"
	const model = "deepseek-v4.1-flash"

	recordAccountFailureForModel(authID, model, 429, "rate limit")
	if !isAuthModelCoolingDown(authID, model) {
		t.Fatal("前置条件不成立：应处于模型冷却")
	}

	resetAccountFailover(authID)

	if isAuthModelCoolingDown(authID, model) {
		t.Fatal("成功请求后模型冷却应被清除")
	}
	if _, _, ok := authModelFailoverSnapshot(authID, model); ok {
		t.Fatal("成功请求后模型维度记录应被清除")
	}
}

// TestResetAuthModelFailoverKeepsAccountState 验证精确清除只影响指定模型，
// 不误伤其它模型的冷却状态。
func TestResetAuthModelFailoverKeepsAccountState(t *testing.T) {
	resetFailover(t)

	const authID = "auth-model-precise"
	recordAccountFailureForModel(authID, "model-a", 429, "rate limit")
	recordAccountFailureForModel(authID, "model-b", 429, "rate limit")

	resetAuthModelFailover(authID, "model-a")

	if isAuthModelCoolingDown(authID, "model-a") {
		t.Fatal("model-a 应已被精确清除")
	}
	if !isAuthModelCoolingDown(authID, "model-b") {
		t.Fatal("model-b 的冷却不应被误清")
	}
}

// TestLastFailoverModelRecorded 验证最后失败模型被记录，供周期 3 探活回放。
func TestLastFailoverModelRecorded(t *testing.T) {
	resetFailover(t)

	const authID = "auth-last-model"
	recordAccountFailureForModel(authID, "model-x", 429, "rate limit")
	if got := lastFailoverModel(authID); got != "model-x" {
		t.Fatalf("lastFailoverModel = %q, want %q", got, "model-x")
	}

	// 账号级失败同样记录模型，探活需要知道用哪个模型回放。
	recordAccountFailureForModel(authID, "model-y", 403, "forbidden")
	if got := lastFailoverModel(authID); got != "model-y" {
		t.Fatalf("账号级失败后 lastFailoverModel = %q, want %q", got, "model-y")
	}

	// 成功后清空。
	resetAccountFailover(authID)
	if got := lastFailoverModel(authID); got != "" {
		t.Fatalf("成功后 lastFailoverModel 应清空，got %q", got)
	}
}

// TestModelCooldownEmptyModelFallsBackToAccount 验证模型名为空时失败不会被
// 静默丢弃，而是退化为账号级记账——保证尚未传模型的旧调用点行为不变。
func TestModelCooldownEmptyModelFallsBackToAccount(t *testing.T) {
	resetFailover(t)

	const authID = "auth-empty-model"
	if !recordAccountFailureForModel(authID, "", 429, "rate limit") {
		t.Fatal("模型名为空时 429 仍应记账")
	}
	if !isAccountCoolingDown(authID) {
		t.Fatal("无模型维度可用时应退化为账号级冷却")
	}
}

// TestModelCooldownIsolatedAcrossAccounts 验证模型冷却按账号隔离：
// A 账号在 model-x 上的冷却不影响 B 账号。
func TestModelCooldownIsolatedAcrossAccounts(t *testing.T) {
	resetFailover(t)

	recordAccountFailureForModel("auth-a", "model-x", 429, "rate limit")

	if !isAuthModelCoolingDown("auth-a", "model-x") {
		t.Fatal("auth-a 应处于 model-x 冷却")
	}
	if isAuthModelCoolingDown("auth-b", "model-x") {
		t.Fatal("auth-b 不应受 auth-a 的模型冷却影响")
	}
}
