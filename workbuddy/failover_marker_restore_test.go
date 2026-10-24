package main

import (
	"encoding/json"
	"testing"
)

// TestRestoreFailoverMarkersIntoDoc_RefillsAfterHostWipe 覆盖宿主 persist 抹掉
// 标记后的补齐：本进程写过禁用、盘上仍 disabled、但三个自有键已被宿主用
// Metadata 白名单重建时抹掉——此时必须把标记补回去，否则探活循环再也找不到
// 这个号（它按标记挑候选），账号会停在「宿主禁用、插件不认领」的死角。
func TestRestoreFailoverMarkersIntoDoc_RefillsAfterHostWipe(t *testing.T) {
	clearFailoverDisabledLocalAll()
	t.Cleanup(clearFailoverDisabledLocalAll)

	markFailoverDisabledLocalAt("wb-w", "idx-w", "2026-10-24T15:06:59Z")
	doc := map[string]any{"disabled": true, "type": providerName}

	if !restoreFailoverMarkersIntoDoc(doc, "wb-w", "deepseek-v4.1-flash") {
		t.Fatal("盘上 disabled 且本进程写过禁用时应补写标记")
	}
	if doc[failoverDisableKey] != true {
		t.Fatalf("%s = %v, want true", failoverDisableKey, doc[failoverDisableKey])
	}
	if doc[failoverDisableModelKey] != "deepseek-v4.1-flash" {
		t.Fatalf("%s = %v, want deepseek-v4.1-flash", failoverDisableModelKey, doc[failoverDisableModelKey])
	}
	if doc[failoverDisableAtKey] != "2026-10-24T15:06:59Z" {
		t.Fatalf("%s = %v, want 原始禁用时刻", failoverDisableAtKey, doc[failoverDisableAtKey])
	}
	if doc["type"] != providerName {
		t.Fatalf("补标记不得改动其它顶层键, type = %v", doc["type"])
	}
}

// TestRestoreFailoverMarkersIntoDoc_SkipsWhenNotOurs 覆盖三种不得补写的场景：
// 本进程没写过禁用（用户手动 / 额度耗尽禁用）、盘上已恢复 enabled、标记本来
// 就在。补写会替别人的禁用伪造归属，或把已恢复的账号重新标成禁用。
func TestRestoreFailoverMarkersIntoDoc_SkipsWhenNotOurs(t *testing.T) {
	clearFailoverDisabledLocalAll()
	t.Cleanup(clearFailoverDisabledLocalAll)

	cases := []struct {
		name   string
		mark   bool
		doc    map[string]any
		reason string
	}{
		{"非本进程禁用", false, map[string]any{"disabled": true}, "没有本地记录就不该认领"},
		{"盘上已恢复", true, map[string]any{"disabled": false}, "已恢复的账号不得重新标成禁用"},
		{"标记仍在", true, map[string]any{"disabled": true, failoverDisableKey: true}, "已有标记无需重复写"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearFailoverDisabledLocalAll()
			if tc.mark {
				markFailoverDisabledLocalAt("wb-s", "idx-s", "2026-10-24T15:06:59Z")
			}
			if restoreFailoverMarkersIntoDoc(tc.doc, "wb-s", "glm-4.6") {
				t.Fatalf("%s：不应补写（%s）", tc.name, tc.reason)
			}
			if _, ok := tc.doc[failoverDisableModelKey]; ok {
				t.Fatalf("%s：不应写入 %s", tc.name, failoverDisableModelKey)
			}
		})
	}
}

// TestRestoreFailoverMarkersIntoJSON_KeepsCounters 覆盖落盘整链路：计数器折入
// 之后，标记必须与 success_count / failed_count 同时存在于同一份文档里——
// 这正是生产上被回退的三个账号缺失的那一半。
func TestRestoreFailoverMarkersIntoJSON_KeepsCounters(t *testing.T) {
	clearFailoverDisabledLocalAll()
	t.Cleanup(clearFailoverDisabledLocalAll)

	markFailoverDisabledLocalAt("wb-c", "idx-c", "2026-10-24T15:52:37Z")
	// 宿主 persist 重建后的真实形态：白名单键 + 已折入的计数器，无本机制标记。
	base, err := json.Marshal(map[string]any{
		"type":          providerName,
		"disabled":      true,
		"success_count": float64(12),
		"failed_count":  float64(3),
	})
	if err != nil {
		t.Fatal(err)
	}

	raw := restoreFailoverMarkersIntoJSON(base, "wb-c")
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("补写后必须是合法 JSON: %v", err)
	}
	if doc[failoverDisableKey] != true {
		t.Fatalf("%s = %v, want true", failoverDisableKey, doc[failoverDisableKey])
	}
	if doc["success_count"] != float64(12) || doc["failed_count"] != float64(3) {
		t.Fatalf("计数器必须原样保留, got (%v,%v)", doc["success_count"], doc["failed_count"])
	}
	if doc["disabled"] != true {
		t.Fatalf("disabled = %v, want true", doc["disabled"])
	}
}

