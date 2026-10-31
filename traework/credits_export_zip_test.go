package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestPerAccountFileName_Zip 验证 zip 内单账号文件名的生成与去重。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：新增，覆盖 zip 导出的文件命名。
func TestPerAccountFileName_Zip(t *testing.T) {
	cases := []struct {
		name      string
		authName  string
		authIndex string
		want      string
	}{
		{name: "沿用宿主文件名", authName: "acct-1.json", authIndex: "idx1", want: "acct-1.json"},
		{name: "缺后缀自动补 .json", authName: "acct-2", authIndex: "idx2", want: "acct-2.json"},
		{name: "空名回退 auth_index", authName: "", authIndex: "idx3", want: "idx3.json"},
		{name: "全空回退固定名", authName: "", authIndex: "", want: providerName + "-account.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			used := map[string]int{}
			if got := perAccountFileName(tc.authName, tc.authIndex, used); got != tc.want {
				t.Fatalf("perAccountFileName(%q,%q) = %q, want %q", tc.authName, tc.authIndex, got, tc.want)
			}
		})
	}

	used := map[string]int{}
	first := perAccountFileName("dup.json", "a", used)
	second := perAccountFileName("dup.json", "b", used)
	if first != "dup.json" || second != "dup-2.json" {
		t.Fatalf("dedup = %q/%q, want dup.json/dup-2.json", first, second)
	}
}

// withAuthStubsZip 替换 host.auth 接缝并在测试结束后恢复。
// [参数] t: 测试上下文；files: 列表桩返回值；listErr: 列表错误；getFn: 读取桩
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：新增，供 zip 导出用例注入内存数据。
func withAuthStubsZip(t *testing.T, files []pluginapi.HostAuthFileEntry, listErr error, getFn func(string) (*hostAuthPhysical, error)) {
	t.Helper()
	origList, origGet := hostAuthListFn, hostAuthGetPhysicalFn
	t.Cleanup(func() {
		hostAuthListFn = origList
		hostAuthGetPhysicalFn = origGet
	})
	hostAuthListFn = func() ([]pluginapi.HostAuthFileEntry, error) { return files, listErr }
	if getFn != nil {
		hostAuthGetPhysicalFn = getFn
	}
}

