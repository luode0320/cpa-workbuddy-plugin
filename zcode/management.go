package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
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

func setManagementBasePath(base string) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return
	}
	managementBasePathMu.Lock()
	defer managementBasePathMu.Unlock()
	managementBasePath = base
}

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

func managementRegistration() managementRegistrationResponse {
	base := "/plugins/" + zcodeProviderID
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: base + "/accounts", Description: "List ZCode accounts."},
			{Method: http.MethodGet, Path: base + "/status", Description: "List ZCode accounts status."},
			{Method: http.MethodPost, Path: base + "/import", Description: "Import ZCode API keys."},
			{Method: http.MethodGet, Path: base + "/export", Description: "Export all ZCode credentials."},
			{Method: http.MethodPost, Path: base + "/export", Description: "Export all ZCode credentials."},
			{Method: http.MethodPost, Path: base + "/delete", Description: "Delete one ZCode account."},
			{Method: http.MethodPost, Path: base + "/enable", Description: "Enable one ZCode account."},
			{Method: http.MethodPost, Path: base + "/disable", Description: "Disable one ZCode account."},
			{Method: http.MethodPost, Path: base + "/enable-all", Description: "Enable all ZCode accounts."},
			{Method: http.MethodPost, Path: base + "/disable-all", Description: "Disable all ZCode accounts."},
			{Method: http.MethodGet, Path: base + "/models", Description: "List models available for one account."},
			{Method: http.MethodPost, Path: base + "/test-active", Description: "Send an active ping chat inference request for one account."},
			{Method: http.MethodGet, Path: base + "/credits", Description: "Query plan quota for one account or all accounts."},
			{Method: http.MethodPost, Path: base + "/claim", Description: "Claim and activate ZCode Start Plan for one account."},
			{Method: http.MethodPost, Path: base + "/activate-start", Description: "Start official OAuth authorization flow for account activation."},
			{Method: http.MethodGet, Path: base + "/activate-poll", Description: "Poll official OAuth authorization flow result and update quota."},
			{Method: http.MethodPost, Path: base + "/refresh", Description: "Start a background quota refresh round for all accounts."},
			{Method: http.MethodGet, Path: base + "/refresh/status", Description: "Read background quota refresh progress."},
		},
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "ZCode", Description: "ZCode Provider dashboard: accounts, models, import, active test."},
			{Path: "/oauth-bridge", Menu: "", Description: "ZCode OAuth authorization bridge page for CPA host login flow."},
		},
	}
}

