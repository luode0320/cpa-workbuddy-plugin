package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestExtractImportPayloadShapes 校验导入载荷在多种形态下的提取结果。
//
// 面板会把手工粘贴的 Cookie、导出汇总 JSON、物理账号文件、以及 {json:{...}}
// 包装都送进同一条 /import 通道，载荷形态必须全部命中 cookie 字段。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 02:58:00；改动原因：新增，锁定批量导入与导出回灌的载荷兼容性。
func TestExtractImportPayloadShapes(t *testing.T) {
	cases := []struct {
		name      string
		payload   map[string]any
		wantLabel string
	}{
		{
			name:      "裸 cookie 对象",
			payload:   map[string]any{"cookie": validCookieHeader, "label": "手工"},
			wantLabel: "手工",
		},
		{
			name:    "纯文本 raw",
			payload: map[string]any{"raw": validCookieHeader},
		},
		{
			name:      "导入包装 json",
			payload:   map[string]any{"json": map[string]any{"cookie_header": validCookieHeader, "label": "包装"}},
			wantLabel: "包装",
		},
		{
			name: "导出汇总回灌",
			payload: map[string]any{
				"version": 1,
				"accounts": []any{map[string]any{
					"name":       "doubao-x.json",
					"label":      "导出",
					"credential": map[string]any{"cookie_header": validCookieHeader},
				}},
			},
			wantLabel: "导出",
		},
	}
	for _, c := range cases {
		body, errMarshal := json.Marshal(c.payload)
		if errMarshal != nil {
			t.Fatalf("%s：构造请求体失败：%v", c.name, errMarshal)
		}
		cookie, label := extractImportPayload(body)
		if cookie != validCookieHeader {
			t.Errorf("%s：cookie 提取错误，实际 %q", c.name, cookie)
		}
		if label != c.wantLabel {
			t.Errorf("%s：label 提取错误，实际 %q，期望 %q", c.name, label, c.wantLabel)
		}
	}
}

// TestExtractImportPayloadRejectsEmpty 校验空载荷返回空 cookie。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 02:58:00；改动原因：新增，负向用例。
func TestExtractImportPayloadRejectsEmpty(t *testing.T) {
	bodies := []string{
		"{}",
		"{\"label\":\"x\"}",
		"{\"accounts\":[]}",
		"",
	}
	for _, body := range bodies {
		if cookie, _ := extractImportPayload([]byte(body)); cookie != "" {
			t.Errorf("空载荷 %q 不应提取出 cookie，实际 %q", body, cookie)
		}
	}
}

// seedExportAccounts 在临时目录预置两个账号，测试结束自动清理。
//
// 走 writeAuthFileDirect 落盘而非直接写文件，保证导出侧读到的是真实账号文件。
// [参数] t: 测试上下文。
// [返回] 账号标识列表。
// 最近修改时间：2026-11-14 02:58:00；改动原因：新增，导出用例共用的账号夹具。
func seedExportAccounts(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })

	ids := []string{doubaoAuthFilePrefix + "exporta", doubaoAuthFilePrefix + "exportb"}
	for _, id := range ids {
		sa := &StoredAuth{
			AuthID:       id,
			CookieHeader: validCookieHeader,
			Label:        "导出测试-" + id,
			ScreenName:   "昵称-" + id,
		}
		if err := writeAuthFileDirect(filepath.Join(dir, id+".json"), sa); err != nil {
			t.Fatalf("预置账号 %s 失败：%v", id, err)
		}
	}
	return ids
}

// TestHandleExportAccountsRoute 校验汇总导出的结构与回灌闭环。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 02:58:00；改动原因：新增，锁定 /export 输出契约。
func TestHandleExportAccountsRoute(t *testing.T) {
	ids := seedExportAccounts(t)

	req := pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/doubao-provider/export"}
	raw, _ := json.Marshal(req)
	out, err := handleManagement(raw)
	if err != nil {
		t.Fatalf("handleManagement 返回错误：%v", err)
	}
	body := decodeMgmtBody(t, out)

	var parsed map[string]any
	if errUnmarshal := json.Unmarshal([]byte(body), &parsed); errUnmarshal != nil {
		t.Fatalf("导出结果不是合法 JSON：%v，原始：%s", errUnmarshal, truncateForTest(body))
	}
	if got, _ := parsed["version"].(float64); got != 1 {
		t.Errorf("version 应为 1，实际 %v", parsed["version"])
	}
	if got, _ := parsed["plugin"].(string); got != providerName {
		t.Errorf("plugin 应为 %s，实际 %v", providerName, parsed["plugin"])
	}
	if got, _ := parsed["exported_at"].(string); got == "" {
		t.Error("exported_at 不应为空")
	}
	accounts, _ := parsed["accounts"].([]any)
	if len(accounts) != len(ids) {
		t.Fatalf("应导出 %d 个账号，实际 %d", len(ids), len(accounts))
	}
	if got, _ := parsed["count"].(float64); int(got) != len(ids) {
		t.Errorf("count 应为 %d，实际 %v", len(ids), parsed["count"])
	}
	// 导出的 credential 必须能被导入侧原样抽回 Cookie，形成导出→导入闭环。
	cookie, _ := extractImportPayload([]byte(body))
	if cookie != validCookieHeader {
		t.Errorf("导出文件回灌应能提取到原始 Cookie，实际 %q", cookie)
	}
}