// TestClearFailoverDisabledLocal_DropsDisableTime 覆盖时间戳的清理：恢复禁用
// 之后若残留旧时刻，下一次禁用会拿一个过期时间写进盘上标记。
func TestClearFailoverDisabledLocal_DropsDisableTime(t *testing.T) {
	clearFailoverDisabledLocalAll()
	t.Cleanup(clearFailoverDisabledLocalAll)

	markFailoverDisabledLocalAt("wb-t", "idx-t", "2026-10-24T15:06:59Z")
	if got := failoverDisableAtFor("wb-t"); got != "2026-10-24T15:06:59Z" {
		t.Fatalf("记录时刻 = %q", got)
	}
	clearFailoverDisabledLocal("wb-t", "idx-t")
	if got := failoverDisableAtFor("wb-t"); got != "" {
		t.Fatalf("清理后仍残留时刻 %q", got)
	}
	if isFailoverDisabledLocal("wb-t") {
		t.Fatal("清理后不应再判为本地禁用")
	}
}

// TestPersistCounterDelta_RestoresMarkersOnDisk 覆盖落盘整链路：宿主 persist 抹掉
// 标记之后，watchdog 的计数器落盘必须把标记补回同一份文档，且计数器增量正确。
// 这条用例存在的理由是变异验证：只测纯函数时，把 persistCounterDelta 里的
// restore 调用整行删掉，全部用例仍然通过（调用点无覆盖）。
func TestPersistCounterDelta_RestoresMarkersOnDisk(t *testing.T) {
	clearFailoverDisabledLocalAll()
	clearCountersForTest()
	t.Cleanup(func() {
		clearFailoverDisabledLocalAll()
		counterResolveIndexFn = resolveAuthIndexAndID
		counterGetPhysicalFn = hostAuthGetPhysical
		counterPersistFn = persistAuthDirect
	})

	markFailoverDisabledLocalAt("uid-p", "idx-p", "2026-10-24T15:52:37Z")

	// 宿主 persist 重建后的真实形态：白名单键 + 旧计数器，无本机制标记。
	disk := map[string]any{
		"type":          providerName,
		"disabled":      true,
		"success_count": float64(7),
		"failed_count":  float64(2),
	}
	var written []byte
	counterResolveIndexFn = func(string) (string, string) { return "idx-p", "uid-p" }
	counterGetPhysicalFn = func(string) (*hostAuthPhysical, error) {
		raw, err := json.Marshal(disk)
		if err != nil {
			return nil, err
		}
		return &hostAuthPhysical{Name: "workbuddy-uid-p.json", Path: "/tmp/host/workbuddy-uid-p.json", JSON: raw, Disabled: true}, nil
	}
	counterPersistFn = func(_, _ string, _ string, raw []byte) error {
		written = raw
		return nil
	}

	if err := persistCounterDelta("uid-p", 1, 0); err != nil {
		t.Fatalf("persistCounterDelta: %v", err)
	}
	if len(written) == 0 {
		t.Fatal("没有触发落盘")
	}
	var doc map[string]any
	if err := json.Unmarshal(written, &doc); err != nil {
		t.Fatalf("落盘内容必须是合法 JSON: %v", err)
	}
	if doc[failoverDisableKey] != true {
		t.Fatalf("落盘时必须补回 %s, got %v", failoverDisableKey, doc[failoverDisableKey])
	}
	if doc[failoverDisableAtKey] != "2026-10-24T15:52:37Z" {
		t.Fatalf("%s = %v, want 原始禁用时刻", failoverDisableAtKey, doc[failoverDisableAtKey])
	}
	if doc["success_count"] != float64(8) || doc["failed_count"] != float64(2) {
		t.Fatalf("计数器增量错误, got (%v,%v), want (8,2)", doc["success_count"], doc["failed_count"])
	}
	if doc["disabled"] != true {
		t.Fatalf("disabled = %v, want true", doc["disabled"])
	}
}

// TestFailoverUnconfirmed_OnlyWhenDiskAndHostDisagree 覆盖面板分叉判据：只有
// 「盘上说禁用、宿主仍 enabled」才提示写盘未生效；两侧一致时不得误报。
func TestFailoverUnconfirmed_OnlyWhenDiskAndHostDisagree(t *testing.T) {
	cases := []struct {
		disk, host, want bool
		reason           string
	}{
		{true, false, true, "盘上禁用而宿主仍路由：写盘未吸收，必须提示"},
		{true, true, false, "两侧都禁用：一致，不提示"},
		{false, false, false, "未禁用：不提示"},
		{false, true, false, "仅宿主禁用（人工禁用）：不是本机制的问题，不提示"},
	}
	for _, tc := range cases {
		if got := failoverUnconfirmed(tc.disk, tc.host); got != tc.want {
			t.Fatalf("failoverUnconfirmed(%v,%v) = %v, want %v（%s）", tc.disk, tc.host, got, tc.want, tc.reason)
		}
	}
}
