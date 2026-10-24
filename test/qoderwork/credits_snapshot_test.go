// credits_snapshot_test.go 锁死积分快照的三条契约：
//
//  1. 跨重启存活：写内存 + 落盘后重建，快照必须能读回；
//  2. 未知不等于 0：nil 积分不得写成 0 分，否则面板会把整池显示成「没额度」；
//  3. 脏数据容错：文件损坏、版本不认识一律按空快照处理。
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// setupCreditsSnapshotDir 把快照指向临时目录，返回文件路径。
func setupCreditsSnapshotDir(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), defaultCreditsSnapshotName)
	restore := isolateCreditSnapshotPathForTest(path)
	t.Cleanup(restore)
	return path
}

// TestCreditsSnapshot_SurvivesRestart 覆盖契约 1。
func TestCreditsSnapshot_SurvivesRestart(t *testing.T) {
	path := setupCreditsSnapshotDir(t)

	cr := &creditsSummary{TotalRemain: 812, TotalUsed: 188, TotalSize: 1000, PackCount: 2}
	recordCreditSnapshot("qoderwork-abc", cr, "pro")
	if err := persistCreditSnapshot(); err != nil {
		t.Fatalf("落盘: %v", err)
	}

	// 模拟插件热重载：清内存，从磁盘重载。
	resetCreditSnapshotForTest()

	entry, ok := creditSnapshotGet("qoderwork-abc")
	if !ok {
		t.Fatal("重启后积分快照丢失")
	}
	if entry.Remain != 812 {
		t.Fatalf("remain = %d, want 812", entry.Remain)
	}
	if entry.Plan != "pro" {
		t.Fatalf("plan = %q, want pro", entry.Plan)
	}
	if entry.Credits == nil || entry.Credits.TotalUsed != 188 {
		t.Fatalf("完整积分明细未随快照存活: %+v", entry.Credits)
	}

	// 落盘文件必须是合法 JSON 且版本正确。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取快照: %v", err)
	}
	var doc creditSnapshotFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("快照不是合法 JSON: %v", err)
	}
	if doc.Version != creditsSnapshotVersion {
		t.Fatalf("版本 = %d, want %d", doc.Version, creditsSnapshotVersion)
	}
}

// TestCreditsSnapshot_IgnoresNilCredits 覆盖契约 2：
// nil 积分代表「未知」，绝不能写成 0 分——0 分会触发禁用。
func TestCreditsSnapshot_IgnoresNilCredits(t *testing.T) {
	setupCreditsSnapshotDir(t)

	recordCreditSnapshot("acc-nil", nil, "pro")
	if _, ok := creditSnapshotGet("acc-nil"); ok {
		t.Fatal("nil 积分被写成了快照，未知会被误读为 0 分")
	}

	// 已有真实快照时，nil 不得覆盖它。
	recordCreditSnapshot("acc-has", &creditsSummary{TotalRemain: 500}, "")
	recordCreditSnapshot("acc-has", nil, "")
	entry, ok := creditSnapshotGet("acc-has")
	if !ok || entry.Remain != 500 {
		t.Fatalf("nil 覆盖了真实快照: ok=%v entry=%+v", ok, entry)
	}
}

// TestCreditsSnapshot_ToleratesCorruptFile 覆盖契约 3。
func TestCreditsSnapshot_ToleratesCorruptFile(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{name: "非法 JSON", content: "{{{"},
		{name: "版本不认识", content: `{"version":999,"accounts":{"x":{"remain":1}}}`},
		{name: "空文件", content: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := setupCreditsSnapshotDir(t)
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("准备脏文件: %v", err)
			}
			// 不得 panic，且按空快照处理。
			if _, ok := creditSnapshotGet("x"); ok {
				t.Fatal("脏文件应被忽略")
			}
		})
	}
}

// TestCreditsSnapshot_Forget 确认删除账号时快照一并清理。
func TestCreditsSnapshot_Forget(t *testing.T) {
	setupCreditsSnapshotDir(t)
	recordCreditSnapshot("acc-del", &creditsSummary{TotalRemain: 100}, "")
	forgetCreditSnapshot("acc-del")
	if _, ok := creditSnapshotGet("acc-del"); ok {
		t.Fatal("forget 未清掉快照")
	}
}

// TestCreditsSnapshot_SeparateFileFromFailoverState 确认两种 sidecar 互不覆盖。
//
// 这是移植时的关键设计：积分是高频刷新的业务数据，禁用是低频故障状态。
// 若共用一个文件，高频写入会通过原子重命名把低频状态整份覆盖掉。
func TestCreditsSnapshot_SeparateFileFromFailoverState(t *testing.T) {
	if defaultCreditsSnapshotName == failoverStateFileName {
		t.Fatalf("积分快照与禁用状态共用文件名 %q，高频写会覆盖低频状态", defaultCreditsSnapshotName)
	}
}