// TestHandleExportAccountsZipRoute 校验 ZIP 导出的响应头与包内条目。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 02:58:00；改动原因：新增，锁定 /export/zip 输出契约。
func TestHandleExportAccountsZipRoute(t *testing.T) {
	ids := seedExportAccounts(t)

	envelope, err := handleExportAccountsZip()
	if err != nil {
		t.Fatalf("handleExportAccountsZip 返回错误：%v", err)
	}
	status, headers, body := decodeRawMgmtResponse(t, envelope)
	if status != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d", status)
	}
	if ct := headers["Content-Type"]; len(ct) == 0 || ct[0] != "application/zip" {
		t.Errorf("Content-Type 应为 application/zip，实际 %v", ct)
	}
	if cd := headers["Content-Disposition"]; len(cd) == 0 || !strings.Contains(cd[0], "doubao-credentials-") {
		t.Errorf("Content-Disposition 应含默认文件名，实际 %v", cd)
	}

	zr, errZip := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if errZip != nil {
		t.Fatalf("响应体不是合法 zip：%v", errZip)
	}
	if len(zr.File) != len(ids) {
		t.Fatalf("zip 内应有 %d 个条目，实际 %d", len(ids), len(zr.File))
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".json") {
			t.Errorf("条目名应以 .json 结尾，实际 %q", f.Name)
		}
		rc, errOpen := f.Open()
		if errOpen != nil {
			t.Fatalf("打开条目 %s 失败：%v", f.Name, errOpen)
		}
		content, _ := io.ReadAll(rc)
		_ = rc.Close()
		if !strings.Contains(string(content), "cookie_header") {
			t.Errorf("条目 %s 应保留 cookie_header 原始字段", f.Name)
		}
	}
}

// decodeRawMgmtResponse 解析管理响应信封，取出状态码、响应头与原始字节。
//
// 与 decodeMgmtBody 的区别：ZIP 响应体是二进制，必须拿 []byte 而不是字符串。
// [参数] t: 测试上下文；envelope: okEnvelope 输出的信封 JSON。
// [返回] 状态码、响应头映射与响应体字节。
// 最近修改时间：2026-11-14 02:58:00；改动原因：新增，二进制响应解析。
func decodeRawMgmtResponse(t *testing.T, envelope []byte) (int, map[string][]string, []byte) {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(envelope, &parsed); err != nil {
		t.Fatalf("解析管理信封失败：%v", err)
	}
	result, _ := parsed["result"].(map[string]any)
	if result == nil {
		t.Fatalf("管理信封缺少 result：%s", truncateForTest(string(envelope)))
	}
	status := 0
	if v, ok := result["StatusCode"].(float64); ok {
		status = int(v)
	}
	headers := map[string][]string{}
	if rawHeaders, ok := result["Headers"].(map[string]any); ok {
		for key, rawList := range rawHeaders {
			list, _ := rawList.([]any)
			values := make([]string, 0, len(list))
			for _, item := range list {
				if s, okStr := item.(string); okStr {
					values = append(values, s)
				}
			}
			headers[key] = values
		}
	}
	encoded, _ := result["Body"].(string)
	body, errDecode := base64.StdEncoding.DecodeString(encoded)
	if errDecode != nil {
		t.Fatalf("响应体 base64 解码失败：%v", errDecode)
	}
	return status, headers, body
}

// TestHandleExportZipEmptyReturns404 校验无账号时 ZIP 导出返回 404 与提示。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 02:58:00；改动原因：新增，空账号负向用例。
func TestHandleExportZipEmptyReturns404(t *testing.T) {
	setAccountsDir(t.TempDir())
	defer setAccountsDir("")

	req := pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/doubao-provider/export/zip"}
	raw, _ := json.Marshal(req)
	out, err := handleManagement(raw)
	if err != nil {
		t.Fatalf("handleManagement 返回错误：%v", err)
	}
	if body := decodeMgmtBody(t, out); !strings.Contains(body, "没有可导出的账号") {
		t.Errorf("无账号时应给出明确提示，实际响应体：%s", truncateForTest(body))
	}
}

// TestExportAccountFileNameAndUniqueness 校验导出命名与重名收敛。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 02:58:00；改动原因：新增，命名规则用例。
func TestExportAccountFileNameAndUniqueness(t *testing.T) {
	if got := exportAccountFileName(nil); got != "doubao-account.json" {
		t.Errorf("空账号应回退默认名，实际 %q", got)
	}
	if got := exportAccountFileName(&StoredAuth{AuthID: "doubao-abc"}); got != "doubao-abc.json" {
		t.Errorf("缺物理路径时应按 AuthID 命名，实际 %q", got)
	}
	if got := exportAccountFileName(&StoredAuth{PhysicalPath: "/tmp/doubao-xyz.json"}); got != "doubao-xyz.json" {
		t.Errorf("有物理路径时应沿用文件名，实际 %q", got)
	}

	used := map[string]int{}
	first := uniqueZipName("doubao-a.json", used)
	second := uniqueZipName("doubao-a.json", used)
	if first != "doubao-a.json" {
		t.Errorf("首次命名应保持原名，实际 %q", first)
	}
	if second != "doubao-a-2.json" {
		t.Errorf("重名应追加序号，实际 %q", second)
	}
}

// TestExportRoutesRegistered 校验导出路由已登记到管理路由表。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 02:58:00；改动原因：新增，防止路由注册漂移。
func TestExportRoutesRegistered(t *testing.T) {
	reg := managementRegistration()
	want := map[string]bool{"export": false, "export/zip": false}
	for _, route := range reg.Routes {
		for key := range want {
			if route.Path == "/plugins/"+providerName+"/"+key && route.Method == http.MethodGet {
				want[key] = true
			}
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("缺少 GET /plugins/%s/%s 路由", providerName, key)
		}
	}
}
