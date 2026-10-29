// backup.go 实现扣子账号凭据的导出备份与备份恢复。
//
// 与 workbuddy / zcode 的导入不同，扣子没有 PAT / API Key 体系：账号唯一来源是
// 浏览器授权换来的会话 Cookie。因此这里不提供「粘贴 Token 换号」，只做两件事：
//
//	导出：把全部落盘账号原文汇总成一个 JSON 文件；
//	恢复：把该文件逐条写回并重新注册到宿主。
//
// 备份必须保留完整明文凭据（cookie_header + cookies 快照）与全部状态字段，
// 否则恢复出来的账号既无法发请求、也丢失禁用意图。
//
// 最近修改时间：2026-10-29；改动原因：新增，补齐面板缺失的导出与导入能力。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// backupVersion 是备份文件的格式版本，恢复时据此识别包装结构。
const backupVersion = 1

// backupAccount 是导出文件 accounts 数组中的单条记录。
//
// Credential 内嵌落盘凭据原文（解析后的 JSON 对象）而不是字符串，
// 使文件可直接阅读与 diff；恢复端接受同一形态。
type backupAccount struct {
	AuthID     string          `json:"auth_id"`
	Label      string          `json:"label,omitempty"`
	ScreenName string          `json:"screen_name,omitempty"`
	Disabled   bool            `json:"disabled"`
	Credential json.RawMessage `json:"credential,omitempty"`
	// LoadError 记录单条账号读取失败的原因；失败项不带 credential。
	LoadError string `json:"load_error,omitempty"`
}

// backupDocument 是导出文件的顶层结构。
type backupDocument struct {
	Version    int             `json:"version"`
	ExportedAt string          `json:"exported_at"`
	Plugin     string          `json:"plugin"`
	Count      int             `json:"count"`
	Accounts   []backupAccount `json:"accounts"`
}

// handleExportAuth 导出全部扣子账号凭据。
//
// 逐号读取物理文件原文而不是序列化内存结构：内存结构可能来自宿主 RPC 的
// 旧快照，原文才是当前生效凭据。单号读取失败只记 load_error，不让整批失败。
// [参数] 无。
// [返回] 管理响应。
// 最近修改时间：2026-10-29；改动原因：新增，面板「导出备份」入口。
func handleExportAuth() ([]byte, error) {
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, errList.Error())))
	}

	entries := make([]backupAccount, 0, len(accounts))
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		entry := backupAccount{
			AuthID:     acc.AuthID,
			Label:      acc.Label,
			ScreenName: acc.ScreenName,
			Disabled:   acc.Disabled,
		}
		// 1. 优先读物理原文，保留字段顺序与未知字段。
		raw, errRead := backupRawOf(acc)
		if errRead != nil {
			entry.LoadError = errRead.Error()
			entries = append(entries, entry)
			continue
		}
		// 2. 原文必须是合法 JSON：非法内容内嵌会产出无法回导的备份。
		if !json.Valid(raw) {
			entry.LoadError = "凭据文件不是合法 JSON"
			entries = append(entries, entry)
			continue
		}
		entry.Credential = json.RawMessage(raw)
		entries = append(entries, entry)
	}

	doc := backupDocument{
		Version:    backupVersion,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Plugin:     providerName,
		Count:      len(entries),
		Accounts:   entries,
	}
	body, errMarshal := marshalNoHTMLEscape(doc)
	if errMarshal != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(`{"error":"备份编码失败"}`))
	}
	return makeHTTPResponse(http.StatusOK, body)
}

// backupRawOf 返回单个账号的落盘原文；无物理路径时退回内存结构序列化。
// [参数] acc: 账号。
// [返回] JSON 字节与错误。
// 最近修改时间：2026-10-29；改动原因：新增，导出原文读取。
func backupRawOf(acc *StoredAuth) ([]byte, error) {
	if path := strings.TrimSpace(acc.PhysicalPath); path != "" && isSafeCozeAuthPath(path) {
		raw, errRead := readAuthFileRaw(path)
		if errRead == nil {
			return raw, nil
		}
	}
	raw, errMarshal := json.MarshalIndent(acc, "", "  ")
	if errMarshal != nil {
		return nil, fmt.Errorf("序列化凭据失败：%w", errMarshal)
	}
	return raw, nil
}

