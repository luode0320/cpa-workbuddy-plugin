package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 本文件锁定「积分巡检持久化 + 低额自动禁用」的核心契约。
//
// 每条用例都对应一个会真实造成事故的路径：
//   - 阈值边界错位会把健康号误禁；
//   - 签到与查余额顺序颠倒会把刚领到 1500 分的号误禁一小时；
//   - 未知积分当 0 会把整池写死；
//   - 归属判定缺失会违背用户手动禁用的意图。
//
// 因此这些用例必须自带变异反证：删掉对应实现后必须 FAIL。

// newGradeFixture 在临时目录写一个账号并返回它。
//
// 分级必须落盘才有意义（判定读的是磁盘当前内容），
// 所以夹具一律走真实写入而不是纯内存结构体。
// [参数] t: 测试上下文；authID: 账号标识（同时决定文件名）。
// [返回] 已落盘的账号。
// 最近修改时间：2026-10-25；改动原因：新增，分级用例夹具。
func newGradeFixture(t *testing.T, authID string) *StoredAuth {
	t.Helper()
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })

	sa := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       authID,
		CookieHeader: "sessionid=test; sid_tt=test",
		Label:        "夹具账号",
		CreatedAt:    "2026-10-25T00:00:00+08:00",
		UpdatedAt:    "2026-10-25T00:00:00+08:00",
	}
	path := filepath.Join(dir, authID+".json")
	if errWrite := writeAuthFileDirect(path, sa); errWrite != nil {
		t.Fatalf("写入账号夹具失败：%v", errWrite)
	}
	sa.PhysicalPath = path
	return sa
}

// reloadAuth 从磁盘重读账号，用于断言「真的写进去了」。
// [参数] t: 测试上下文；path: 账号文件路径。
// [返回] 重读到的账号。
// 最近修改时间：2026-10-25；改动原因：新增，落盘断言辅助。
func reloadAuth(t *testing.T, path string) *StoredAuth {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("重读账号失败：%v", errRead)
	}
	var sa StoredAuth
	if errUnmarshal := json.Unmarshal(raw, &sa); errUnmarshal != nil {
		t.Fatalf("解析账号失败：%v", errUnmarshal)
	}
	return &sa
}

// TestCreditTierForBoundary 锁定阈值边界：199 禁用、200 正常、201 正常。
//
// 必须严格小于：等于红线仍应承载请求。把 `<` 写成 `<=` 会把
// 恰好 200 分的号误禁，这类越界只能靠边界用例发现。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，阈值边界锚定。
func TestCreditTierForBoundary(t *testing.T) {
	cases := []struct {
		remain  int64
		redline int64
		want    creditTier
	}{
		{199, 200, creditTierDisabled},
		{200, 200, creditTierNormal},
		{201, 200, creditTierNormal},
		{0, 200, creditTierDisabled},
		// 未知积分不降级。
		{creditUnknownRemain, 200, creditTierUnknown},
		// kill switch：红线为 0 时任何余额都不禁用。
		{0, 0, creditTierNormal},
		{10, 0, creditTierNormal},
	}
	for _, c := range cases {
		if got := creditTierFor(c.remain, c.redline); got != c.want {
			t.Fatalf("tierFor(remain=%d, redline=%d) = %v，期望 %v", c.remain, c.redline, got, c.want)
		}
	}
}

// TestGradeDisablesBelowRedline 验证低于红线会写盘禁用并打归属标记。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，自动禁用的主路径。
func TestGradeDisablesBelowRedline(t *testing.T) {
	t.Cleanup(setMinRoutableCredits(defaultMinRoutableCredits))
	sa := newGradeFixture(t, "coze-low")

	tier := gradeCreditsForAccount(sa, &creditSummary{Remain: 199, Total: 1500})
	if tier != creditTierDisabled {
		t.Fatalf("199 分应判 disabled，实际 %v", tier)
	}
	stored := reloadAuth(t, sa.PhysicalPath)
	if !stored.Disabled {
		t.Fatal("低于红线必须写盘禁用")
	}
	if !stored.LowCreditDisable {
		t.Fatal("必须打上 low_credit_disable 归属标记，否则无法自动解禁")
	}
	if stored.LowCreditDisableBelow != loadedMinRoutableCredits() {
		t.Fatalf("禁用阈值应记录为 %d，实际 %d", loadedMinRoutableCredits(), stored.LowCreditDisableBelow)
	}
	if stored.LowCreditDisableAt == "" {
		t.Fatal("禁用时刻必须落盘，供面板展示")
	}
}

