// credits_handler.go implements the management API endpoints that mutate or
// read account state: import credential, toggle check-in, claim trial, select
// active auth, and query credits for one account or all.
package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// handleImportAuth accepts nested or flat credential JSON and persists via host.auth.save.
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
	// Persist nested storage + top-level type/note/logo/disabled for Auth page.
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
	// Remove legacy workbuddy.json if it exists and differs from the saved name.
	if saveResp.Name != "" && !strings.EqualFold(saveResp.Name, authFileName) {
		legacyPath := strings.TrimSpace(saveResp.Path)
		// Best-effort: if auth dir is known via saveResp.Path parent, try removing sibling workbuddy.json.
		if legacyPath != "" {
			dir := filepath.Dir(legacyPath)
			legacyFile := filepath.Join(dir, authFileName)
			// A-35: use deleteAuthFileInDir for absolute path + directory confinement.
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

// handleExportAuth returns every WorkBuddy credential known to the host as a
// single JSON document, so the panel's 导出凭证 button can offer a one-click
// backup that round-trips through the existing /import endpoint (parseStored
// accepts the on-disk nested `{"auth":{...},"account":{...}}` form directly).
//
// Response shape (HTTP body, application/json):
//
//	{
//	  "version":     1,
//	  "exported_at": "2026-08-22T11:30:00Z",
//	  "plugin":      "workbuddy-provider",
//	  "count":       13,
//	  "accounts": [
//	    {
//	      "name":       "workbuddy-17021653110.json",
//	      "auth_index": "...",
//	      "uid":        "17021653110",
//	      "nickname":   "...",
//	      "region":     "cn",
//	      "credential": { ... raw physical JSON (parsed) ... }
//	    },
//	    ...
//	  ]
//	}
//
// Per-account failures (load error / parse error) are recorded inline as a
// `load_error` or `parse_error` field, with `credential` omitted — the export
// is best-effort and never fails the whole batch for one bad file.
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
		// Re-decode the physical JSON into a generic map so the response carries
		// parsed JSON (the import endpoint already accepts either nested object
		// or raw bytes via body.json/body.raw). Keeping it parsed makes the
		// output diff-friendly and unambiguous about what came back.
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
			entry["region"] = accountRegion(sa)
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

func handleCheckinConfig(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	_ = json.Unmarshal(req.Body, &body)
	checkinAutoMu.Lock()
	if body.Enabled != nil {
		// Runtime-only toggle: the CPA host exposes no plugin-config write
		// callback, so persisting would mean editing the host's config.yaml
		// from inside the plugin (fragile under docker volume mounts). The
		// value from config_yaml wins again on CPA restart.
		checkinAuto = *body.Enabled
	}
	cur := checkinAuto
	checkinAutoMu.Unlock()
	return map[string]any{"checkin_auto": cur, "persistent": false}
}

// handleClaimTrial claims the expert trial pack for one Global account.
// CN accounts are rejected — the trial endpoint is Global-only.
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
		if !isGlobalDomain(sa.Auth.Domain) {
			return map[string]any{"auth_index": authIndex, "error": "专家加油包仅适用于国际版账号"}
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
		// Invalidate credits cache (copy entry, set credits=nil, keep plan/checkin).
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

// handleSelectAuth sets the panel-selected account used for chat routing.
// Region (CN/Global) is read from that account's stored domain on each request.
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
			"region":      accountRegion(sa),
			"nickname":    sa.Account.Nickname,
			"uid":         sa.Account.UID,
		}
	}
	return map[string]any{"error": "account not found", "auth_index": authIndex}
}

// handleDeleteAuth removes one WorkBuddy account and its physical auth file.
// This panel entry is strict (unlike lifecycle deleteAuth, which disables as a
// fallback when no path is known): it refuses to act unless the target account
// exists, belongs to WorkBuddy, and its physical path is safe and known. Only
// auth_index is accepted from the body; the host's own list/get responses are
// the sole source of truth for the delete target (never a client-supplied
// uid/name/path).
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
		// hostAuthList already prefix-filters on workbuddy-*, but double-check
		// the concrete name so a legacy or mis-shaped entry can't slip through.
		if !isWorkbuddyAuthFileName(f.Name) {
			return map[string]any{"error": "不是 WorkBuddy 认证文件", "auth_index": authIndex}
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
		// Physical delete confined to the auth directory.
		if err := deleteAuthFileInDir(path, filepath.Dir(path)); err != nil {
			return map[string]any{"error": "删除认证文件失败: " + err.Error(), "auth_index": authIndex}
		}
		// Also remove legacy workbuddy.json if this UID was dual-named historically.
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

// handleCreditsQuery returns real-time credits for one or all accounts.
// Pass ?auth_index=<idx> to query a single account; omit for all.
// Single-account mode returns full account info (nickname, region, credits,
// exhausted, trial_claimed) so the panel can update one card without
// reloading the entire dashboard.
func handleCreditsQuery(req pluginapi.ManagementRequest) map[string]any {
	authIndex := ""
	if vals := req.Query["auth_index"]; len(vals) > 0 {
		authIndex = strings.TrimSpace(vals[0])
	}
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	// Single-account: return one full account row (like dashboard entry).
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
cr, err := fetchUserResource(sa)
		acct := map[string]any{
			"auth_index": authIndex,
			"nickname":   sa.Account.Nickname,
			"uid":        sa.Account.UID,
			"region":     accountRegion(sa),
			"name":       f.Name,
			"label":      f.Label,
			"disabled":   f.Disabled,
			"selected":   getActiveAuthID() == f.ID,
			"preserve":   isPreserve(f.ID),
		}
			if err != nil {
				acct["error"] = err.Error()
			} else {
				acct["credits"] = cr
				acct["exhausted"] = isCreditsExhausted(cr)
				if isGlobalDomain(sa.Auth.Domain) {
					acct["trial_claimed"] = hasTrialPack(cr)
				}
				// Also fetch plan so the badge updates on lazy load.
				acct["plan"] = fetchPaymentType(sa)
				// Update cache so subsequent dashboard loads see fresh data.
				now := time.Now()
				if cr != nil {
					cr.FetchedAt = now.UTC().Format(time.RFC3339)
				}
				// Merge into existing cache entry (keep checkin if present).
				var prev *accountCacheEntry
				if v, ok := accountCache.Load(f.ID); ok {
					prev, _ = v.(*accountCacheEntry)
				}
				var ci *checkinSummary
				if prev != nil {
					ci = prev.checkin
				}
				plan, _ := acct["plan"].(string)
				accountCache.Store(f.ID, &accountCacheEntry{
					checkin: ci, credits: cr, plan: plan, fetched: now,
				})
			}
			return map[string]any{"accounts": []map[string]any{acct}}
		}
		return map[string]any{"error": "account not found"}
	}
	// All accounts: return simplified list.
	type acctCredits struct {
		AuthIndex string          `json:"auth_index"`
		Nickname  string          `json:"nickname"`
		UID       string          `json:"uid"`
		Credits   *creditsSummary `json:"credits,omitempty"`
		Error     string          `json:"error,omitempty"`
	}
	var out []acctCredits
	for _, f := range files {
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			out = append(out, acctCredits{AuthIndex: f.AuthIndex, Error: "load auth: " + err.Error()})
			continue
		}
		cr, err := fetchUserResource(sa)
		ac := acctCredits{AuthIndex: f.AuthIndex, Nickname: sa.Account.Nickname, UID: sa.Account.UID}
		if err != nil {
			ac.Error = err.Error()
		} else {
			ac.Credits = cr
		}
		out = append(out, ac)
	}
	return map[string]any{"accounts": out}
}
