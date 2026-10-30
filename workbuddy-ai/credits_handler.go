package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func accountRegion(sa *storedAuth) string {
	return "global"
}

func isGlobalDomain(domain string) bool {
	return true
}

func handleImportAuth(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		JSON json.RawMessage `json:"json"`
		Raw  string          `json:"raw"`
	}
	_ = json.Unmarshal(req.Body, &body)
	raw := []byte(strings.TrimSpace(body.Raw))
	if len(body.JSON) > 0 {
		raw = body.JSON
	}
	if len(raw) == 0 {
		return map[string]any{"success": false, "error": "missing json/raw credential payload"}
	}
	sa, err := parseStored(raw)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	sa.Auth.Domain = "www.workbuddy.ai"
	// 导入的凭据可能带着本机制标记（例如从本插件导出的禁用号再导回），
	// 白名单重建不搬运就会把 failover_disable 抹掉，探活循环认不出该账号。
	fileJSON, err := buildAuthFileJSON(sa, false, displayNote(sa, nil, false), putFailoverMarkersIntoExtra(nil, raw))
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	auth := toAuthData(sa)
	saveReq := pluginapi.HostAuthSaveRequest{
		Name: auth.FileName,
		JSON: fileJSON,
	}
	saveBody, _ := json.Marshal(saveReq)
	rawResp, err := hostCall(pluginabi.MethodHostAuthSave, saveBody)
	if err != nil {
		return map[string]any{"success": false, "error": "host.auth.save: " + err.Error()}
	}
	var env envelope
	if err := json.Unmarshal(rawResp, &env); err != nil || !env.OK {
		msg := "host.auth.save failed"
		if env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return map[string]any{"success": false, "error": msg}
	}
	var saveResp pluginapi.HostAuthSaveResponse
	_ = json.Unmarshal(env.Result, &saveResp)
	if saveResp.Name != "" && !strings.EqualFold(saveResp.Name, authFileName) {
		legacyPath := strings.TrimSpace(saveResp.Path)
		if legacyPath != "" {
			dir := filepath.Dir(legacyPath)
			legacyFile := filepath.Join(dir, authFileName)
			_ = deleteAuthFileInDir(legacyFile, dir)
		}
	}
	return map[string]any{
		"success":  true,
		"name":     saveResp.Name,
		"path":     saveResp.Path,
		"uid":      sa.Account.UID,
		"nickname": sa.Account.Nickname,
		"file":     auth.FileName,
	}
}

func handleExportAuth(req pluginapi.ManagementRequest) map[string]any {
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": "host.auth.list failed: " + err.Error(), "count": 0, "accounts": []any{}}
	}
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		phys, gerr := hostAuthGetPhysical(f.AuthIndex)
		if gerr != nil || phys == nil {
			out = append(out, map[string]any{
				"name":       f.Name,
				"auth_index": f.AuthIndex,
				"load_error": errString(gerr),
			})
			continue
		}
		var cred any
		_ = json.Unmarshal(phys.JSON, &cred)
		sa, perr := parseStored(phys.JSON)
		entry := map[string]any{
			"name":       f.Name,
			"auth_index": f.AuthIndex,
			"credential": cred,
		}
		if sa != nil {
			entry["uid"] = sa.Account.UID
			entry["nickname"] = sa.Account.Nickname
			entry["region"] = "global"
		} else {
			entry["parse_error"] = errString(perr)
		}
		out = append(out, entry)
	}
	return map[string]any{
		"version":     1,
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"plugin":      providerName,
		"count":       len(out),
		"accounts":    out,
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// handleCheckinConfig toggles auto check-in at runtime; the value is not
// persisted and config_yaml decides again on restart.
func handleCheckinConfig(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	_ = json.Unmarshal(req.Body, &body)
	checkinAutoMu.Lock()
	if body.Enabled != nil {
		checkinAuto = *body.Enabled
	}
	cur := checkinAuto
	checkinAutoMu.Unlock()
	return map[string]any{"checkin_auto": cur, "persistent": false}
}

func handleClaimTrial(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		return map[string]any{"error": "auth_index is required"}
	}
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	for _, f := range files {
		if f.AuthIndex != authIndex {
			continue
		}
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			return map[string]any{"auth_index": authIndex, "error": err.Error()}
		}
		res, err := performTrialCall(sa)
		out := map[string]any{"auth_index": authIndex, "nickname": sa.Account.Nickname}
		if err != nil {
			out["error"] = err.Error()
		} else {
			for k, v := range res {
				out[k] = v
			}
		}
		if v, ok := accountCache.Load(f.ID); ok {
			if e, ok2 := v.(*accountCacheEntry); ok2 {
				fresh := *e
				fresh.credits = nil
				fresh.fetched = time.Now()
				accountCache.Store(f.ID, &fresh)
			}
		}
		if lifecycleEnabled() {
			_, _ = reconcileOneAccount(authIndex, f.ID, true)
		}
		return out
	}
	return map[string]any{"error": "account not found"}
}