// TestGradeKeepsAccountAtRedline 验证恰好等于红线不禁用。
//
// 这是「< 而不是 <=」的落盘级验证：分级判定正确但写盘条件写错，
// 同样会把 200 分的号禁掉。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，边界防越界。
func TestGradeKeepsAccountAtRedline(t *testing.T) {
	sa := newGradeFixture(t, "coze-eq200")

	tier := gradeCreditsForAccount(sa, &creditSummary{Remain: 200, Total: 1500})
	if tier != creditTierNormal {
		t.Fatalf("恰好 200 分应判 normal，实际 %v", tier)
	}
	if stored := reloadAuth(t, sa.PhysicalPath); stored.Disabled {
		t.Fatal("恰好等于红线不得禁用")
	}
}

// TestGradeNeverDisablesUnknownCredits 验证积分未知时绝不写盘禁用。
//
// 把「查不到」当「没钱」会把整池账号写死，且恢复要等下一轮积分到账。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，未知语义保护。
func TestGradeNeverDisablesUnknownCredits(t *testing.T) {
	sa := newGradeFixture(t, "coze-unknown")

	if tier := gradeCreditsForAccount(sa, nil); tier != creditTierUnknown {
		t.Fatalf("nil 积分应判 unknown，实际 %v", tier)
	}
	if tier := gradeCreditsForAccount(sa, &creditSummary{Remain: creditUnknownRemain}); tier != creditTierUnknown {
		t.Fatalf("未知哨兵值应判 unknown，实际 %v", tier)
	}
	if stored := reloadAuth(t, sa.PhysicalPath); stored.Disabled {
		t.Fatal("积分未知时绝不能禁用账号")
	}
}

// TestGradeKillSwitchDisablesNothing 验证红线为 0 时整体不动作。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，kill switch 语义。
func TestGradeKillSwitchDisablesNothing(t *testing.T) {
	t.Cleanup(setMinRoutableCredits(defaultMinRoutableCredits))
	setMinRoutableCredits(0)

	sa := newGradeFixture(t, "coze-kill")
	gradeCreditsForAccount(sa, &creditSummary{Remain: 0, Total: 1500})

	if stored := reloadAuth(t, sa.PhysicalPath); stored.Disabled {
		t.Fatal("红线为 0 时不得禁用任何账号")
	}
}

// TestGradeReenablesWhenCreditsRecover 验证积分回升后自动解禁。
//
// 恢复判据是纯积分（余额查询成功且 >= 红线），不做推理探活：
// coze 查余额本身就要有效 Cookie，探活还会真实消耗积分。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，自动解禁主路径。
func TestGradeReenablesWhenCreditsRecover(t *testing.T) {
	sa := newGradeFixture(t, "coze-recover")
	gradeCreditsForAccount(sa, &creditSummary{Remain: 10, Total: 1500})
	if stored := reloadAuth(t, sa.PhysicalPath); !stored.Disabled {
		t.Fatal("前置条件失败：应先被禁用")
	}

	// 模拟次日签到到账 1500 分。
	reloaded := reloadAuth(t, sa.PhysicalPath)
	reloaded.PhysicalPath = sa.PhysicalPath
	tier := gradeCreditsForAccount(reloaded, &creditSummary{Remain: 1500, Total: 1500})
	if tier != creditTierNormal {
		t.Fatalf("1500 分应判 normal，实际 %v", tier)
	}
	stored := reloadAuth(t, sa.PhysicalPath)
	if stored.Disabled {
		t.Fatal("积分回升后必须自动解禁")
	}
	if stored.LowCreditDisable {
		t.Fatal("解禁后必须清掉归属标记")
	}
}

// TestGradeNeverReenablesManualDisabled 验证用户手动禁用的号永不被自动解禁。
//
// 这是最重要的防串权规则：没有归属标记的禁用号归用户管，
// 积分回升也不该被积分机制放回路由。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，防串权核心用例。
func TestGradeNeverReenablesManualDisabled(t *testing.T) {
	sa := newGradeFixture(t, "coze-manual")

	// 模拟用户手动禁用：直接写 disabled，不带归属标记。
	if errMutate := setAccountDisabled(sa.AuthID, true); errMutate != nil {
		t.Fatalf("手动禁用失败：%v", errMutate)
	}
	reloaded := reloadAuth(t, sa.PhysicalPath)
	reloaded.PhysicalPath = sa.PhysicalPath

	gradeCreditsForAccount(reloaded, &creditSummary{Remain: 99999, Total: 99999})

	stored := reloadAuth(t, sa.PhysicalPath)
	if !stored.Disabled {
		t.Fatal("用户手动禁用的账号不得被积分机制自动解禁")
	}
}

