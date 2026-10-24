package main

import (
	"encoding/base64"
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
// 最近修改时间：2026-10-18；改动原因：新增，管理前缀热更新入口。
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
// 最近修改时间：2026-10-18；改动原因：新增，前缀读取入口。
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
// 最近修改时间：2026-10-18；改动原因：新增，CYCLE-00 认证与账号管理路由。
func managementRegistration() managementRegistrationResponse {
	base := "/plugins/" + providerName
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: base + "/accounts", Description: "列出扣子账号。"},
			{Method: http.MethodPost, Path: base + "/login-start", Description: "生成一次性授权会话与脚本（body: {}）。"},
			{Method: http.MethodPost, Path: base + "/delete", Description: "删除一个扣子账号。"},
			{Method: http.MethodPost, Path: base + "/enable", Description: "启用一个扣子账号。"},
			{Method: http.MethodPost, Path: base + "/disable", Description: "禁用一个扣子账号。"},
			{Method: http.MethodPost, Path: base + "/renew", Description: "对指定账号执行一轮续期（body: {auth_id}）。"},
			{Method: http.MethodGet, Path: base + "/models", Description: "列出指定账号可测试的模型（query: auth_id）。"},
			{Method: http.MethodPost, Path: base + "/test-active", Description: "对单个账号发起一次登录态与模型可见性探活（body: {auth_id, model?}）。"},
			{Method: http.MethodGet, Path: base + "/credits", Description: "查询指定账号积分余额与即将过期明细（query: auth_id）。"},
			{Method: http.MethodPost, Path: base + "/refresh-all", Description: "立即触发一轮全量积分巡检（含签到、落盘与低额分级，body: {}）。"},
			{Method: http.MethodPost, Path: base + "/claim-daily", Description: "立即为指定账号领取当日登录积分（body: {auth_id}）。"},
			{Method: http.MethodPost, Path: base + "/register-host", Description: "把本地已落盘的账号补注册到宿主认证目录（body: {}）。"},
		},
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "Coze", Description: "扣子 Provider 管理面板：授权登录、账号管理与续期。"},
			{Path: "/login", Description: "宿主「添加账号」弹窗打开的授权引导页。"},
			{Path: callbackPath, Description: "一次性脚本回传令牌的接收页（免管理密钥，state 短时凭证）。"},
		},
	}
}

// normalizeMgmtSubPath 归一化管理子路径，兼容宿主三种前缀形态。
// [参数] rawPath: 宿主传入的原始路径。
// [返回] 归一化子路径与从路径解析出的查询参数。
// 最近修改时间：2026-10-18；改动原因：新增，兼容 resource 与 management 两种前缀。
func normalizeMgmtSubPath(rawPath string) (string, url.Values) {
	u, err := url.Parse(rawPath)
	path := rawPath
	var query url.Values
	if err == nil {
		path = u.Path
		query = u.Query()
	}
	path = strings.TrimRight(path, "/")

	// 兼容 /v0/resource/plugins/coze-provider/...
	resPrefix := "/v0/resource/plugins/" + providerName
	if strings.HasPrefix(path, resPrefix) {
		sub := strings.TrimPrefix(path, resPrefix)
		if sub == "" {
			return "/panel", query
		}
		return sub, query
	}

	// 兼容 /v0/management/plugins/coze-provider/... 与 /plugins/coze-provider/...
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
// 最近修改时间：2026-10-18；改动原因：新增，授权页与账号管理分发。
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
		state := strings.TrimSpace(req.Query.Get("state"))
		if state == "" && urlQuery != nil {
			state = strings.TrimSpace(urlQuery.Get("state"))
		}
		return okEnvelope(handleLoginBridge(state))
	}

	// 脚本回调走 resource 路由（宿主以 GET 免密钥放行）。
	if method == http.MethodGet && subPath == callbackPath {
		return okEnvelope(handleLoginCallback(req))
	}

	switch {
	case method == http.MethodGet && subPath == "/accounts":
		return handleListAccounts()

	case method == http.MethodPost && subPath == "/login-start":
		return handleLoginStartFromPanel()

	case method == http.MethodPost && subPath == "/delete":
		return handleDeleteAccount(req.Body)

	case method == http.MethodPost && subPath == "/enable":
		return handleSetAccountStatus(req.Body, false)

	case method == http.MethodPost && subPath == "/disable":
		return handleSetAccountStatus(req.Body, true)

	case method == http.MethodPost && subPath == "/renew":
		return handleRenewAccount(req.Body)

	case method == http.MethodGet && subPath == "/models":
		return makeJSONResponse(handleModelsQuery(req))

	case method == http.MethodPost && subPath == "/test-active":
		return makeJSONResponse(handleTestActive(req))

	case method == http.MethodGet && subPath == "/credits":
		return makeJSONResponse(handleCreditsQuery(req))

	case method == http.MethodPost && subPath == "/refresh-all":
		return handleRefreshAll()

	case method == http.MethodPost && subPath == "/claim-daily":
		return handleClaimDaily(req.Body)

	case method == http.MethodPost && subPath == "/register-host":
		return handleReRegister()

	default:
		return makeHTTPResponse(http.StatusNotFound, []byte(`{"error":"not_found"}`))
	}
}

