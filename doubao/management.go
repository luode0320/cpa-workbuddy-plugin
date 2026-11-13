package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

var (
	managementBasePathMu sync.RWMutex
	managementBasePath   = "/v0/management"
)

// setManagementBasePath 记录宿主下发的管理路由前缀。
// [参数] base: 宿主管理 API 基础路径。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，管理前缀热更新入口。
func setManagementBasePath(base string) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return
	}
	managementBasePathMu.Lock()
	defer managementBasePathMu.Unlock()
	managementBasePath = base
}

// getManagementBasePath 读取当前管理路由前缀。
// [参数] 无。
// [返回] 管理基础路径。
// 最近修改时间：2026-10-17；改动原因：新增，前缀读取入口。
func getManagementBasePath() string {
	managementBasePathMu.RLock()
	defer managementBasePathMu.RUnlock()
	return managementBasePath
}

type managementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

// managementRegistration 声明插件对宿主暴露的管理路由与资源页。
// [参数] 无。
// [返回] 路由与资源注册结构。
// 最近修改时间：2026-10-17；改动原因：新增登录态导入与账号启停路由。
func managementRegistration() managementRegistrationResponse {
	base := "/plugins/" + providerName
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: base + "/accounts", Description: "列出豆包账号。"},
			{Method: http.MethodGet, Path: base + "/status", Description: "列出豆包账号状态。"},
			{Method: http.MethodPost, Path: base + "/import", Description: "导入浏览器登录态（body: {cookie, label?}）。"},
			{Method: http.MethodPost, Path: base + "/delete", Description: "删除一个豆包账号。"},
			{Method: http.MethodPost, Path: base + "/enable", Description: "启用一个豆包账号。"},
			{Method: http.MethodPost, Path: base + "/disable", Description: "禁用一个豆包账号。"},
			{Method: http.MethodGet, Path: base + "/models", Description: "列出账号可测试的模型（query: auth_index）。"},
			{Method: http.MethodPost, Path: base + "/test-active", Description: "对单个账号发起一次真实推理测试（body: {auth_index, model?}）。"},
		},
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "Doubao", Description: "豆包 Provider 管理面板：登录态导入、账号管理、状态诊断。"},
			{Path: "/login", Description: "宿主「添加账号」弹窗打开的登录引导页（浏览器 Cookie 导入桥接）。"},
			{Path: "/login/import", Description: "登录引导页的 Cookie 提交入口（免管理密钥，state 短时凭证）。"},
		},
	}
}

// normalizeMgmtSubPath 归一化管理子路径，兼容宿主三种前缀形态。
// [参数] rawPath: 宿主传入的原始路径。
// [返回] 归一化子路径与从路径解析出的查询参数。
// 最近修改时间：2026-10-17；改动原因：新增，兼容 resource 前缀与 management 前缀两种形态。
func normalizeMgmtSubPath(rawPath string) (string, url.Values) {
	u, err := url.Parse(rawPath)
	path := rawPath
	var query url.Values
	if err == nil {
		path = u.Path
		query = u.Query()
	}
	path = strings.TrimRight(path, "/")

	// 兼容 /v0/resource/plugins/doubao-provider/...
	resPrefix := "/v0/resource/plugins/" + providerName
	if strings.HasPrefix(path, resPrefix) {
		sub := strings.TrimPrefix(path, resPrefix)
		if sub == "" {
			return "/panel", query
		}
		return sub, query
	}

	// 兼容 /v0/management/plugins/doubao-provider/... 与 /plugins/doubao-provider/...
	fullPrefix := getManagementBasePath() + "/plugins/" + providerName
	shortPrefix := "/plugins/" + providerName
	switch {
	case strings.HasPrefix(path, fullPrefix):
		path = strings.TrimPrefix(path, fullPrefix)
	case strings.HasPrefix(path, shortPrefix):
		path = strings.TrimPrefix(path, shortPrefix)
	}
	if path == "" {
		path = "/"
	}
	return path, query
}

