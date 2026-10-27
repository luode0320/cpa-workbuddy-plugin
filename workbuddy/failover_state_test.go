// failover_state_test.go 覆盖 sidecar 的核心契约：禁用状态跨插件重启存活。
//
// 为什么必须单独成文件：这里验证的不是「写盘成功」，而是「写盘不生效 / 宿主没
// 吸收 / 插件热重载」这三种情况下路由判据仍然排除该账号。它是 2026-10-27
// 「写盘未生效·仍在接流量」问题的回归防线。
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// isolateFailoverState 把 sidecar 指向临时文件，并在用例结束时还原。
// 生产默认落在 <CPA root>/data/，测试绝不能写到那里。
// [参数] t：测试上下文。
// [返回] 无（还原登记在 t.Cleanup）。
// 最近修改时间：2026-10-27；改动原因：新增。
func isolateFailoverState(t *testing.T) {
	t.Helper()
	restore := isolateFailoverStatePathForTest(filepath.Join(t.TempDir(), "workbuddy-failover.json"))
	t.Cleanup(restore)
}

// TestFailoverStateSurvivesPluginRestart 是本轮改动的核心回归：
// 账号被禁用后插件重启（内存全空、只剩 sidecar 文件），路由判据必须仍然排除它。
//
// 变异方向：删掉 isAccountCoolingDown 里的 failoverStateIsDisabled 分支，或让
// failoverStateMarkDisabled 不落盘，本用例都会失败——这正是修复前的真实缺陷。
//
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增，锁定跨重启排除契约。
func TestFailoverStateSurvivesPluginRestart(t *testing.T) {
	resetFailover(t)
	isolateFailoverState(t)

	const authID = "wb-restart"
	// 1. 禁用：写下 sidecar + 内存集合，模拟一次真实失败后的完整状态。
	failoverStateMarkDisabled(authID, "glm-4.6")
	markFailoverDisabledLocal(authID, "idx-restart")
	if !isAccountCoolingDown(authID) {
		t.Fatal("禁用后应立即被路由判据排除")
	}

	// 2. 模拟插件热重载 / 进程重启：内存集合与内存冷却全清，磁盘 sidecar 保留。
	clearFailoverDisabledLocalAll()
	clearFailoverProbeModels()
	clearFailoverStates()
	resetFailoverStateForTest()

	// 3. 关键断言：内存已空，但 sidecar 仍让该账号被排除。
	if isFailoverDisabledLocal(authID) {
		t.Fatal("用例前提被破坏：重启后内存集合应为空")
	}
	if !isAccountCoolingDown(authID) {
		t.Fatal("重启后 sidecar 未生效：账号会重新接流量（写盘未生效场景的真实缺陷）")
	}
}

// TestFailoverStatePersistsModelForProbe 验证重启后探活仍知道该回放哪个模型。
// 内存集合为空时，模型只能来自 sidecar；拿不到模型就永远探不了，属于永久排除死角。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增。
func TestFailoverStatePersistsModelForProbe(t *testing.T) {
	resetFailover(t)
	isolateFailoverState(t)

	const authID = "wb-model"
	failoverStateMarkDisabled(authID, "deepseek-v4.1-flash")
	resetFailoverStateForTest()

	entry, ok := failoverStateGet(authID)
	if !ok {
		t.Fatal("重启后应从 sidecar 读回该账号")
	}
	if entry.Model != "deepseek-v4.1-flash" {
		t.Fatalf("sidecar 模型 = %q, want deepseek-v4.1-flash", entry.Model)
	}
	if !entry.Disabled {
		t.Fatal("sidecar 应记录禁用")
	}
}