// makeJSONResponse 把载荷编码为 UTF-8 JSON 并以 200 返回。
//
// 与 makeHTTPResponse 的区别只在入参形态：本函数接收 map 载荷，供
// /models、/test-active 这类纯数据接口使用，避免每个调用点重复 Marshal。
// [参数] payload: 响应载荷。
// [返回] 管理响应信封与错误。
// 最近修改时间：2026-10-18；改动原因：新增，简化数据型路由的响应构造。
func makeJSONResponse(payload map[string]any) ([]byte, error) {
	body, errMarshal := marshalNoHTMLEscape(payload)
	if errMarshal != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(`{"error":"响应编码失败"}`))
	}
	return makeHTTPResponse(http.StatusOK, body)
}

// makeHTTPResponse 包装管理 API 的 JSON 响应信封。
// [参数] status: HTTP 状态码；body: 响应体字节。
// [返回] 管理响应信封与错误。
// 最近修改时间：2026-10-18；改动原因：新增，统一管理响应构造。
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

// accountSummary 是面板账号列表项。
type accountSummary struct {
	AuthID     string `json:"auth_id"`
	AuthIndex  string `json:"auth_index"`
	Label      string `json:"label"`
	ScreenName string `json:"screen_name,omitempty"`
	Mobile     string `json:"mobile,omitempty"`
	SecUserID  string `json:"sec_user_id,omitempty"`
	UserID     int64  `json:"user_id,omitempty"`
	Disabled   bool   `json:"disabled"`
	// AuthState 是登录态状态：ok（有效）/ expired（待重新授权）。
	AuthState string `json:"auth_state"`
	// LoginAt / LastRenewAt 让面板区分「刚登录」与「靠续期维持」的账号。
	LoginAt     string `json:"login_at,omitempty"`
	LastRenewAt string `json:"last_renew_at,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	// Credits 是积分摘要；查询失败时 Error 非空，面板据此显示「未知」。
	Credits *creditSummary `json:"credits,omitempty"`
	// LowCredits 表示剩余积分低于红线，该账号不再承载新请求。
	LowCredits bool `json:"low_credits"`
	// CreditTier 是分级结果：normal / disabled / unknown。
	CreditTier string `json:"credit_tier,omitempty"`
	// AutoDisabled 表示该账号的禁用由积分机制写入，可被自动解禁。
	AutoDisabled bool `json:"auto_disabled"`
	// AutoDisabledAt 是自动禁用时刻，供面板解释「什么时候被禁的」。
	AutoDisabledAt string `json:"auto_disabled_at,omitempty"`
	// SnapshotAt 是积分快照的拉取时刻，供面板显示数据新鲜度。
	SnapshotAt string `json:"snapshot_at,omitempty"`
	// DailyCredit 是当日登录积分的领取状态，面板据此显示「今日已领」。
	DailyCredit *dailyCreditView `json:"daily_credit,omitempty"`
}

// handleListAccounts 返回全部扣子账号摘要。
//
// 积分一律读落盘快照，不发任何上游请求（2026-10-25 起）：
// 巡检每小时已经把全部账号刷过一遍，面板打开再逐号并发打上游既是
// 无界 goroutine 扇出，也让「打开面板」变成一次上游压测。
// 需要最新数字时走单卡「刷新积分」（force=1）或「立即扫描」。
// [参数] 无。
// [返回] 管理响应。
// 最近修改时间：2026-10-25；改动原因：改为零上游读快照，替代逐号并发查询。
func handleListAccounts() ([]byte, error) {
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, errList.Error())))
	}

	snapshots := creditSnapshotAll()
	redline := loadedMinRoutableCredits()
	list := make([]accountSummary, len(accounts))
	for i, acc := range accounts {
		list[i] = buildAccountSummary(acc, snapshots, redline)
	}

	body, _ := json.Marshal(map[string]any{
		"accounts": list,
		"count":    len(list),
		"redline":  redline,
		"sweep":    lastCreditSweep(),
		"running":  creditSweepIsRunning(),
	})
	return makeHTTPResponse(http.StatusOK, body)
}

// buildAccountSummary 把账号与其积分快照组装成面板视图。
//
// 快照缺失时 Credits 必须保持 nil：面板据此显示「等待首轮巡检」。
// 填一个 0 分摘要会让用户看到「积分耗尽」，而实际只是还没跑过巡检——
// 这两件事的处置完全不同（前者要补积分，后者只需等待或手动巡检）。
// [参数] acc：账号；snapshots：全部积分快照；redline：当前红线。
// [返回] 面板账号视图。
// 最近修改时间：2026-10-25；改动原因：抽出纯函数，让「缺失不当 0」可被测试锁定。
func buildAccountSummary(acc *StoredAuth, snapshots map[string]creditSnapshotEntry, redline int64) accountSummary {
	out := accountSummary{
		AuthID:         acc.AuthID,
		AuthIndex:      acc.AuthID,
		Label:          acc.Label,
		ScreenName:     acc.ScreenName,
		Mobile:         acc.Mobile,
		SecUserID:      maskIdentifier(acc.SecUserID),
		UserID:         acc.UserID,
		Disabled:       acc.Disabled,
		AuthState:      authStateOf(acc),
		LoginAt:        acc.LoginAt,
		LastRenewAt:    acc.LastRenewAt,
		CreatedAt:      acc.CreatedAt,
		UpdatedAt:      acc.UpdatedAt,
		DailyCredit:    buildDailyCreditView(acc.AuthID),
		AutoDisabled:   acc.LowCreditDisable,
		AutoDisabledAt: acc.LowCreditDisableAt,
	}
	entry, ok := snapshots[creditSnapshotKey(acc.AuthID)]
	if !ok {
		// 快照缺失：只回档位，绝不构造 0 分摘要。
		out.CreditTier = creditTierUnknown.String()
		return out
	}
	out.Credits = entry.Credits
	out.SnapshotAt = entry.FetchedAt
	out.CreditTier = creditTierFor(entry.Remain, redline).String()
	// 低额标记与闸门同源：都读同一份快照和同一个红线，
	// 避免面板显示「够用」而实际已被拦截。
	out.LowCredits = entry.Remain >= 0 && redline > 0 && entry.Remain < redline
	return out
}

// authStateOf 判定账号的登录态状态。
//
// 依赖账号文件中的会话主 Cookie 快照：有快照看快照，没有则从 CookieHeader
// 重建（首次授权后尚未滚动写回的账号）。任一会话 Cookie 存在且未过期即 ok。
//
// 优先采信上游的明确判定：续期返回「会话过期」时 AuthInvalid 会被置位，
// 此时即便本地 Cookie 看起来仍在也必须报 expired——扣子的会话 Cookie
// 不带 Expires，只按本地过期时间判活会让失效账号永远显示「可用」
// （2026-10-19 多账号实测：两个账号上游已拒绝，面板仍全部显示可用）。
// [参数] sa: 账号结构。
// [返回] "ok" 或 "expired"。
// 最近修改时间：2026-10-19；改动原因：上游明确失效优先于本地 Cookie 判活。
func authStateOf(sa *StoredAuth) string {
	if sa == nil {
		return "expired"
	}
	if sa.AuthInvalid {
		return "expired"
	}
	store := newCookieStore()
	if len(sa.Cookies) > 0 {
		store.restore(sa.Cookies)
	} else if strings.TrimSpace(sa.CookieHeader) != "" {
		seedStoreFromHeader(store, sa.CookieHeader, cozeSiteHost+"/")
	}
	if store.hasLiveSession() {
		return "ok"
	}
	return "expired"
}

// handleDeleteAccount 删除指定账号。
// [参数] body: 含 auth_id 的请求体。
// [返回] 管理响应。
// 最近修改时间：2026-10-18；改动原因：新增，账号删除入口。
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
// 最近修改时间：2026-10-18；改动原因：新增，账号启停入口。
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

// handleRenewAccount 对指定账号执行一轮续期。
// 续期失败统一用 200 + error 字段表达：面板的 api() 只在 401/403 上走密钥
// 重填分支，业务失败必须靠响应体区分，否则会被误判成密钥失效。
// [参数] body: 含 auth_id 的请求体。
// [返回] 管理响应。
// 最近修改时间：2026-10-18；改动原因：新增，手动续期入口。
func handleRenewAccount(body []byte) ([]byte, error) {
	authID := extractAuthID(body)
	if authID == "" {
		return makeHTTPResponse(http.StatusBadRequest, []byte(`{"error":"auth_id 不能为空"}`))
	}
	res, errRenew := renewAccountByID(authID)
	if errRenew != nil {
		payload, _ := json.Marshal(map[string]any{"ok": false, "error": errRenew.Error()})
		return makeHTTPResponse(http.StatusOK, payload)
	}
	payload, _ := json.Marshal(map[string]any{
		"ok":      true,
		"renewed": res != nil && res.Renewed,
		"note":    renewNote(res),
		"message": "续期完成",
	})
	return makeHTTPResponse(http.StatusOK, payload)
}

// renewNote 提取续期说明，res 为 nil 时返回空串。
// [参数] res: 续期结果。
// [返回] 说明文本。
// 最近修改时间：2026-10-18；改动原因：新增，避免 nil 解引用。
func renewNote(res *renewResult) string {
	if res == nil {
		return ""
	}
	return res.Note
}

// handleLoginStartFromPanel 供面板直接生成一次授权会话与脚本。
//
// 与 handleStartLogin 的区别：那条路径由宿主弹窗驱动（返回桥接页地址），
// 本路径在面板内直接给出可复制脚本——用户不必先经过宿主弹窗，
// 少一次跳转。两者共用同一份会话登记逻辑。
// [参数] 无。
// [返回] 管理响应。
// 最近修改时间：2026-10-18；改动原因：新增，面板内授权入口。
func handleLoginStartFromPanel() ([]byte, error) {
	state := randomState()

	loginSessionsMu.Lock()
	pruneExpiredLoginSessionsLocked()
	loginSessions[state] = &loginSession{startedAt: time.Now()}
	loginSessionsMu.Unlock()

	// 脚本以 base64 返回：宿主在回写 ManagementResponse.Body 时会再做一次
	// JSON 序列化并开启 HTML 转义（实测：错误文案里的 `<`、`'` 会变成
	// `&lt;`、`&#39;`），任何含 `=>`、`'` 的 JS 都会被实体化而无法执行。
	// base64 不含 HTML 特殊字符，绕开该行为；面板端解码后再展示。
	script := buildAuthScript(state)
	payload, _ := marshalNoHTMLEscape(map[string]any{
		"ok":         true,
		"state":      state,
		"script":     base64.StdEncoding.EncodeToString([]byte(script)),
		"encoding":   "base64",
		"expires_in": int(loginStateTTL.Seconds()),
	})
	return makeHTTPResponse(http.StatusOK, payload)
}

