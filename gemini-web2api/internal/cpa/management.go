package cpa

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/app"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/auth"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/chromeauth"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/logging"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginapi"
)

type rpcManagementRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcManagementRegistrationResponse struct {
	Routes    []pluginapi.ManagementRoute `json:"routes,omitempty"`
	Resources []pluginapi.ResourceRoute   `json:"resources,omitempty"`
}

func (p *Plugin) handleCLIRegister() (any, error) {
	return pluginapi.CommandLineRegistrationResponse{
		Flags: []pluginapi.CommandLineFlag{
			{
				Name:         "gemini-web2api-login",
				Type:         "bool",
				DefaultValue: "false",
				Usage:        "Launch local Chrome Incognito window to sign in to Google Gemini Web and import account",
			},
			{
				Name:         "gemini-web2api-status",
				Type:         "bool",
				DefaultValue: "false",
				Usage:        "Print Gemini-Web2API plugin account pool and runtime status",
			},
		},
	}, nil
}

func (p *Plugin) handleCLIRun(raw []byte) (any, error) {
	var req pluginapi.CommandLineExecutionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	rt, err := p.Runtime()
	if err != nil {
		return pluginapi.CommandLineExecutionResponse{
			ExitCode: 1,
			Stderr:   []byte("Failed to initialize Gemini-Web2API runtime: " + err.Error() + "\n"),
		}, nil
	}

	if flag, ok := req.TriggeredFlags["gemini-web2api-login"]; ok && flag.Set && strings.EqualFold(flag.Value, "true") {
		res, err := chromeauth.RunIncognitoLogin(p.ctx, chromeauth.IsolatedLoginRequest{
			ProxyURL: p.appCfg.ProxyURL,
			Timeout:  5 * time.Minute,
		})
		if err != nil {
			return pluginapi.CommandLineExecutionResponse{
				ExitCode: 1,
				Stderr:   []byte("Chrome Incognito login failed: " + err.Error() + "\n"),
			}, nil
		}
		stateBytes, _ := json.MarshalIndent(res.StorageState, "", "  ")
		importedID, err := rt.Accounts.ImportState(stateBytes)
		if err != nil {
			return pluginapi.CommandLineExecutionResponse{
				ExitCode: 1,
				Stderr:   []byte("Failed to import logged-in account: " + err.Error() + "\n"),
			}, nil
		}
		_ = p.SyncAccountIDToHostAuth("", importedID)
		out, _ := json.MarshalIndent(map[string]any{
			"account_id": importedID,
			"email":      res.Email,
		}, "", "  ")
		return pluginapi.CommandLineExecutionResponse{
			ExitCode: 0,
			Stdout:   []byte("Successfully logged in and imported Gemini Web account:\n" + string(out) + "\n"),
		}, nil
	}

	if flag, ok := req.TriggeredFlags["gemini-web2api-status"]; ok && flag.Set && strings.EqualFold(flag.Value, "true") {
		poolStatus := rt.Pool.Status()
		out, _ := json.MarshalIndent(poolStatus, "", "  ")
		return pluginapi.CommandLineExecutionResponse{
			ExitCode: 0,
			Stdout:   []byte(string(out) + "\n"),
		}, nil
	}

	return pluginapi.CommandLineExecutionResponse{ExitCode: 0}, nil
}

