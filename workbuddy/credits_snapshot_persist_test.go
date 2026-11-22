// credits_snapshot_persist_test.go 锁定快照落盘的并发与容错语义。
//
// 生产背景（2026-11-20 / 2026-11-22）：多代实例并存时，固定临时名
// `path + ".tmp"` 让两个写者互相删除对方的临时文件，落盘持续报
// `rename ...: no such file or directory`；同时整文件覆盖让 A 实例刚写入的
// 账号被 B 实例的旧内存镜像抹掉。本文件锁定三条不变量：临时名唯一、写前
// 键级合并、载入失败可重试。
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPersistCreditSnapshotUniqueTempName 锁定临时名唯一：落盘结束后目录里不应
// 留下任何 `.tmp` 残留，且写入过程中不会与他人共用同一个临时文件名。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 18:00:00；改动原因：新增，锁住 D3 的唯一临时名修复。
func TestPersistCreditSnapshotUniqueTempName(t *testing.T) {
	base := withTempSnapshotDir(t)
	recordCreditSnapshot("wb-uniq", &creditsSummary{TotalRemain: 300}, "", "cn")
	if err := persistCreditSnapshot(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	// 1. 落盘后目录里只应有正式文件，临时文件必须已被重命名消化掉。
	entries, err := os.ReadDir(filepath.Join(base, "data"))
	if err != nil {
		t.Fatalf("read data dir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("落盘后残留临时文件 %s", e.Name())
		}
	}
}

// TestPersistCreditSnapshotMergesDiskEntries 锁定键级合并：磁盘上有 A/B、内存里
// 只有 A 时，落盘结果必须同时保留 B，多实例并存期间不会互相抹号。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 18:00:00；改动原因：新增，锁住 D3 的合并写兜底。
func TestPersistCreditSnapshotMergesDiskEntries(t *testing.T) {
	base := withTempSnapshotDir(t)
	path := filepath.Join(base, "data", defaultCreditsSnapshotName)
	// 1. 造一份磁盘快照：A 已由「另一个实例」写入，B 只有磁盘有。
	disk := creditSnapshotFile{
		Version:   creditsSnapshotVersion,
		UpdatedAt: time.Now().UTC(),
		Accounts: map[string]creditSnapshotEntry{
			"wb-a": {Remain: 100, Tier: creditTierNormal, FetchedAt: time.Now().UTC()},
			"wb-b": {Remain: 200, Tier: creditTierNormal, FetchedAt: time.Now().UTC()},
		},
	}
	raw, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		t.Fatalf("marshal disk snapshot: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write disk snapshot: %v", err)
	}
	// 2. 内存镜像里只有 A，且 A 的余量已更新。
	resetCreditSnapshotForTest()
	recordCreditSnapshot("wb-a", &creditsSummary{TotalRemain: 111}, "", "cn")
	if err := persistCreditSnapshot(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	// 3. 重新载入后 A 取内存新值，B 必须仍在。
	resetCreditSnapshotForTest()
	if entry, ok := creditSnapshotGet("wb-a"); !ok || entry.Remain != 111 {
		t.Fatalf("A 应取内存新值 111，got %+v ok=%v", entry, ok)
	}
	if entry, ok := creditSnapshotGet("wb-b"); !ok || entry.Remain != 200 {
		t.Fatalf("磁盘独有的 B 被抹掉了，got %+v ok=%v", entry, ok)
	}
}

// TestEnsureCreditSnapshotLoadedRetriesAfterFailure 锁定载入可重试：首次读盘遇到
// 真实 IO 错误时不得置位「已载入」，修好文件后下一次调用必须能读到数据。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 18:00:00；改动原因：新增，锁住 D4 的失败可重试语义。
func TestEnsureCreditSnapshotLoadedRetriesAfterFailure(t *testing.T) {
	base := withTempSnapshotDir(t)
	path := filepath.Join(base, "data", defaultCreditsSnapshotName)
	// 1. 把 sidecar 造成目录，让 ReadFile 返回真实 IO 错误（EISDIR，不是 ErrNotExist）。
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("造目录失败: %v", err)
	}
	resetCreditSnapshotForTest()
	if _, ok := creditSnapshotGet("wb-retry"); ok {
		t.Fatal("读盘失败时不应返回任何快照")
	}
	// 2. 失败态必须可观测，且落盘前会被拦下。
	if err := currentCreditSnapshotLoadErr(); err == nil {
		t.Fatal("真实 IO 错误必须被记录，否则落盘会拿空镜像覆盖磁盘")
	}
	if err := persistCreditSnapshot(); err == nil {
		t.Fatal("载入失败时落盘必须放弃，不能覆盖磁盘")
	}
	// 3. 修好文件后重新调用必须能读到：证明 loaded 没有被提前置位。
	if err := os.RemoveAll(path); err != nil {
		t.Fatalf("清理目录失败: %v", err)
	}
	good := creditSnapshotFile{
		Version:   creditsSnapshotVersion,
		UpdatedAt: time.Now().UTC(),
		Accounts: map[string]creditSnapshotEntry{
			"wb-retry": {Remain: 777, Tier: creditTierNormal, FetchedAt: time.Now().UTC()},
		},
	}
	raw, err := json.MarshalIndent(good, "", "  ")
	if err != nil {
		t.Fatalf("marshal good snapshot: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write good snapshot: %v", err)
	}
	entry, ok := creditSnapshotGet("wb-retry")
	if !ok || entry.Remain != 777 {
		t.Fatalf("修复文件后必须能重试读回，got %+v ok=%v", entry, ok)
	}
	if err := currentCreditSnapshotLoadErr(); err != nil {
		t.Fatalf("载入成功后错误应被清空，got %v", err)
	}
}
