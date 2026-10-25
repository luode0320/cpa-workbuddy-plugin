package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// failoverFakeHost 是写盘禁用链路的假宿主：把物理文档放在临时目录，
// 通过注入点替换 resolveAuthIndexForID 与 readAuthDocByIndex。
type failoverFakeHost struct {
	mu      sync.Mutex
	dir     string
	indexes map[string]string // authID → auth_index
	docs    map[string]map[string]any
	writes  int
}

// newFailoverFakeHost 构造假宿主并注册清理。
// [参数] t：当前测试。
// [返回] 假宿主实例。
// 最近修改时间：2026-10-25；改动原因：新增，隔离真实宿主 RPC。
func newFailoverFakeHost(t *testing.T) *failoverFakeHost {
	t.Helper()
	h := &failoverFakeHost{
		dir:     t.TempDir(),
		indexes: make(map[string]string),
		docs:    make(map[string]map[string]any),
	}
	prevResolve, prevDoc, prevNow, prevWrite, prevList := failoverResolveIdxFn, failoverGetDocFn, failoverNowFn, failoverWriteDocFn, failoverListFn
	failoverResolveIdxFn = func(authID string) (string, string) {
		h.mu.Lock()
		defer h.mu.Unlock()
		idx := h.indexes[normalizeFailoverKey(authID)]
		if idx == "" {
			return "", ""
		}
		return idx, filepath.Join(h.dir, "zcode-"+idx+".json")
	}
	failoverGetDocFn = func(authIndex string) (map[string]any, string, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		doc, ok := h.docs[authIndex]
		if !ok {
			return nil, "", os.ErrNotExist
		}
		// 返回深拷贝，模拟「读到的盘上内容」与后续写入互不影响。
		cp := make(map[string]any, len(doc))
		for k, v := range doc {
			cp[k] = v
		}
		return cp, filepath.Join(h.dir, "zcode-"+authIndex+".json"), nil
	}
	failoverWriteDocFn = func(path string, doc map[string]any) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		// 从路径反推 auth_index，把写入反映到内存文档，模拟真实落盘后可回读。
		base := strings.TrimSuffix(filepath.Base(path), ".json")
		idx := strings.TrimPrefix(base, "zcode-")
		if _, ok := h.docs[idx]; !ok {
			return os.ErrNotExist
		}
		cp := make(map[string]any, len(doc))
		for k, v := range doc {
			cp[k] = v
		}
		h.docs[idx] = cp
		h.writes++
		return nil
	}
	failoverListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		out := make([]pluginapi.HostAuthFileEntry, 0, len(h.docs))
		for idx, doc := range h.docs {
			disabled, _ := doc["disabled"].(bool)
			out = append(out, pluginapi.HostAuthFileEntry{
				ID:        idx,
				AuthIndex: idx,
				Name:      "zcode-" + idx + ".json",
				Disabled:  disabled,
			})
		}
		return out, nil
	}
	t.Cleanup(func() {
		failoverResolveIdxFn, failoverGetDocFn, failoverNowFn, failoverWriteDocFn = prevResolve, prevDoc, prevNow, prevWrite
		failoverListFn = prevList
		resetFailoverDisableState()
	})
	return h
}

// addAccount 在假宿主里登记一个账号及其盘上文档。
// [参数] authID：账号标识；authIndex：宿主索引；extra：额外顶层键。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，构造测试前置状态。
func (h *failoverFakeHost) addAccount(authID, authIndex string, extra map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.indexes[normalizeFailoverKey(authID)] = authIndex
	doc := map[string]any{
		"type":     zcodeProviderID,
		"provider": zcodeProviderID,
		"auth_id":  authID,
		"api_key":  "sk-test",
		"disabled": false,
	}
	for k, v := range extra {
		doc[k] = v
	}
	h.docs[authIndex] = doc
}

// docOf 返回某账号当前的盘上文档快照。
// [参数] authIndex：宿主索引。
// [返回] 文档副本。
// 最近修改时间：2026-10-25；改动原因：新增，断言用。
func (h *failoverFakeHost) docOf(authIndex string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.docs[authIndex]
}

// resetFailoverDisableState 清空本机制的三张内存表，测试之间互不影响。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，避免用例串味。
func resetFailoverDisableState() {
	failoverStateMu.Lock()
	defer failoverStateMu.Unlock()
	failoverProbeModels = make(map[string]string)
	failoverDisabledLocal = make(map[string]struct{})
	failoverDisableAtLocal = make(map[string]string)
}

