package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resetCreditsPersist 清空积分持久化的两张内存表，保证用例互不污染。
func resetCreditsPersist(t *testing.T) {
	t.Helper()
	creditsPersistMu.Lock()
	creditsPersistEntries = make(map[string]creditsPersistEntry)
	creditsPersistLoaded = make(map[string]bool)
	creditsPersistMu.Unlock()
	t.Cleanup(func() {
		creditsPersistMu.Lock()
		creditsPersistEntries = make(map[string]creditsPersistEntry)
		creditsPersistLoaded = make(map[string]bool)
		creditsPersistMu.Unlock()
	})
}

// TestParseCreditsSnapshotFromAuthJSON 覆盖快照解析的四种形态。
func TestParseCreditsSnapshotFromAuthJSON(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantNil bool
		remain  int64
	}{
		{"missing", `{"disabled":false}`, true, 0},
		{"malformed", `{`, true, 0},
		{"empty-ledger", `{"credits_snapshot":{"total_remain":0,"total_used":0,"total_size":0}}`, true, 0},
		{"zero-remain-with-usage", `{"credits_snapshot":{"total_remain":0,"total_used":100,"total_size":100}}`, false, 0},
		{"healthy", `{"credits_snapshot":{"total_remain":350,"total_used":10,"total_size":360}}`, false, 350},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCreditsSnapshotFromAuthJSON([]byte(tc.raw))
			if tc.wantNil {
				if got != nil {
					t.Fatalf("want nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("want non-nil snapshot")
			}
			if got.TotalRemain != tc.remain {
				t.Fatalf("TotalRemain = %d, want %d", got.TotalRemain, tc.remain)
			}
		})
	}
}

// TestFoldCreditsSnapshotIntoDocPreservesOtherKeys 断言落盘只增不改：
// 盘上既有顶层键（禁用标记、计数器、备注等）都必须原样保留。
func TestFoldCreditsSnapshotIntoDocPreservesOtherKeys(t *testing.T) {
	base := []byte(`{"disabled":true,"manual_disable":true,"success_count":9,` +
		`"failover_disable":true,"note":"ok","auth":{"accessToken":"t"}}`)
	cr := &creditsSummary{TotalRemain: 100, TotalUsed: 5, TotalSize: 105}
	out := foldCreditsSnapshotIntoDoc(base, cr)

	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"disabled", "manual_disable", "failover_disable", "note", "auth"} {
		if _, ok := doc[k]; !ok {
			t.Fatalf("既有顶层键 %q 被丢弃: %s", k, out)
		}
	}
	if doc["success_count"] != float64(9) {
		t.Fatalf("success_count = %v, want 9", doc["success_count"])
	}
	got, ok := doc[creditsSnapshotKey].(map[string]any)
	if !ok {
		t.Fatalf("credits_snapshot 缺失或类型错误: %s", out)
	}
	if got["total_remain"] != float64(100) {
		t.Fatalf("total_remain = %v, want 100", got["total_remain"])
	}
}

// TestFoldCreditsSnapshotIntoDocTolerantOfBadJSON 断言坏 JSON 不会让落盘失败，
// 与计数器的 foldCounterIntoDoc 保持一致。
func TestFoldCreditsSnapshotIntoDocTolerantOfBadJSON(t *testing.T) {
	out := foldCreditsSnapshotIntoDoc([]byte(`not json`), &creditsSummary{TotalRemain: 7})
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output must be valid JSON: %v (%s)", err, out)
	}
	if _, ok := doc[creditsSnapshotKey]; !ok {
		t.Fatalf("坏 JSON 时仍应折出新快照: %s", out)
	}
}

// TestFoldCreditsSnapshotIntoDocNilSnapshotIsNoop 断言 nil 快照不写盘，
// 避免把「本次没取到数据」误写成空账本。
func TestFoldCreditsSnapshotIntoDocNilSnapshotIsNoop(t *testing.T) {
	base := []byte(`{"disabled":false}`)
	out := foldCreditsSnapshotIntoDoc(base, nil)
	if string(out) != string(base) {
		t.Fatalf("nil 快照不得改动文档, got %s", out)
	}
}

// TestTagCreditsPersistSerialAndIdempotency 断言打标递增 serial，
// 且 uid / cr 为空时不产生待落盘项。
func TestTagCreditsPersistSerialAndIdempotency(t *testing.T) {
	resetCreditsPersist(t)

	tagCreditsPersist("", &creditsSummary{TotalRemain: 1})
	tagCreditsPersist("uid-a", nil)
	if n := countCreditsPersistPending(); n != 0 {
		t.Fatalf("空 uid / nil 快照不得入表, pending = %d", n)
	}

	tagCreditsPersist("uid-a", &creditsSummary{TotalRemain: 10})
	if n := countCreditsPersistPending(); n != 1 {
		t.Fatalf("首次打标后 pending = %d, want 1", n)
	}

	// 第二次打标（快照变化）仍只有一条待落盘，但 serial 必须前进。
	creditsPersistMu.Lock()
	before := creditsPersistEntries["uid-a"].serial
	creditsPersistMu.Unlock()
	tagCreditsPersist("uid-a", &creditsSummary{TotalRemain: 20})
	creditsPersistMu.Lock()
	after := creditsPersistEntries["uid-a"].serial
	creditsPersistMu.Unlock()
	if after <= before {
		t.Fatalf("serial 未前进: %d -> %d", before, after)
	}
	if n := countCreditsPersistPending(); n != 1 {
		t.Fatalf("重复打标后 pending = %d, want 1", n)
	}
}

