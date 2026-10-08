package main

import (
	"encoding/json"
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
	fileJSON, err := buildAuthFileJSON(sa, false, displayNote(sa, nil, false), nil)
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
				acct["exhausted"] = isCreditsExhausted(cr)
				acct["trial_claimed"] = hasTrialPack(cr)
				acct["plan"] = fetchPaymentType(sa)
				if v, ok := accountCache.Load(f.ID); ok {
					if e, ok2 := v.(*accountCacheEntry); ok2 {
						fresh := *e
						fresh.credits = cr
						fresh.plan = acct["plan"].(string)
						fresh.fetched = time.Now()
						accountCache.Store(f.ID, &fresh)
					}
				}
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
			row["exhausted"] = isCreditsExhausted(cr)
			row["trial_claimed"] = hasTrialPack(cr)
			row["plan"] = fetchPaymentType(sa)
		}
		out = append(out, row)
	}
	return map[string]any{"accounts": out}
}
