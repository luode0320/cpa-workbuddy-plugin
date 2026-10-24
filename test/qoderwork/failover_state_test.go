// failover_state_test.go 锁死 sidecar 的三条核心契约：
//
//  1. 跨重启存活：写盘后重建内存镜像，状态必须还能读回来；
//  2. 脏数据容错：文件损坏、版本不认识一律按空状态处理，绝不 panic；
//  3. 路由判据生效：sidecar 标记的禁用必须让 isAccountCoolingDown 返回 true。
//
// 这三条是「不再依赖宿主 auth 文件」这个重构的地基——生产上宿主会按内存
// Status 写回覆盖 disabled、fsnotify 会静默失效（2026-10-25 实证），
// 若 sidecar 自身不可靠，整条链路就没有任何可信来源。
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// setupFailoverStateDir 把 sidecar 指向临时目录，返回文件路径与还原函数。
func setupFailoverStateDir(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), failoverStateFileName)
	restore := isolateFailoverStatePathForTest(path)
	t.Cleanup(restore)
	return path
}

// TestFailoverState_SurvivesRestart 覆盖契约 1：写盘后状态必须能重建读回。
func TestFailoverState_SurvivesRestart(t *testing.T) {
	path := setupFailoverStateDir(t)

	failoverStateMarkDisabled("qoderwork-abc123", "kmodel_latest")
	if !failoverStateIsDisabled("qoderwork-abc123") {
		t.Fatal("写入后应立即判定为禁用")
	}

	// 模拟进程重启：丢掉内存镜像，从磁盘重新载入。
	resetFailoverStateForTest()

	if !failoverStateIsDisabled("qoderwork-abc123") {
		t.Fatal("重启后禁用状态丢失，sidecar 未真正落盘")
	}
	entry, ok := failoverStateGet("qoderwork-abc123")
	if !ok || entry.Model != "kmodel_latest" {
		t.Fatalf("探活模型未随状态存活: ok=%v entry=%+v", ok, entry)
	}

	// 落盘文件必须是合法 JSON 且版本正确。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 sidecar: %v", err)
	}
	var doc failoverStateFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("sidecar 不是合法 JSON: %v", err)
	}
	if doc.Version != failoverStateVersion {
		t.Fatalf("sidecar 版本 = %d, want %d", doc.Version, failoverStateVersion)
	}
}

// TestFailoverState_ToleratesCorruptFile 覆盖契约 2：脏数据不得让插件崩溃。
func TestFailoverState_ToleratesCorruptFile(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{name: "非法 JSON", content: "not json at all"},
		{name: "版本不认识", content: `{"version":999,"accounts":{"x":{"disabled":true}}}`},
		{name: "空文件", content: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := setupFailoverStateDir(t)
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("准备脏文件: %v", err)
			}
			// 不得 panic，且按空状态处理。
			if got := failoverStateDisabledIDs(); len(got) != 0 {
				t.Fatalf("脏文件应被忽略，却读出 %d 条禁用: %v", len(got), got)
			}
		})
	}
}

// TestFailoverState_DisabledDrivesRouting 覆盖契约 3：
// sidecar 的禁用标记必须让路由判据成立，且清除后恢复可路由。
func TestFailoverState_DisabledDrivesRouting(t *testing.T) {
	setupFailoverStateDir(t)
	resetFailover(t)

	const authID = "qoderwork-routing-fixture"
	if isAccountCoolingDown(authID) {
		t.Fatal("前置条件不成立：未禁用时不应被排除")
	}

	failoverStateMarkDisabled(authID, "kmodel_latest")
	if !isAccountCoolingDown(authID) {
		t.Fatal("sidecar 标记禁用后，路由仍在放行该账号")
	}

	failoverStateClearDisabled(authID)
	if isAccountCoolingDown(authID) {
		t.Fatal("清除 sidecar 禁用后，账号应重新可路由")
	}
}

// TestFailoverState_ClearOnlyTouchesTarget 确保清除操作不会误伤其他账号。
func TestFailoverState_ClearOnlyTouchesTarget(t *testing.T) {
	setupFailoverStateDir(t)

	failoverStateMarkDisabled("acc-a", "m1")
	failoverStateMarkDisabled("acc-b", "m2")
	failoverStateClearDisabled("acc-a")

	if failoverStateIsDisabled("acc-a") {
		t.Fatal("acc-a 应已解除禁用")
	}
	if !failoverStateIsDisabled("acc-b") {
		t.Fatal("acc-b 的禁用被误清，清除操作影响面越界")
	}
}

// TestFailoverState_CooldownPersistsAcrossRestart 锁定冷却的跨重启语义：
// 插件热重载不能等于给所有坏号免冷却。
func TestFailoverState_CooldownPersistsAcrossRestart(t *testing.T) {
	setupFailoverStateDir(t)
	resetFailover(t)

	const authID = "qoderwork-cooldown-fixture"
	recordAccountFailure(authID, 403, "403: forbidden")
	if !isAccountCoolingDown(authID) {
		t.Fatal("前置条件不成立：失败后应进入冷却")
	}

	// 模拟重启：清内存阶梯，只剩 sidecar。
	clearFailoverStates()

	if !isAccountCoolingDown(authID) {
		t.Fatal("重启后冷却丢失，插件热重载等于给坏号免冷却")
	}
}
