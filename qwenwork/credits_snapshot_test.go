// credits_snapshot_test.go 覆盖积分快照的落盘、载入与合并语义。
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withSnapshotPath 把快照路径重定向到临时目录，并在测试结束后恢复。
func withSnapshotPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := creditSnapshotFilePathFn
	creditSnapshotFilePathFn = func() string { return filepath.Join(dir, defaultCreditsSnapshotName) }
	t.Cleanup(func() { creditSnapshotFilePathFn = old })
	resetCreditSnapshotForTest()
	t.Cleanup(resetCreditSnapshotForTest)
	return dir
}

// TestRecordAndGetSnapshot 验证写入内存镜像后可按键读回。
func TestRecordAndGetSnapshot(t *testing.T) {
	withSnapshotPath(t)
	cr := &creditsSummary{TotalRemain: 400, TotalUsed: 0, TotalSize: 400, PackCount: 1}
	recordCreditSnapshot("acct-a", cr, "昵称A", "cn")

	entry, ok := creditSnapshotGet("acct-a")
	if !ok {
		t.Fatal("snapshot must be readable after record")
	}
	if entry.Remain != 400 {
		t.Fatalf("remain = %d, want 400", entry.Remain)
	}
	if entry.Nickname != "昵称A" || entry.Region != "cn" {
		t.Fatalf("metadata = %q/%q, want 昵称A/cn", entry.Nickname, entry.Region)
	}
	if entry.Credits == nil || entry.Credits.TotalRemain != 400 {
		t.Fatal("full credits detail must be preserved for panel rendering")
	}
	if entry.FetchedAt.IsZero() {
		t.Fatal("fetched_at must be stamped")
	}
}

// TestRecordSnapshot_NilIgnored 验证 nil 积分不会被写成「未知」覆盖真实快照。
func TestRecordSnapshot_NilIgnored(t *testing.T) {
	withSnapshotPath(t)
	recordCreditSnapshot("acct-nil", nil, "", "cn")
	if _, ok := creditSnapshotGet("acct-nil"); ok {
		t.Fatal("nil credits must not create a snapshot entry")
	}
}

// TestPersistAndReload 验证落盘后重启（清空内存）仍能读回。
func TestPersistAndReload(t *testing.T) {
	dir := withSnapshotPath(t)
	recordCreditSnapshot("acct-p", &creditsSummary{TotalRemain: 88, TotalSize: 100}, "P", "cn")
	if err := persistCreditSnapshot(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, defaultCreditsSnapshotName))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var file creditSnapshotFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}
	if file.Version != creditsSnapshotVersion {
		t.Fatalf("version = %d, want %d", file.Version, creditsSnapshotVersion)
	}

	resetCreditSnapshotForTest() // 模拟重启
	entry, ok := creditSnapshotGet("acct-p")
	if !ok {
		t.Fatal("snapshot must survive a restart via the sidecar file")
	}
	if entry.Remain != 88 {
		t.Fatalf("reloaded remain = %d, want 88", entry.Remain)
	}
}

// TestPersistSnapshot_MergesDiskKeys 验证落盘是键级合并：磁盘有、内存没有的
// 账号不会被抹掉（多实例并存时的互相覆盖防护）。
//
// 构造要点：必须让内存镜像「先载入后又被清空」，否则 ensureCreditSnapshotLoaded
// 会把磁盘条目载入内存，跳不跳过磁盘合并都能通过，测试等于没测。
func TestPersistSnapshot_MergesDiskKeys(t *testing.T) {
	dir := withSnapshotPath(t)
	path := filepath.Join(dir, defaultCreditsSnapshotName)
	disk := creditSnapshotFile{
		Version:   creditsSnapshotVersion,
		UpdatedAt: time.Now().UTC(),
		Accounts: map[string]creditSnapshotEntry{
			"acct-other": {Remain: 999, FetchedAt: time.Now().UTC()},
		},
	}
	raw, _ := json.Marshal(disk)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("seed disk: %v", err)
	}

	// 模拟「另一个实例写了磁盘，本实例内存里只有自己的账号」：先载入再清空，
	// 使 loaded=true 且内存为空，后续 persist 不会重新从磁盘装入。
	ensureCreditSnapshotLoaded()
	creditSnapshotMu.Lock()
	creditSnapshotData = map[string]creditSnapshotEntry{}
	creditSnapshotMu.Unlock()

	recordCreditSnapshot("acct-mine", &creditsSummary{TotalRemain: 5}, "", "cn")
	if err := persistCreditSnapshot(); err != nil {
		t.Fatalf("persist: %v", err)
	}

	var file creditSnapshotFile
	got, _ := os.ReadFile(path)
	if err := json.Unmarshal(got, &file); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := file.Accounts[normalizeFailoverKey("acct-other")]; !ok {
		t.Fatal("disk-only entries must survive the merge")
	}
	if _, ok := file.Accounts[normalizeFailoverKey("acct-mine")]; !ok {
		t.Fatal("in-memory entries must be written")
	}
}

// TestEnsureSnapshotLoaded_CorruptFileIsEmpty 验证损坏文件按空快照处理且不 panic。
func TestEnsureSnapshotLoaded_CorruptFileIsEmpty(t *testing.T) {
	dir := withSnapshotPath(t)
	if err := os.WriteFile(filepath.Join(dir, defaultCreditsSnapshotName), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	resetCreditSnapshotForTest()
	if _, ok := creditSnapshotGet("anything"); ok {
		t.Fatal("corrupt sidecar must load as an empty snapshot")
	}
}

// TestForgetSnapshot 验证删号清理。
func TestForgetSnapshot(t *testing.T) {
	withSnapshotPath(t)
	recordCreditSnapshot("acct-del", &creditsSummary{TotalRemain: 1}, "", "cn")
	forgetCreditSnapshot("acct-del")
	if _, ok := creditSnapshotGet("acct-del"); ok {
		t.Fatal("snapshot must be removed after forget")
	}
}