type accountSummary struct {
	AuthID           string   `json:"auth_id"`
	AuthIndex        string   `json:"auth_index"`
	Label            string   `json:"label"`
	APIKeyMasked     string   `json:"api_key_masked"`
	UserID           string   `json:"user_id,omitempty"`
	Username         string   `json:"username,omitempty"`
	Email            string   `json:"email,omitempty"`
	AvatarURL        string   `json:"avatar_url,omitempty"`
	ExpiresAt        string   `json:"expires_at,omitempty"`
	Disabled         bool     `json:"disabled"`
	TestFailed       bool     `json:"test_failed"`
	FailoverDisabled bool     `json:"failover_disabled"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
	Models           []string `json:"models"`
	// SlowCoolingModels 列出该账号当前因「显著慢于同池同模型水平」而冷却的模型。
	// 慢速冷却按「账号×模型」生效，不是账号整体停用，因此必须是清单而非布尔值。
	SlowCoolingModels []string `json:"slow_cooling_models,omitempty"`
	// QuotaDisabled 标记该账号因额度耗尽被写盘禁用；QuotaResetAt 是预期重置时刻
	// （Unix 秒），供面板展示恢复时间。两者都由 quota_disable.go 的三标记派生。
	QuotaDisabled bool   `json:"quota_disabled,omitempty"`
	QuotaResetAt  int64  `json:"quota_reset_at,omitempty"`
	QuotaResetTxt string `json:"quota_reset_text,omitempty"`
	// ProviderFamily 是账号的授权站点归属（`zai` 国际站 / `bigmodel` 中国区），
	// 供面板卡片展示归属标识；历史账号缺省为 `zai`。
	ProviderFamily string `json:"provider_family,omitempty"`
	// Quota 是已缓存的额度快照；无缓存时为 null，由面板进入时触发的后台刷新补齐。
	Quota *zcodeQuotaSnapshot `json:"quota,omitempty"`
}

func normalizeMgmtSubPath(rawPath string) (string, url.Values) {
	u, err := url.Parse(rawPath)
	path := rawPath
	var query url.Values
	if err == nil {
		path = u.Path
		query = u.Query()
	}
	path = strings.TrimRight(path, "/")

	// 兼容 /v0/resource/plugins/zcode-provider/...
	resPrefix := "/v0/resource/plugins/" + zcodeProviderID
	if strings.HasPrefix(path, resPrefix) {
		sub := strings.TrimPrefix(path, resPrefix)
		if sub == "" {
			return "/panel", query
		}
		return sub, query
	}

	// 兼容 /v0/management/plugins/zcode-provider/... 与 /plugins/zcode-provider/...
	fullPrefix := getManagementBasePath() + "/plugins/" + zcodeProviderID
	shortPrefix := "/plugins/" + zcodeProviderID
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

	if method == http.MethodGet && subPath == "/oauth-bridge" {
		// 宿主将 URL path 与 query 分开传递：Path 不含 query，参数在 req.Query。
		// 必须直接透传 req.Query，禁止用从 path 解析出的 query 覆盖它，否则 state 会丢失。
		return okEnvelope(handleOAuthBridge(req))
	}

	getQueryParam := func(key string) string {
		if vals := req.Query[key]; len(vals) > 0 && strings.TrimSpace(vals[0]) != "" {
			return strings.TrimSpace(vals[0])
		}
		if urlQuery != nil {
			if v := strings.TrimSpace(urlQuery.Get(key)); v != "" {
				return v
			}
		}
		return ""
	}

	switch {
	case method == http.MethodGet && (subPath == "/accounts" || subPath == "/status"):
		return handleListAccounts()

	case method == http.MethodPost && subPath == "/import":
		return handleImportAccounts(req.Body)

	case (method == http.MethodPost || method == http.MethodGet) && subPath == "/export":
		return handleExportAccounts()

	case method == http.MethodPost && subPath == "/delete":
		return handleDeleteAccount(req.Body)

	case method == http.MethodPost && subPath == "/enable":
		return handleSetAccountStatus(req.Body, false)

	case method == http.MethodPost && subPath == "/disable":
		return handleSetAccountStatus(req.Body, true)

	case method == http.MethodPost && subPath == "/enable-all":
		return handleSetAllAccountStatus(false)

	case method == http.MethodPost && subPath == "/disable-all":
		return handleSetAllAccountStatus(true)

	case method == http.MethodGet && subPath == "/models":
		authID := getQueryParam("auth_index")
		if authID == "" {
			authID = getQueryParam("auth_id")
		}
		if authID == "" {
			return makeHTTPResponse(http.StatusBadRequest, []byte(`{"error":"auth_index is required"}`))
		}
		data, err := handleModelsQuery(authID)
		if err != nil {
			return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
		}
		return makeHTTPResponse(http.StatusOK, data)

	case method == http.MethodPost && subPath == "/test-active":
		data, err := handleTestActive(req.Body)
		if err != nil {
			return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
		}
		return makeHTTPResponse(http.StatusOK, data)

	case method == http.MethodGet && subPath == "/credits":
		authID := getQueryParam("auth_index")
		if authID == "" {
			authID = getQueryParam("auth_id")
		}
		return handleQuotaQuery(authID, getQueryParam("force") == "1" || getQueryParam("force") == "true")

	case method == http.MethodPost && subPath == "/claim":
		return handleClaimAccount(req.Body)

	case method == http.MethodPost && subPath == "/activate-start":
		return handleActivateStart(req.Body)

	case method == http.MethodGet && subPath == "/activate-poll":
		state := getQueryParam("state")
		return handleActivatePoll(state)

	case method == http.MethodPost && subPath == "/refresh":
		return handleRefreshStart()

	case method == http.MethodGet && subPath == "/refresh/status":
		return handleRefreshStatus()

	default:
		return makeHTTPResponse(http.StatusNotFound, []byte(`{"error":"not_found"}`))
	}
}

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

func handleListAccounts() ([]byte, error) {
	accounts, err := listAllAuthFiles()
	if err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}

	list := make([]accountSummary, 0, len(accounts))
	for _, acc := range accounts {
		models := acc.Models
		if len(models) == 0 {
			models = defaultModels
		}
		quotaDisabled, quotaResetAt := isQuotaDisabled(acc.AuthID)
		list = append(list, accountSummary{
			AuthID:           acc.AuthID,
			AuthIndex:        acc.AuthID,
			Label:            acc.Label,
			APIKeyMasked:     maskAPIKey(acc.APIKey),
			UserID:           acc.UserID,
			Username:         acc.Username,
			Email:            acc.Email,
			AvatarURL:        acc.AvatarURL,
			ExpiresAt:        acc.ExpiresAt,
			Disabled:         acc.Disabled,
			TestFailed:       acc.TestFailed,
			FailoverDisabled: isFailoverDisabled(acc.AuthID),
			// 慢速侧：只列出该号当前判慢的模型，不影响它在其它模型上的可用性。
			SlowCoolingModels: slowCoolingModelsFor(acc.AuthID, time.Now()),
			QuotaDisabled:     quotaDisabled,
			QuotaResetAt:      quotaResetAt,
			QuotaResetTxt:     quotaResetText(quotaResetAt),
			CreatedAt:         acc.CreatedAt,
			UpdatedAt:         acc.UpdatedAt,
			Models:            models,
			ProviderFamily:    providerFamilyOf(acc),
			Quota:             cachedQuota(acc.AuthID),
		})
	}

	body, _ := json.Marshal(map[string]any{
		"accounts": list,
		"count":    len(list),
	})
	return makeHTTPResponse(http.StatusOK, body)
}

func handleImportAccounts(body []byte) ([]byte, error) {
	content := gjson.GetBytes(body, "content").String()
	if content == "" {
		content = string(body)
	}

	lines := strings.Split(content, "\n")
	imported := 0
	skipped := 0
	var errs []string

	now := time.Now().Format(time.RFC3339)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		var apiKey, label string
		if strings.Contains(line, "----") {
			parts := strings.SplitN(line, "----", 2)
			apiKey = strings.TrimSpace(parts[0])
			label = strings.TrimSpace(parts[1])
		} else if strings.HasPrefix(line, "{") {
			apiKey = gjson.Get(line, "token").String()
			if apiKey == "" {
				apiKey = gjson.Get(line, "api_key").String()
			}
			if apiKey == "" {
				apiKey = gjson.Get(line, "access_token").String()
			}
			label = gjson.Get(line, "label").String()
			if label == "" {
				label = gjson.Get(line, "username").String()
			}
			if label == "" {
				label = gjson.Get(line, "email").String()
			}
		} else {
			apiKey = line
		}

		if apiKey == "" {
			skipped++
			continue
		}

		authID := authIDFor(apiKey)
		fileName := authFileNameFor(apiKey)
		fullPath := filepath.Join(getAccountsDir(), fileName)

		if label == "" {
			label = "ZCode " + maskAPIKey(apiKey)
		}

		sa := &StoredAuth{
			Type:       zcodeProviderID,
			Provider:   zcodeProviderID,
			AuthID:     authID,
			APIKey:     apiKey,
			Label:      label,
			Disabled:   false,
			CreatedAt:  now,
			UpdatedAt:  now,
			Models:     defaultModels,
			TestFailed: false,
		}

		if err := writeAuthFileDirect(fullPath, sa); err != nil {
			errs = append(errs, fmt.Sprintf("failed to save %s: %v", authID, err))
		} else {
			imported++
			globalQuotaRefresh.EnqueueOne(sa.AuthID)
		}
	}

	res := map[string]any{
		"ok":       true,
		"imported": imported,
		"skipped":  skipped,
		"errors":   errs,
	}
	resBytes, _ := json.Marshal(res)
	return makeHTTPResponse(http.StatusOK, resBytes)
}

func handleExportAccounts() ([]byte, error) {
	accounts, err := listAllAuthFiles()
	if err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}

	data, _ := json.MarshalIndent(accounts, "", "  ")
	return makeHTTPResponse(http.StatusOK, data)
}

func extractAuthIDFromBody(body []byte) string {
	id := strings.TrimSpace(gjson.GetBytes(body, "auth_id").String())
	if id == "" {
		id = strings.TrimSpace(gjson.GetBytes(body, "auth_index").String())
	}
	return id
}

func handleDeleteAccount(body []byte) ([]byte, error) {
	authID := extractAuthIDFromBody(body)
	if authID == "" {
		return makeHTTPResponse(http.StatusBadRequest, []byte(`{"error":"auth_id is required"}`))
	}

	if err := deleteAuthFileDirect(authID); err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}
	invalidateQuota(authID)

	return makeHTTPResponse(http.StatusOK, []byte(`{"ok":true}`))
}

func handleSetAccountStatus(body []byte, disabled bool) ([]byte, error) {
	authID := extractAuthIDFromBody(body)
	if authID == "" {
		return makeHTTPResponse(http.StatusBadRequest, []byte(`{"error":"auth_id is required"}`))
	}

	accounts, err := listAllAuthFiles()
	if err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}

	var found *StoredAuth
	for _, acc := range accounts {
		if acc.AuthID == authID {
			found = acc
			break
		}
	}

	if found == nil {
		return makeHTTPResponse(http.StatusNotFound, []byte(`{"error":"account not found"}`))
	}

	found.Disabled = disabled
	found.UpdatedAt = time.Now().Format(time.RFC3339)
	fullPath := filepath.Join(getAccountsDir(), authFileNameFor(found.APIKey))

	if err := writeAuthFileDirect(fullPath, found); err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}

	return makeHTTPResponse(http.StatusOK, []byte(`{"ok":true}`))
}

func handleSetAllAccountStatus(disabled bool) ([]byte, error) {
	accounts, err := listAllAuthFiles()
	if err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}

	now := time.Now().Format(time.RFC3339)
	count := 0
	for _, acc := range accounts {
		acc.Disabled = disabled
		acc.UpdatedAt = now
		fullPath := filepath.Join(getAccountsDir(), authFileNameFor(acc.APIKey))
		if err := writeAuthFileDirect(fullPath, acc); err == nil {
			count++
		}
	}

	return makeHTTPResponse(http.StatusOK, []byte(fmt.Sprintf(`{"ok":true,"updated":%d}`, count)))
}

// handleQuotaQuery 查询单个账号或全部账号的额度。
//
// [参数] authID: 账号标识；为空时查询全部账号；force: true 时跳过缓存强制刷新。
// [返回] {accounts:[{auth_index, quota|error}]} 或单账号对象。
func handleQuotaQuery(authID string, force bool) ([]byte, error) {
	authID = strings.TrimSpace(authID)
	accounts, err := listAllAuthFiles()
	if err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}

	// 单账号模式：面板单卡刷新使用，返回完整额度或错误。
	if authID != "" {
		for _, acc := range accounts {
			if acc.AuthID != authID {
				continue
			}
			snap, errQuota := quotaForAccount(acc, force)
			if errQuota != nil {
				body, _ := json.Marshal(map[string]any{
					"accounts": []map[string]any{{"auth_index": authID, "error": errQuota.Error()}},
				})
				return makeHTTPResponse(http.StatusOK, body)
			}
			body, _ := json.Marshal(map[string]any{
				"accounts": []map[string]any{{"auth_index": authID, "quota": snap}},
			})
			return makeHTTPResponse(http.StatusOK, body)
		}
		return makeHTTPResponse(http.StatusNotFound, []byte(`{"error":"account not found"}`))
	}

	// 全量模式：逐账号返回缓存或实时额度，单个失败不影响整体。
	out := make([]map[string]any, 0, len(accounts))
	for _, acc := range accounts {
		snap, errQuota := quotaForAccount(acc, force)
		if errQuota != nil {
			out = append(out, map[string]any{"auth_index": acc.AuthID, "error": errQuota.Error()})
			continue
		}
		out = append(out, map[string]any{"auth_index": acc.AuthID, "quota": snap})
	}
	body, _ := json.Marshal(map[string]any{"accounts": out})
	return makeHTTPResponse(http.StatusOK, body)
}

// handleRefreshStart 启动一轮全量额度后台刷新。
//
// [参数] 无。
// [返回] {queued, total, status}；已有刷新在跑时 queued=false 并返回当前进度。
func handleRefreshStart() ([]byte, error) {
	accounts, err := listAllAuthFiles()
	if err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}
	targets := make([]refreshTarget, 0, len(accounts))
	for _, acc := range accounts {
		targets = append(targets, refreshTarget{AuthID: acc.AuthID, Auth: acc})
	}
	queued := globalQuotaRefresh.EnqueueAll(targets)
	body, _ := json.Marshal(map[string]any{
		"ok":     true,
		"queued": queued >= 0,
		"total":  len(targets),
		"status": globalQuotaRefresh.Snapshot(),
	})
	return makeHTTPResponse(http.StatusOK, body)
}

// handleRefreshStatus 读取后台额度刷新进度。
//
// [参数] 无。
// [返回] refreshSnapshot 视图。
func handleRefreshStatus() ([]byte, error) {
	body, _ := json.Marshal(globalQuotaRefresh.Snapshot())
	return makeHTTPResponse(http.StatusOK, body)
}

// friendlyClaimError 把上游 claim 业务层错误翻译为面向用户的中文提示。
//
// 上游在「当前无可领取计划」时返回英文 `no permission`；这类属于上游活动态而非
// 插件缺陷，直接抛英文原文会让用户误判为操作失败。此处按已知语义映射为可读提示，
// 未知错误保留原文以便排障。
// [参数] errMsg: claimAccountStartPlan 返回的原始错误信息。
// [返回] 面向用户的中文提示。
// 最近修改时间：2026-10-15；改动原因：新增，把 no permission 等上游英文错误翻译为友好提示。
func friendlyClaimError(errMsg string) string {
	lower := strings.ToLower(errMsg)
	switch {
	case strings.Contains(lower, "no permission"):
		return "当前上游未开放 Start Plan 领取（活动暂不可领取），请稍后重试或在 ZCode 客户端内领取"
	case strings.Contains(lower, "1003"), strings.Contains(errMsg, "已领取"), strings.Contains(lower, "already exists"):
		return "该账号已领取 Start Plan，无需重复领取"
	case strings.Contains(lower, "1001"), strings.Contains(lower, "target not found"):
		return "上游未找到可领取的 Start Plan（批次可能已变更），请稍后重试"
	default:
		return errMsg
	}
}

// handleClaimAccount 处理 POST /claim 激活账号 Start Plan 请求。
//
// [参数] body: 包含 auth_index / auth_id，以及可选的 captcha_verify_param、captcha_region。
// [返回] 激活成功后的额度快照；若上游要求人机验证，返回结构化 captcha_required 配置。
func handleClaimAccount(body []byte) ([]byte, error) {
	authID := extractAuthIDFromBody(body)
	if authID == "" {
		return makeHTTPResponse(http.StatusBadRequest, []byte(`{"error":"auth_id is required"}`))
	}

	accounts, err := listAllAuthFiles()
	if err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}

	var target *StoredAuth
	for _, acc := range accounts {
		if acc.AuthID == authID {
			target = acc
			break
		}
	}
	if target == nil {
		return makeHTTPResponse(http.StatusNotFound, []byte(`{"error":"account not found"}`))
	}

	verifyParam := gjson.GetBytes(body, "captcha_verify_param").String()
	region := gjson.GetBytes(body, "captcha_region").String()
	if region == "" {
		region = defaultAliyunRegion
	}

	snap, errClaim := claimAccountStartPlan(target, verifyParam, region)
	if errClaim != nil {
		errMsg := errClaim.Error()
		if strings.Contains(errMsg, "captcha") || strings.Contains(errMsg, "3007") {
			bodyResp, _ := json.Marshal(map[string]any{
				"ok":               false,
				"captcha_required": true,
				"scene_id":         defaultAliyunSceneID,
				"prefix":           defaultAliyunPrefix,
				"region":           defaultAliyunRegion,
				"error":            "需要完成安全验证以激活套餐",
			})
			return makeHTTPResponse(http.StatusOK, bodyResp)
		}
		// 上游业务层错误按语义翻译为中文提示，避免把英文原文直接抛给用户。
		return makeHTTPResponse(http.StatusBadRequest, []byte(fmt.Sprintf(`{"error":%q}`, friendlyClaimError(errMsg))))
	}

	// 激活成功后，若套餐中含有可用模型，尝试动态更新至该账号的 Models 字段中
	if snap != nil && len(snap.Entitlements) > 0 {
		var activeModels []string
		mSeen := make(map[string]struct{})
		for _, ent := range snap.Entitlements {
			name := strings.TrimSpace(ent.ShowName)
			if name == "" {
				name = strings.TrimPrefix(ent.Capability, "model:")
			}
			name = normalizeOfficialGLMModelID(name)
			if name != "" {
				if _, ok := mSeen[name]; !ok {
					mSeen[name] = struct{}{}
					activeModels = append(activeModels, name)
				}
			}
		}
		if len(activeModels) > 0 {
			target.Models = activeModels
			target.UpdatedAt = time.Now().Format(time.RFC3339)
			fullPath := filepath.Join(getAccountsDir(), authFileNameFor(target.APIKey))
			_ = writeAuthFileDirect(fullPath, target)
		}
	}

	bodyResp, _ := json.Marshal(map[string]any{
		"ok":      true,
		"message": "Start Plan 激活成功",
		"quota":   snap,
	})
	return makeHTTPResponse(http.StatusOK, bodyResp)
}

// handleActivateStart 处理 POST /activate-start 发起账号官方 OAuth 授权登录。
// [参数] body: 可包含 auth_id（指定存量账号就地更新凭据）与 provider（`zai` / `bigmodel`
// 站点归属，缺省 `bigmodel` 中国区），为空时代表新账号授权登录。
// [返回] 授权页面 URL、state 状态码、flow_id 与生效的 provider；provider 非法时返回 400。
// 最近修改时间：2026-10-14；改动原因：默认站点改为中国区 bigmodel，注释同步。
func handleActivateStart(body []byte) ([]byte, error) {
	authID := extractAuthIDFromBody(body)
	provider, errProvider := resolveOAuthProvider(gjson.GetBytes(body, "provider").String())
	if errProvider != nil {
		return makeHTTPResponse(http.StatusBadRequest, []byte(fmt.Sprintf(`{"ok":false,"error":%q}`, errProvider.Error())))
	}
	var label string
	if authID != "" {
		accounts, _ := listAllAuthFiles()
		for _, acc := range accounts {
			if acc.AuthID == authID {
				label = acc.Label
				break
			}
		}
	}

	initResp, state, err := startAccountOAuth(authID, label, provider)
	if err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"ok":false,"error":%q}`, err.Error())))
	}

	res := map[string]any{
		"ok":            true,
		"state":         state,
		"flow_id":       initResp.Data.FlowID,
		"authorize_url": initResp.Data.AuthorizeURL,
		"auth_id":       authID,
		"provider":      provider,
	}
	resBytes, _ := json.Marshal(res)
	return makeHTTPResponse(http.StatusOK, resBytes)
}

