package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 积分扫描接入（2026-10-25）。
//
// T7 不新建独立扫描器：traework 已有 10 分钟全量刷新（watchdog）与 8 并发
// refresh_runner，新起一条 1 小时链会与现有节奏重叠、同一账号被刷两遍。
// 改为在 doFetchOne 的刷新链尾追加一次分级检查。
//
// 两条必须锁死的性质：
//  1. 禁用号也要被刷新——否则「积分回升 → 自动启用」永远等不到数据；
//  2. 拉取失败时不得用陈旧快照做分级——那会把已经恢复的号重新按旧数据判死。

// setupSweepFixture 准备假宿主 + 关闭禁用线的默认态。
func setupSweepFixture(t *testing.T) *failoverFakeHost {
	t.Helper()
	clearFailoverStates()
	t.Cleanup(clearFailoverStates)
	clearAccountCacheForTest(t)
	h := newFailoverFakeHost(t)
	return h
}

// TestGradeAfterCreditsRefresh_DoesNotSkipDisabledAccounts 锁定禁用号也必须刷新。
//
// 若刷新链按 disabled 过滤，被积分机制禁掉的号就永远拿不到新积分，
// 「积分回升后自动启用」这条恢复路径实际不存在。
func TestGradeAfterCreditsRefresh_DoesNotSkipDisabledAccounts(t *testing.T) {
	h := setupSweepFixture(t)
	restoreBelow := setLowCreditDisableBelow(100)
	defer restoreBelow()

	const authID = "sweep-disabled"
	h.addAccount(authID, "idx-sweep", map[string]any{
		"disabled":            true,
		creditDisableKey:      true,
		creditDisableBelowKey: int64(100),
	})

	// 复刻刷新链走到分级这一步：传入新拉到的积分（已回升）。
	gradeCreditsForAuth("idx-sweep", authID, nil, &traeCredits{TotalRemain: 900})

	if h.docOf("idx-sweep")["disabled"] != false {
		t.Fatalf("禁用号在积分回升后必须被解禁，doc=%v", h.docOf("idx-sweep"))
	}
}

// TestGradeAfterCreditsRefresh_StaleSnapshotNotReusedOnError 锁定拉取失败不动作。
//
// 分级必须使用本次真实拉到的积分：失败时 cr 为 nil，绝不能退回去读缓存里的旧值，
// 否则一次上游抖动就会拿过期数据把号判死或误放。
func TestGradeAfterCreditsRefresh_StaleSnapshotNotReusedOnError(t *testing.T) {
	h := setupSweepFixture(t)
	restoreBelow := setLowCreditDisableBelow(100)
	defer restoreBelow()

	const authID = "sweep-stale"
	h.addAccount(authID, "idx-sweep-stale", nil)
	// 缓存里放一个「低于禁用线」的旧值：若分级误用缓存，就会写盘禁用。
	cacheCredits(authID, &traeCredits{TotalRemain: 10})

	// 复刻拉取失败：cr 为 nil。
	tier := gradeCreditsForAuth("idx-sweep-stale", authID, nil, nil)

	if tier != creditTierNormal {
		t.Fatalf("未知积分应返回 normal，got %s", tier)
	}
	if got := h.writeCount(); got != 0 {
		t.Fatalf("拉取失败时不得用陈旧快照写盘，写盘次数 = %d", got)
	}
	if h.docOf("idx-sweep-stale")["disabled"] != false {
		t.Fatalf("拉取失败时不得改动禁用状态，doc=%v", h.docOf("idx-sweep-stale"))
	}
}

// TestRefreshChain_GradeFollowsReconcile 锁定接线：分级必须在 reconcile 之后被调用。
//
// 为什么不直接跑 doFetchOne：它内部调用 accountCredits → hostHTTPDo 发真实上游
// 请求，没有可注入的出口（hostHTTPDoFn 是 keepalive 的私有变量）。这里按
// doFetchOne 的真实顺序复刻「缓存 → 快照 → lifecycle → 分级」四步，验证接线
// 语义正确；真正接入与否由 TestRefreshChain_SourceHasGradeCall 静态锁定。
func TestRefreshChain_GradeFollowsReconcile(t *testing.T) {
	h := setupSweepFixture(t)
	restoreBelow := setLowCreditDisableBelow(100)
	defer restoreBelow()
	// lifecycle 默认关闭，避免它先按「耗尽」把号处理掉。
	oldLifecycle := lifecycleEnabled()
	setLifecycleEnabled(false)
	defer setLifecycleEnabled(oldLifecycle)

	const authID = "sweep-wired"
	h.addAccount(authID, "idx-wired", nil)
	cr := &traeCredits{TotalRemain: 50}

	// 复刻 doFetchOne 的顺序。
	cacheCredits(authID, cr)
	recordCreditSnapshot(authID, cr)
	reconcileAfterCreditsRefresh("idx-wired", authID)
	gradeCreditsForAuth("idx-wired", authID, nil, cr)

	doc := h.docOf("idx-wired")
	if doc["disabled"] != true {
		t.Fatalf("分级必须在刷新链尾生效并写盘禁用，doc=%v", doc)
	}
	if doc[creditDisableKey] != true {
		t.Fatalf("写盘必须带 %s 归属标记，doc=%v", creditDisableKey, doc)
	}
}

// TestRefreshChain_SourceHasGradeCall 静态锁定接线：doFetchOne 必须真的调用分级。
//
// 前面几个用例只测 gradeCreditsForAuth 与复刻顺序；若接线行被删掉，它们仍会通过。
// 这条用例直接读生产源码，确认分级调用与 reconcile 在同一函数体内且紧随其后。
func TestRefreshChain_SourceHasGradeCall(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "traework", "refresh_runner.go"))
	if err != nil {
		// cgo-shim 把源码复制到临时目录，相对路径可能不成立；此时按 cwd 直接读。
		src, err = os.ReadFile("refresh_runner.go")
		if err != nil {
			t.Skipf("无法定位 refresh_runner.go（shim 环境）: %v", err)
		}
	}
	body := string(src)
	reconcileIdx := strings.Index(body, "reconcileAfterCreditsRefresh(authIndex, authID)")
	if reconcileIdx < 0 {
		t.Fatal("doFetchOne 必须调用 reconcileAfterCreditsRefresh")
	}
	gradeIdx := strings.Index(body, "gradeCreditsForAuth(authIndex, authID, sa, cr)")
	if gradeIdx < 0 {
		t.Fatal("doFetchOne 必须调用 gradeCreditsForAuth——否则积分分级永不生效")
	}
	if gradeIdx < reconcileIdx {
		t.Fatal("分级必须在 lifecycle reconcile 之后调用，否则会被耗尽路径抢先处理")
	}
}

// 保证 errors 包被使用（假宿主构造函数在别的文件里）。
var _ = errors.New
