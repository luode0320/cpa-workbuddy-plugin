package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var (
	managementBasePathMu sync.RWMutex
	managementBasePath   = "/v0/management"
)

// setManagementBasePath 设置管理接口基路径。
// [参数] base: 宿主下发的基路径。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, 面板管理路由。
func setManagementBasePath(base string) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return
	}
	managementBasePathMu.Lock()
	defer managementBasePathMu.Unlock()
	managementBasePath = base
}

// getManagementBasePath 读取管理接口基路径。
// [参数] 无。
// [返回] 基路径。
// 最近修改时间：2026-11-28 新增, 面板管理路由。
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

// managementRegistration 声明管理路由。
// [参数] 无。
// [返回] 路由注册信息。
// 最近修改时间：2026-11-28 新增, 面板管理路由。
func managementRegistration() managementRegistrationResponse {
	base := "/plugins/" + providerName
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: base + "/accounts", Description: "List glm accounts."},
			{Method: http.MethodGet, Path: base + "/status", Description: "List glm account status."},
			{Method: http.MethodPost, Path: base + "/import", Description: "Import chatglm_refresh_token."},
			{Method: http.MethodGet, Path: base + "/export", Description: "Export all credentials."},
			{Method: http.MethodPost, Path: base + "/delete", Description: "Delete one account."},
			{Method: http.MethodPost, Path: base + "/enable", Description: "Enable one account."},
			{Method: http.MethodPost, Path: base + "/disable", Description: "Disable one account."},
			{Method: http.MethodGet, Path: base + "/models", Description: "List available models."},
			{Method: http.MethodPost, Path: base + "/test-active", Description: "Active ping one account."},
		},
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "GLM", Description: "智谱清言 Provider 管理面板：账号管理、授权、测试。"},
			{Path: "/login", Description: "授权录入页：粘贴 chatglm_refresh_token 完成授权（宿主登录弹窗落地页）。"},
			{Path: callbackPath, Description: "授权回调页：接收 chatglm_refresh_token 并落盘（免管理密钥）。"},
		},
	}
}

type accountSummary struct {
	AuthID        string `json:"auth_id"`
	AuthIndex     string `json:"auth_index"`
	Label         string `json:"label"`
	RefreshMasked string `json:"refresh_token_masked"`
	UserID        string `json:"user_id,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	Disabled      bool   `json:"disabled"`
	TestFailed    bool   `json:"test_failed"`
}

// jsonResponse 把任意结果包装成标准的 JSON 管理响应。
// [参数] v: 要序列化的结果对象。
// [返回] 管理响应信封字节、错误。
// 最近修改时间：2026-11-28 16:25:00；改动原因：管理接口必须返回 ManagementResponse，修复返回空响应。
func jsonResponse(v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
		},
		Body: body,
	})
}

// normalizeMgmtSubPath 归一化管理请求子路径，兼容 resource 与 management 两种前缀。
// [参数] path: 请求路径。
// [返回] 子路径与 query。
// 最近修改时间：2026-11-28 14:50:00；改动原因：支持 /v0/resource/plugins/<id> 前缀。
func normalizeMgmtSubPath(path string) (string, url.Values) {
	parsed, _ := url.Parse(path)
	sub := parsed.Path
	sub = strings.TrimRight(sub, "/")

	resPrefix := "/v0/resource/plugins/" + providerName
	if strings.HasPrefix(sub, resPrefix) {
		rest := strings.TrimPrefix(sub, resPrefix)
		if rest == "" {
			return "/panel", parsed.Query()
		}
		return rest, parsed.Query()
	}

	fullPrefix := getManagementBasePath() + "/plugins/" + providerName
	shortPrefix := "/plugins/" + providerName
	switch {
	case strings.HasPrefix(sub, fullPrefix):
		sub = strings.TrimPrefix(sub, fullPrefix)
	case strings.HasPrefix(sub, shortPrefix):
		sub = strings.TrimPrefix(sub, shortPrefix)
	}
	if sub == "" {
		sub = "/"
	}
	return sub, parsed.Query()
}

// handleManagement 处理管理请求。
// [参数] raw: ManagementRequest JSON。
// [返回] 响应、错误。
// 最近修改时间：2026-11-28 新增, 管理请求分发。
func handleManagement(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	subPath, urlQuery := normalizeMgmtSubPath(req.Path)
	method := strings.ToUpper(req.Method)
	rememberPublicOrigin(req.Headers)

	if method == http.MethodGet && (subPath == "/" || subPath == "/panel") {
		return handlePanelPage(), nil
	}
	if method == http.MethodGet && subPath == "/login" {
		return handleLoginBridge(req)
	}
	if method == http.MethodPost && subPath == callbackPath {
		return handleLoginCallback(req)
	}

	_ = urlQuery
	_ = req.Query

	switch {
	case method == http.MethodGet && (subPath == "/accounts" || subPath == "/status"):
		return handleListAccounts()
	case method == http.MethodPost && subPath == "/import":
		return handleMgmtImport(req.Body)
	case (method == http.MethodGet || method == http.MethodPost) && subPath == "/export":
		return handleMgmtExport()
	case method == http.MethodPost && subPath == "/delete":
		return handleMgmtDelete(req.Body)
	case method == http.MethodPost && (subPath == "/enable" || subPath == "/disable"):
		return handleMgmtSetStatus(req.Body, subPath == "/disable")
	case method == http.MethodGet && subPath == "/models":
		return jsonResponse(map[string]any{"models": allModelInfos()})
	case method == http.MethodPost && subPath == "/test-active":
		return handleMgmtTest(req.Body)
	default:
		return jsonResponse(map[string]any{"error": "unknown management path: " + subPath})
	}
}

// handleListAccounts 列出所有账号摘要。
// [参数] 无。
// [返回] 账号列表响应。
// 最近修改时间：2026-11-28 新增, 面板账号列表。
func handleListAccounts() ([]byte, error) {
	accounts, err := listAllAuthFiles()
	if err != nil {
		return nil, err
	}
	var summaries []accountSummary
	for _, sa := range accounts {
		expires := ""
		if sa.ExpiresAt > 0 {
			expires = time.Unix(sa.ExpiresAt, 0).Format(time.RFC3339)
		}
		summaries = append(summaries, accountSummary{
			AuthID:        sa.AuthID,
			AuthIndex:     sa.AuthID,
			Label:         sa.Label,
			RefreshMasked: maskRefreshToken(sa.RefreshToken),
			UserID:        sa.UserID,
			ExpiresAt:     expires,
			Disabled:      sa.Disabled,
			TestFailed:    sa.TestFailed,
		})
	}
	return jsonResponse(map[string]any{"accounts": summaries})
}

// handleMgmtImport 导入 refresh_token 账号。
// [参数] body: 请求体。
// [返回] 导入响应。
// 最近修改时间：2026-11-28 新增, 面板 token 导入。
func handleMgmtImport(body []byte) ([]byte, error) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
		Label        string `json:"label"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	sa, err := saveImportedToken(req.RefreshToken, req.Label)
	if err != nil {
		return jsonResponse(map[string]any{"error": err.Error()})
	}
	return jsonResponse(map[string]any{"imported": sa.AuthID})
}

