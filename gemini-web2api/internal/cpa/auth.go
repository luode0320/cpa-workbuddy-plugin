package cpa

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/app"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/auth"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/balancer"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/chromeauth"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginabi"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginapi"
	"github.com/google/uuid"
)

type rpcIdentifierResponse struct {
	Identifier          string   `json:"identifier"`
	Identifiers         []string `json:"identifiers,omitempty"`
	CredentialProviders []string `json:"credential_providers,omitempty"`
}

type rpcAuthLoginStartRequest struct {
	pluginapi.AuthLoginStartRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthLoginPollRequest struct {
	pluginapi.AuthLoginPollRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthRefreshRequest struct {
	pluginapi.AuthRefreshRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// AuthFilePayload is the JSON structure stored in CLIProxyAPI's auths/ directory for gemini-web2api.
type AuthFilePayload struct {
	Type           string             `json:"type"`
	Provider       string             `json:"provider,omitempty"`
	AccountID      string             `json:"account_id,omitempty"`
	Email          string             `json:"email,omitempty"`
	Label          string             `json:"label,omitempty"`
	Disabled       *bool              `json:"disabled,omitempty"`
	ServiceStopped bool               `json:"service_stopped,omitempty"`
	Source         string             `json:"source,omitempty"`
	Profile        string             `json:"profile,omitempty"`
	Proxy          string             `json:"proxy,omitempty"`
	AuthUser       int                `json:"auth_user,omitempty"`
	Cookies        string             `json:"cookies,omitempty"`
	CookieHeader   string             `json:"cookie_header,omitempty"`
	StorageState   *auth.StorageState `json:"storage_state,omitempty"`
	Tier           string             `json:"tier,omitempty"`
	State          string             `json:"state,omitempty"`
	UpdatedAt      string             `json:"updated_at,omitempty"`
}

func isGeminiWebProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "gemini-web2api", "gemini-web", "geminiweb2api", "geminiweb":
		return true
	default:
		return false
	}
}

func (p *Plugin) handleAuthIdentifier() (any, error) {
	return rpcIdentifierResponse{
		Identifier:          ProviderID,
		Identifiers:         []string{ProviderID},
		CredentialProviders: []string{"gemini-web2api", "gemini-web", "geminiweb2api"},
	}, nil
}

func (p *Plugin) handleAuthParse(raw []byte) (any, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode auth.parse request: %w", err)
	}

	var payload AuthFilePayload
	if len(req.RawJSON) > 0 {
		_ = json.Unmarshal(req.RawJSON, &payload)
	}

	fileName := strings.ToLower(strings.TrimSpace(req.FileName))
	baseName := strings.ToLower(filepath.Base(fileName))
	slashPath := strings.ToLower(filepath.ToSlash(req.Path))
	switch baseName {
	case "account.json", "storage-state.json", "runtime-state.json", "cooldowns.json", "requests.db":
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	if strings.Contains(slashPath, "/accounts/") || strings.Contains(filepath.ToSlash(fileName), "/") {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}

	providerMatched := isGeminiWebProvider(payload.Type) ||
		isGeminiWebProvider(payload.Provider) ||
		strings.HasPrefix(baseName, "gemini-web2api-") ||
		strings.HasPrefix(baseName, "gemini-web-")

	if !providerMatched {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}

	authData, err := p.parseAndSyncAuthPayload(p.ctx, req.FileName, req.RawJSON, payload)
	if err != nil {
		return nil, err
	}
	return pluginapi.AuthParseResponse{
		Handled: true,
		Auth:    authData,
	}, nil
}

func (p *Plugin) parseAndSyncAuthPayload(ctx context.Context, fileName string, rawJSON []byte, payload AuthFilePayload) (pluginapi.AuthData, error) {
	_ = ctx
	accountID := strings.ToLower(strings.TrimSpace(payload.AccountID))
	if accountID == "" {
		accountID = strings.ToLower(strings.TrimSpace(payload.Email))
	}
	if accountID == "" {
		accountID = strings.ToLower(strings.TrimSpace(payload.Label))
	}

	prevDisabled := payload.Disabled
	prevServiceStopped := payload.ServiceStopped
	prevTier := payload.Tier
	prevState := payload.State
	prevEmail := payload.Email
	state, hasState := extractStorageStateFromPayload(rawJSON, payload)
	rt, rtErr := p.Runtime()
	serviceStopped := p.serviceStopped.Load()
	inStartupWindow := false
	if regNs := p.lastRegisterNs.Load(); regNs > 0 && time.Since(time.Unix(0, regNs)) < 5*time.Second {
		inStartupWindow = true
	}

	var importedStatus *balancer.AccountStatus
	wantEnabled := true
	if serviceStopped {
		wantEnabled = false
	} else if prevServiceStopped || inStartupWindow {
		wantEnabled = true
	} else if prevDisabled != nil {
		wantEnabled = !*prevDisabled
	}

	if rtErr == nil && hasState {
		wantDis := !wantEnabled
		payloadWithWant := payload
		payloadWithWant.Disabled = &wantDis
		if id, err := p.upsertAccountInRuntime(rt, accountID, payloadWithWant, state); err == nil && id != "" {
			accountID = strings.ToLower(strings.TrimSpace(id))
			payload.AccountID = accountID
			importedStatus = findPoolAccountStatus(rt, accountID)
			if importedStatus != nil {
				if importedStatus.Enabled != wantEnabled {
					_ = rt.Accounts.SetEnabled(importedStatus.AccountID, wantEnabled)
					importedStatus = findPoolAccountStatus(rt, importedStatus.AccountID)
				}
				if importedStatus != nil {
					payload.Tier = importedStatus.Tier
					payload.State = importedStatus.State
					payload.Source = importedStatus.Source
				}
			}
		}
	}

	if accountID == "" {
		base := strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
		base = strings.TrimPrefix(base, "gemini-web2api-")
		base = strings.TrimPrefix(base, "gemini-web-")
		if base != "" {
			accountID = strings.ToLower(strings.TrimSpace(base))
		}
	}

	if rtErr == nil && importedStatus == nil && accountID != "" {
		importedStatus = findPoolAccountStatus(rt, accountID)
	}
	disabled := false
	if serviceStopped {
		disabled = true
	} else if prevServiceStopped || inStartupWindow {
		disabled = false
		if rtErr == nil && importedStatus != nil && !importedStatus.Enabled {
			_ = rt.Accounts.SetEnabled(importedStatus.AccountID, true)
			importedStatus = findPoolAccountStatus(rt, importedStatus.AccountID)
		}
	} else if prevDisabled != nil {
		disabled = *prevDisabled
		if rtErr == nil && importedStatus != nil {
			wantEnabled := !disabled
			if importedStatus.Enabled != wantEnabled {
				_ = rt.Accounts.SetEnabled(importedStatus.AccountID, wantEnabled)
				importedStatus = findPoolAccountStatus(rt, importedStatus.AccountID)
			}
		}
	} else if importedStatus != nil {
		disabled = !importedStatus.Enabled
	}
	if importedStatus != nil {
		if importedStatus.Tier != "" {
			payload.Tier = formatTierDisplay(importedStatus.Tier)
		}
		if importedStatus.State != "" {
			payload.State = importedStatus.State
		}
		if importedStatus.Source != "" {
			payload.Source = importedStatus.Source
		}
	}
	payload.Tier = formatTierDisplay(payload.Tier)
	if rtErr == nil && accountID != "" {
		if client := rt.Pool.Client(accountID); client != nil && client.Email() != "" {
			realEmail := strings.ToLower(strings.TrimSpace(client.Email()))
			payload.Email = realEmail
			if payload.Label == "" || payload.Label == accountID {
				payload.Label = realEmail
			}
		}
	}

	label := strings.TrimSpace(payload.Label)
	if label == "" {
		label = strings.TrimSpace(payload.Email)
	}
	if label == "" {
		label = accountID
	}
	if label == "" {
		label = "gemini-web2api-pool"
	}

	authID := fileName
	if authID == "" {
		if accountID != "" {
			authID = "gemini-web2api-" + sanitizeFileComponent(accountID) + ".json"
		} else {
			authID = "gemini-web2api-pool.json"
		}
	}

	payload.Type = ProviderID
	payload.Provider = ProviderID
	payload.AccountID = accountID
	payload.Label = label
	payload.Disabled = &disabled
	payload.ServiceStopped = serviceStopped
	if hasState && payload.StorageState == nil {
		payload.StorageState = &state
	}
	if payload.UpdatedAt == "" {
		payload.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	storageBytes, err := json.Marshal(payload)
	if err != nil {
		storageBytes = rawJSON
	}

	metadata := map[string]any{
		"type":       ProviderID,
		"provider":   ProviderID,
		"account_id": accountID,
		"email":      payload.Email,
		"label":      label,
		"disabled":   disabled,
	}
	if payload.Tier != "" {
		metadata["tier"] = payload.Tier
	}
	if payload.State != "" {
		metadata["state"] = payload.State
	}

	attributes := map[string]string{
		"provider":   ProviderID,
		"account_id": accountID,
	}
	if payload.Tier != "" {
		attributes["tier"] = payload.Tier
	}

	authData := pluginapi.AuthData{
		ID:               authID,
		Provider:         ProviderID,
		FileName:         fileName,
		Label:            label,
		Disabled:         disabled,
		ProxyURL:         strings.TrimSpace(payload.Proxy),
		StorageJSON:      storageBytes,
		Metadata:         metadata,
		Attributes:       attributes,
		NextRefreshAfter: time.Now().Add(30 * time.Minute),
	}
	if fileName != "" && (prevDisabled == nil || *prevDisabled != disabled || prevServiceStopped != serviceStopped || prevTier != payload.Tier || prevState != payload.State || prevEmail != payload.Email) {
		_ = writeAuthDataToDefaultHostDir(authData)
	}
	return authData, nil
}

func findPoolAccountStatus(rt *app.PluginRuntime, accountID string) *balancer.AccountStatus {
	if rt == nil || accountID == "" {
		return nil
	}
	status := rt.Pool.Status()
	for i := range status.Accounts {
		if strings.EqualFold(status.Accounts[i].AccountID, accountID) {
			return &status.Accounts[i]
		}
	}
	return nil
}

func extractStorageStateFromPayload(rawJSON []byte, payload AuthFilePayload) (auth.StorageState, bool) {
	if payload.StorageState != nil && len(payload.StorageState.Cookies) > 0 {
		return *payload.StorageState, true
	}
	var direct auth.StorageState
	if len(rawJSON) > 0 && json.Unmarshal(rawJSON, &direct) == nil && len(direct.Cookies) > 0 {
		return direct, true
	}
	return auth.StorageState{}, false
}

func (p *Plugin) upsertAccountInRuntime(
	rt *app.PluginRuntime,
	accountID string,
	payload AuthFilePayload,
	state auth.StorageState,
) (string, error) {
	if rt == nil {
		return "", fmt.Errorf("runtime not initialized")
	}
	if state.Metadata.ID == "" {
		if accountID != "" {
			state.Metadata.ID = accountID
		} else if payload.Email != "" {
			state.Metadata.ID = chromeauth.AccountID(payload.Email)
		}
	}
	if state.Metadata.Proxy == "" && strings.TrimSpace(payload.Proxy) != "" {
		state.Metadata.Proxy = strings.TrimSpace(payload.Proxy)
	}
	if payload.Disabled != nil {
		state.Metadata.Disabled = *payload.Disabled
	}

	targetID := chromeauth.AccountID(state.Metadata.ID)
	if targetID != "" {
		if actualID, stateName, _, exists := rt.Pool.LookupAccount(targetID); exists && stateName != balancer.StateAuthRequired {
			if stateName == balancer.StateInitializing {
				rt.Accounts.WaitFirstInit(p.ctx, actualID)
			}
			return actualID, nil
		}
		if existing := findPoolAccountStatus(rt, targetID); existing != nil && existing.State == balancer.StateAuthRequired {
			if _, existingState, err := rt.ReadAccountStorageState(targetID); err == nil && existingState != nil {
				if sameCoreAuthCookies(*existingState, state) {
					return existing.AccountID, nil
				}
			}
		}
	}

	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return "", err
	}
	return rt.Accounts.ImportState(raw)
}

func sameCoreAuthCookies(a, b auth.StorageState) bool {
	cookieMap := func(st auth.StorageState) map[string]string {
		m := make(map[string]string, len(st.Cookies))
		for _, c := range st.Cookies {
			m[c.Name] = c.Value
		}
		return m
	}
	ma := cookieMap(a)
	mb := cookieMap(b)
	for _, name := range []string{"SAPISID", "__Secure-1PSID", "__Secure-3PSID", "SID"} {
		if ma[name] != mb[name] {
			return false
		}
	}
	return ma["SAPISID"] != "" || ma["__Secure-1PSID"] != ""
}

func resolveHostOrigin(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed != "" {
		if parsed, err := url.Parse(trimmed); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
			return parsed.Scheme + "://" + parsed.Host
		}
	}
	return "http://127.0.0.1:8317"
}