// TestEnsureCreditsPersistLoadedKeepsFresherMemoryValue 是本机制最关键的幂等契约：
// 若本进程已经取到更新的积分，用盘上旧快照回灌绝不能把它覆盖掉。
func TestEnsureCreditsPersistLoadedKeepsFresherMemoryValue(t *testing.T) {
	resetCreditsPersist(t)

	// 场景 1：本进程没有数据 → 用盘上快照播种，并视为已落盘。
	disk := []byte(`{"credits_snapshot":{"total_remain":50,"total_used":5,"total_size":55}}`)
	ensureCreditsPersistLoaded("uid-fresh", disk)
	creditsPersistMu.Lock()
	e := creditsPersistEntries["uid-fresh"]
	creditsPersistMu.Unlock()
	if e.cr == nil || e.cr.TotalRemain != 50 {
		t.Fatalf("空内存时应用盘上快照播种, got %+v", e.cr)
	}
	if e.serial != e.persistedSerial {
		t.Fatalf("播种后应视为已落盘: serial=%d persisted=%d", e.serial, e.persistedSerial)
	}
	if n := countCreditsPersistPending(); n != 0 {
		t.Fatalf("刚播种不应有待落盘项, got %d", n)
	}

	// 场景 2：内存已有更新的快照 → 盘上旧值不得覆盖，且标记为待落盘。
	resetCreditsPersist(t)
	tagCreditsPersist("uid-live", &creditsSummary{TotalRemain: 999})
	ensureCreditsPersistLoaded("uid-live", disk)
	creditsPersistMu.Lock()
	e = creditsPersistEntries["uid-live"]
	creditsPersistMu.Unlock()
	if e.cr == nil || e.cr.TotalRemain != 999 {
		t.Fatalf("内存新快照被盘上旧值覆盖, got %+v", e.cr)
	}
	if n := countCreditsPersistPending(); n != 1 {
		t.Fatalf("新快照应待落盘, got %d", n)
	}
}

// TestEnsureCreditsPersistLoadedIsIdempotent 断言重复回灌只生效一次。
func TestEnsureCreditsPersistLoadedIsIdempotent(t *testing.T) {
	resetCreditsPersist(t)
	disk := []byte(`{"credits_snapshot":{"total_remain":80}}`)
	ensureCreditsPersistLoaded("uid-x", disk)
	// 第二次传入另一份不同数据：已被 loaded 标记挡住，不得覆盖。
	ensureCreditsPersistLoaded("uid-x", []byte(`{"credits_snapshot":{"total_remain":1}}`))
	creditsPersistMu.Lock()
	got := creditsPersistEntries["uid-x"].cr
	creditsPersistMu.Unlock()
	if got == nil || got.TotalRemain != 80 {
		t.Fatalf("重复回灌应被忽略, got %+v", got)
	}
}

// TestSeedCreditsCacheFromDisk 断言盘上快照能回灌进 accountCache，
// 且绝不覆盖已有缓存（必须是更旧的数据不能顶掉更新的数据）。
func TestSeedCreditsCacheFromDisk(t *testing.T) {
	const id = "wbai-seed"
	accountCache.Delete(id)
	t.Cleanup(func() { accountCache.Delete(id) })

	disk := []byte(`{"credits_snapshot":{"total_remain":120,"total_used":10,"total_size":130}}`)
	if !seedCreditsCacheFromDisk(id, disk) {
		t.Fatal("空缓存时应回灌成功")
	}
	if cr := cachedCredits(id); cr == nil || cr.TotalRemain != 120 {
		t.Fatalf("回灌后 accountCache 积分 = %+v, want remain=120", cr)
	}
	// 回灌的 fetched 必须是当前时间，否则会被 45s TTL 立刻判为过期。
	if v, ok := accountCache.Load(id); ok {
		if e, ok2 := v.(*accountCacheEntry); ok2 && time.Since(e.fetched) > time.Second {
			t.Fatalf("回灌的 fetched 过旧: %v", e.fetched)
		}
	}

	// 已有缓存时不得覆盖。
	accountCache.Store(id, &accountCacheEntry{credits: &creditsSummary{TotalRemain: 777}})
	if seedCreditsCacheFromDisk(id, disk) {
		t.Fatal("已有缓存时不应回灌")
	}
	if cr := cachedCredits(id); cr == nil || cr.TotalRemain != 777 {
		t.Fatalf("已有缓存被覆盖, got %+v", cr)
	}
}