// handleRegisterAccount 补齐单个账号的注册链路（注册地 + 云账号 + 试用包）。
// 用于存量未注册账号的人工补注册，以及 OAuth 自动补注册失败后的重试入口。
// [参数] req 管理请求，body 需含 auth_index
// [返回] 补注册结果明细
// 最近修改时间：2026-10-15 18:20:00；改动原因：新增补注册链路
func handleRegisterAccount(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		return map[string]any{"error": "auth_index is required"}
	}
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	for _, f := range files {
		if f.AuthIndex != authIndex {
			continue
		}
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			return map[string]any{"auth_index": authIndex, "error": err.Error()}
		}
		res, err := ensureAccountRegistered(sa)
		out := map[string]any{
			"auth_index": authIndex,
			"nickname":   sa.Account.Nickname,
			"uid":        sa.Account.UID,
		}
		for k, v := range res {
			out[k] = v
		}
		if err != nil {
			out["error"] = err.Error()
		}
		if v, ok := accountCache.Load(f.ID); ok {
			if e, ok2 := v.(*accountCacheEntry); ok2 {
				fresh := *e
				fresh.credits = nil
				fresh.fetched = time.Now()
				accountCache.Store(f.ID, &fresh)
			}
		}
		if lifecycleEnabled() {
			_, _ = reconcileOneAccount(authIndex, f.ID, true)
		}
		return out
	}
	return map[string]any{"error": "account not found"}
}

func handleSelectAuth(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		return map[string]any{"error": "auth_index is required", "active_auth": getActiveAuthID()}
	}
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	for _, f := range files {
		if f.AuthIndex != authIndex {
			continue
		}
		if f.Disabled {
			return map[string]any{"error": "账号已禁用，无法选中", "auth_index": authIndex}
		}
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			return map[string]any{"error": err.Error(), "auth_index": authIndex}
		}
		setActiveAuthID(f.ID)
		return map[string]any{
			"ok":          true,
			"active_auth": f.ID,
			"region":      "global",
			"nickname":    sa.Account.Nickname,
			"uid":         sa.Account.UID,
		}
	}
	return map[string]any{"error": "account not found", "auth_index": authIndex}
}

func handleDeleteAuth(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		return map[string]any{"error": "auth_index is required"}
	}
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": "host.auth.list: " + err.Error()}
	}
	for _, f := range files {
		if f.AuthIndex != authIndex {
			continue
		}
		if !isWorkbuddyAuthFileName(f.Name) {
			return map[string]any{"error": "不是 WorkBuddy AI 认证文件", "auth_index": authIndex}
		}
		sa, phys, err := hostAuthGetBundle(authIndex)
		if err != nil {
			return map[string]any{"error": "host.auth.get: " + err.Error(), "auth_index": authIndex}
		}
		if sa == nil {
			return map[string]any{"error": "认证内容解析失败", "auth_index": authIndex}
		}
		if phys == nil || strings.TrimSpace(phys.AuthIndex) != authIndex {
			return map[string]any{"error": "认证索引不一致", "auth_index": authIndex}
		}
		path := strings.TrimSpace(phys.Path)
		if path == "" {
			return map[string]any{"error": "认证文件路径缺失，无法安全删除", "auth_index": authIndex}
		}
		if !isSafeWorkbuddyAuthPath(path) {
			return map[string]any{"error": "认证文件路径不安全，已拒绝删除", "auth_index": authIndex}
		}
		nickname := sa.Account.Nickname
		uid := sa.Account.UID
		if err := deleteAuthFileInDir(path, filepath.Dir(path)); err != nil {
			return map[string]any{"error": "删除认证文件失败: " + err.Error(), "auth_index": authIndex}
		}
		if strings.TrimSpace(uid) != "" {
			if dir := filepath.Dir(path); dir != "" {
				legacy := filepath.Join(dir, authFileName)
				if isLegacyWorkbuddyAuthName(filepath.Base(legacy)) {
					_ = deleteAuthFileInDir(legacy, dir)
				}
			}
		}
		clearDeletedAccountState(f.ID, authIndex, uid)
		return map[string]any{
			"ok":         true,
			"auth_index": authIndex,
			"nickname":   nickname,
			"uid":        uid,
			"deleted":    f.Name,
		}
	}
	return map[string]any{"error": "account not found", "auth_index": authIndex}
}