func (p *Plugin) handleAuthLoginStart(raw []byte) (any, error) {
	var req rpcAuthLoginStartRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}
	rt, err := p.Runtime()
	if err != nil {
		return nil, err
	}
	initial := make(map[string]struct{})
	for _, acc := range rt.Pool.Status().Accounts {
		initial[strings.ToLower(strings.TrimSpace(acc.AccountID))] = struct{}{}
	}

	stateID := uuid.NewString()
	session := &loginSessionState{
		CreatedAt:       time.Now(),
		InitialAccounts: initial,
		Provider:        ProviderID,
		CallbackID:      req.HostCallbackID,
	}
	now := time.Now()
	p.loginStateMu.Lock()
	for k, v := range p.loginStates {
		if v == nil || now.Sub(v.CreatedAt) > 15*time.Minute {
			delete(p.loginStates, k)
		}
	}
	p.loginStates[stateID] = session
	p.loginStateMu.Unlock()

	proxyURL := strings.TrimSpace(p.appCfg.ProxyURL)
	if proxyURL == "" {
		proxyURL = strings.TrimSpace(req.Host.ProxyURL)
	}
	hostCallbackID := req.HostCallbackID

	// Immediately launch the local Chrome Incognito window in the background when the user clicks "Start Login" in CPA/ECPA
	if os.Getenv("GEMINI_WEB2API_NO_AUTO_BROWSER") == "" {
		if chromeBin := chromeauth.FindLocalChromeBinary(); chromeBin != "" && p.chromeLogin.TryLock() {
			go func(state string, proxy string, cbID string) {
				defer p.chromeLogin.Unlock()
				defer func() {
					if r := recover(); r != nil {
						p.loginStateMu.Lock()
						if st, ok := p.loginStates[state]; ok && st != nil {
							st.Done = true
							st.ErrorMessage = fmt.Sprintf("login panic: %v", r)
						}
						p.loginStateMu.Unlock()
					}
				}()

				res, loginErr := chromeauth.RunIncognitoLogin(p.ctx, chromeauth.IsolatedLoginRequest{
					ProxyURL: proxy,
					Timeout:  5 * time.Minute,
				})
				var importedID string
				if loginErr == nil {
					if stateBytes, mErr := json.MarshalIndent(res.StorageState, "", "  "); mErr == nil {
						importedID, loginErr = rt.Accounts.ImportState(stateBytes)
					} else {
						loginErr = mErr
					}
				}

				p.loginStateMu.Lock()
				if st, ok := p.loginStates[state]; ok && st != nil {
					st.Done = true
					if loginErr != nil {
						st.ErrorMessage = loginErr.Error()
					} else {
						st.AccountID = importedID
						st.Email = res.Email
					}
				}
				p.loginStateMu.Unlock()

				if loginErr == nil && importedID != "" {
					_ = p.SyncAccountIDToHostAuth(cbID, importedID)
				}
			}(stateID, proxyURL, hostCallbackID)
		}
	}

	// origin 优先取浏览器上报缓存：宿主给的 BaseURL 恒为 127.0.0.1:8317
	// （managementCallbackURL 硬编码 loopback），直接用它会让控制台弹出的
	// 授权链接在用户浏览器里打不开。
	origin := effectiveLoginOrigin(nil, req.BaseURL)
	if origin == "" {
		origin = resolveHostOrigin(req.BaseURL)
	}
	loginURL := origin + loginBridgePath
	return pluginapi.AuthLoginStartResponse{
		Provider:  ProviderID,
		URL:       loginURL,
		State:     stateID,
		ExpiresAt: time.Now().Add(10 * time.Minute),
		Metadata: map[string]any{
			"instructions":  "打开 " + loginURL + " 完成一键授权：复制配对码，用浏览器授权助手扩展推送 Google 会话 Cookie，或在该页手动粘贴 Cookie。若本机装有 Chrome，也可直接用弹出的无痕窗口登录。",
			"resource_url":  loginURL,
			"pair_code":     loginPairPrefix + "|" + origin + "|" + stateID,
			"extension_url": origin + loginExtensionPath,
		},
	}, nil
}