// handleImportCred 恢复一条备份凭据。
//
// 请求体为 {cred: <凭据 JSON>}，由面板把导出文件拆成单条后逐条提交，
// 因此进度与失败原因能精确到单个账号。
// [参数] body: 含 cred 的请求体。
// [返回] 管理响应；失败用 200 + success:false 表达，避免面板把业务失败
// 误判成密钥失效（api() 只在 401/403 上走重填分支）。
// 最近修改时间：2026-10-29；改动原因：新增，备份恢复入口。
func handleImportCred(body []byte) ([]byte, error) {
	var req struct {
		Cred json.RawMessage `json:"cred"`
	}
	_ = json.Unmarshal(body, &req)
	if len(req.Cred) == 0 {
		return importFailure("缺少 cred 字段")
	}

	var sa StoredAuth
	if errUnmarshal := json.Unmarshal(req.Cred, &sa); errUnmarshal != nil {
		return importFailure("凭据解析失败：" + errUnmarshal.Error())
	}
	// 1. 会话 Cookie 是扣子账号的唯一可用性判据，缺失即无效凭据。
	if strings.TrimSpace(sa.CookieHeader) == "" {
		return importFailure("凭据缺少 cookie_header，不是有效的扣子账号备份")
	}
	// 2. 标识缺失时按 sec_user_id 重算：同一账号必须落回同一个文件。
	if strings.TrimSpace(sa.AuthID) == "" {
		sa.AuthID = authIDForSecUserID(sa.SecUserID)
	}
	sa.AuthID = strings.TrimSuffix(strings.TrimSpace(sa.AuthID), ".json")
	if sa.AuthID == "" {
		return importFailure("凭据缺少 auth_id 且无法由 sec_user_id 派生")
	}
	if !isCozeAuthFileName(sa.AuthID + ".json") {
		return importFailure("凭据 auth_id 不符合扣子命名空间：" + sa.AuthID)
	}
	// 3. 归属字段强制归一：备份可能来自其他 provider 或早期版本。
	sa.Type = providerName
	sa.Provider = providerName
	now := time.Now().Format(time.RFC3339)
	if strings.TrimSpace(sa.CreatedAt) == "" {
		sa.CreatedAt = now
	}
	sa.UpdatedAt = now

	// 4. 落点固定在插件账号目录：与首次授权（saveFromToken）同一口径。
	// 同一 auth_id 重复恢复即覆盖同一文件，天然幂等；恢复后再推宿主一份。
	targetPath := getAccountsDir() + "/" + sa.AuthID + ".json"
	sa.PhysicalPath = targetPath
	if errWrite := writeAuthFileDirect(targetPath, &sa); errWrite != nil {
		return importFailure("写入凭据失败：" + errWrite.Error())
	}
	// 5. 统一补注册宿主：writeAuthFileDirect 见到 PhysicalPath 非空会跳过自动注册，
	// 而恢复禁用账号同样需要在宿主 spool 里存在且带 disabled 标记。
	//
	// 注册失败不整体判负：凭据已经落盘、插件侧可见可续期，只是宿主暂时不路由，
	// 面板会提示重试或用「补注册」修复。把整体标失败会让用户误以为数据没写进去。
	warning := ""
	if errRegister := reRegisterOneToHost(&sa); errRegister != nil {
		warning = "已写入本地但注册宿主失败：" + errRegister.Error()
		hostLogf("warn", "备份恢复注册宿主失败", map[string]any{
			"auth_id": sa.AuthID,
			"error":   errRegister.Error(),
		})
	}

	payload, errMarshal := json.Marshal(map[string]any{
		"success":    true,
		"auth_id":    sa.AuthID,
		"name":       authFileNameOf(sa.AuthID),
		"file":       targetPath,
		"label":      sa.Label,
		"disabled":   sa.Disabled,
		"registered": warning == "",
		"warning":    warning,
	})
	if errMarshal != nil {
		return importFailure("响应编码失败：" + errMarshal.Error())
	}
	return makeHTTPResponse(http.StatusOK, payload)
}

// importFailure 构造恢复失败响应。
// [参数] message: 失败原因。
// [返回] 管理响应。
// 最近修改时间：2026-10-29；改动原因：新增，统一失败响应构造。
func importFailure(message string) ([]byte, error) {
	payload, _ := json.Marshal(map[string]any{"success": false, "error": message})
	return makeHTTPResponse(http.StatusOK, payload)
}