// TestDisableAccountForFailoverWritesMarkers 断言失败禁用写入 disabled 与三个归属标记，
// 且盘上其它顶层键原样保留。
func TestDisableAccountForFailoverWritesMarkers(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-aaaa1111", "idx-1", map[string]any{"custom_key": "keep-me"})

	if !disableAccountForFailover("zcode-aaaa1111", "glm-5.3") {
		t.Fatal("首次失败应写入禁用")
	}

	doc := h.docOf("idx-1")
	if doc["disabled"] != true {
		t.Fatalf("disabled = %v, want true", doc["disabled"])
	}
	if doc[failoverDisableKey] != true {
		t.Fatalf("%s = %v, want true", failoverDisableKey, doc[failoverDisableKey])
	}
	if doc[failoverDisableModelKey] != "glm-5.3" {
		t.Fatalf("%s = %v, want glm-5.3", failoverDisableModelKey, doc[failoverDisableModelKey])
	}
	if at, _ := doc[failoverDisableAtKey].(string); at == "" {
		t.Fatalf("%s 应为 RFC3339 时刻，实际为空", failoverDisableAtKey)
	}
	if doc["custom_key"] != "keep-me" {
		t.Fatalf("未知顶层键被抹掉: %v", doc["custom_key"])
	}
	if !isFailoverDisabledLocal("zcode-aaaa1111") {
		t.Fatal("内存应记录本进程写过禁用")
	}
}

// TestDisableAccountForFailoverSkipsManualDisabled 断言用户手动禁用的账号不被接管。
func TestDisableAccountForFailoverSkipsManualDisabled(t *testing.T) {
	h := newFailoverFakeHost(t)
	// 盘上已 disabled 但无本机制标记 = 用户手动禁用。
	h.addAccount("zcode-bbbb2222", "idx-2", map[string]any{"disabled": true})

	if disableAccountForFailover("zcode-bbbb2222", "glm-5.3") {
		t.Fatal("手动禁用不应被本机制认领")
	}
	doc := h.docOf("idx-2")
	if _, has := doc[failoverDisableKey]; has {
		t.Fatalf("不应给手动禁用号写归属标记: %v", doc)
	}
	if doc["disabled"] != true {
		t.Fatalf("手动禁用的 disabled 不应被改动: %v", doc["disabled"])
	}
}

// TestDisableAccountForFailoverIsIdempotent 断言重复失败不重复改写盘上归属标记。
func TestDisableAccountForFailoverIsIdempotent(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-cccc3333", "idx-3", nil)

	if !disableAccountForFailover("zcode-cccc3333", "glm-5.3") {
		t.Fatal("首次失败应写入禁用")
	}
	firstAt := h.docOf("idx-3")[failoverDisableAtKey]

	if !disableAccountForFailover("zcode-cccc3333", "glm-5.3") {
		t.Fatal("再次失败仍应视为受本机制管辖")
	}
	if got := h.docOf("idx-3")[failoverDisableAtKey]; got != firstAt {
		t.Fatalf("重复禁用不应重写禁用时刻：%v → %v", firstAt, got)
	}
}

// TestDisableAccountForFailoverSkipsUnknownAuth 断言解析不到 auth_index 时不写盘。
func TestDisableAccountForFailoverSkipsUnknownAuth(t *testing.T) {
	newFailoverFakeHost(t)
	if disableAccountForFailover("zcode-missing0", "glm-5.3") {
		t.Fatal("解析不到 auth_index 应返回 false")
	}
}

// TestReenableAccountForFailoverClearsMarkers 断言恢复只清本机制标记并保留其它键。
func TestReenableAccountForFailoverClearsMarkers(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-dddd4444", "idx-4", map[string]any{"custom_key": "keep-me"})

	if !disableAccountForFailover("zcode-dddd4444", "glm-5.3") {
		t.Fatal("准备阶段禁用失败")
	}
	if !reenableAccountForFailover("idx-4", "zcode-dddd4444") {
		t.Fatal("恢复应成功")
	}

	doc := h.docOf("idx-4")
	if doc["disabled"] != false {
		t.Fatalf("disabled = %v, want false", doc["disabled"])
	}
	for _, key := range []string{failoverDisableKey, failoverDisableModelKey, failoverDisableAtKey} {
		if _, has := doc[key]; has {
			t.Fatalf("标记 %s 应被清除: %v", key, doc)
		}
	}
	if doc["custom_key"] != "keep-me" {
		t.Fatalf("恢复不应抹掉其它顶层键: %v", doc["custom_key"])
	}
	if isFailoverDisabledLocal("zcode-dddd4444") {
		t.Fatal("内存禁用记忆应被清除")
	}
}