func (p *Plugin) handleAuthLoginPoll(raw []byte) (any, error) {
	var req rpcAuthLoginPollRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	rt, err := p.Runtime()
	if err != nil {
		return nil, err
	}
	stateKey := strings.TrimSpace(req.State)
	p.loginStateMu.Lock()
	session, exists := p.loginStates[stateKey]
	var doneAccountID string
	var doneErr string
	var initial map[string]struct{}
	if exists && session != nil {
		initial = session.InitialAccounts
		if session.Done {
			doneAccountID = session.AccountID
			doneErr = session.ErrorMessage
		}
	}
	p.loginStateMu.Unlock()

	if doneAccountID != "" {
		if authData, err := p.buildAuthDataForAccountID(rt, doneAccountID); err == nil {
			p.loginStateMu.Lock()
			delete(p.loginStates, stateKey)
			p.loginStateMu.Unlock()
			return pluginapi.AuthLoginPollResponse{
				Status:  "success",
				Message: "Gemini Web account logged in via Chrome Incognito: " + doneAccountID,
				Auth:    authData,
			}, nil
		}
	}

	for _, acc := range rt.Pool.Status().Accounts {
		id := strings.ToLower(strings.TrimSpace(acc.AccountID))
		if exists && initial != nil {
			if _, wasPresent := initial[id]; wasPresent {
				continue
			}
		}
		authData, err := p.buildAuthDataForAccountID(rt, acc.AccountID)
		if err != nil {
			continue
		}
		p.loginStateMu.Lock()
		delete(p.loginStates, stateKey)
		p.loginStateMu.Unlock()
		return pluginapi.AuthLoginPollResponse{
			Status:  "success",
			Message: "Gemini Web account imported: " + acc.AccountID,
			Auth:    authData,
		}, nil
	}

	if doneErr != "" {
		p.loginStateMu.Lock()
		delete(p.loginStates, stateKey)
		p.loginStateMu.Unlock()
		return pluginapi.AuthLoginPollResponse{
			Status:  "error",
			Message: doneErr,
		}, nil
	}

	return pluginapi.AuthLoginPollResponse{
		Status:  "pending",
		Message: "Waiting for Google Gemini Web login in the local Chrome Incognito window...",
	}, nil
}

