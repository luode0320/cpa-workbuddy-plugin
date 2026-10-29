// backup_test.go 覆盖扣子账号凭据的导出与备份恢复契约。
//
// 关注三件事：导出必须携带完整原文（否则恢复后无法发请求）、恢复必须
// 复用同一账号文件（否则同一账号会双重落盘）、非法凭据必须拒绝而不是
// 写出一个看起来可用实则无效的账号。
package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// newBackupFixture 在临时目录准备账号目录并返回它。
// [参数] t: 测试上下文。
// [返回] 账号目录。
func newBackupFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := getAccountsDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir(prev) })
	return dir
}

// writeBackupFixtureAccount 落盘一个可用于导入的账号文件。
// [参数] t: 测试上下文；dir: 账号目录；authID: 账号标识；disabled: 是否禁用。
// [返回] 落盘后的账号。
func writeBackupFixtureAccount(t *testing.T, dir, authID string, disabled bool) *StoredAuth {
	t.Helper()
	sa := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       authID,
		CookieHeader: "sessionid=abc; sid_tt=def",
		SecUserID:    "sec-user-" + authID,
		ScreenName:   "夹具昵称",
		Cookies: []savedCookie{{
			Source: cozeSiteHost + "/",
			Cookie: http.Cookie{Name: "sessionid", Value: "abc", Domain: ".coze.cn", Path: "/"},
		}},
		Label:     "夹具 " + authID,
		Disabled:  disabled,
		LoginAt:   "2026-10-29T00:00:00+08:00",
		CreatedAt: "2026-10-29T00:00:00+08:00",
		UpdatedAt: "2026-10-29T00:00:00+08:00",
	}
	path := filepath.Join(dir, authID+".json")
	if errWrite := writeAuthFileDirect(path, sa); errWrite != nil {
		t.Fatalf("写入账号夹具失败：%v", errWrite)
	}
	sa.PhysicalPath = path
	return sa
}

// TestHandleExportAuth_KeepsFullCredential 验证导出保留完整原文。
//
// 备份是唯一恢复手段，缺 cookie_header 或 cookies 快照都会让恢复出的账号
// 既发不出请求也无法重建 Cookie 容器。
// [参数] t: 测试上下文。
// [返回] 无。
func TestHandleExportAuth_KeepsFullCredential(t *testing.T) {
	dir := newBackupFixture(t)
	writeBackupFixtureAccount(t, dir, "coze-export01", false)
	writeBackupFixtureAccount(t, dir, "coze-export02", true)

	out := decodeManagementBody(t, mustExport(t))

	if got := int(out["count"].(float64)); got != 2 {
		t.Fatalf("count = %d; want 2", got)
	}
	if out["plugin"] != providerName {
		t.Fatalf("plugin = %v; want %s", out["plugin"], providerName)
	}
	if v := int(out["version"].(float64)); v != backupVersion {
		t.Fatalf("version = %d; want %d", v, backupVersion)
	}
	accounts, _ := out["accounts"].([]any)
	for _, item := range accounts {
		entry, _ := item.(map[string]any)
		cred, ok := entry["credential"].(map[string]any)
		if !ok {
			t.Fatalf("账号 %v 缺少 credential", entry["auth_id"])
		}
		if header, _ := cred["cookie_header"].(string); strings.TrimSpace(header) == "" {
			t.Fatalf("账号 %v 的 cookie_header 为空，备份无法恢复", entry["auth_id"])
		}
		if _, exists := cred["cookies"]; !exists {
			t.Fatalf("账号 %v 缺少 cookies 快照", entry["auth_id"])
		}
	}
}

// TestHandleImportCred_RoundTripsDisabledAccount 验证禁用账号可完整往返。
//
// 禁用意图必须随备份一起恢复：恢复成启用会让一个本不该参与路由的账号
// 立刻承载流量。
// [参数] t: 测试上下文。
// [返回] 无。
func TestHandleImportCred_RoundTripsDisabledAccount(t *testing.T) {
	dir := newBackupFixture(t)
	src := writeBackupFixtureAccount(t, dir, "coze-roundtrip", true)

	raw, errRead := os.ReadFile(src.PhysicalPath)
	if errRead != nil {
		t.Fatalf("读取夹具失败：%v", errRead)
	}
	// 模拟灾备：清空账号目录后再用备份内容恢复。
	if errRemove := os.Remove(src.PhysicalPath); errRemove != nil {
		t.Fatalf("清理夹具失败：%v", errRemove)
	}

	body, _ := json.Marshal(map[string]any{"cred": json.RawMessage(raw)})
	out := decodeManagementBody(t, mustImport(t, body))
	if out["success"] != true {
		t.Fatalf("恢复失败：%v", out["error"])
	}
	if out["disabled"] != true {
		t.Fatal("禁用状态必须随备份恢复")
	}

	restored, errLoad := readAuthFile(filepath.Join(dir, "coze-roundtrip.json"))
	if errLoad != nil {
		t.Fatalf("恢复后账号文件不可读：%v", errLoad)
	}
	if restored.CookieHeader != src.CookieHeader {
		t.Fatalf("cookie_header 未原样恢复：%q", restored.CookieHeader)
	}
	if restored.Label != src.Label {
		t.Fatalf("label 未恢复：%q", restored.Label)
	}
	if !restored.Disabled {
		t.Fatal("恢复后 disabled 丢失")
	}
	if len(restored.Cookies) != 1 {
		t.Fatalf("cookies 快照未恢复，实际 %d 条", len(restored.Cookies))
	}
}