// handleRefreshAll 立即触发一轮全量积分巡检。
//
// 异步返回而不是同步等待：一轮巡检要逐号打上游（含签到），9 个账号
// 通常需要数秒到数十秒，同步等待会让面板请求超时。面板拿到 queued 后
// 轮询 /accounts 的 running 字段判断何时完成。
// [参数] 无。
// [返回] 管理响应。
// 最近修改时间：2026-10-25；改动原因：新增，面板「立即扫描」入口。
func handleRefreshAll() ([]byte, error) {
	started := runCreditSweepNonOverlap()
	payload, _ := json.Marshal(map[string]any{
		"ok":      started,
		"queued":  started,
		"running": creditSweepIsRunning(),
		"sweep":   lastCreditSweep(),
		"message": refreshAllMessage(started),
	})
	return makeHTTPResponse(http.StatusOK, payload)
}

// refreshAllMessage 生成扫描触发结果的说明文案。
// [参数] started: 是否真的启动了新一轮。
// [返回] 结果说明。
// 最近修改时间：2026-10-25；改动原因：新增，避免面板显示「未知」。
func refreshAllMessage(started bool) string {
	if started {
		return "已开始全量巡检，完成后自动刷新"
	}
	return "上一轮巡检尚未结束，请稍后再试"
}