func (p *Plugin) handleAuthRefresh(raw []byte) (any, error) {
	var req rpcAuthRefreshRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	rt, err := p.Runtime()
	if err != nil {
		return nil, err
	}

	accountID := extractAccountIDFromAuth(req.StorageJSON, req.Metadata, req.Attributes)
	if accountID == "" {
		accs := rt.Pool.Status().Accounts
		if len(accs) > 0 {
			accountID = accs[0].AccountID
		}
	}
	if accountID == "" {
		return pluginapi.AuthRefreshResponse{
			Auth: pluginapi.AuthData{
				ID:               req.AuthID,
				Provider:         ProviderID,
				StorageJSON:      req.StorageJSON,
				Metadata:         req.Metadata,
				Attributes:       req.Attributes,
				NextRefreshAfter: time.Now().Add(30 * time.Minute),
			},
			NextRefreshAfter: time.Now().Add(30 * time.Minute),
		}, nil
	}

	_ = rt.Accounts.Verify(accountID)
	if authData, err := p.buildAuthDataForAccountID(rt, accountID); err == nil {
		if req.AuthID != "" {
			authData.ID = req.AuthID
		}
		return pluginapi.AuthRefreshResponse{
			Auth:             authData,
			NextRefreshAfter: authData.NextRefreshAfter,
		}, nil
	}

	return pluginapi.AuthRefreshResponse{
		Auth: pluginapi.AuthData{
			ID:               req.AuthID,
			Provider:         ProviderID,
			StorageJSON:      req.StorageJSON,
			Metadata:         req.Metadata,
			Attributes:       req.Attributes,
			NextRefreshAfter: time.Now().Add(30 * time.Minute),
		},
		NextRefreshAfter: time.Now().Add(30 * time.Minute),
	}, nil
}

