package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// exportAccountEntry 是导出的单个账号条目。
//
// credential 是账号物理文件的解析结果（保留宿主与后续版本写入的未知字段），
// 面板可原样回灌 /import：导入侧会依次尝试 cookie / cookie_header / raw 等键，
// 因此导出 → 导入是同构闭环。
type exportAccountEntry struct {
	Name       string `json:"name"`
	AuthIndex  string `json:"auth_index"`
	UID        string `json:"uid,omitempty"`
	Nickname   string `json:"nickname,omitempty"`
	Label      string `json:"label,omitempty"`
	Credential any    `json:"credential,omitempty"`
	LoadError  string `json:"load_error,omitempty"`
	ParseError string `json:"parse_error,omitempty"`
}

// handleExportAccounts 把全部豆包账号导出为单个 JSON 汇总文件。
//
// 单账号读取失败只在该条目内联 load_error / parse_error，不中断整批导出。
// [参数] 无。
// [返回] 管理响应（application/json）。
// 最近修改时间：2026-11-14 02:48:00；改动原因：新增，面板「导出凭证」入口。
func handleExportAccounts() ([]byte, error) {
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return makeHTTPResponse(http.StatusInternalServerError, jsonErrorBody("读取账号列表失败："+errList.Error()))
	}

	entries := make([]exportAccountEntry, 0, len(accounts))
	for _, acc := range accounts {
		entry := exportAccountEntry{
			Name:      exportAccountFileName(acc),
			AuthIndex: acc.AuthID,
			UID:       acc.SecUserID,
			Nickname:  acc.ScreenName,
			Label:     acc.Label,
		}
		raw, errRead := rawCredentialBytes(acc)
		if errRead != nil {
			entry.LoadError = errRead.Error()
			entries = append(entries, entry)
			continue
		}
		var parsed any
		if errParse := json.Unmarshal(raw, &parsed); errParse != nil {
			// 物理文件不是合法 JSON 时保留原文，至少让用户能抢救出内容。
			entry.ParseError = errParse.Error()
			entry.Credential = string(raw)
			entries = append(entries, entry)
			continue
		}
		entry.Credential = parsed
		entries = append(entries, entry)
	}

	payload, _ := json.Marshal(map[string]any{
		"version":     1,
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"plugin":      providerName,
		"count":       len(entries),
		"accounts":    entries,
	})
	return makeHTTPResponse(http.StatusOK, payload)
}

// handleExportAccountsZip 把每个账号的原始凭据打成 zip，包内每个账号一个 JSON。
//
// 与 handleExportAccounts 的区别：那个返回单个汇总 JSON，适合整库备份；本接口
// 面向按账号分发/迁移，解压后直接得到 doubao-<id>.json 一组文件。
// 单账号读取失败时写入同名 .error.txt 说明原因，保持「每账号一个条目」。
// [参数] 无。
// [返回] 管理响应（application/zip）；无账号时返回 404 JSON。
// 最近修改时间：2026-11-14 02:52:00；改动原因：新增，面板「导出 ZIP」入口。
func handleExportAccountsZip() ([]byte, error) {
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return makeHTTPResponse(http.StatusInternalServerError, jsonErrorBody("读取账号列表失败："+errList.Error()))
	}
	if len(accounts) == 0 {
		return makeHTTPResponse(http.StatusNotFound, jsonErrorBody("没有可导出的账号"))
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	used := make(map[string]int, len(accounts))
	for _, acc := range accounts {
		name := uniqueZipName(exportAccountFileName(acc), used)
		raw, errRead := rawCredentialBytes(acc)
		if errRead != nil {
			writer, errCreate := zw.Create(strings.TrimSuffix(name, ".json") + ".error.txt")
			if errCreate == nil {
				_, _ = writer.Write([]byte("读取出错：" + errRead.Error()))
			}
			continue
		}
		writer, errCreate := zw.Create(name)
		if errCreate != nil {
			continue
		}
		// 原样写入物理文件字节：保留原始排版与未知字段，解压后可直接回灌导入。
		if _, errWrite := writer.Write(raw); errWrite != nil {
			continue
		}
	}
	if errClose := zw.Close(); errClose != nil {
		return makeHTTPResponse(http.StatusInternalServerError, jsonErrorBody("压缩包收尾失败："+errClose.Error()))
	}

	stamp := time.Now().Format("2006-01-02")
	headers := http.Header{}
	headers.Set("Content-Type", "application/zip")
	headers.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "doubao-credentials-"+stamp+".zip"))
	resp := pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: headers, Body: buf.Bytes()}
	return okEnvelope(resp)
}

