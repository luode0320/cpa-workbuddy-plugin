// oauth.go implements the AuthProvider login flow for WorkBuddy AI (International).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status: pluginapi.AuthLoginStatusSuccess,
		Auth:   toAuthData(sa),
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

	return okEnvelope(pluginapi.AuthRefreshResponse{
		Auth: toAuthData(sa),
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
