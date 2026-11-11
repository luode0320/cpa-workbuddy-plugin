package main

import (
	"encoding/json"
	"os"
	"testing"
)

// TestParseRealExportAllEntries 用本机真实导出文件回归 51 条账号回灌：
// 修复前 11 条（内层 credential 被 HTML 实体转义 / 云 API 响应包装 / 两层嵌套）
// 稳定失败，且报错被 storage.json 分支覆盖成「缺 icube 键」。
// 文件不存在时跳过，避免在无该文件的机器上制造假失败。
//
// [参数]
//   - t: 测试上下文
//
// [返回]
//   - 无
//
// 最近修改时间：2026-11-11；改动原因：新增，锁定真实导出文件的端到端回灌。
func TestParseRealExportAllEntries(t *testing.T) {
	const path = `C:\Users\luode\Downloads\traework-credentials-2026-11-11.json`
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("skip: 本机无导出文件 (%v)", err)
	}
	var doc struct {
		Accounts []json.RawMessage `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("导出文件不是合法 JSON: %v", err)
	}
	if len(doc.Accounts) == 0 {
		t.Fatal("导出文件 accounts 为空")
	}
	failed := 0
	uids := map[string]bool{}
	for i, entry := range doc.Accounts {
		a, perr := parseCredentialImport(entry)
		if perr != nil {
			failed++
			t.Logf("[%d] 解析失败: %v", i+1, perr)
			continue
		}
		if !a.hasToken() {
			failed++
			t.Logf("[%d] 无可用 token", i+1)
			continue
		}
		if a.UserID == "" {
			failed++
			t.Logf("[%d] uid 为空，会落盘成 legacy traework.json", i+1)
			continue
		}
		if _, dup := uids[a.UserID]; dup {
			t.Logf("[%d] uid 重复（源数据本身重复）: %s", i+1, a.UserID)
		}
		uids[a.UserID] = true
	}
	if failed != 0 {
		t.Fatalf("%d/%d 条解析失败", failed, len(doc.Accounts))
	}
	t.Logf("全部 %d 条解析成功，去重后 %d 个 uid", len(doc.Accounts), len(uids))
}