// TestManualDisableClearsLowCreditMarkers 验证人工启停会清掉归属标记。
//
// 不清标记的后果：用户手动禁用一个已被自动禁用的号后，
// 次日签到到账会违背用户意愿自动解禁。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，防串权第二半。
func TestManualDisableClearsLowCreditMarkers(t *testing.T) {
	sa := newGradeFixture(t, "coze-takeover")
	gradeCreditsForAccount(sa, &creditSummary{Remain: 10, Total: 1500})
	if stored := reloadAuth(t, sa.PhysicalPath); !stored.LowCreditDisable {
		t.Fatal("前置条件失败：应先被自动禁用")
	}

	// 用户此时手动点「禁用」——状态本就是 disabled，关键是清标记。
	if errMutate := setAccountDisabled(sa.AuthID, true); errMutate != nil {
		t.Fatalf("手动禁用失败：%v", errMutate)
	}
	stored := reloadAuth(t, sa.PhysicalPath)
	if stored.LowCreditDisable {
		t.Fatal("人工禁用必须清掉低额归属标记")
	}

	// 次日签到大额到账，也不得自动解禁。
	reloaded := reloadAuth(t, sa.PhysicalPath)
	reloaded.PhysicalPath = sa.PhysicalPath
	gradeCreditsForAccount(reloaded, &creditSummary{Remain: 99999, Total: 99999})
	if final := reloadAuth(t, sa.PhysicalPath); !final.Disabled {
		t.Fatal("人工禁用后的账号不得被签到到账自动解禁")
	}
}

// TestManualEnableClearsLowCreditMarkers 验证人工启用也会清标记。
//
// 用户既然要它跑，就不该再被低额机制二次接管。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，人工启用语义。
func TestManualEnableClearsLowCreditMarkers(t *testing.T) {
	sa := newGradeFixture(t, "coze-enable")
	gradeCreditsForAccount(sa, &creditSummary{Remain: 10, Total: 1500})

	if errMutate := setAccountDisabled(sa.AuthID, false); errMutate != nil {
		t.Fatalf("手动启用失败：%v", errMutate)
	}
	stored := reloadAuth(t, sa.PhysicalPath)
	if stored.Disabled || stored.LowCreditDisable {
		t.Fatalf("人工启用后应既未禁用也无标记，实际 disabled=%v marker=%v",
			stored.Disabled, stored.LowCreditDisable)
	}
}

// TestGradeRespectsConcurrentManualDisable 验证锁内重读，不覆盖人工操作。
//
// 场景：巡检读到「未禁用」后，用户在同一时刻点了禁用。若分级用读到的
// 旧快照判定并写盘，就会把用户的手动禁用改写成本机制的自动禁用，
// 次日签到到账后又自动解禁——用户的操作被静默吞掉。
// 这里用「先手动禁用，再用带旧内存状态的账号去分级」模拟该时序。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，并发覆盖防护。
func TestGradeRespectsConcurrentManualDisable(t *testing.T) {
	sa := newGradeFixture(t, "coze-race")

	// 巡检持有的是「尚未禁用」的内存视图。
	stale := *sa
	stale.Disabled = false

	// 同一时刻用户手动禁用。
	if errMutate := setAccountDisabled(sa.AuthID, true); errMutate != nil {
		t.Fatalf("手动禁用失败：%v", errMutate)
	}

	// 巡检用旧视图分级：锁内重读后应看到「已禁用」，直接放弃写入。
	gradeCreditsForAccount(&stale, &creditSummary{Remain: 10, Total: 1500})

	stored := reloadAuth(t, sa.PhysicalPath)
	if !stored.Disabled {
		t.Fatal("账号应保持禁用")
	}
	if stored.LowCreditDisable {
		t.Fatal("锁内重读失效：把用户的手动禁用覆盖成了自动禁用")
	}
}