// handleManagement 分发插件管理路由。
// [参数] raw: 宿主 management.handle 载荷。
// [返回] 管理响应信封与错误。
// 最近修改时间：2026-10-17；改动原因：新增导入与账号启停分发分支。
func handleManagement(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	subPath, urlQuery := normalizeMgmtSubPath(req.Path)
	method := strings.ToUpper(req.Method)

	if method == http.MethodGet && (subPath == "/" || subPath == "/panel") {
		return handlePanelPage(), nil
	}

	if method == http.MethodGet && subPath == "/login" {
		state := ""
		if vals := req.Query["state"]; len(vals) > 0 {
			state = strings.TrimSpace(vals[0])
		}
		if state == "" && urlQuery != nil {
			state = strings.TrimSpace(urlQuery.Get("state"))
		}
		return okEnvelope(handleLoginBridge(state, req.Path))
	}

	// 桥接页的提交入口：宿主把 resource 请求强制改写为 GET 且不转发请求体，
	// 因此这里必须用 GET 分支；Cookie 由桥接页放进自定义请求头。
	if method == http.MethodGet && subPath == "/login/import" {
		return okEnvelope(handleLoginBridgeImport(req))
	}

	switch {
	case method == http.MethodGet && (subPath == "/accounts" || subPath == "/status"):
		return handleListAccounts()

	case method == http.MethodPost && subPath == "/import":
		return handleImportCredentials(req.Body)
	case method == http.MethodPost && subPath == "/delete":
		return handleDeleteAccount(req.Body)

	case method == http.MethodPost && subPath == "/enable":
		return handleSetAccountStatus(req.Body, false)

	case method == http.MethodPost && subPath == "/disable":
		return handleSetAccountStatus(req.Body, true)

	case method == http.MethodGet && subPath == "/models":
		return makeJSONResponse(handleModelsQuery(req))

	case method == http.MethodPost && subPath == "/test-active":
		return makeJSONResponse(handleTestActive(req))

	default:
		return makeHTTPResponse(http.StatusNotFound, []byte(`{"error":"not_found"}`))
	}
}

// makeHTTPResponse 包装管理 API 的 JSON 响应信封。
// [参数] status: HTTP 状态码；body: 响应体字节。
// [返回] 管理响应信封与错误。
// 最近修改时间：2026-10-17；改动原因：新增，统一管理响应构造。
func makeHTTPResponse(status int, body []byte) ([]byte, error) {
	resp := pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
		},
		Body: body,
	}
	return okEnvelope(resp)
}

// makeJSONResponse 把 map 结果编码成 200 管理响应。
//
// 测试类接口统一用「200 + error 字段」表达失败：面板的 api() 只在 401/403 上
// 走密钥重填分支，业务失败必须靠响应体区分，否则会被误判成密钥失效。
// [参数] payload: 结果对象。
// [返回] 管理响应信封与错误。
// 最近修改时间：2026-10-17；改动原因：新增，map 结果统一编码。
func makeJSONResponse(payload map[string]any) ([]byte, error) {
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(`{"error":"结果编码失败"}`))
	}
	return makeHTTPResponse(http.StatusOK, body)
}