func (p *Plugin) buildAuthDataForAccountID(rt *app.PluginRuntime, accountID string) (pluginapi.AuthData, error) {
	if rt == nil {
		return pluginapi.AuthData{}, fmt.Errorf("runtime not initialized")
	}
	accStatus := findPoolAccountStatus(rt, accountID)
	if accStatus != nil && accStatus.State == balancer.StateInitializing {
		rt.Accounts.WaitFirstInit(p.ctx, accStatus.AccountID)
		accStatus = findPoolAccountStatus(rt, accountID)
	}
	cleanID := strings.ToLower(strings.TrimSpace(accountID))
	enabled := true
	var proxyURL, tier, stateStr, sourceStr, profileStr string
	if accStatus != nil {
		cleanID = strings.ToLower(strings.TrimSpace(accStatus.AccountID))
		enabled = accStatus.Enabled
		proxyURL = accStatus.Proxy
		tier = formatTierDisplay(accStatus.Tier)
		stateStr = accStatus.State
		sourceStr = accStatus.Source
		profileStr = accStatus.Profile
	}

	email := cleanID
	if client := rt.Pool.Client(accountID); client != nil && client.Email() != "" {
		email = strings.ToLower(strings.TrimSpace(client.Email()))
	}

	fileName := fmt.Sprintf("gemini-web2api-%s.json", sanitizeFileComponent(cleanID))
	serviceStopped := p.serviceStopped.Load()
	disabled := !enabled || serviceStopped

	payload := AuthFilePayload{
		Type:           ProviderID,
		Provider:       ProviderID,
		AccountID:      cleanID,
		Email:          email,
		Label:          email,
		Disabled:       &disabled,
		ServiceStopped: serviceStopped,
		Source:         sourceStr,
		Profile:        profileStr,
		Proxy:          proxyURL,
		Tier:           tier,
		State:          stateStr,
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
	}

	if _, st, err := rt.ReadAccountStorageState(accountID); err == nil && st != nil && len(st.Cookies) > 0 {
		payload.StorageState = st
		if st.Metadata.Proxy != "" {
			payload.Proxy = st.Metadata.Proxy
			proxyURL = st.Metadata.Proxy
		}
	}

	storageBytes, err := json.Marshal(payload)
	if err != nil {
		return pluginapi.AuthData{}, err
	}

	return pluginapi.AuthData{
		ID:               fileName,
		Provider:         ProviderID,
		FileName:         fileName,
		Label:            email,
		Disabled:         disabled,
		ProxyURL:         proxyURL,
		StorageJSON:      storageBytes,
		NextRefreshAfter: time.Now().Add(30 * time.Minute),
		Metadata: map[string]any{
			"type":       ProviderID,
			"provider":   ProviderID,
			"account_id": cleanID,
			"email":      email,
			"label":      email,
			"tier":       tier,
			"state":      stateStr,
			"source":     sourceStr,
			"disabled":   disabled,
		},
		Attributes: map[string]string{
			"provider":   ProviderID,
			"account_id": cleanID,
			"tier":       tier,
		},
	}, nil
}
func formatTierDisplay(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "pro":
		return "Pro"
	case "ultra":
		return "Ultra"
	case "plus":
		return "Plus"
	case "free":
		return "Free"
	default:
		return strings.TrimSpace(tier)
	}
}