// TestCreditSnapshotRoundTrip 验证快照能跨「进程重启」读回。
//
// 用 resetCreditSnapshotForTest 模拟重启：清空内存镜像后必须能从磁盘恢复。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，持久化主路径。
func TestCreditSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLI_PROXY_ROOT", dir)
	t.Cleanup(resetCreditSnapshotForTest)
	resetCreditSnapshotForTest()

	// 直接写到一个可控路径：用 sidecar 的真实写盘函数。
	recordCreditSnapshot("coze-persist", &creditSummary{
		Total: 3000, Remain: 1500,
		FetchedAt: "2026-10-25T00:00:00Z",
	}, "持久化账号")
	if errPersist := persistCreditSnapshot(); errPersist != nil {
		t.Fatalf("快照落盘失败：%v", errPersist)
	}

	// 模拟重启：清空内存后重新载入。
	resetCreditSnapshotForTest()
	entry, ok := creditSnapshotGet("coze-persist")
	if !ok {
		t.Fatal("重启后必须能从磁盘读回快照")
	}
	if entry.Remain != 1500 || entry.Nickname != "持久化账号" {
		t.Fatalf("快照内容不一致：remain=%d nickname=%q", entry.Remain, entry.Nickname)
	}
	if entry.Credits == nil || entry.Credits.Total != 3000 {
		t.Fatal("快照必须保留完整积分明细，供面板离线渲染")
	}
}

// TestCreditSnapshotMissingMeansUnknown 验证无快照时是未知而不是 0。
//
// 缺失当 0 会触发禁用，把「没数据」当成「没钱」会把整池写死。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，未知语义。
func TestCreditSnapshotMissingMeansUnknown(t *testing.T) {
	t.Cleanup(resetCreditSnapshotForTest)
	resetCreditSnapshotForTest()

	if _, ok := creditSnapshotGet("coze-never-seen"); ok {
		t.Fatal("无快照时不应返回条目")
	}
}

// TestCreditSnapshotRecordPreservesDailyClaim 验证写快照不会抹掉签到态。
//
// 签到确认发生在 fetch 之后：若 recordCreditSnapshot 整体覆盖条目，
// 同一轮里刚确认的「今日已领」会被抹掉，面板退回「未领」。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，同轮覆盖防护。
func TestCreditSnapshotRecordPreservesDailyClaim(t *testing.T) {
	t.Cleanup(resetCreditSnapshotForTest)
	resetCreditSnapshotForTest()

	recordDailyClaimedSnapshot("coze-keep", 1500)
	recordCreditSnapshot("coze-keep", &creditSummary{Total: 1500, Remain: 1500}, "保留账号")

	entry, ok := creditSnapshotGet("coze-keep")
	if !ok {
		t.Fatal("条目应存在")
	}
	if entry.DailyClaimedAt == "" {
		t.Fatal("写积分快照不得抹掉已确认的签到时刻")
	}
	if entry.DailyClaimedAmount != 1500 {
		t.Fatalf("签到额度应保留为 1500，实际 %d", entry.DailyClaimedAmount)
	}
}

// TestBuildAccountSummaryMissingSnapshotIsUnknown 验证无快照时不渲染成 0 分。
//
// 填一个 0 分摘要会让用户看到「积分耗尽」，而实际只是还没跑过巡检：
// 两者处置完全不同（前者要补积分，后者只需等待或手动巡检）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，快照缺失语义防护。
func TestBuildAccountSummaryMissingSnapshotIsUnknown(t *testing.T) {
	acc := &StoredAuth{AuthID: "coze-nosnap", CookieHeader: "sessionid=x"}

	got := buildAccountSummary(acc, map[string]creditSnapshotEntry{}, 200)
	if got.Credits != nil {
		t.Fatalf("无快照时 Credits 必须为 nil，实际 %+v", got.Credits)
	}
	if got.CreditTier != creditTierUnknown.String() {
		t.Fatalf("无快照时档位应为 unknown，实际 %q", got.CreditTier)
	}
	if got.LowCredits {
		t.Fatal("无快照不得标记低额（未知不等于低额）")
	}
}

// TestBuildAccountSummaryUsesSnapshotAndRedline 验证视图消费快照与红线。
//
// 低额标记必须与闸门同源：都读同一份快照和同一个红线，
// 否则会出现「面板显示够用、实际已被拦截」的矛盾。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，视图与闸门口径一致性。
func TestBuildAccountSummaryUsesSnapshotAndRedline(t *testing.T) {
	acc := &StoredAuth{AuthID: "coze-view", CookieHeader: "sessionid=x"}
	snapshots := map[string]creditSnapshotEntry{
		creditSnapshotKey(acc.AuthID): {
			Remain:    150,
			FetchedAt: "2026-10-25T00:00:00Z",
			Credits:   &creditSummary{Total: 1500, Remain: 150},
		},
	}

	got := buildAccountSummary(acc, snapshots, 200)
	if got.Credits == nil || got.Credits.Remain != 150 {
		t.Fatalf("视图应带上快照积分，实际 %+v", got.Credits)
	}
	if got.CreditTier != creditTierDisabled.String() {
		t.Fatalf("150 分应判 disabled，实际 %q", got.CreditTier)
	}
	if !got.LowCredits {
		t.Fatal("150 分低于红线 200，必须标记低额")
	}
	if got.SnapshotAt == "" {
		t.Fatal("视图必须暴露快照时刻，供面板显示新鲜度")
	}

	// 红线为 0（kill switch）时不应标记低额。
	off := buildAccountSummary(acc, snapshots, 0)
	if off.LowCredits {
		t.Fatal("红线为 0 时不得标记低额")
	}
}