// TestCreditsSnapshotForAuthIDPrecedence 断言读取优先级：内存 > 盘上 > nil。
func TestCreditsSnapshotForAuthIDPrecedence(t *testing.T) {
	resetCreditsPersist(t)
	const id = "wbai-prec"
	accountCache.Delete(id)
	t.Cleanup(func() { accountCache.Delete(id) })

	disk := []byte(`{"credits_snapshot":{"total_remain":60}}`)
	if cr := creditsSnapshotForAuthID(id, "", disk); cr == nil || cr.TotalRemain != 60 {
		t.Fatalf("无内存时应用盘上快照, got %+v", cr)
	}
	accountCache.Store(id, &accountCacheEntry{credits: &creditsSummary{TotalRemain: 500}})
	if cr := creditsSnapshotForAuthID(id, "", disk); cr == nil || cr.TotalRemain != 500 {
		t.Fatalf("内存应优先于盘上, got %+v", cr)
	}
	if cr := creditsSnapshotForAuthID("wbai-none", "", nil); cr != nil {
		t.Fatalf("都没有时应返回 nil, got %+v", cr)
	}
}

// TestFlushCreditsSnapshotsClearsPendingOnSuccess 用一个可替换的写盘接缝验证：
// 写盘成功后待落盘项归零，失败则保留（下次 tick 重试）。
func TestFlushCreditsSnapshotsClearsPendingOnSuccess(t *testing.T) {
	resetCreditsPersist(t)
	// 没有 auth_index 时 persistCreditsSnapshot 必然失败 → 待落盘项应保留。
	tagCreditsPersist("uid-nofile", &creditsSummary{TotalRemain: 3})
	flushCreditsSnapshots()
	if n := countCreditsPersistPending(); n != 1 {
		t.Fatalf("写盘失败后应保留待落盘项, got %d", n)
	}
}

// TestCreditsSnapshotRoundTrip 端到端验证「折进文档 → 再解析」不丢字段。
func TestCreditsSnapshotRoundTrip(t *testing.T) {
	orig := &creditsSummary{
		TotalRemain: 250,
		TotalUsed:   50,
		TotalSize:   300,
		PackCount:   2,
		FetchedAt:   "2026-10-24T12:00:00Z",
		Packages: []packageSummary{
			{Name: "Free Plan Subscription", Remain: 0, Used: 100, Size: 100,
				CycleStart: "2026-10-01 00:00:00", CycleEnd: "2026-10-31 23:59:59"},
		},
	}
	raw := foldCreditsSnapshotIntoDoc([]byte(`{"disabled":false}`), orig)
	got := parseCreditsSnapshotFromAuthJSON(raw)
	if got == nil {
		t.Fatal("round-trip 解析失败")
	}
	if got.TotalRemain != orig.TotalRemain || got.TotalUsed != orig.TotalUsed ||
		got.TotalSize != orig.TotalSize || got.PackCount != orig.PackCount {
		t.Fatalf("数值不一致: got %+v, want %+v", got, orig)
	}
	if len(got.Packages) != 1 || got.Packages[0].Name != "Free Plan Subscription" {
		t.Fatalf("积分包丢失: %+v", got.Packages)
	}
	if !strings.Contains(raw2String(raw), creditsSnapshotKey) {
		t.Fatalf("文档里缺少 %s", creditsSnapshotKey)
	}
}

// raw2String 是测试内的小工具，避免为断言引入新的生产函数。
func raw2String(b []byte) string { return string(b) }

// TestCreditsPersistWiringIsPresent 是接线审计闸门。
//
// 为什么要有这条：积分持久化的每个接缝都是「删掉也不报错、只是悄悄失效」——
// 不打标就不落盘、不回灌就永远显示加载中、不 flush 就永远攒在内存里。这类
// 静默失效靠人工 review 记不住，因此在源码层面机械断言四个接缝都还在。
// 注意：只扫生产文件（*_test.go 排除），避免测试里的同名调用把断言喂饱。
func TestCreditsPersistWiringIsPresent(t *testing.T) {
	// 接缝 → 期望出现的最小次数。tagCreditsPersist 必须恰好出现在缓存写入点，
	// 少一处就意味着那条刷新路径的积分不会落盘。
	required := map[string]int{
		"tagCreditsPersist(":               1,
		"loadCreditsFromDisk(":             1,
		"flushCreditsSnapshots()":          2, // watchdog tick + refresh runner 收尾
		"seedCreditsCacheFromDisk(":        1, // loadCreditsFromDisk 内部（定义行已排除）
		"parseCreditsSnapshotFromAuthJSON": 4, // 播种 / 回灌 / creditsSnapshotForAuthID / 面板兜底
	}
	counts := make(map[string]int, len(required))
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for _, ln := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(ln)
			// 跳过注释行：注释里提到函数名不算被调用。
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for name := range required {
				// 跳过该接缝自己的定义行：定义本身不是调用点，
				// 否则删掉生产调用后断言仍会被定义喂饱（实测踩到）。
				if strings.HasPrefix(trimmed, "func "+name) {
					continue
				}
				counts[name] += strings.Count(ln, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk package: %v", err)
	}
	for name, want := range required {
		if got := counts[name]; got < want {
			t.Errorf("接缝 %s 只出现 %d 次（期望 ≥%d）：积分持久化链路被断开", name, got, want)
		}
	}
}