// handleActivatePoll 处理 GET /activate-poll 轮询官方 OAuth 授权登录结果。
// [参数] state: 授权会话状态标识。
// [返回] status (pending/success/error)，若授权成功并拉取到额度，附带 quota 快照（无套餐时为 null）。
// 最近修改时间：2026-10-14；改动原因：纠正「激活」措辞——本链路只完成授权登录，额度需官方客户端内领取。
func handleActivatePoll(state string) ([]byte, error) {
	state = strings.TrimSpace(state)
	if state == "" {
		return makeHTTPResponse(http.StatusBadRequest, []byte(`{"error":"state is required"}`))
	}

	rawSession, ok := oauthSessions.Load(state)
	if !ok {
		return makeHTTPResponse(http.StatusOK, []byte(`{"status":"error","message":"授权会话不存在或已过期"}`))
	}
	session, ok := rawSession.(*oauthLoginSession)
	if !ok || session == nil {
		return makeHTTPResponse(http.StatusOK, []byte(`{"status":"error","message":"授权会话数据异常"}`))
	}

	if time.Since(session.CreatedAt) > oauthTTL {
		oauthSessions.Delete(state)
		return makeHTTPResponse(http.StatusOK, []byte(`{"status":"error","message":"授权会话已超时（15分钟），请重新发起"}`))
	}

	if session.Completed {
		if session.Error != "" {
			return makeHTTPResponse(http.StatusOK, []byte(fmt.Sprintf(`{"status":"error","message":%q}`, session.Error)))
		}
		var quotaSnap *zcodeQuotaSnapshot
		var authID string
		if session.Auth != nil {
			authID = session.Auth.ID
			quotaSnap = cachedQuota(authID)
		}
		res := map[string]any{
			"status":  "success",
			"message": "ZCode 账号授权登录成功！",
			"auth_id": authID,
			"quota":   quotaSnap,
		}
		resBytes, _ := json.Marshal(res)
		return makeHTTPResponse(http.StatusOK, resBytes)
	}

	pollResp, errPoll := zaiCLIPoll(session.FlowID, session.PollToken)
	if errPoll != nil {
		return makeHTTPResponse(http.StatusOK, []byte(`{"status":"pending","message":"等待官方授权结果（网络重试中）..."}`))
	}

	switch strings.TrimSpace(pollResp.Data.Status) {
	case "pending":
		return makeHTTPResponse(http.StatusOK, []byte(`{"status":"pending","message":"等待在 Z.ai 页面完成授权登录..."}`))
	case "failed":
		session.Completed = true
		session.Error = "OAuth 授权失败或已取消"
		oauthSessions.Delete(state)
		return makeHTTPResponse(http.StatusOK, []byte(`{"status":"error","message":"OAuth 授权失败或已取消"}`))
	case "ready":
		authData, errSettle := settleZaiPollResult(pollResp, session.TargetLabel, session.TargetAuthID, session.Provider)
		if errSettle != nil {
			session.Completed = true
			session.Error = errSettle.Error()
			oauthSessions.Delete(state)
			return makeHTTPResponse(http.StatusOK, []byte(fmt.Sprintf(`{"status":"error","message":%q}`, errSettle.Error())))
		}
		session.Auth = authData
		session.Completed = true
		oauthSessions.Delete(state)

		// 授权成功后，立即拉取该账号最新生效额度
		var quotaSnap *zcodeQuotaSnapshot
		accounts, _ := listAllAuthFiles()
		for _, acc := range accounts {
			if acc.AuthID == authData.ID {
				if snap, errQ := quotaForAccount(acc, true); errQ == nil {
					quotaSnap = snap
				}
				break
			}
		}

		res := map[string]any{
			"status":  "success",
			"message": "ZCode 账号授权登录成功！",
			"auth_id": authData.ID,
			"quota":   quotaSnap,
		}
		resBytes, _ := json.Marshal(res)
		return makeHTTPResponse(http.StatusOK, resBytes)
	default:
		return makeHTTPResponse(http.StatusOK, []byte(`{"status":"pending","message":"等待在 Z.ai 页面完成授权登录..."}`))
	}
}