// TestHandleImportCred_RejectsMissingCookie 验证缺失会话 Cookie 的凭据被拒绝。
//
// 只校验 JSON 能否解析是不够的：扣子账号的唯一可用性判据是 cookie_header，
// 放行会写出一批「能列出、发不出请求」的坏账号。
// [参数] t: 测试上下文。
// [返回] 无。
func TestHandleImportCred_RejectsMissingCookie(t *testing.T) {
	newBackupFixture(t)

	for _, cred := range []string{
		`{"auth_id":"coze-nocookie","label":"x"}`,
		`{"cookie_header":"   ","auth_id":"coze-blank"}`,
	} {
		body, _ := json.Marshal(map[string]any{"cred": json.RawMessage(cred)})
		out := decodeManagementBody(t, mustImport(t, body))
		if out["success"] == true {
			t.Fatalf("缺失 cookie_header 的凭据必须被拒绝：%s", cred)
		}
	}
}

// TestHandleImportCred_DerivesAuthIDFromSecUserID 验证标识缺失时按 sec_user_id 重算。
//
// 账号标识是文件名的一部分，缺失时若随手编一个，同一账号每次恢复都会
// 产生新文件，账号数越恢复越多。
// [参数] t: 测试上下文。
// [返回] 无。
func TestHandleImportCred_DerivesAuthIDFromSecUserID(t *testing.T) {
	dir := newBackupFixture(t)
	secUserID := "sec-user-derive"
	expected := authIDForSecUserID(secUserID)
	cred := `{"cookie_header":"sessionid=abc","sec_user_id":"` + secUserID + `"}`

	body, _ := json.Marshal(map[string]any{"cred": json.RawMessage(cred)})
	out := decodeManagementBody(t, mustImport(t, body))
	if out["success"] != true {
		t.Fatalf("恢复失败：%v", out["error"])
	}
	if out["auth_id"] != expected {
		t.Fatalf("auth_id = %v; want %s", out["auth_id"], expected)
	}
	if _, errStat := os.Stat(filepath.Join(dir, expected+".json")); errStat != nil {
		t.Fatalf("账号文件未按派生标识落盘：%v", errStat)
	}
}

// TestHandleImportCred_IsIdempotentPerAuthID 验证重复恢复写回同一文件。
//
// 落点由 auth_id 唯一决定：反复导入同一账号只覆盖一份文件，
// 不会每次多出一个副本（灾备重试是常见操作，必须幂等）。
// [参数] t: 测试上下文。
// [返回] 无。
func TestHandleImportCred_IsIdempotentPerAuthID(t *testing.T) {
	dir := newBackupFixture(t)
	sa := writeBackupFixtureAccount(t, dir, "coze-idempotent", false)
	payload := importPayloadOf(t, sa.PhysicalPath)

	for i := 0; i < 3; i++ {
		out := decodeManagementBody(t, mustImport(t, payload))
		if out["success"] != true {
			t.Fatalf("第 %d 次恢复失败：%v", i+1, out["error"])
		}
	}

	entries, errReadDir := os.ReadDir(dir)
	if errReadDir != nil {
		t.Fatalf("读取账号目录失败：%v", errReadDir)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("重复恢复产生了多个文件：%v", names)
	}
}

// TestHandleImportCred_RejectsForeignAuthID 验证非扣子命名空间标识被拒绝。
//
// auth_id 直接参与文件名拼接，放行外部形态等于允许写穿账号目录边界。
// [参数] t: 测试上下文。
// [返回] 无。
func TestHandleImportCred_RejectsForeignAuthID(t *testing.T) {
	newBackupFixture(t)
	body, _ := json.Marshal(map[string]any{
		"cred": json.RawMessage(`{"cookie_header":"sessionid=abc","auth_id":"../escape"}`),
	})
	out := decodeManagementBody(t, mustImport(t, body))
	if out["success"] == true {
		t.Fatal("非法 auth_id 必须被拒绝")
	}
}

// mustExport 调用导出路由并返回管理响应原文。
// [参数] t: 测试上下文。
// [返回] 响应字节。
func mustExport(t *testing.T) []byte {
	t.Helper()
	raw, errHandle := handleManagement(managementPayload(t, "GET", "/export", nil))
	if errHandle != nil {
		t.Fatalf("导出调用失败：%v", errHandle)
	}
	return raw
}

// mustImport 调用恢复路由并返回管理响应原文。
// [参数] t: 测试上下文；body: 请求体。
// [返回] 响应字节。
func mustImport(t *testing.T, body []byte) []byte {
	t.Helper()
	raw, errHandle := handleManagement(managementPayload(t, "POST", "/import-cred", body))
	if errHandle != nil {
		t.Fatalf("恢复调用失败：%v", errHandle)
	}
	return raw
}

// managementPayload 构造宿主下发的 management.handle 载荷。
// [参数] t: 测试上下文；method: HTTP 方法；subPath: 插件子路径；body: 请求体。
// [返回] 序列化后的载荷。
func managementPayload(t *testing.T, method, subPath string, body []byte) []byte {
	t.Helper()
	req := pluginapi.ManagementRequest{
		Method: method,
		Path:   getManagementBasePath() + "/plugins/" + providerName + subPath,
		Body:   body,
		Query:  url.Values{},
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}
	raw, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatalf("构造管理载荷失败：%v", errMarshal)
	}
	return raw
}

// importPayloadOf 用磁盘上的账号文件构造恢复请求体。
// [参数] t: 测试上下文；path: 账号文件路径。
// [返回] 请求体字节。
func importPayloadOf(t *testing.T, path string) []byte {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("读取账号文件失败：%v", errRead)
	}
	body, errMarshal := json.Marshal(map[string]any{"cred": json.RawMessage(raw)})
	if errMarshal != nil {
		t.Fatalf("构造请求体失败：%v", errMarshal)
	}
	return body
}