// handleCreditsQuery 处理面板的单卡「刷新」：按 auth_index 直连上游取最新积分。
//
// 面板已移除自动刷新，这是仅剩的手动取数入口，因此取到新积分后必须当场落盘
// （tagCreditsPersist + flushCreditsSnapshots），否则下次打开面板仍显示旧快照。
// [参数] req：管理请求，Query 可带 auth_index（缺省表示全部账号）。
// [返回] {accounts:[...]}；单账号失败时该行带 error。
// 最近修改时间：2026-10-24；改动原因：手动刷新成为唯一取数入口，取到即落盘
func handleCreditsQuery(req pluginapi.ManagementRequest) map[string]any {
	authIndex := ""
	if vals := req.Query["auth_index"]; len(vals) > 0 {
		authIndex = strings.TrimSpace(vals[0])
	}
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	if authIndex != "" {
		for _, f := range files {
			if f.AuthIndex != authIndex {
				continue
			}
			sa, err := hostAuthGet(f.AuthIndex)
			if err != nil {
				return map[string]any{"accounts": []map[string]any{{
					"auth_index": authIndex, "error": "load auth: " + err.Error(),
				}}}
			}
			if vals := req.Query["track"]; len(vals) > 0 {
				if t := strings.TrimSpace(vals[0]); t == "1" || t == "true" {
					globalRefresh.EnqueueOne(authIndex, f.ID, "credits")
					return map[string]any{
						"queued":     true,
						"auth_index": authIndex,
						"status":     globalRefresh.Snapshot(),
					}
				}
			}
			cr, err := fetchUserResource(sa)
			acct := map[string]any{
				"auth_index":  authIndex,
				"nickname":    sa.Account.Nickname,
				"uid":         sa.Account.UID,
				"region":      "global",
				"name":        f.Name,
				"label":       f.Label,
				"disabled":    f.Disabled,
				"selected":    getActiveAuthID() == f.ID,
				"test_failed": isTestFailed(f.ID),
			}
			if err != nil {
				acct["error"] = err.Error()
			} else {
				acct["credits"] = cr
				acct["trial_claimed"] = hasTrialPack(cr)
				acct["plan"] = fetchPaymentType(sa)
				if registered, regErr := fetchRegistrationStatus(sa); regErr == nil {
					acct["registered"] = registered
				}
				if v, ok := accountCache.Load(f.ID); ok {
					if e, ok2 := v.(*accountCacheEntry); ok2 {
						fresh := *e
						fresh.credits = cr
						fresh.plan = acct["plan"].(string)
						fresh.fetched = time.Now()
						accountCache.Store(f.ID, &fresh)
					}
				}
				// 单卡「刷新」是面板唯一的上游取数入口（自动刷新已移除），
				// 取到就必须落盘：否则用户手动刷出最新积分后，下次打开面板
				// 仍显示盘上的旧快照，持久化停在旧值。
				tagCreditsPersist(sa.Account.UID, cr)
				flushCreditsSnapshots()
			}
			return map[string]any{"accounts": []map[string]any{acct}}
		}
		return map[string]any{"error": "account not found", "auth_index": authIndex}
	}
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			out = append(out, map[string]any{
				"auth_index": f.AuthIndex,
				"error":      err.Error(),
			})
			continue
		}
		cr, err := fetchUserResource(sa)
		row := map[string]any{
			"auth_index":  f.AuthIndex,
			"nickname":    sa.Account.Nickname,
			"uid":         sa.Account.UID,
			"region":      "global",
			"name":        f.Name,
			"label":       f.Label,
			"disabled":    f.Disabled,
			"selected":    getActiveAuthID() == f.ID,
			"test_failed": isTestFailed(f.ID),
		}
		if err != nil {
			row["error"] = err.Error()
		} else {
			row["credits"] = cr
			row["trial_claimed"] = hasTrialPack(cr)
			row["plan"] = fetchPaymentType(sa)
		}
		out = append(out, row)
	}
	return map[string]any{"accounts": out}
}