// handleManagementRegister 返回本插件在宿主控制台要注册的管理路由与资源路由。
//
// 资源路由里只有 /panel 挂 Menu（控制台左侧插件树的一个入口），
// 其余 /login 系列照旧注册但不挂 Menu，仅供桥接页与浏览器扩展免密访问。
//
// [参数] 无。
// [返回] rpcManagementRegistrationResponse，含 16 条管理路由与 5 条资源路由。
// [返回] error，始终为 nil，保留签名以对齐宿主 management.register 分发。
// 最近修改时间：2026-11-21；改动原因：面板收口为单入口 /panel，Menu 名改为 Gemini Web。
func (p *Plugin) handleManagementRegister() (any, error) {
	return rpcManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: "/v0/management/gemini-web2api/status", Description: "Get Gemini-Web2API runtime status, accounts, models, guest session, and cooldowns"},
			{Method: http.MethodPost, Path: "/v0/management/gemini-web2api/service/start", Description: "Start Gemini-Web2API generation service"},
			{Method: http.MethodPost, Path: "/v0/management/gemini-web2api/service/stop", Description: "Stop Gemini-Web2API generation service"},
			{Method: http.MethodPost, Path: "/v0/management/gemini-web2api/guest/relaunch", Description: "Start or relaunch anonymous Gemini Web guest session"},
			{Method: http.MethodPost, Path: "/v0/management/gemini-web2api/guest/stop", Description: "Stop anonymous Gemini Web guest session"},
			{Method: http.MethodGet, Path: "/v0/management/gemini-web2api/accounts", Description: "List Gemini-Web2API accounts"},
			{Method: http.MethodPost, Path: "/v0/management/gemini-web2api/accounts", Description: "Import Gemini-Web2API account from Cookie header, Netscape cookies.txt, or StorageState JSON"},
			{Method: http.MethodPost, Path: "/v0/management/gemini-web2api/accounts/login-browser", Description: "Launch local Chrome Incognito window to sign in to Google Gemini Web and auto-import account"},
			{Method: http.MethodPost, Path: "/v0/management/gemini-web2api/accounts/relogin", Description: "Re-authenticate an existing Gemini-Web2API account via local Chrome Incognito window"},
			{Method: http.MethodPost, Path: "/v0/management/gemini-web2api/accounts/refresh", Description: "Verify and refresh an account's model catalog and usage quota"},
			{Method: http.MethodPost, Path: "/v0/management/gemini-web2api/accounts/update", Description: "Update account settings (enabled, proxy)"},
			{Method: http.MethodPost, Path: "/v0/management/gemini-web2api/accounts/delete", Description: "Delete an account"},
			{Method: http.MethodPost, Path: "/v0/management/gemini-web2api/accounts/sync-auth", Description: "Sync all Gemini-Web2API accounts to CLIProxyAPI auths/ directory"},
			{Method: http.MethodPost, Path: "/v0/management/gemini-web2api/cooldowns/clear", Description: "Clear account cooldowns"},
			{Method: http.MethodGet, Path: "/v0/management/gemini-web2api/models", Description: "List discovered Gemini Web models"},
			{Method: http.MethodGet, Path: "/v0/management/gemini-web2api/logs", Description: "List recent Gemini-Web2API logs"},
		},
		Resources: []pluginapi.ResourceRoute{
			// 控制台只挂一条 Menu（路径统一 /panel，名称用简短产品名），
			// 与 coze / doubao / workbuddy 等兄弟插件保持一致；
			// 多挂一条会让左侧插件树出现两个同名子项。
			{
				Path:        panelResourcePath,
				Menu:        consoleMenuName,
				Description: "Gemini Web 账户池、登录授权、访客会话与冷却管理",
			},
			// 下面四条不挂 Menu：不进控制台菜单，但必须注册，否则宿主
			// ServeResourceHTTP 查不到路由会直接 404，桥接页与扩展的回传会全部失效。
			{
				Path:        "/login",
				Description: "一键授权桥接页：配对码 + 浏览器扩展回传 Google 会话 Cookie",
			},
			{
				Path:        "/login/extension.zip",
				Description: "下载浏览器授权助手扩展（解压后用开发者模式加载）",
			},
			{
				Path:        "/login/submit",
				Description: "接收浏览器扩展或手工粘贴推送的 Google 会话 Cookie（GET + query）",
			},
			{
				Path:        "/login/poll",
				Description: "桥接页轮询一键授权的导入进度",
			},
		},
	}, nil
}