// extractAuthID 从请求体中提取 auth_id / auth_index / authID / id。
// [参数] body: 请求体字节。
// [返回] 账号标识；均缺失时返回空串。
// 最近修改时间：2026-10-18；改动原因：新增，账号标识提取。
func extractAuthID(body []byte) string {
	for _, key := range []string{"auth_id", "auth_index", "authID", "id"} {
		if v := strings.TrimSpace(gjson.GetBytes(body, key).String()); v != "" {
			return v
		}
	}
	return ""
}

// setAccountDisabled 修改指定账号的禁用状态并落盘。
//
// 必须清掉本机制的归属标记：用户手动禁用一个「已被自动禁用」的账号时，
// disabled 已经是 true，若不清标记，次日签到到账会违背用户意愿自动解禁；
// 手动启用同理——用户既然要它跑，就不该再被低额机制二次接管。
// [参数] authID: 账号标识；disabled: 目标状态。
// [返回] 错误。
// 最近修改时间：2026-10-25；改动原因：改走锁内读改写并清除低额归属标记。
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
		path := acc.PhysicalPath
		if path == "" {
			path = getAccountsDir() + "/" + authID + ".json"
		}
		// 锁内读改写：避免与巡检的自动禁用/解禁并发覆盖。
		_, errMutate := mutateAuthFile(path, func(auth *StoredAuth) bool {
			auth.Disabled = disabled
			clearLowCreditMarkers(auth)
			return true
		})
		return errMutate
	}
	return fmt.Errorf("账号不存在：%s", authID)
}
