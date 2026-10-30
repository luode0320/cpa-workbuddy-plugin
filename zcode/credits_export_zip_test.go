package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// extractManagementResponse 反解 okEnvelope 包装的管理响应，供 zip 用例断言。
// [参数] t: 测试上下文；raw: 信封字节
// [返回] 解包后的 ManagementResponse
// 最近修改时间：2026-10-30；改动原因：新增，zip 导出的返回值是信封包装。
func extractManagementResponse(t *testing.T, raw []byte) pluginapi.ManagementResponse {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("envelope not ok: %s", string(raw))
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return resp
}

// withAccountsDir 把账号目录临时指向 t.TempDir()，测试结束恢复原值。
// [参数] t: 测试上下文
// [返回] 临时账号目录路径
// 最近修改时间：2026-10-30；改动原因：新增，供 zip 导出用例隔离磁盘。
func withAccountsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	authFilesMu.Lock()
	orig := accountsDir
	accountsDir = dir
	authFilesMu.Unlock()
	t.Cleanup(func() {
		authFilesMu.Lock()
		accountsDir = orig
		authFilesMu.Unlock()
	})
	return dir
}

// TestZcodeAccountFileName 验证 zip 内单账号文件名的生成与去重。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：新增。
func TestZcodeAccountFileName(t *testing.T) {
	used := map[string]int{}
	if got := zcodeAccountFileName("zcode-a.json", used); got != "zcode-a.json" {
		t.Fatalf("got %q, want zcode-a.json", got)
	}
	if got := zcodeAccountFileName("zcode-a.json", used); got != "zcode-a-2.json" {
		t.Fatalf("dedup got %q, want zcode-a-2.json", got)
	}
	if got := zcodeAccountFileName(" ", map[string]int{}); got != "zcode-account.json" {
		t.Fatalf("blank name got %q, want zcode-account.json", got)
	}
}

// TestHandleExportAccountsZip_OneFilePerAccount 验证 zip 内每个账号一个独立 JSON，
// 内容与磁盘文件字节一致。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：新增核心用例。
func TestHandleExportAccountsZip_OneFilePerAccount(t *testing.T) {
	dir := withAccountsDir(t)
	payloads := map[string]string{
		zcodeAuthFilePrefix + "111.json": `{"auth":{"token":"tok-111"},"uid":"111"}`,
		zcodeAuthFilePrefix + "222.json": `{"token":"tok-222"}`,
	}
	for name, body := range payloads {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}

	raw, err := handleExportAccountsZip()
	if err != nil {
		t.Fatalf("handleExportAccountsZip: %v", err)
	}
	resp := extractManagementResponse(t, raw)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Headers.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content-type = %q, want application/zip", ct)
	}

	zr, err := zip.NewReader(bytes.NewReader(resp.Body), int64(len(resp.Body)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	if len(zr.File) != len(payloads) {
		t.Fatalf("zip entries = %d, want %d", len(zr.File), len(payloads))
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, errOpen := f.Open()
		if errOpen != nil {
			t.Fatalf("open %s: %v", f.Name, errOpen)
		}
		data, errRead := io.ReadAll(rc)
		_ = rc.Close()
		if errRead != nil {
			t.Fatalf("read %s: %v", f.Name, errRead)
		}
		got[f.Name] = string(data)
	}
	for name, want := range payloads {
		if got[name] != want {
			t.Fatalf("%s content = %q, want %q (原始字节必须原样保留)", name, got[name], want)
		}
	}
}

// TestHandleExportAccountsZip_Empty 验证无账号时返回 404。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：新增边界用例。
func TestHandleExportAccountsZip_Empty(t *testing.T) {
	withAccountsDir(t)
	raw, err := handleExportAccountsZip()
	if err != nil {
		t.Fatalf("handleExportAccountsZip: %v", err)
	}
	resp := extractManagementResponse(t, raw)
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, got %v", body)
	}
}