// TestFailoverStateClearedOnReenable 验证解除禁用会同步清掉 sidecar。
// 只清内存会让账号在重启后重新被排除，形成「盘上已启用、插件却回不来」的反向死锁。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增。
func TestFailoverStateClearedOnReenable(t *testing.T) {
	resetFailover(t)
	isolateFailoverState(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-clear", "idx-clear", nil)

	disableAccountForFailover("wb-clear", "glm-4.6")
	if !failoverStateIsDisabled("wb-clear") {
		t.Fatal("禁用后 sidecar 应记录")
	}
	if !reenableAccountForFailover("idx-clear", "wb-clear") {
		t.Fatal("本机制禁用的账号应能解除")
	}
	if failoverStateIsDisabled("wb-clear") {
		t.Fatal("解除后 sidecar 应清除，否则重启后仍被排除")
	}
}

// TestFailoverStateWrittenWhenHostDocUnavailable 验证宿主文档读不到时仍写下 sidecar。
// 这种情况下写盘通道整体不可用，sidecar 是唯一的排除来源；若此处直接放弃，
// 坏号会继续接流量——正是本轮要解决的问题。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增。
func TestFailoverStateWrittenWhenHostDocUnavailable(t *testing.T) {
	resetFailover(t)
	isolateFailoverState(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-nodoc", "idx-nodoc", nil)
	// 让物理文档读取失败，模拟宿主 spool 未就绪。
	origGetPhysical := failoverGetPhysicalFn
	t.Cleanup(func() { failoverGetPhysicalFn = origGetPhysical })
	failoverGetPhysicalFn = func(string) (*hostAuthPhysical, error) {
		return nil, os.ErrNotExist
	}

	if !disableAccountForFailover("wb-nodoc", "glm-4.6") {
		t.Fatal("宿主文档不可用时仍应通过 sidecar 生效")
	}
	if !failoverStateIsDisabled("wb-nodoc") {
		t.Fatal("宿主文档不可用时 sidecar 必须记录禁用")
	}
	resetFailoverStateForTest()
	if !isAccountCoolingDown("wb-nodoc") {
		t.Fatal("重启后仍应排除该账号")
	}
}

// TestFailoverProbeTargetsIncludeSidecarOnly 验证探活候选认 sidecar，
// 而不是只看宿主 f.Disabled。
//
// 为什么必须：宿主没吸收写盘时列表仍报 enabled。若探活也跳过它，这个号既被
// 路由排除、又永远探不回来，只能人工启用——比不排除更糟。
//
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增。
func TestFailoverProbeTargetsIncludeSidecarOnly(t *testing.T) {
	resetFailover(t)
	isolateFailoverState(t)
	h := newFailoverFakeHost(t)
	// 宿主侧明确 enabled（写盘未生效形态），盘上也没有本机制标记。
	h.addAccount("wb-pending", "idx-pending", nil)
	failoverStateMarkDisabled("wb-pending", "glm-4.6")

	targets := failoverProbeTargets()
	if len(targets) != 1 {
		t.Fatalf("候选数 = %d, want 1（宿主报 enabled 也要能探活）: %+v", len(targets), targets)
	}
	if targets[0].authIndex != "idx-pending" || targets[0].model != "glm-4.6" {
		t.Fatalf("候选不符: %+v", targets[0])
	}
}

// TestReconcileKeepsSidecarWhenHostEnabledButDiskDisabled 验证对账不会把
// 「写盘未被宿主吸收」误判成「用户已启用」。
//
// 判据必须是盘上 disabled:false，而不是宿主报 enabled：后者在本机制写盘未落地时
// 同样成立，按它清排除等于把刚摘掉的坏号放回流量。
//
// 造分叉的方式：假宿主的 Disabled 默认由盘上 disabled 推导，两者永远一致，
// 因此这里把 list 包一层强制报 enabled，才能复刻「宿主没吸收」这一真实形态。
//
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增。
func TestReconcileKeepsSidecarWhenHostEnabledButDiskDisabled(t *testing.T) {
	resetFailover(t)
	isolateFailoverState(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-pend2", "idx-pend2", nil)
	// 盘上 disabled + 本机制标记，但宿主列表仍报 enabled（写盘未吸收）。
	doc := h.docOf("idx-pend2")
	doc["disabled"] = true
	doc[failoverDisableKey] = true
	doc[failoverDisableModelKey] = "glm-4.6"
	failoverStateMarkDisabled("wb-pend2", "glm-4.6")
	markFailoverDisabledLocal("wb-pend2", "idx-pend2")
	forceHostEnabledInList(t)

	cleared := reconcileFailoverLocalSet()
	if cleared != 0 {
		t.Fatalf("对账清除数 = %d, want 0（写盘未吸收不是用户启用）", cleared)
	}
	if !failoverStateIsDisabled("wb-pend2") {
		t.Fatal("sidecar 不应被清：宿主报 enabled 但盘上仍 disabled")
	}
	if !isAccountCoolingDown("wb-pend2") {
		t.Fatal("该账号应继续被排除")
	}
}

// forceHostEnabledInList 让宿主列表对所有账号都报 enabled，模拟「写盘未被吸收」。
// 覆盖 failoverListFn 而不是改假盘文档：改文档会同时改变物理读盘结果，
// 两者一起变就造不出分叉，等于用例自证其结论。
// [参数] t：测试上下文。
// [返回] 无（还原登记在 t.Cleanup）。
// 最近修改时间：2026-10-27；改动原因：新增。
func forceHostEnabledInList(t *testing.T) {
	t.Helper()
	origList := failoverListFn
	t.Cleanup(func() { failoverListFn = origList })
	failoverListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		files, err := origList()
		if err != nil {
			return nil, err
		}
		for i := range files {
			files[i].Disabled = false
		}
		return files, nil
	}
}

// TestReconcileClearsSidecarWhenUserEnabledOnDisk 验证真正的用户启用仍能解除排除，
// 否则 sidecar 会把账号永久挡在路由外。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增。
func TestReconcileClearsSidecarWhenUserEnabledOnDisk(t *testing.T) {
	resetFailover(t)
	isolateFailoverState(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-user", "idx-user", nil)
	failoverStateMarkDisabled("wb-user", "glm-4.6")
	markFailoverDisabledLocal("wb-user", "idx-user")
	// 用户在外部启用：盘上 disabled:false 且本机制标记已被清掉。
	h.docOf("idx-user")["disabled"] = false

	cleared := reconcileFailoverLocalSet()
	if cleared == 0 {
		t.Fatal("盘上已启用时应清除排除状态")
	}
	if failoverStateIsDisabled("wb-user") {
		t.Fatal("用户启用后 sidecar 应清除")
	}
	if isAccountCoolingDown("wb-user") {
		t.Fatal("用户启用后账号应恢复可路由")
	}
}

// TestFailoverStateIgnoresCorruptFile 验证损坏的 sidecar 不会让插件崩溃或误禁。
// 读取失败一律按空状态处理：丢一次禁用只会让号多接一点流量，panic 会让整插件不可用。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增。
func TestFailoverStateIgnoresCorruptFile(t *testing.T) {
	resetFailover(t)
	path := filepath.Join(t.TempDir(), "workbuddy-failover.json")
	restore := isolateFailoverStatePathForTest(path)
	t.Cleanup(restore)
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("写入损坏文件失败: %v", err)
	}

	if failoverStateIsDisabled("wb-corrupt") {
		t.Fatal("损坏文件应按空状态处理，不得误判为禁用")
	}
	// 写一次仍应正常：损坏文件不应阻断后续落盘。
	failoverStateMarkDisabled("wb-corrupt", "glm-4.6")
	if !failoverStateIsDisabled("wb-corrupt") {
		t.Fatal("损坏文件后应能重新写入并生效")
	}
}

// TestFailoverStateMarkIsIdempotent 验证同账号同模型重复禁用不重复落盘。
// 连续失败可能高频触发禁用，每次写盘等于把失败放大成磁盘 IO。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增。
func TestFailoverStateMarkIsIdempotent(t *testing.T) {
	resetFailover(t)
	isolateFailoverState(t)

	failoverStateMarkDisabled("wb-idem", "glm-4.6")
	path := failoverStatePath()
	first, err := os.Stat(path)
	if err != nil {
		t.Fatalf("首次落盘后文件应存在: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	failoverStateMarkDisabled("wb-idem", "glm-4.6")
	second, err := os.Stat(path)
	if err != nil {
		t.Fatalf("二次调用后文件应存在: %v", err)
	}
	if !first.ModTime().Equal(second.ModTime()) {
		t.Fatal("同账号同模型重复禁用不应重复写盘")
	}
}

// TestFailoverStateNormalizesKeys 验证文件名 / UID / auth_index 三种形态
// 归一到同一个 sidecar 键，否则「禁用写一个键、路由查另一个键」会漏排除。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增。
func TestFailoverStateNormalizesKeys(t *testing.T) {
	resetFailover(t)
	isolateFailoverState(t)

	failoverStateMarkDisabled("workbuddy-abc123.json", "glm-4.6")
	for _, alias := range []string{"abc123", "workbuddy-abc123", "WORKBUDDY-ABC123.JSON"} {
		if !failoverStateIsDisabled(alias) {
			t.Fatalf("别名 %q 应命中同一条 sidecar 记录", alias)
		}
	}
}

// resetFailoverStateForTest 清空内存镜像与载入标记，仅供测试使用。
//
// 它刻意保留磁盘文件：「重启后状态仍在」这条契约正需要「只清内存、留盘上文件」
// 来复刻。要在用例之间彻底清场请用 clearFailoverStateForTest。
//
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增，避免并发用例互相污染。
func resetFailoverStateForTest() {
	failoverStateMu.Lock()
	failoverStateAccounts = make(map[string]failoverStateEntry)
	failoverStateLoaded = false
	failoverStateMu.Unlock()
}

// clearFailoverStateForTest 清空内存镜像并删除磁盘 sidecar，仅供测试清场使用。
//
// 为什么必须连磁盘一起清：sidecar 是进程级共享状态，用例 A 写下的禁用条目会留在
// 文件里，用例 B 首次读取时被重新载入，于是 B 里的假账号凭空处于禁用中——
// 表现为「单独跑通过、整套跑失败」的随机 FAIL。生产路径绝不调用它：生产只能
// 按账号逐个解除禁用，整表删除等于把所有坏号一次性放回路由。
//
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增，补齐测试清场对磁盘状态的覆盖。
func clearFailoverStateForTest() {
	path := ""
	failoverStateMu.Lock()
	path = failoverStatePath()
	failoverStateAccounts = make(map[string]failoverStateEntry)
	failoverStateLoaded = false
	failoverStateMu.Unlock()
	if path != "" {
		_ = os.Remove(path)
	}
}

// isolateFailoverStatePathForTest 把 sidecar 指向调用方提供的临时文件，返回还原函数。
// 测试里用它把状态隔离到 t.TempDir()，避免用例之间经由真实 data 目录互相污染，
// 也避免测试写到生产插件目录。
// [参数] path：临时文件路径。
// [返回] 还原函数。
// 最近修改时间：2026-10-27；改动原因：新增。
func isolateFailoverStatePathForTest(path string) func() {
	failoverStateMu.Lock()
	old := failoverStatePathOverride
	failoverStatePathOverride = path
	failoverStateAccounts = make(map[string]failoverStateEntry)
	failoverStateLoaded = false
	failoverStateMu.Unlock()
	return func() {
		failoverStateMu.Lock()
		failoverStatePathOverride = old
		failoverStateAccounts = make(map[string]failoverStateEntry)
		failoverStateLoaded = false
		failoverStateMu.Unlock()
	}
}
