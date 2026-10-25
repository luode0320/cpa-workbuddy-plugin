package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestReadWriteAuthDocPreservesUnknownKeys 是本机制的核心不变量：
// 写盘只能改目标键，盘上其它顶层键（含宿主字段与别的机制标记）必须原样保留。
// 这条不成立，「禁用标记」写进去就会在下一次插件写盘时被结构体重建抹掉。
func TestReadWriteAuthDocPreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zcode-abcdef12.json")

	original := map[string]any{
		"type":                     "zcode-provider",
		"provider":                 "zcode-provider",
		"auth_id":                  "zcode-abcdef12",
		"api_key":                  "sk-test",
		"created_at":               "2026-10-01T00:00:00Z",
		"updated_at":               "2026-10-01T00:00:00Z",
		"custom_host_field":        "must-survive",
		"another_mechanism_marker": true,
	}
	raw, _ := json.MarshalIndent(original, "", "  ")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatalf("准备样本失败: %v", err)
	}

	doc, _, err := readAuthDoc(path)
	if err != nil {
		t.Fatalf("readAuthDoc 失败: %v", err)
	}
	if doc["custom_host_field"] != "must-survive" {
		t.Fatalf("未知顶层键未被读出: %v", doc["custom_host_field"])
	}

	// 模拟写禁用标记。
	doc["disabled"] = true
	doc["failover_disable"] = true
	if err := writeAuthDoc(path, doc); err != nil {
		t.Fatalf("writeAuthDoc 失败: %v", err)
	}

	reread, _, err := readAuthDoc(path)
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if reread["custom_host_field"] != "must-survive" {
		t.Fatalf("写回后未知顶层键丢失: %v", reread["custom_host_field"])
	}
	if reread["another_mechanism_marker"] != true {
		t.Fatalf("写回后其它机制标记丢失: %v", reread["another_mechanism_marker"])
	}
	if reread["disabled"] != true || reread["failover_disable"] != true {
		t.Fatalf("写入的新标记未生效: %v", reread)
	}
}

// TestReadAuthDocRejectsPathTraversal 断言路径穿越与前缀不符被拒绝。
// 路径必须用字符串拼接构造：filepath.Join 会清理 `..`，拼出来的其实是合法路径，
// 那样断言会因为「文件不存在」而假通过，测不到穿越防护本身。
func TestReadAuthDocRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	cases := []string{
		dir + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "zcode-evil.json",
		filepath.Join(dir, "other-abcdef12.json"),
		filepath.Join(dir, "notjson.txt"),
	}
	for _, p := range cases {
		if _, _, err := readAuthDoc(p); err == nil {
			t.Fatalf("应拒绝非法路径 %q", p)
		}
	}
}

// TestWriteAuthDocRejectsPathTraversal 断言写盘同样拒绝非法路径，不得产生文件。
func TestWriteAuthDocRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	bad := dir + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "zcode-evil.json"
	if err := writeAuthDoc(bad, map[string]any{"a": 1}); err == nil {
		t.Fatal("应拒绝非法写盘路径")
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatalf("非法路径不应产生文件")
	}
}

// TestReadAuthDocRejectsInvalidJSON 断言非法 JSON 返回错误且不返回可写 doc。
func TestReadAuthDocRejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zcode-bad00000.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatalf("准备样本失败: %v", err)
	}
	doc, _, err := readAuthDoc(path)
	if err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
	if doc != nil {
		t.Fatalf("非法 JSON 不应返回可写 doc，实际 %v", doc)
	}
}

// TestReadAuthDocMissingFile 断言文件不存在时返回错误。
func TestReadAuthDocMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zcode-missing0.json")
	if _, _, err := readAuthDoc(path); err == nil {
		t.Fatal("文件不存在应返回错误")
	}
}

// TestWriteAuthFileDirectPreservesUnknownKeys 断言按结构体落盘的路径也保留未知顶层键。
//
// 这是 E2E 探针实测暴露的真实缺陷：探活成功后 saveAuthTestStatus 会按 StoredAuth
// 整篇重建文档，把刚由 reenableAccountForFailover 写好的 disabled=false 覆盖回内存
// 里的旧值，同时抹掉结构体未声明的宿主字段。归并写入是这条链路的正确形态。
func TestWriteAuthFileDirectPreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	path := filepath.Join(dir, "zcode-merge001.json")

	original := map[string]any{
		"type":                "zcode-provider",
		"provider":            "zcode-provider",
		"auth_id":             "zcode-merge001",
		"api_key":             "sk-test",
		"disabled":            false,
		"custom_host_field":   "must-survive",
		"failover_disable":    true,
		"failover_disable_at": "2026-10-25T00:00:00Z",
	}
	raw, _ := json.MarshalIndent(original, "", "  ")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatalf("准备样本失败: %v", err)
	}

	sa := &StoredAuth{AuthID: "zcode-merge001", APIKey: "sk-test", Disabled: false, TestFailed: false}
	if err := writeAuthFileDirect(path, sa); err != nil {
		t.Fatalf("writeAuthFileDirect 失败: %v", err)
	}

	doc, _, err := readAuthDoc(path)
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if doc["custom_host_field"] != "must-survive" {
		t.Fatalf("未知顶层键被整篇重建抹掉了: %v", doc["custom_host_field"])
	}
	if doc["failover_disable"] != true {
		t.Fatalf("禁用标记被整篇重建抹掉了: %v", doc["failover_disable"])
	}
	if doc["disabled"] != false {
		t.Fatalf("disabled 应保持 false，实际 %v", doc["disabled"])
	}
	if doc["api_key"] != "sk-test" {
		t.Fatalf("结构体已知字段应被写入，实际 %v", doc["api_key"])
	}
}