// TestHandleExportAuthZip_OneFilePerAccount 验证 zip 内每个账号一个独立 JSON，
// 且内容与宿主物理文件字节逐字节一致。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：新增核心用例。
func TestHandleExportAuthZip_OneFilePerAccount(t *testing.T) {
	files := []pluginapi.HostAuthFileEntry{
		{Name: "acct-111.json", AuthIndex: "idx-111"},
		{Name: "acct-222.json", AuthIndex: "idx-222"},
	}
	payloads := map[string]string{
		"idx-111": `{"auth":{"accessToken":"tok-111"},"account":{"uid":"111"}}`,
		"idx-222": `{"accessToken":"tok-222"}`,
	}
	withAuthStubsZip(t, files, nil, func(idx string) (*hostAuthPhysical, error) {
		body, ok := payloads[idx]
		if !ok {
			return nil, errors.New("not found")
		}
		return &hostAuthPhysical{AuthIndex: idx, JSON: []byte(body)}, nil
	})

	resp := handleExportAuthZip(pluginapi.ManagementRequest{Method: http.MethodGet})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Headers.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content-type = %q, want application/zip", ct)
	}

	zr, err := zip.NewReader(bytes.NewReader(resp.Body), int64(len(resp.Body)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	if len(zr.File) != 2 {
		t.Fatalf("zip entries = %d, want 2", len(zr.File))
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
	for name, idx := range map[string]string{"acct-111.json": "idx-111", "acct-222.json": "idx-222"} {
		if got[name] != payloads[idx] {
			t.Fatalf("%s content = %q, want %q (原始字节必须原样保留)", name, got[name], payloads[idx])
		}
	}
}

// TestHandleExportAuthZip_PartialFailure 验证单账号读取失败不影响其余账号。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：新增容错用例。
func TestHandleExportAuthZip_PartialFailure(t *testing.T) {
	files := []pluginapi.HostAuthFileEntry{
		{Name: "ok.json", AuthIndex: "ok"},
		{Name: "bad.json", AuthIndex: "bad"},
	}
	withAuthStubsZip(t, files, nil, func(idx string) (*hostAuthPhysical, error) {
		if idx == "bad" {
			return nil, errors.New("auth store unavailable")
		}
		return &hostAuthPhysical{AuthIndex: idx, JSON: []byte(`{"accessToken":"x"}`)}, nil
	})

	resp := handleExportAuthZip(pluginapi.ManagementRequest{Method: http.MethodGet})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	zr, err := zip.NewReader(bytes.NewReader(resp.Body), int64(len(resp.Body)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["ok.json"] {
		t.Fatalf("missing ok.json in zip: %v", names)
	}
	if !names["bad.error.txt"] {
		t.Fatalf("bad account must yield bad.error.txt: %v", names)
	}
}

// TestHandleExportAuthZip_Empty 验证无账号时返回 404 JSON 错误。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：新增边界用例。
func TestHandleExportAuthZip_Empty(t *testing.T) {
	withAuthStubsZip(t, nil, nil, nil)
	resp := handleExportAuthZip(pluginapi.ManagementRequest{Method: http.MethodGet})
	if resp.StatusCode != http.StatusNotFound {
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

// TestHandleExportAuthZip_ListError 验证 host 列表失败返回 500。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：新增错误路径用例。
func TestHandleExportAuthZip_ListError(t *testing.T) {
	withAuthStubsZip(t, nil, errors.New("host.auth.list boom"), nil)
	resp := handleExportAuthZip(pluginapi.ManagementRequest{Method: http.MethodGet})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if !strings.Contains(string(resp.Body), "host.auth.list") {
		t.Fatalf("body should mention host.auth.list, got %s", string(resp.Body))
	}
}

// TestExportAuthZipSurvivesEnvelopeRoundTrip 验证 zip 二进制经宿主 envelope
// 往返后仍逐字节完好、可正常解压。
//
// 背景：管理接口统一走 okEnvelope → json.Marshal → 宿主还原 Result 的链路。
// 曾有人据静态代码推测 ManagementResponse.Body 会被 base64 二次编码而损坏
// zip，实测证明 envelope 往返无损。该用例把这条实测结论固化为回归断言：
// 任何改动导致二进制在 envelope 里失真，这里立刻失败。
//
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-31；改动原因：新增，锁定 zip 二进制经 envelope 无损。
func TestExportAuthZipSurvivesEnvelopeRoundTrip(t *testing.T) {
	files := []pluginapi.HostAuthFileEntry{
		{Name: "acct-1.json", AuthIndex: "idx-1"},
	}
	withAuthStubsZip(t, files, nil, func(idx string) (*hostAuthPhysical, error) {
		return &hostAuthPhysical{AuthIndex: idx, JSON: []byte(`{"accessToken":"tok"}`)}, nil
	})

	// 走生产链路：handler → okEnvelope。
	raw, err := okEnvelope(handleExportAuthZip(pluginapi.ManagementRequest{Method: http.MethodGet}))
	if err != nil {
		t.Fatalf("okEnvelope: %v", err)
	}

	// 还原宿主侧的解包结果。
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("envelope ok = false, raw=%s", string(raw))
	}
	var restored pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &restored); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if restored.StatusCode != http.StatusOK {
		t.Fatalf("restored status = %d, want 200", restored.StatusCode)
	}
	if len(restored.Body) < 4 || !bytes.Equal(restored.Body[:4], []byte("PK\x03\x04")) {
		t.Fatalf("restored body 不是合法 zip 头: %x", restored.Body[:min(4, len(restored.Body))])
	}

	// 端到端可解压，且内容与原始凭证字节一致。
	zr, err := zip.NewReader(bytes.NewReader(restored.Body), int64(len(restored.Body)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	if len(zr.File) != 1 {
		t.Fatalf("zip entries = %d, want 1", len(zr.File))
	}
	rc, errOpen := zr.File[0].Open()
	if errOpen != nil {
		t.Fatalf("open entry: %v", errOpen)
	}
	data, errRead := io.ReadAll(rc)
	_ = rc.Close()
	if errRead != nil {
		t.Fatalf("read entry: %v", errRead)
	}
	if string(data) != `{"accessToken":"tok"}` {
		t.Fatalf("entry content = %q, want 原始凭证字节", string(data))
	}
}

// min 返回两个整数中的较小者（Go 1.21 之前的标准库无内置 min）。
// [参数] a, b: 待比较的两个整数
// [返回] 较小值
// 最近修改时间：2026-10-31；改动原因：新增，供切片边界保护使用。
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
