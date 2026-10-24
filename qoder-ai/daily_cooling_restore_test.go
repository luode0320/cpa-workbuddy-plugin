package main

import (
	"encoding/json"
	"testing"
	"time"
)

// TestRestoreDailyCoolingIntoDoc_RefillsAfterHostWipe 覆盖宿主 persist 抹掉
// daily_cooling_until 之后的补齐：内存镜像仍在冷却期内时，落盘必须把字段补回，
// 否则进程重启或 refreshDailyCoolingSetFromDisk 再也恢复不出这个号的冷却，
// 账号会提前回到路由。
func TestRestoreDailyCoolingIntoDoc_RefillsAfterHostWipe(t *testing.T) {
	clearDailyCoolingForTest(t)

	until := time.Now().Add(12 * time.Hour).UTC().Truncate(time.Second)
	dailyCoolingMu.Lock()
	dailyCoolingMap["qai-w"] = until
	dailyCoolingMu.Unlock()

	doc := map[string]any{"type": providerName}
	if !restoreDailyCoolingIntoDoc(doc, "qai-w") {
		t.Fatal("内存仍在冷却期内时应补写 daily_cooling_until")
	}
	if got := doc["daily_cooling_until"]; got != until.Format(time.RFC3339) {
		t.Fatalf("daily_cooling_until = %v, want %v", got, until.Format(time.RFC3339))
	}
}

// TestRestoreDailyCoolingIntoDoc_SkipsWhenNotCooling 覆盖三种不得补写的场景：
// 内存里没有记录（不是本进程写的冷却）、冷却已过期（该放回路由）、字段本来
// 就在（无需重复写）。补写会凭空冻结一个本该可用的账号。
func TestRestoreDailyCoolingIntoDoc_SkipsWhenNotCooling(t *testing.T) {
	cases := []struct {
		name string
		set  func()
		doc  map[string]any
	}{
		{
			name: "无冷却记录",
			set:  func() {},
			doc:  map[string]any{"type": providerName},
		},
		{
			name: "冷却已过期",
			set: func() {
				dailyCoolingMu.Lock()
				dailyCoolingMap["qai-e"] = time.Now().Add(-time.Hour)
				dailyCoolingMu.Unlock()
			},
			doc: map[string]any{"type": providerName},
		},
		{
			name: "字段仍在",
			set: func() {
				dailyCoolingMu.Lock()
				dailyCoolingMap["qai-k"] = time.Now().Add(6 * time.Hour)
				dailyCoolingMu.Unlock()
			},
			doc: map[string]any{"daily_cooling_until": "2026-10-25T00:00:00Z"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearDailyCoolingForTest(t)
			tc.set()
			if restoreDailyCoolingIntoDoc(tc.doc, "qai-e") && tc.name != "字段仍在" {
				t.Fatalf("%s：不应补写", tc.name)
			}
			if restoreDailyCoolingIntoDoc(tc.doc, "qai-k") && tc.name == "字段仍在" {
				t.Fatal("字段仍在时不应重复补写")
			}
		})
	}
}

// TestPersistCounterDelta_RestoresDailyCooling 覆盖落盘整链路：宿主 persist 抹掉
// 冷却字段之后，watchdog 的计数器落盘必须把它补回同一份文档，且计数增量正确。
// 这条用例存在的理由是变异验证：只测纯函数时，把 persistCounterDelta 里的
// restore 调用整行删掉，全部用例仍然通过（调用点无覆盖）。
func TestPersistCounterDelta_RestoresDailyCooling(t *testing.T) {
	clearDailyCoolingForTest(t)
	clearCountersForTest()
	t.Cleanup(func() {
		counterResolveIndexFn = resolveAuthIndexAndID
		counterGetPhysicalFn = hostAuthGetPhysical
		counterPersistFn = persistAuthDirect
	})

	until := time.Now().Add(8 * time.Hour).UTC().Truncate(time.Second)
	dailyCoolingMu.Lock()
	dailyCoolingMap["uid-d"] = until
	dailyCoolingMu.Unlock()

	// 宿主 persist 重建后的真实形态：白名单键 + 旧计数器，无冷却字段。
	disk := map[string]any{
		"type":          providerName,
		"success_count": float64(5),
		"failed_count":  float64(1),
	}
	var written []byte
	counterResolveIndexFn = func(string) (string, string) { return "idx-d", "uid-d" }
	counterGetPhysicalFn = func(string) (*hostAuthPhysical, error) {
		raw, err := json.Marshal(disk)
		if err != nil {
			return nil, err
		}
		return &hostAuthPhysical{Name: "qoder-uid-d.json", Path: "/tmp/host/qoder-uid-d.json", JSON: raw}, nil
	}
	counterPersistFn = func(_, _ string, _ string, raw []byte) error {
		written = raw
		return nil
	}

	if err := persistCounterDelta("uid-d", 1, 0); err != nil {
		t.Fatalf("persistCounterDelta: %v", err)
	}
	if len(written) == 0 {
		t.Fatal("没有触发落盘")
	}
	var doc map[string]any
	if err := json.Unmarshal(written, &doc); err != nil {
		t.Fatalf("落盘内容必须是合法 JSON: %v", err)
	}
	if got := doc["daily_cooling_until"]; got != until.Format(time.RFC3339) {
		t.Fatalf("落盘时必须补回 daily_cooling_until, got %v", got)
	}
	if doc["success_count"] != float64(6) || doc["failed_count"] != float64(1) {
		t.Fatalf("计数器增量错误, got (%v,%v), want (6,1)", doc["success_count"], doc["failed_count"])
	}
}

// clearDailyCoolingForTest 清空冷却内存镜像，避免用例间互相污染。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，供本文件用例隔离。
func clearDailyCoolingForTest(t *testing.T) {
	t.Helper()
	dailyCoolingMu.Lock()
	dailyCoolingMap = make(map[string]time.Time)
	dailyCoolingMu.Unlock()
	t.Cleanup(func() {
		dailyCoolingMu.Lock()
		dailyCoolingMap = make(map[string]time.Time)
		dailyCoolingMu.Unlock()
	})
}