type adminAccountView struct {
	ID            string                  `json:"id"`
	Email         string                  `json:"email"`
	State         string                  `json:"state"`
	Enabled       bool                    `json:"enabled"`
	Source        string                  `json:"source,omitempty"`
	Profile       string                  `json:"profile,omitempty"`
	Proxy         string                  `json:"proxy,omitempty"`
	ProxySource   string                  `json:"proxy_source,omitempty"`
	BenefitTier   string                  `json:"benefit_tier,omitempty"`
	ContextWindow int                     `json:"context_window,omitempty"`
	Models        []string                `json:"models"`
	Pending       int                     `json:"pending"`
	Message       string                  `json:"message,omitempty"`
	Cooldowns     []adminCooldownItemView `json:"cooldowns,omitempty"`
}

type adminCooldownItemView struct {
	AccountID    string    `json:"account_id"`
	AccountLabel string    `json:"account_label"`
	Channel      string    `json:"channel"`
	ModelID      string    `json:"model_id"`
	Scope        string    `json:"scope"`
	Until        time.Time `json:"until"`
	Reason       string    `json:"reason"`
}

// handleManagementHandle 分发宿主转来的管理 API 与资源路由请求。
//
// 分发顺序固定为：登录面（桥接页 / 扩展 / 回传 / 轮询）→ 静态面板 HTML →
// 管理 API。登录面必须先判，否则资源前缀会把 /login 系列一起吞成面板 HTML。
//
// [参数] raw 是宿主透传的 rpcManagementRequest JSON，含 method / path / headers / query / body。
// [返回] pluginapi.ManagementResponse 或业务 payload；分发失败时返回错误。
// 最近修改时间：2026-11-21；改动原因：静态面板分发新增 /panel 入口并保留历史路径兼容。
func (p *Plugin) handleManagementHandle(raw []byte) (any, error) {
	var req rpcManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	rawPath := strings.TrimSpace(req.Path)
	path := rawPath
	if idx := strings.IndexByte(rawPath, '?'); idx >= 0 {
		path = rawPath[:idx]
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}

	// 登录面（桥接页 / 扩展下载 / Cookie 回传 / 轮询）必须先于静态面板分发，
	// 否则 /v0/resource/plugins/gemini-web2api 前缀会把它们一起吞成面板 HTML。
	if action := p.handleLoginResource(req, path); action.Handled {
		return action.Response, nil
	}

	// Serve static Web Resource UI only
	// /panel 是控制台入口（与兄弟插件统一），/accounts 与 /webui 作为历史路径
	// 保留兼容，剩下的 /v0/resource/plugins/gemini-web2api 前缀兜底交给面板 HTML。
	if path == panelResourcePath || path == legacyPanelResourcePath || path == "/webui" || path == "/" || strings.HasPrefix(path, loginResourcePrefix) {
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":  []string{"text/html; charset=utf-8"},
				"Cache-Control": []string{"no-store"},
			},
			Body: []byte(webUIDashboardHTML),
		}, nil
	}
	if strings.HasPrefix(path, "/gemini-web2api/") {
		path = "/v0/management" + path
	}

	// Allow runtime access even when serviceStopped is true for management routes
	p.mu.RLock()
	rt := p.runtime
	p.mu.RUnlock()
	if rt == nil {
		var err error
		wasStopped := p.serviceStopped.Swap(false)
		rt, err = p.Runtime()
		if wasStopped {
			p.serviceStopped.Store(true)
		}
		if err != nil {
			return jsonManagementResponse(http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			}), nil
		}
	}

	switch {
	case method == http.MethodGet && path == "/v0/management/gemini-web2api/status":
		return p.buildStatusManagementResponse(rt), nil

	case method == http.MethodPost && path == "/v0/management/gemini-web2api/service/start":
		p.serviceStopped.Store(false)
		go rt.StartBackground(boolOrDefault(p.Config().GuestEnabled, true))
		if boolOrDefault(p.Config().SyncAuthFiles, true) {
			p.setHostAuthFilesDisabled(rt, false)
		}
		return p.buildStatusManagementResponse(rt), nil

	case method == http.MethodPost && path == "/v0/management/gemini-web2api/service/stop":
		p.serviceStopped.Store(true)
		if boolOrDefault(p.Config().SyncAuthFiles, true) {
			p.setHostAuthFilesDisabled(rt, true)
		}
		return p.buildStatusManagementResponse(rt), nil


	case method == http.MethodPost && path == "/v0/management/gemini-web2api/guest/relaunch":
		rt.Accounts.RelaunchGuest()
		return p.buildStatusManagementResponse(rt), nil

	case method == http.MethodPost && path == "/v0/management/gemini-web2api/guest/stop":
		rt.Accounts.StopGuest()
		return p.buildStatusManagementResponse(rt), nil
	case method == http.MethodGet && path == "/v0/management/gemini-web2api/accounts":
		views, _ := p.buildAccountsAndCooldowns(rt)
		return jsonManagementResponse(http.StatusOK, map[string]any{"accounts": views}), nil

	case method == http.MethodPost && path == "/v0/management/gemini-web2api/accounts/login-browser":
		if !p.chromeLogin.TryLock() {
			return jsonManagementResponse(http.StatusConflict, map[string]any{"error": "已有一个本机 Chrome 无痕登录窗口正在进行中，请在该窗口完成登录或关闭它后重试"}), nil
		}
		defer p.chromeLogin.Unlock()
		var body struct {
			Proxy     string `json:"proxy"`
			AccountID string `json:"account_id"`
		}
		if len(req.Body) > 0 {
			_ = json.Unmarshal(req.Body, &body)
		}
		proxy := strings.TrimSpace(body.Proxy)
		if proxy == "" {
			proxy = p.appCfg.ProxyURL
		}
		res, err := chromeauth.RunIncognitoLogin(p.ctx, chromeauth.IsolatedLoginRequest{
			ProxyURL:  proxy,
			AccountID: strings.TrimSpace(body.AccountID),
			Timeout:   5 * time.Minute,
		})
		if err != nil {
			return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": err.Error()}), nil
		}
		stateBytes, err := json.MarshalIndent(res.StorageState, "", "  ")
		if err != nil {
			return jsonManagementResponse(http.StatusInternalServerError, map[string]any{"error": err.Error()}), nil
		}
		importedID, err := rt.Accounts.ImportState(stateBytes)
		if err != nil {
			return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": err.Error()}), nil
		}
		p.serviceStopped.Store(false)
		_ = p.SyncAccountIDToHostAuth(req.HostCallbackID, importedID)
		return jsonManagementResponse(http.StatusCreated, map[string]any{
			"account": map[string]any{
				"id":    importedID,
				"email": res.Email,
			},
		}), nil

	case method == http.MethodPost && path == "/v0/management/gemini-web2api/accounts/relogin":
		if !p.chromeLogin.TryLock() {
			return jsonManagementResponse(http.StatusConflict, map[string]any{"error": "已有一个本机 Chrome 无痕登录窗口正在进行中，请在该窗口完成登录或关闭它后重试"}), nil
		}
		defer p.chromeLogin.Unlock()
		var body struct {
			AccountID string `json:"account_id"`
			Proxy     string `json:"proxy"`
		}
		if len(req.Body) > 0 {
			_ = json.Unmarshal(req.Body, &body)
		}
		proxy := strings.TrimSpace(body.Proxy)
		if proxy == "" {
			proxy = p.appCfg.ProxyURL
		}
		res, err := chromeauth.RunIncognitoLogin(p.ctx, chromeauth.IsolatedLoginRequest{
			ProxyURL:  proxy,
			AccountID: strings.TrimSpace(body.AccountID),
			Timeout:   5 * time.Minute,
		})
		if err != nil {
			return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": err.Error()}), nil
		}
		stateBytes, err := json.MarshalIndent(res.StorageState, "", "  ")
		if err != nil {
			return jsonManagementResponse(http.StatusInternalServerError, map[string]any{"error": err.Error()}), nil
		}
		importedID, err := rt.Accounts.ImportState(stateBytes)
		if err != nil {
			return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": err.Error()}), nil
		}
		p.serviceStopped.Store(false)
		_ = p.SyncAccountIDToHostAuth(req.HostCallbackID, importedID)
		return jsonManagementResponse(http.StatusOK, map[string]any{
			"account": map[string]any{
				"id":    importedID,
				"email": res.Email,
			},
		}), nil

	case method == http.MethodPost && path == "/v0/management/gemini-web2api/accounts":
		var body struct {
			Label        string             `json:"label"`
			Email        string             `json:"email"`
			ID           string             `json:"id"`
			AuthUser     int                `json:"auth_user"`
			Proxy        string             `json:"proxy"`
			Cookies      string             `json:"cookies"`
			StorageState *auth.StorageState `json:"storage_state"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": "invalid JSON body: " + err.Error()}), nil
		}
		accID := strings.TrimSpace(body.ID)
		if accID == "" {
			accID = strings.TrimSpace(body.Label)
		}
		if accID == "" {
			accID = strings.TrimSpace(body.Email)
		}

		var importedID string
		var err error
		if body.StorageState != nil && len(body.StorageState.Cookies) > 0 {
			st := *body.StorageState
			if st.Metadata.ID == "" {
				st.Metadata.ID = accID
			}
			if st.Metadata.Proxy == "" && strings.TrimSpace(body.Proxy) != "" {
				st.Metadata.Proxy = strings.TrimSpace(body.Proxy)
			}
			rawState, _ := json.MarshalIndent(st, "", "  ")
			importedID, err = rt.Accounts.ImportState(rawState)
		} else if strings.TrimSpace(body.Cookies) != "" {
			importedID, err = rt.Accounts.ImportCookie(p.ctx, body.Cookies, accID, strings.TrimSpace(body.Proxy), body.AuthUser)
		} else {
			return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": "cookies or storage_state is required"}), nil
		}
		if err != nil {
			return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": err.Error()}), nil
		}
		p.serviceStopped.Store(false)
		_ = p.SyncAccountIDToHostAuth(req.HostCallbackID, importedID)
		return jsonManagementResponse(http.StatusCreated, map[string]any{
			"account": map[string]any{"id": importedID},
		}), nil

	case method == http.MethodPost && path == "/v0/management/gemini-web2api/accounts/refresh":
		var body struct {
			AccountID string `json:"account_id"`
		}
		_ = json.Unmarshal(req.Body, &body)
		accID := strings.TrimSpace(body.AccountID)
		if accID == "" {
			return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": "account_id is required"}), nil
		}
		if err := rt.Accounts.Verify(accID); err != nil {
			return jsonManagementResponse(http.StatusBadGateway, map[string]any{"error": err.Error()}), nil
		}
		_ = p.SyncAccountIDToHostAuth(req.HostCallbackID, accID)
		models := p.collectCatalogModelsForAccount(accID)
		return jsonManagementResponse(http.StatusOK, map[string]any{"models_count": len(models), "models": models}), nil

	case method == http.MethodPost && path == "/v0/management/gemini-web2api/accounts/update":
		var body struct {
			AccountID string  `json:"account_id"`
			Enabled   *bool   `json:"enabled"`
			Proxy     *string `json:"proxy"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": err.Error()}), nil
		}
		accID := strings.TrimSpace(body.AccountID)
		if accID == "" {
			return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": "account_id is required"}), nil
		}
		if body.Proxy != nil {
			if err := rt.Accounts.SetProxy(accID, strings.TrimSpace(*body.Proxy)); err != nil {
				return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": err.Error()}), nil
			}
		}
		if body.Enabled != nil {
			if err := rt.Accounts.SetEnabled(accID, *body.Enabled); err != nil {
				return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": err.Error()}), nil
			}
		}
		_ = p.SyncAccountIDToHostAuth(req.HostCallbackID, accID)
		return jsonManagementResponse(http.StatusOK, map[string]any{"account": map[string]any{"id": accID}}), nil

	case method == http.MethodPost && path == "/v0/management/gemini-web2api/accounts/delete":
		var body struct {
			AccountID string `json:"account_id"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": err.Error()}), nil
		}
		accID := strings.TrimSpace(body.AccountID)
		if err := rt.Accounts.Remove(accID); err != nil {
			return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": err.Error()}), nil
		}
		p.RemoveAccountFromHostAuth(accID)
		return jsonManagementResponse(http.StatusOK, map[string]any{"deleted": accID}), nil

	case method == http.MethodPost && path == "/v0/management/gemini-web2api/accounts/sync-auth":
		poolStatus := rt.Pool.Status()
		synced := 0
		var syncErrors []string
		for _, acc := range poolStatus.Accounts {
			if err := p.SyncAccountIDToHostAuth(req.HostCallbackID, acc.AccountID); err != nil {
				syncErrors = append(syncErrors, fmt.Sprintf("%s: %v", acc.AccountID, err))
			} else {
				synced++
			}
		}
		return jsonManagementResponse(http.StatusOK, map[string]any{
			"synced": synced,
			"errors": syncErrors,
		}), nil

	case method == http.MethodPost && path == "/v0/management/gemini-web2api/cooldowns/clear":
		var body struct {
			AccountID string `json:"account_id"`
			Scope     string `json:"scope"`
		}
		_ = json.Unmarshal(req.Body, &body)
		accountID := strings.TrimSpace(body.AccountID)
		scope := strings.TrimSpace(body.Scope)
		if accountID == "" {
			return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": "请指定要清除冷却的账户 ID (account_id)"}), nil
		}
		if scope == "*" {
			scope = ""
		}
		if scope != "" {
			if err := rt.Pool.ClearCooldown(accountID, scope); err != nil {
				return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": err.Error()}), nil
			}
		} else {
			_ = rt.Pool.ClearCooldown(accountID, "")
			if st := findPoolAccountStatus(rt, accountID); st != nil {
				for _, mc := range st.ModelCooldowns {
					_ = rt.Pool.ClearCooldown(accountID, mc.Model)
				}
			}
		}
		p.notifyHostCooldownReset(req.HostCallbackID, "", accountID, scope)
		return jsonManagementResponse(http.StatusOK, map[string]any{"cleared": true, "account_id": accountID}), nil

	case method == http.MethodGet && path == "/v0/management/gemini-web2api/models":
		models := p.collectCatalogModelsForAccount("")
		return jsonManagementResponse(http.StatusOK, map[string]any{"models": models}), nil

	case method == http.MethodGet && path == "/v0/management/gemini-web2api/logs":
		logs, _, cancel := logging.Subscribe(500)
		cancel()
		return jsonManagementResponse(http.StatusOK, map[string]any{"logs": logs}), nil
	default:
		return jsonManagementResponse(http.StatusNotFound, map[string]any{
			"error": fmt.Sprintf("route %s %s not found", method, path),
		}), nil
	}
}

func redactProxyURLString(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.User == nil {
		return trimmed
	}
	return parsed.Redacted()
}

func (p *Plugin) buildAccountsAndCooldowns(rt *app.PluginRuntime) ([]adminAccountView, []adminCooldownItemView) {
	poolStatus := rt.Pool.Status()
	views := make([]adminAccountView, 0, len(poolStatus.Accounts))
	var allCooldowns []adminCooldownItemView

	for _, acc := range poolStatus.Accounts {
		email := acc.AccountID
		if client := rt.Pool.Client(acc.AccountID); client != nil && client.Email() != "" {
			email = client.Email()
		}
		var accCooldowns []adminCooldownItemView
		if acc.CooldownUntil != nil && !acc.CooldownUntil.IsZero() {
			cd := adminCooldownItemView{
				AccountID:    acc.AccountID,
				AccountLabel: email,
				Channel:      "all",
				ModelID:      "*",
				Scope:        "*",
				Until:        *acc.CooldownUntil,
				Reason:       acc.CooldownReason,
			}
			accCooldowns = append(accCooldowns, cd)
			allCooldowns = append(allCooldowns, cd)
		}
		for _, mc := range acc.ModelCooldowns {
			cd := adminCooldownItemView{
				AccountID:    acc.AccountID,
				AccountLabel: email,
				Channel:      "web",
				ModelID:      mc.Model,
				Scope:        mc.Model,
				Until:        mc.Until,
				Reason:       mc.Reason,
			}
			accCooldowns = append(accCooldowns, cd)
			allCooldowns = append(allCooldowns, cd)
		}
		tier := formatTierDisplay(acc.Tier)
		if tier == "" {
			tier = "Free"
		}
		views = append(views, adminAccountView{
			ID:            acc.AccountID,
			Email:         email,
			State:         acc.State,
			Enabled:       acc.Enabled,
			Source:        acc.Source,
			Profile:       acc.Profile,
			Proxy:         redactProxyURLString(acc.Proxy),
			ProxySource:   acc.ProxySource,
			BenefitTier:   tier,
			ContextWindow: acc.ContextWindow,
			Models:        acc.Models,
			Pending:       acc.Pending,
			Message:       acc.LastError,
			Cooldowns:     accCooldowns,
		})
	}
	return views, allCooldowns
}

func (p *Plugin) buildStatusManagementResponse(rt *app.PluginRuntime) pluginapi.ManagementResponse {
	poolStatus := rt.Pool.Status()
	accounts, cooldowns := p.buildAccountsAndCooldowns(rt)
	models := p.collectCatalogModelsForAccount("")
	chromeBin := chromeauth.FindLocalChromeBinary()
	isStopped := p.serviceStopped.Load()
	stateStr := "RUNNING"
	if isStopped {
		stateStr = "STOPPED"
	}
	guestState := "disabled"
	busyCount := 0
	for _, acc := range poolStatus.Accounts {
		if acc.Pending > 0 {
			busyCount++
		}
	}
	if poolStatus.Guest != nil {
		guestState = poolStatus.Guest.State
	}

	return jsonManagementResponse(http.StatusOK, map[string]any{
		"status": map[string]any{
			"state":            stateStr,
			"running":          !isStopped,
			"ready":            poolStatus.Ready() && !isStopped,
			"guest_state":      guestState,
			"routing_strategy": p.appCfg.RoutingStrategy,
			"accounts": map[string]any{
				"total":         poolStatus.Total,
				"ready":         poolStatus.Available,
				"busy":          busyCount,
				"cooldown":      poolStatus.CoolingDown,
				"initializing":  poolStatus.Initializing,
				"auth_required": poolStatus.AuthRequired,
				"unavailable":   poolStatus.Unavailable,
				"disabled":      poolStatus.Disabled,
			},
		},
		"accounts":         accounts,
		"guest":            poolStatus.Guest,
		"cooldowns":        cooldowns,
		"active_requests":  logging.ActiveRequests(),
		"models_count":     len(models),
		"data_dir":         p.pluginCfg.DataDir,
		"accounts_dir":     p.pluginCfg.AccountsDir,
		"chrome_available": chromeBin != "",
		"chrome_binary":    chromeBin,
	})
}

func jsonManagementResponse(status int, payload any) pluginapi.ManagementResponse {
	raw, _ := json.Marshal(payload)
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
		},
		Body: raw,
	}
}