// accountSummary 是面板账号列表项。
type accountSummary struct {
	AuthID     string `json:"auth_id"`
	AuthIndex  string `json:"auth_index"`
	Label      string `json:"label"`
	ScreenName string `json:"screen_name,omitempty"`
	Mobile     string `json:"mobile,omitempty"`
	SecUserID  string `json:"sec_user_id,omitempty"`
	Disabled   bool   `json:"disabled"`
	// AuthState 是登录态状态：ok（有效）/ expired（待重新授权）。
	// 判定依据是账号文件中会话主 Cookie 是否仍在有效期内（见 cookiejar.go）。
	AuthState string `json:"auth_state"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// handleListAccounts 返回全部豆包账号摘要。
// [参数] 无。
// [返回] 管理响应。
// 最近修改时间：2026-10-17；改动原因：改为读取真实账号列表（合并宿主与本地目录）。
func handleListAccounts() ([]byte, error) {
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, errList.Error())))
	}

	list := make([]accountSummary, 0, len(accounts))
	for _, acc := range accounts {
		list = append(list, accountSummary{
			AuthID:     acc.AuthID,
			AuthIndex:  acc.AuthID,
			Label:      acc.Label,
			ScreenName: acc.ScreenName,
			Mobile:     acc.Mobile,
			SecUserID:  maskIdentifier(acc.SecUserID),
			Disabled:   acc.Disabled,
			AuthState:  authStateOf(acc),
			CreatedAt:  acc.CreatedAt,
			UpdatedAt:  acc.UpdatedAt,
		})
	}
	body, _ := json.Marshal(map[string]any{"accounts": list, "count": len(list)})
	return makeHTTPResponse(http.StatusOK, body)
}

// authStateOf 判定账号的登录态状态。
//
// 依据账号文件中的会话主 Cookie 快照：无快照时回退用 CookieHeader 判断
// （首次导入尚未滚动写回的账号），任一会话 Cookie 存在且未过期即为 ok。
// [参数] sa: 账号结构。
// [返回] "ok" 或 "expired"。
// 最近修改时间：2026-10-17；改动原因：新增，登录态失效状态上报（AC-002 标准③）。
func authStateOf(sa *StoredAuth) string {
	jar := newDurableJar()
	if len(sa.Cookies) > 0 {
		jar.restore(sa.Cookies)
	} else {
		jar.seedFromHeader(sa.CookieHeader, doubaoSiteHost+"/")
	}
	if jar.hasLiveSession() {
		return "ok"
	}
	return "expired"
}

// handleImportCredentials 处理登录态导入请求。
//
// 请求体：{cookie, label?}。cookie 是用户在浏览器中复制的完整 Cookie header。
// 处理链路：解析 → 字段齐备性校验 → 判据端点确认登录态 → 落盘。
// [参数] body: 请求体字节。
// [返回] 管理响应。
// 最近修改时间：2026-10-17；改动原因：新增，登录态导入入口（AC-001）。
func handleImportCredentials(body []byte) ([]byte, error) {
	var req struct {
		Cookie string `json:"cookie"`
		Label  string `json:"label"`
	}
	if errUnmarshal := json.Unmarshal(body, &req); errUnmarshal != nil {
		return makeHTTPResponse(http.StatusBadRequest, []byte(fmt.Sprintf(`{"error":%q}`, "请求体格式错误，应为 JSON")))
	}
	if strings.TrimSpace(req.Cookie) == "" {
		return makeHTTPResponse(http.StatusBadRequest, []byte(`{"error":"cookie 不能为空"}`))
	}

	timeout := time.Duration(currentConfig().TimeoutSeconds) * time.Second
	sa, errSave := saveCredentials(req.Cookie, req.Label, timeout)
	if errSave != nil {
		// 校验失败是预期路径（垃圾 Cookie / 缺字段 / 会话过期），返回 400 并给出可操作提示。
		msg, _ := json.Marshal(map[string]any{"error": errSave.Error()})
		return makeHTTPResponse(http.StatusBadRequest, msg)
	}

	respBody, _ := json.Marshal(map[string]any{
		"ok":      true,
		"auth_id": sa.AuthID,
		"label":   sa.Label,
		"message": "登录态导入成功",
	})
	return makeHTTPResponse(http.StatusOK, respBody)
}

// handleDeleteAccount 删除指定账号。
// [参数] body: 含 auth_id 的请求体。
// [返回] 管理响应。
// 最近修改时间：2026-10-17；改动原因：新增，账号删除入口。
func handleDeleteAccount(body []byte) ([]byte, error) {
	authID := extractAuthID(body)
	if authID == "" {
		return makeHTTPResponse(http.StatusBadRequest, []byte(`{"error":"auth_id 不能为空"}`))
	}
	if errDelete := deleteAuthFileDirect(authID); errDelete != nil {
		return makeHTTPResponse(http.StatusNotFound, []byte(fmt.Sprintf(`{"error":%q}`, errDelete.Error())))
	}
	return makeHTTPResponse(http.StatusOK, []byte(`{"ok":true}`))
}

// handleSetAccountStatus 启用或禁用单个账号。
// [参数] body: 含 auth_id 的请求体；disabled: 目标状态。
// [返回] 管理响应。
// 最近修改时间：2026-10-17；改动原因：新增，账号启停入口。
func handleSetAccountStatus(body []byte, disabled bool) ([]byte, error) {
	authID := extractAuthID(body)
	if authID == "" {
		return makeHTTPResponse(http.StatusBadRequest, []byte(`{"error":"auth_id 不能为空"}`))
	}
	if errSet := setAccountDisabled(authID, disabled); errSet != nil {
		return makeHTTPResponse(http.StatusNotFound, []byte(fmt.Sprintf(`{"error":%q}`, errSet.Error())))
	}
	return makeHTTPResponse(http.StatusOK, []byte(`{"ok":true}`))
}

// extractAuthID 从请求体中提取 auth_id / auth_index / authID / id。
// [参数] body: 请求体字节。
// [返回] 账号标识；均缺失时返回空串。
// 最近修改时间：2026-10-17；改动原因：新增，账号标识提取。
func extractAuthID(body []byte) string {
	for _, key := range []string{"auth_id", "auth_index", "authID", "id"} {
		if v := strings.TrimSpace(gjson.GetBytes(body, key).String()); v != "" {
			return v
		}
	}
	return ""
}

// setAccountDisabled 修改指定账号的禁用状态并落盘。
// [参数] authID: 账号标识；disabled: 目标状态。
// [返回] 错误。
// 最近修改时间：2026-10-17；改动原因：新增，账号启停状态直写物理文件。
func setAccountDisabled(authID string, disabled bool) error {
	authID = strings.TrimSpace(authID)
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return errList
	}
	for _, acc := range accounts {
		if acc.AuthID != authID && acc.AuthID != strings.TrimSuffix(authID, ".json") {
			continue
		}
		acc.Disabled = disabled
		acc.UpdatedAt = time.Now().Format(time.RFC3339)
		path := acc.PhysicalPath
		if path == "" {
			path = getAccountsDir() + "/" + authID + ".json"
		}
		return writeAuthFileDirect(path, acc)
	}
	return fmt.Errorf("账号不存在：%s", authID)
}