// TestReenableAccountForFailoverSkipsManualDisabled 断言手动禁用号不会被本机制恢复。
func TestReenableAccountForFailoverSkipsManualDisabled(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-eeee5555", "idx-5", map[string]any{"disabled": true})

	if reenableAccountForFailover("idx-5", "zcode-eeee5555") {
		t.Fatal("无本机制标记的号不应被恢复")
	}
	if h.docOf("idx-5")["disabled"] != true {
		t.Fatal("手动禁用的 disabled 不应被改动")
	}
}

// TestRestoreFailoverMarkersIntoDoc 断言宿主抹掉标记后能被补回，
// 且三种不该补的情形都不补。
func TestRestoreFailoverMarkersIntoDoc(t *testing.T) {
	newFailoverFakeHost(t)

	t.Run("本进程写过禁用且盘上仍 disabled 时补回", func(t *testing.T) {
		resetFailoverDisableState()
		markFailoverDisabledLocalAt("zcode-ffff6666", "", "2026-10-25T00:00:00Z")
		doc := map[string]any{"disabled": true, "custom": "x"}

		if !restoreFailoverMarkersIntoDoc(doc, "zcode-ffff6666", "glm-5.3") {
			t.Fatal("应补回标记")
		}
		if doc[failoverDisableKey] != true {
			t.Fatalf("标记未写入: %v", doc)
		}
		if doc[failoverDisableAtKey] != "2026-10-25T00:00:00Z" {
			t.Fatalf("应保留原始禁用时刻，实际 %v", doc[failoverDisableAtKey])
		}
		if doc[failoverDisableModelKey] != "glm-5.3" {
			t.Fatalf("模型未写入: %v", doc[failoverDisableModelKey])
		}
	})

	t.Run("本进程没写过禁用时不补", func(t *testing.T) {
		resetFailoverDisableState()
		doc := map[string]any{"disabled": true}
		if restoreFailoverMarkersIntoDoc(doc, "zcode-unknown0", "glm-5.3") {
			t.Fatal("非本进程禁用不应补标记")
		}
	})

	t.Run("盘上已启用时不补", func(t *testing.T) {
		resetFailoverDisableState()
		markFailoverDisabledLocalAt("zcode-gggg7777", "", "2026-10-25T00:00:00Z")
		doc := map[string]any{"disabled": false}
		if restoreFailoverMarkersIntoDoc(doc, "zcode-gggg7777", "glm-5.3") {
			t.Fatal("已恢复的账号不应被重新标禁用")
		}
	})

	t.Run("标记已存在时不重复补", func(t *testing.T) {
		resetFailoverDisableState()
		markFailoverDisabledLocalAt("zcode-hhhh8888", "", "2026-10-25T00:00:00Z")
		doc := map[string]any{"disabled": true, failoverDisableKey: true}
		if restoreFailoverMarkersIntoDoc(doc, "zcode-hhhh8888", "glm-5.3") {
			t.Fatal("标记已存在时不应重复补写")
		}
	})
}

// TestRecordFailureScopedTriggersDisable 断言失败记账真的会触达写盘禁用。
// 这是「接线」验证：纯函数测试通过不代表调用点被覆盖。
func TestRecordFailureScopedTriggersDisable(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-iiii9999", "idx-9", nil)

	recordFailureScoped("zcode-iiii9999", "glm-5.3", failureScopeAccount, "upstream 500")

	doc := h.docOf("idx-9")
	if doc["disabled"] != true {
		t.Fatal("失败记账应触发写盘禁用")
	}
	if doc[failoverDisableKey] != true {
		t.Fatalf("应带归属标记: %v", doc)
	}
}

// TestRecordFailureScopedSkipsNone 断言分类为 none 时不写盘（请求形状问题不该禁用账号）。
func TestRecordFailureScopedSkipsNone(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-jjjj0000", "idx-10", nil)

	recordFailureScoped("zcode-jjjj0000", "glm-5.3", failureScopeNone, "upstream 400")

	if h.docOf("idx-10")["disabled"] != false {
		t.Fatal("分类为 none 时不应禁用账号")
	}
}

// TestFailoverMarkersRoundTripJSON 断言标记经过 JSON 往返后仍可读出。
func TestFailoverMarkersRoundTripJSON(t *testing.T) {
	doc := map[string]any{
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "glm-5.3",
		failoverDisableAtKey:    "2026-10-25T00:00:00Z",
	}
	raw, _ := json.Marshal(doc)
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("往返失败: %v", err)
	}
	marked, model, at := parseFailoverMarkers(back)
	if !marked || model != "glm-5.3" || at != "2026-10-25T00:00:00Z" {
		t.Fatalf("标记往返后读错: marked=%v model=%q at=%q", marked, model, at)
	}
	if !strings.Contains(string(raw), failoverDisableKey) {
		t.Fatalf("序列化后应含标记键名: %s", raw)
	}
}
