// oauth.go implements the AuthProvider login flow for WorkBuddy AI (International).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func newLoginClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: sharedHTTPClient().Transport,
		Jar:       jar,
	}
}

func doJSON(client *http.Client, method, fullURL string, headers func(*http.Request), body io.Reader) (json.RawMessage, int, error) {
	req, err := http.NewRequest(method, fullURL, body)
	if err != nil {
		return nil, 0, err
	}
	if headers != nil {
		headers(req)
	} else {
		commonHeaders(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, fmt.Errorf("http_error: upstream %d", resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("http_error: upstream redirect %d (location: %s)", resp.StatusCode, resp.Header.Get("Location"))
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("parse failed: %w", err)
	}
	if env.Code != 0 {
		return nil, resp.StatusCode, fmt.Errorf("code=%d msg=%s", env.Code, truncateRedacted(env.Msg, 120))
	}
	return env.Data, resp.StatusCode, nil
}

func handleStartLogin(raw []byte) ([]byte, error) {
	client := newLoginClient()
	data, _, err := doJSON(client, http.MethodPost, endpointAuthState, nil, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("auth state failed: %w", err)
	}
	var st authStateData
	_ = json.Unmarshal(data, &st)
	if st.State == "" || st.AuthURL == "" {
		return nil, fmt.Errorf("auth state: missing state or authUrl — please restart the login flow")
	}
	loginStates.Store(st.State, &loginCtx{client: client, expires: time.Now().Add(loginTTL)})
	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  providerName,
		URL:       st.AuthURL,
		State:     st.State,
		ExpiresAt: time.Now().Add(loginTTL).UTC(),
		Metadata:  map[string]any{"logo": pluginLogoURL},
	})
}

// handlePollLogin 处理 OAuth 轮询：待授权完成拿到 token 后，先尽力补齐账号注册
// 链路（注册地 + 云账号 + 试用包），再返回凭证。补注册失败不阻断授权成功。
// [参数] raw 宿主传入的 AuthLoginPollRequest JSON（含 state）
// [返回] AuthLoginPollResponse 信封；state 缺失/过期/上游异常时返回错误
// 最近修改时间：2026-10-15 18:20:00；改动原因：新增授权后补注册钩子
func handlePollLogin(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginPollRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	state := strings.TrimSpace(req.State)
	if state == "" {
		return nil, fmt.Errorf("poll: empty state")
	}
	v, ok := loginStates.Load(state)
	if !ok {
		return nil, fmt.Errorf("poll: unknown state (restart login) — the login session was lost; please re-initiate login")
	}
	lc := v.(*loginCtx)
	if time.Now().After(lc.expires) {
		loginStates.Delete(state)
		return nil, fmt.Errorf("poll: state expired (restart login) — login timed out after 5 minutes; please try again")
	}

	tokRaw, status, errTok := doJSON(lc.client, http.MethodGet, endpointAuthToken+state, nil, nil)
	if errTok != nil {
		if status == 0 || status >= 500 {
			loginStates.Delete(state)
			return nil, fmt.Errorf("poll: network error: %w — upstream returned HTTP %d; please check your network and try again", errTok, status)
		}
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: "waiting for browser login completion",
		})
	}
	var tok tokenData
	if err := json.Unmarshal(tokRaw, &tok); err != nil || tok.AccessToken == "" {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: "waiting for browser login completion",
		})
	}

	var acct accountData
	acctHeaders := func(r *http.Request) {
		commonHeaders(r)
		r.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	}
	if acctRaw, _, errAcct := doJSON(lc.client, http.MethodGet, endpointLoginAcct+state, acctHeaders, nil); errAcct == nil {
		_ = json.Unmarshal(acctRaw, &acct)
	}

	sa := &storedAuth{
		Auth: storedTokens{
			AccessToken:  tok.AccessToken,
			RefreshToken: tok.RefreshToken,
			ExpiresAt:    time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix(),
			Domain:       "www.workbuddy.ai",
		},
		Account: storedAccount{
			UID:          acct.UID,
			EnterpriseID: acct.EnterpriseID,
			Nickname:     acct.Nickname,
		},
	}

	loginStates.Delete(state)

	// 授权成功不等于注册完成：浏览器登录页在 platform=CLI 下会跳过注册地选择，
	// 账号将以 areaInfoComplete=false 落盘且拿不到额度池。这里在返回凭证前尽力
	// 补齐注册链路；失败只记录状态、不阻断授权（授权本身已成功，落盘仍然有效）。
	message := "login successful"
	if res, err := ensureAccountRegistered(sa); err != nil {
		log.Printf("[workbuddy-ai] post-login registration incomplete for %s: %v", sa.Account.UID, err)
		message = "login successful; registration incomplete: " + err.Error()
	} else if ok, _ := res["registered"].(bool); ok {
		if region, _ := res["region"].(string); region != "" {
			message = "login successful; registered (" + region + ")"
		}
	}

	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusSuccess,
		Message: message,
		Auth:    toAuthData(sa),
	})
}

func handleRefreshAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthRefreshRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	sa, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	if sa.Auth.RefreshToken == "" {
		return nil, fmt.Errorf("refresh: missing refreshToken in storage")
	}

	refreshBody, _ := json.Marshal(map[string]string{"refreshToken": sa.Auth.RefreshToken})
	refreshHeaders := func(r *http.Request) {
		commonHeaders(r)
		r.Header.Set("Authorization", "Bearer "+sa.Auth.AccessToken)
		r.Header.Set("X-Refresh-Token", sa.Auth.RefreshToken)
	}

	httpReq, err := http.NewRequest(http.MethodPost, endpointTokenRefresh, bytes.NewReader(refreshBody))
	if err != nil {
		return nil, err
	}
	refreshHeaders(httpReq)
	resp, err := hostHTTPDo(httpReq)
	if err != nil {
		return nil, fmt.Errorf("refresh: host.http.do failed: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("refresh: upstream %d (body: %s)", resp.StatusCode, truncateRedacted(string(resp.Body), 120))
	}
	var env apiEnvelope
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, fmt.Errorf("refresh: bad envelope: %w", err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("refresh: upstream code=%d msg=%s", env.Code, truncateRedacted(env.Msg, 120))
	}

	var tok tokenData
	if err := json.Unmarshal(env.Data, &tok); err != nil {
		return nil, fmt.Errorf("refresh: bad token payload: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("refresh: empty accessToken returned")
	}

	sa.Auth.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		sa.Auth.RefreshToken = tok.RefreshToken
	}
	sa.Auth.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix()
	sa.Auth.Domain = "www.workbuddy.ai"

	// 刷新回传必须沿用盘上原有的禁用状态与本机制标记。固定回 disabled:false
	// 会让禁用中的号被一次后台 token 刷新静默解封，完全绕开 10 分钟探活。
	ad := toAuthDataOpts(sa, nil, parseDisabledFromAuthJSON(req.StorageJSON))
	carryFailoverKeysIntoMetadata(ad.Metadata, req.StorageJSON)
	ad.FileName = "" // 由宿主按原路径回填，避免改名产生重复条目
	ad.ID = ""
	return okEnvelope(pluginapi.AuthRefreshResponse{
		Auth: ad,
	})
}

// preserveExpiry reuses the previous token's expiresAt when the refresh
// response omits expiresIn.
func preserveExpiry(newExpiry, oldExpiry int64) int64 {
	if newExpiry > 0 {
		return newExpiry
	}
	return oldExpiry
}