// handleMgmtExport 导出所有凭据。
// [参数] 无。
// [返回] 导出响应。
// 最近修改时间：2026-11-28 新增, 面板凭据导出。
func handleMgmtExport() ([]byte, error) {
	accounts, err := listAllAuthFiles()
	if err != nil {
		return nil, err
	}
	return jsonResponse(map[string]any{"accounts": accounts})
}

// handleMgmtDelete 删除账号。
// [参数] body: 请求体。
// [返回] 删除响应。
// 最近修改时间：2026-11-28 新增, 面板账号删除。
func handleMgmtDelete(body []byte) ([]byte, error) {
	var req struct {
		AuthID string `json:"auth_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	if err := removeAuthFile(req.AuthID); err != nil {
		return jsonResponse(map[string]any{"error": err.Error()})
	}
	return jsonResponse(map[string]any{"deleted": req.AuthID})
}

// handleMgmtSetStatus 设置账号启用状态。
// [参数] body: 请求体；disabled: 目标状态。
// [返回] 设置响应。
// 最近修改时间：2026-11-28 新增, 面板启停账号。
func handleMgmtSetStatus(body []byte, disabled bool) ([]byte, error) {
	var req struct {
		AuthID string `json:"auth_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	sa := findAuthByID(req.AuthID)
	if sa == nil {
		return jsonResponse(map[string]any{"error": "account not found"})
	}
	sa.Disabled = disabled
	sa.UpdatedAt = time.Now().Format(time.RFC3339)
	path := sa.PhysicalPath
	if path == "" {
		path = filepath.Join(getAccountsDir(), sa.AuthID+".json")
	}
	if err := writeAuthFileDirect(path, sa); err != nil {
		return jsonResponse(map[string]any{"error": err.Error()})
	}
	return jsonResponse(map[string]any{"auth_id": sa.AuthID, "disabled": disabled})
}

// handleMgmtTest 对指定账号做流式探活。
// [参数] body: 请求体。
// [返回] 探活响应。
// 最近修改时间：2026-11-28 新增, 面板测试按钮。
func handleMgmtTest(body []byte) ([]byte, error) {
	var req struct {
		AuthID string `json:"auth_id"`
		Model  string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	sa := findAuthByID(req.AuthID)
	if sa == nil {
		return jsonResponse(map[string]any{"ok": false, "error": "account not found"})
	}
	cfg := currentConfig()
	accessToken, err := acquireToken(sa.RefreshToken, cfg)
	if err != nil {
		return jsonResponse(map[string]any{"ok": false, "error": fmt.Sprintf("token: %v", err)})
	}
	model := req.Model
	if model == "" {
		model = defaultModels[0]
	}
	payload, err := buildStreamPayload(completionRequest{Model: model, Messages: []completionMessage{{Role: "user", Content: "hi"}}, Stream: true}, cfg, sa)
	if err != nil {
		return jsonResponse(map[string]any{"ok": false, "error": err.Error()})
	}
	httpReq, err := prepareStreamRequest(context.Background(), payload, cfg, accessToken)
	if err != nil {
		return jsonResponse(map[string]any{"ok": false, "error": err.Error()})
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return jsonResponse(map[string]any{"ok": false, "error": err.Error()})
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := readLimited(resp.Body)
		return jsonResponse(map[string]any{"ok": false, "error": fmt.Sprintf("upstream %d: %s", resp.StatusCode, string(bodyBytes))})
	}
	resetAccountFailure(sa.AuthID)
	return jsonResponse(map[string]any{"ok": true, "model": model})
}