// rawCredentialBytes 读取账号的原始凭据字节。
//
// 优先读物理文件（保留宿主写入的未知字段与原始排版）；物理路径缺失或不安全时
// 回退把内存结构序列化，保证导出永远有内容。
// [参数] acc: 账号结构。
// [返回] 凭据字节与读取错误。
// 最近修改时间：2026-11-14 02:44:00；改动原因：新增，导出取字节入口。
func rawCredentialBytes(acc *StoredAuth) ([]byte, error) {
	if acc == nil {
		return nil, fmt.Errorf("账号为空")
	}
	if acc.PhysicalPath != "" && isSafeDoubaoAuthPath(acc.PhysicalPath) {
		raw, errRead := os.ReadFile(acc.PhysicalPath)
		if errRead == nil && len(raw) > 0 {
			return raw, nil
		}
		if errRead != nil && !os.IsNotExist(errRead) {
			return nil, errRead
		}
	}
	raw, errMarshal := json.MarshalIndent(acc, "", "  ")
	if errMarshal != nil {
		return nil, errMarshal
	}
	return raw, nil
}

// exportAccountFileName 生成导出文件名（形如 doubao-<id>.json）。
// [参数] acc: 账号结构。
// [返回] 文件名。
// 最近修改时间：2026-11-14 02:45:00；改动原因：新增，导出命名。
func exportAccountFileName(acc *StoredAuth) string {
	if acc == nil {
		return "doubao-account.json"
	}
	if acc.PhysicalPath != "" {
		if base := strings.TrimSpace(filepath.Base(acc.PhysicalPath)); isDoubaoAuthFileName(base) {
			return base
		}
	}
	name := strings.TrimSpace(acc.AuthID)
	if name == "" {
		name = strings.TrimSpace(acc.FlowSecUserID)
	}
	if name == "" {
		name = "account"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		name += ".json"
	}
	return name
}

// uniqueZipName 保证 zip 内文件名唯一，重名时追加序号。
// [参数] name: 候选文件名；used: 已用计数表。
// [返回] 唯一文件名。
// 最近修改时间：2026-11-14 02:46:00；改动原因：新增，避免 zip 内条目重名。
func uniqueZipName(name string, used map[string]int) string {
	used[name]++
	if n := used[name]; n > 1 {
		return fmt.Sprintf("%s-%d.json", strings.TrimSuffix(name, ".json"), n)
	}
	return name
}

// jsonErrorBody 构造 {"error": "..."} 响应体。
// [参数] msg: 错误文案。
// [返回] JSON 字节。
// 最近修改时间：2026-11-14 02:40:00；改动原因：新增，导出错误统一构造。
func jsonErrorBody(msg string) []byte {
	body, _ := json.Marshal(map[string]any{"error": msg})
	return body
}

// extractImportPayload 从多种请求体形态中提取 Cookie header 与备注名。
//
// 面板可能提交三种形态：{cookie,label}（手工粘贴）、{raw:"..."}（纯文本）、
// {json:{...}}（导出文件里的 credential 对象，凭据在 cookie_header 字段）。
// 递归下钻时按 cookie / cookie_header / raw / json / credential / accounts 的顺序
// 查找，命中即返回，避免把整份导出文件当成单个 Cookie 处理。
// [参数] body: 请求体字节。
// [返回] Cookie header 与备注名，均可能为空。
// 最近修改时间：2026-11-14 02:55:00；改动原因：新增，兼容批量/导出回灌导入。
func extractImportPayload(body []byte) (string, string) {
	var probe any
	if errUnmarshal := json.Unmarshal(body, &probe); errUnmarshal != nil {
		return strings.TrimSpace(string(body)), ""
	}
	return cookieFromValue(probe, 0)
}

// cookieFromValue 在任意 JSON 值里递归查找 Cookie header。
// [参数] v: 待查找的值；depth: 当前递归深度（上限 4，防环）。
// [返回] Cookie header 与备注名。
// 最近修改时间：2026-11-14 02:56:00；改动原因：新增，导入载荷下钻。
func cookieFromValue(v any, depth int) (string, string) {
	if depth > 4 {
		return "", ""
	}
	switch typed := v.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return "", ""
		}
		// 字符串里可能嵌着 JSON（导出文件被整体粘贴时就是这种形态）。
		if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
			var inner any
			if json.Unmarshal([]byte(text), &inner) == nil {
				return cookieFromValue(inner, depth+1)
			}
		}
		return text, ""
	case map[string]any:
		label := ""
		for _, key := range []string{"label", "name", "remark"} {
			if s, ok := typed[key].(string); ok && strings.TrimSpace(s) != "" {
				label = strings.TrimSpace(s)
				break
			}
		}
		for _, key := range []string{"cookie", "cookie_header", "cookieHeader", "raw", "text", "json", "credential", "accounts", "auth", "account"} {
			child, exists := typed[key]
			if !exists {
				continue
			}
			if cookie, childLabel := cookieFromValue(child, depth+1); cookie != "" {
				if label == "" {
					label = childLabel
				}
				return cookie, label
			}
		}
		return "", label
	case []any:
		for _, child := range typed {
			if cookie, label := cookieFromValue(child, depth+1); cookie != "" {
				return cookie, label
			}
		}
		return "", ""
	}
	return "", ""
}