func (p *Plugin) SyncAccountIDToHostAuth(callbackID string, accountID string) error {
	if !boolOrDefault(p.Config().SyncAuthFiles, true) {
		return nil
	}
	rt, err := p.Runtime()
	if err != nil {
		return err
	}
	authData, err := p.buildAuthDataForAccountID(rt, accountID)
	if err != nil {
		return err
	}
	req := map[string]any{
		"host_callback_id": strings.TrimSpace(callbackID),
		"name":             authData.FileName,
		"json":             json.RawMessage(authData.StorageJSON),
		"metadata":         authData.Metadata,
		"attributes":       authData.Attributes,
	}
	hostSaved := false
	if _, hostErr := p.callHost(pluginabi.MethodHostAuthSave, req); hostErr == nil {
		hostSaved = true
	}
	diskErr := writeAuthDataToDefaultHostDir(authData)
	if hostSaved || diskErr == nil {
		return nil
	}
	return diskErr
}

func writeAuthDataToDefaultHostDir(authData pluginapi.AuthData) error {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	authDir := filepath.Join(home, ".cli-proxy-api")
	if info, err := os.Stat(authDir); err != nil || !info.IsDir() {
		return fmt.Errorf("host auth dir %s not found", authDir)
	}
	target := filepath.Join(authDir, filepath.Base(authData.FileName))
	return os.WriteFile(target, authData.StorageJSON, 0o600)
}