// TestHandleRefreshAllRejectsOverlap 验证巡检不重叠。
//
// 上一轮未结束时应直接返回 queued=false，而不是排队重跑一遍：
// 排队只会让同一批账号被连续扫两次，第二遍拿到的还是刚刚的分数。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，手动触发的不重叠语义。
func TestHandleRefreshAllRejectsOverlap(t *testing.T) {
	// 手动占用扫描锁，模拟「上一轮仍在跑」。
	creditSweepMu.Lock()
	defer creditSweepMu.Unlock()

	raw, errHandle := handleRefreshAll()
	if errHandle != nil {
		t.Fatalf("接口不应报错：%v", errHandle)
	}
	env := decodeManagementBody(t, raw)
	if queued, _ := env["queued"].(bool); queued {
		t.Fatal("上一轮未结束时应拒绝启动新一轮")
	}
}

// decodeManagementBody 解开管理响应信封，取出业务 JSON。
// [参数] t: 测试上下文；raw: 管理响应字节。
// [返回] 业务载荷。
// 最近修改时间：2026-10-25；改动原因：新增，测试断言辅助。
func decodeManagementBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Body []byte `json:"body"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		t.Fatalf("解析管理信封失败：%v", errUnmarshal)
	}
	var out map[string]any
	if errUnmarshal := json.Unmarshal(env.Result.Body, &out); errUnmarshal != nil {
		t.Fatalf("解析业务载荷失败：%v", errUnmarshal)
	}
	return out
}

// TestCreditDepletedTrustWindowCoversSweepInterval 锁定采信窗口必须覆盖巡检周期。
//
// 窗口若小于巡检周期，每轮中后段缓存都会被判成「数据不可信」，
// 「全池耗尽」快速失败静默失效、请求又回去白打上游。
//
// 断言用绝对下限而不是 window±1：相对断言会跟着被改的常量一起变，
// 两种实现都能通过，等于没有护栏。旧值 15 分钟在 1 小时周期下必然失败。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增绝对下限锚定，防止窗口被改回短值。
func TestCreditDepletedTrustWindowCoversSweepInterval(t *testing.T) {
	minWindow := time.Duration(creditRefreshDefaultSeconds) * time.Second
	if creditDepletedTrustWindow < minWindow {
		t.Fatalf("采信窗口 %v 小于巡检周期 %v：每轮中后段快速失败会静默失效",
			creditDepletedTrustWindow, minWindow)
	}
	if creditDepletedTrustWindow < 90*time.Minute {
		t.Fatalf("采信窗口应为 90 分钟（周期 + 50%% 余量），实际 %v", creditDepletedTrustWindow)
	}
	// 巡检间隔默认值本身也要钉住：它决定上面的下限是否有意义。
	if creditRefreshDefaultSeconds != 3600 {
		t.Fatalf("巡检间隔默认值应为 3600 秒，实际 %d", creditRefreshDefaultSeconds)
	}
}

// TestDailyClaimedSurvivesRestart 验证签到态能跨重启读取。
//
// 当日额度被消耗光后会从明细里消失，只靠明细反推会把已领误报成未领。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，签到态持久化。
func TestDailyClaimedSurvivesRestart(t *testing.T) {
	t.Cleanup(resetCreditSnapshotForTest)
	t.Cleanup(resetDailyCreditState)
	resetCreditSnapshotForTest()
	resetDailyCreditState()

	recordDailyClaimedSnapshot("coze-daily", 1500)
	if errPersist := persistCreditSnapshot(); errPersist != nil {
		t.Fatalf("快照落盘失败：%v", errPersist)
	}

	// 模拟重启：内存态全清。
	resetCreditSnapshotForTest()
	resetDailyCreditState()

	if !dailyCreditClaimedToday("coze-daily") {
		t.Fatal("重启后仍应判定今日已领（内存态已清，必须回落快照）")
	}
	view := buildDailyCreditView("coze-daily")
	if view == nil {
		t.Fatal("重启后面板仍应拿到签到视图，不能返回 nil")
	}
	if !view.ClaimedToday {
		t.Fatal("视图应显示今日已领")
	}
}