// handleExportAuthZip 把每个账号的原始凭证打成 zip，包内每个账号一个独立 JSON。
//
// 与 handleExportAuth 的区别：那个返回单个汇总 JSON（accounts[] 数组），适合
// 一次性整库备份；本接口面向「按账号分发/迁移」——解压后直接得到
// <provider>-<uid>.json 一组文件，可逐个拖进导入弹窗，也可直接放进 auth 目录。
// 单账号读取失败不影响其余账号：失败项以 <name>.error.txt 记录原因。
// [参数] req: 管理请求（当前不读取参数）
// [返回] zip 二进制响应；无账号时返回 404 JSON 错误
// 最近修改时间：2026-10-30；改动原因：新增每账号一个 JSON 的 zip 导出。
func handleExportAuthZip(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	files, err := hostAuthListFn()
	if err != nil {
		return mgmtJSONResponse(http.StatusInternalServerError, map[string]any{
			"error": "host.auth.list failed: " + err.Error(),
		})
	}
	if len(files) == 0 {
		return mgmtJSONResponse(http.StatusNotFound, map[string]any{"error": "没有可导出的账号"})
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	used := make(map[string]int, len(files))
	for _, f := range files {
		name := perAccountFileName(f.Name, f.AuthIndex, used)
		phys, gerr := hostAuthGetPhysicalFn(f.AuthIndex)
		if gerr != nil || phys == nil {
			// 读取失败：写一个同名 .error.txt 说明原因，保持「每个账号一个条目」。
			w, werr := zw.Create(strings.TrimSuffix(name, ".json") + ".error.txt")
			if werr == nil {
				_, _ = w.Write([]byte("读取出错：" + errString(gerr)))
			}
			continue
		}
		w, werr := zw.Create(name)
		if werr != nil {
			continue
		}
		// 原样写入物理文件字节：保留 nested/flat 形态与未知字段，保证可被
		// /import 直接回灌，也不会因重新序列化丢失上游私有权重字段。
		if _, werr = w.Write(phys.JSON); werr != nil {
			continue
		}
	}
	if errClose := zw.Close(); errClose != nil {
		return mgmtJSONResponse(http.StatusInternalServerError, map[string]any{
			"error": "zip finalize failed: " + errClose.Error(),
		})
	}

	stamp := time.Now().Format("2006-01-02")
	h := http.Header{}
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-credentials-%s.zip"`, providerName, stamp))
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: h, Body: buf.Bytes()}
}

// perAccountFileName 生成 zip 内的单账号文件名，并保证同名冲突时追加序号。
//
// 命名优先沿用宿主 auth 文件名（已含 uid，形如 <provider>-<uid>.json）；缺失时
// 回退 auth_index，再回退固定名，避免 zip 内出现空名或重复条目。
// [参数] name: 宿主侧 auth 文件名；authIndex: 账号索引；used: 已用文件名计数表
// [返回] 唯一且以 .json 结尾的文件名
// 最近修改时间：2026-10-30；改动原因：新增，配合 zip 导出。
func perAccountFileName(name, authIndex string, used map[string]int) string {
	base := strings.TrimSpace(filepath.Base(strings.TrimSpace(name)))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = strings.TrimSpace(authIndex)
	}
	if base == "" {
		base = providerName + "-account"
	}
	if !strings.HasSuffix(strings.ToLower(base), ".json") {
		base += ".json"
	}
	used[base]++
	if n := used[base]; n > 1 {
		base = fmt.Sprintf("%s-%d.json", strings.TrimSuffix(base, ".json"), n)
	}
	return base
}