func (p *Plugin) SyncAllAccountsToHostDir(rt *app.PluginRuntime) {
	if rt == nil {
		return
	}
	for _, acc := range rt.Pool.Status().Accounts {
		if authData, err := p.buildAuthDataForAccountID(rt, acc.AccountID); err == nil {
			_ = writeAuthDataToDefaultHostDir(authData)
		}
	}
}

func (p *Plugin) setHostAuthFilesDisabled(rt *app.PluginRuntime, forceDisabled bool) {
	enabledByAccount := make(map[string]bool)
	if rt != nil {
		for _, acc := range rt.Pool.Status().Accounts {
			enabledByAccount[strings.ToLower(strings.TrimSpace(acc.AccountID))] = acc.Enabled
			if authData, bErr := p.buildAuthDataForAccountID(rt, acc.AccountID); bErr == nil {
				_ = writeAuthDataToDefaultHostDir(authData)
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return
	}
	authDir := filepath.Join(home, ".cli-proxy-api")
	entries, err := os.ReadDir(authDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".json") || (!strings.HasPrefix(lower, "gemini-web2api-") && !strings.HasPrefix(lower, "gemini-web-")) {
			continue
		}
		fullPath := filepath.Join(authDir, name)
		raw, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
			continue
		}
		accID, _ := doc["account_id"].(string)
		accID = strings.ToLower(strings.TrimSpace(accID))
		targetDisabled := forceDisabled
		if !forceDisabled {
			if accEnabled, ok := enabledByAccount[accID]; ok {
				targetDisabled = !accEnabled
			} else {
				targetDisabled = false
			}
		}
		curDisabled, hasDisabled := doc["disabled"].(bool)
		curStopped, _ := doc["service_stopped"].(bool)
		if hasDisabled && curDisabled == targetDisabled && curStopped == forceDisabled {
			continue
		}
		doc["disabled"] = targetDisabled
		if forceDisabled {
			doc["service_stopped"] = true
		} else {
			delete(doc, "service_stopped")
		}
		doc["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		if updated, err := json.Marshal(doc); err == nil {
			_ = os.WriteFile(fullPath, updated, 0o600)
		}
	}
}

func (p *Plugin) RemoveAccountFromHostAuth(accountID string) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return
	}
	fileName := fmt.Sprintf("gemini-web2api-%s.json", sanitizeFileComponent(strings.ToLower(strings.TrimSpace(accountID))))
	_ = os.Remove(filepath.Join(home, ".cli-proxy-api", fileName))
}

func extractAccountIDFromAuth(storageJSON []byte, metadata map[string]any, attributes map[string]string) string {
	if attributes != nil {
		if id := strings.ToLower(strings.TrimSpace(attributes["account_id"])); id != "" {
			return id
		}
		if id := strings.ToLower(strings.TrimSpace(attributes["email"])); id != "" {
			return id
		}
	}
	if metadata != nil {
		if id, ok := metadata["account_id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.ToLower(strings.TrimSpace(id))
		}
		if id, ok := metadata["email"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.ToLower(strings.TrimSpace(id))
		}
	}
	if len(storageJSON) > 0 {
		var payload AuthFilePayload
		if json.Unmarshal(storageJSON, &payload) == nil {
			if id := strings.ToLower(strings.TrimSpace(payload.AccountID)); id != "" {
				return id
			}
			if id := strings.ToLower(strings.TrimSpace(payload.Email)); id != "" {
				return id
			}
			if id := strings.ToLower(strings.TrimSpace(payload.Label)); id != "" {
				return id
			}
		}
	}
	return ""
}

func sanitizeFileComponent(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '@' || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "account"
	}
	return b.String()
}
