package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestMain_RegistrationAndCapabilities(t *testing.T) {
	if os.Getenv("ZCODE_SENTINEL_FAILURE") == "1" {
		t.Fatalf("SENTINEL_FAILURE: verifying that tests actually compile and run")
	}

	raw, err := handleMethod(pluginabi.MethodPluginRegister, []byte(`{}`))
	if err != nil {
		t.Fatalf("handleMethod register failed: %v", err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope failed: %v", err)
	}
	if !env.OK {
		t.Fatalf("registration envelope not OK")
	}

	var reg registration
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatalf("unmarshal registration failed: %v", err)
	}

	if reg.Metadata.Name != providerName {
		t.Fatalf("expected name %s, got %s", providerName, reg.Metadata.Name)
	}
	if reg.Metadata.Version != version {
		t.Fatalf("expected version %s, got %s", version, reg.Metadata.Version)
	}
	// schema 6 起管理响应保持原始 JSON；旧协议会把 OAuth URL 的 & 转成 &amp;。
	if reg.SchemaVersion < 6 {
		t.Fatalf("OAuth management responses require schema >= 6 to preserve URL query parameters, got %d", reg.SchemaVersion)
	}
	if !reg.Capabilities.AuthProvider {
		t.Fatalf("expected AuthProvider capability to be true for OAuth login")
	}
	if reg.Capabilities.ExecutorModelScope != pluginapi.ExecutorModelScopeOAuth {
		t.Fatalf("expected ExecutorModelScope to be OAuth, got %+v", reg.Capabilities.ExecutorModelScope)
	}
	if !reg.Capabilities.UsagePlugin {
		t.Fatalf("expected UsagePlugin capability to be true")
	}
	if !reg.Capabilities.ModelProvider || !reg.Capabilities.Executor || !reg.Capabilities.Scheduler || !reg.Capabilities.ManagementAPI {
		t.Fatalf("missing required capabilities: %+v", reg.Capabilities)
	}
}

// mockZaiCLIServer 启动一个模拟官方 CLI OAuth 端点（init + poll）的测试服务器。
// pollReady 控制 poll 返回 pending 还是 ready。
func mockZaiCLIServer(t *testing.T, pollReady *bool) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/init"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
				return
			}
			var req zaiCLIInitRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Provider != "zai" {
				http.Error(w, "invalid provider", http.StatusBadRequest)
				return
			}
			resp := zaiCLIInitResponse{Code: 0}
			resp.Data.FlowID = "mock-flow-id-12345"
			resp.Data.PollToken = "mock-poll-token-67890"
			resp.Data.AuthorizeURL = "https://chat.z.ai/api/oauth/authorize?client_id=" + zaiOAuthClientID +
				"&redirect_uri=https://zcode.z.ai/api/v1/oauth/cli/callback/zai&state=mock-state-abcdef&response_type=code"
			resp.Data.ExpiresAt = time.Now().Add(15 * time.Minute).Unix()
			resp.Data.PollIntervalSec = 2
			_ = json.NewEncoder(w).Encode(resp)

		case strings.Contains(r.URL.Path, "/poll/"):
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
				return
			}
			resp := zaiCLIPollResponse{Code: 0}
			if pollReady == nil || !*pollReady {
				resp.Data.Status = "pending"
				_ = json.NewEncoder(w).Encode(resp)
				return
			}
			resp.Data.Status = "ready"
			resp.Data.Token = "mock_jwt_token_99999"
			resp.Data.Zai.AccessToken = "mock_access_token_88888"
			resp.Data.Zai.TokenType = "Bearer"
			resp.Data.User = &ZaiUserInfo{
				UserID: "u_test_123",
				Name:   "测试用户",
				Email:  "test@z.ai",
			}
			_ = json.NewEncoder(w).Encode(resp)

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	return ts
}

func TestMain_OAuthLoginFlow(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "zcode_oauth_test_*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	setAccountsDir(tempDir)

	pollReady := false
	ts := mockZaiCLIServer(t, &pollReady)
	defer ts.Close()
	mockOAuthCLIInitURL = ts.URL + "/init"
	mockOAuthCLIPollURL = ts.URL + "/poll/"
	defer func() { mockOAuthCLIInitURL = ""; mockOAuthCLIPollURL = "" }()

	// 1. StartLogin：必须返回官方 CLI 签发的 authorize_url（含服务端注册回调）
	rawStart, err := handleMethod(pluginabi.MethodAuthLoginStart, nil)
	if err != nil {
		t.Fatalf("handleMethod AuthLoginStart failed: %v", err)
	}
	var envStart envelope
	if err := json.Unmarshal(rawStart, &envStart); err != nil || !envStart.OK {
		t.Fatalf("unmarshal AuthLoginStart failed: %v", err)
	}
	var startResp pluginapi.AuthLoginStartResponse
	if err := json.Unmarshal(envStart.Result, &startResp); err != nil {
		t.Fatalf("unmarshal startResp failed: %v", err)
	}
	if startResp.Provider != providerName || startResp.State == "" {
		t.Fatalf("unexpected startResp: %+v", startResp)
	}
	if !strings.HasPrefix(startResp.URL, "https://chat.z.ai/api/oauth/authorize?") {
		t.Fatalf("expected official CLI authorize URL, got %s", startResp.URL)
	}
	if !strings.Contains(startResp.URL, "client_id=client_P8X5CMWmlaRO9gyO-KSqtg") {
		t.Fatalf("expected client_id in auth URL, got %s", startResp.URL)
	}
	// 关键回归：回调必须是官方服务端注册地址，绝不能是本地地址（否则被白名单拒绝）
	startURL, errParseStart := url.Parse(startResp.URL)
	if errParseStart != nil {
		t.Fatalf("parse start URL failed: %v", errParseStart)
	}
	if gotRedirect := startURL.Query().Get("redirect_uri"); gotRedirect != "https://zcode.z.ai/api/v1/oauth/cli/callback/zai" {
		t.Fatalf("expected server-registered CLI callback redirect_uri, got %q", gotRedirect)
	}
	if strings.Contains(startResp.URL, "localhost") || strings.Contains(startResp.URL, "127.0.0.1") {
		t.Fatalf("auth URL must not use a local redirect_uri (rejected by whitelist): %s", startResp.URL)
	}
	if startURL.Query().Get("response_type") != "code" {
		t.Fatalf("expected response_type=code in auth URL, got %s", startResp.URL)
	}
	if startURL.Query().Get("state") != startResp.State {
		t.Fatalf("expected state in auth URL, got %s", startResp.URL)
	}

	// 2. Poll (pending)
	pollBody, _ := json.Marshal(pluginapi.AuthLoginPollRequest{State: startResp.State})
	rawPoll, err := handleMethod(pluginabi.MethodAuthLoginPoll, pollBody)
	if err != nil {
		t.Fatalf("handleMethod AuthLoginPoll failed: %v", err)
	}
	var envPoll envelope
	_ = json.Unmarshal(rawPoll, &envPoll)
	var pollResp pluginapi.AuthLoginPollResponse
	_ = json.Unmarshal(envPoll.Result, &pollResp)
	if pollResp.Status != pluginapi.AuthLoginStatusPending {
		t.Fatalf("expected pending status, got %s", pollResp.Status)
	}

	// 3. 模拟用户在浏览器完成授权（poll 转为 ready）
	pollReady = true

	// 4. Poll (success)：插件主动轮询官方端点后落盘账号
	rawPoll2, _ := handleMethod(pluginabi.MethodAuthLoginPoll, pollBody)
	var envPoll2 envelope
	_ = json.Unmarshal(rawPoll2, &envPoll2)
	var pollResp2 pluginapi.AuthLoginPollResponse
	_ = json.Unmarshal(envPoll2.Result, &pollResp2)
	if pollResp2.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("expected success status, got %s msg=%s", pollResp2.Status, pollResp2.Message)
	}
	if pollResp2.Auth.Provider != providerName || !strings.Contains(pollResp2.Auth.Label, "测试用户") {
		t.Fatalf("unexpected Auth record: %+v", pollResp2.Auth)
	}

	// 5. 账号已落盘
	list, errList := listAllAuthFiles()
	if errList != nil || len(list) != 1 {
		t.Fatalf("expected 1 persisted account, got %d (err=%v)", len(list), errList)
	}

	// 6. Refresh
	refreshBody, _ := json.Marshal(map[string]any{"auth": pollResp2.Auth})
	rawRefresh, err := handleMethod(pluginabi.MethodAuthRefresh, refreshBody)
	if err != nil {
		t.Fatalf("handleMethod AuthRefresh failed: %v", err)
	}
	var envRefresh envelope
	_ = json.Unmarshal(rawRefresh, &envRefresh)
	if !envRefresh.OK {
		t.Fatalf("expected AuthRefresh OK")
	}
}

// TestMain_OAuthBridgeReceivesHostQuery 复现宿主真实调用契约：宿主把 URL path
// 与 query 分开传递（Path 不含 query，参数在 Query），桥接页必须能读到 state。
func TestMain_OAuthBridgeReceivesHostQuery(t *testing.T) {
	ts := mockZaiCLIServer(t, nil)
	defer ts.Close()
	mockOAuthCLIInitURL = ts.URL + "/init"
	mockOAuthCLIPollURL = ts.URL + "/poll/"
	defer func() { mockOAuthCLIInitURL = ""; mockOAuthCLIPollURL = "" }()

	rawStart, err := handleMethod(pluginabi.MethodAuthLoginStart, nil)
	if err != nil {
		t.Fatalf("start login failed: %v", err)
	}
	var envStart envelope
	_ = json.Unmarshal(rawStart, &envStart)
	var startResp pluginapi.AuthLoginStartResponse
	_ = json.Unmarshal(envStart.Result, &startResp)

	// 宿主契约：Path 不含 query，query 单独在 Query 字段。
	bridgeBody, _ := json.Marshal(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/" + providerName + "/oauth-bridge",
		Query:  url.Values{"state": []string{startResp.State}},
	})
	rawBridge, err := handleMethod(pluginabi.MethodManagementHandle, bridgeBody)
	if err != nil {
		t.Fatalf("management handle failed: %v", err)
	}
	var envBridge envelope
	if err := json.Unmarshal(rawBridge, &envBridge); err != nil || !envBridge.OK {
		t.Fatalf("bridge envelope not OK: %v", err)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(envBridge.Result, &resp); err != nil {
		t.Fatalf("unmarshal management response failed: %v", err)
	}
	body := string(resp.Body)
	if strings.Contains(body, "缺少 state 参数") {
		t.Fatalf("bridge page lost the host-provided state query")
	}
	if !strings.Contains(body, "Z.ai 官方登录") {
		t.Fatalf("bridge page did not render the authorization form, body=%s", body)
	}
}

// TestZaiOAuth_AuthorizeURLUsesRegisteredCallback 锁定 redirect_uri 必须为官方
// 服务端注册地址（回归：曾照抄桌面端 deep-link / 改用本地回调，均被白名单拒绝）。
func TestZaiOAuth_AuthorizeURLUsesRegisteredCallback(t *testing.T) {
	ts := mockZaiCLIServer(t, nil)
	defer ts.Close()
	mockOAuthCLIInitURL = ts.URL + "/init"
	mockOAuthCLIPollURL = ts.URL + "/poll/"
	defer func() { mockOAuthCLIInitURL = ""; mockOAuthCLIPollURL = "" }()

	initResp, err := zaiCLIInit("zai")
	if err != nil {
		t.Fatalf("zaiCLIInit failed: %v", err)
	}
	if initResp.Data.FlowID == "" || initResp.Data.PollToken == "" {
		t.Fatalf("expected flow_id and poll_token, got %+v", initResp.Data)
	}

	u, errParse := url.Parse(initResp.Data.AuthorizeURL)
	if errParse != nil {
		t.Fatalf("parse authorize_url failed: %v", errParse)
	}
	if u.Host != "chat.z.ai" || u.Path != "/api/oauth/authorize" {
		t.Fatalf("unexpected authorize endpoint: %s", initResp.Data.AuthorizeURL)
	}
	q := u.Query()
	if q.Get("client_id") != zaiOAuthClientID {
		t.Fatalf("unexpected client_id: %s", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != "https://zcode.z.ai/api/v1/oauth/cli/callback/zai" {
		t.Fatalf("redirect_uri must be the server-registered CLI callback, got %s", q.Get("redirect_uri"))
	}
	if q.Get("response_type") != "code" || q.Get("state") == "" {
		t.Fatalf("missing response_type/state: %s", initResp.Data.AuthorizeURL)
	}
}

// TestZaiOAuth_PollFailedReturnsError 验证 status=failed 时正确上报失败。
func TestZaiOAuth_PollFailedReturnsError(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "zcode_poll_failed_*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	setAccountsDir(tempDir)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/init") {
			resp := zaiCLIInitResponse{Code: 0}
			resp.Data.FlowID = "failed-flow"
			resp.Data.PollToken = "pt"
			resp.Data.AuthorizeURL = "https://chat.z.ai/api/oauth/authorize?state=failed-state-1&response_type=code"
			resp.Data.ExpiresAt = time.Now().Add(10 * time.Minute).Unix()
			resp.Data.PollIntervalSec = 2
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		resp := zaiCLIPollResponse{Code: 0}
		resp.Data.Status = "failed"
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()
	mockOAuthCLIInitURL = ts.URL + "/init"
	mockOAuthCLIPollURL = ts.URL + "/poll/"
	defer func() { mockOAuthCLIInitURL = ""; mockOAuthCLIPollURL = "" }()

	rawStart, _ := handleMethod(pluginabi.MethodAuthLoginStart, nil)
	var envStart envelope
	_ = json.Unmarshal(rawStart, &envStart)
	var startResp pluginapi.AuthLoginStartResponse
	_ = json.Unmarshal(envStart.Result, &startResp)

	pollBody, _ := json.Marshal(pluginapi.AuthLoginPollRequest{State: startResp.State})
	rawPoll, _ := handleMethod(pluginabi.MethodAuthLoginPoll, pollBody)
	var envPoll envelope
	_ = json.Unmarshal(rawPoll, &envPoll)
	var pollResp pluginapi.AuthLoginPollResponse
	_ = json.Unmarshal(envPoll.Result, &pollResp)
	if pollResp.Status != pluginapi.AuthLoginStatusError {
		t.Fatalf("expected error status for failed flow, got %s", pollResp.Status)
	}
}

// TestZaiOAuth_InitFailurePropagates 验证 init 失败时 StartLogin 返回错误。
func TestZaiOAuth_InitFailurePropagates(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":500,"msg":"init exploded"}`, http.StatusInternalServerError)
	}))
	defer ts.Close()
	mockOAuthCLIInitURL = ts.URL + "/init"
	defer func() { mockOAuthCLIInitURL = "" }()

	if _, err := handleMethod(pluginabi.MethodAuthLoginStart, nil); err == nil {
		t.Fatalf("expected StartLogin to fail when cli init fails")
	}
}

func TestMain_IdentifierAndModels(t *testing.T) {
	// 认证标识
	rawAuth, err := handleMethod(pluginabi.MethodAuthIdentifier, nil)
	if err != nil {
		t.Fatalf("auth identifier failed: %v", err)
	}
	var envAuth envelope
	_ = json.Unmarshal(rawAuth, &envAuth)
	var idResp identifierResponse
	_ = json.Unmarshal(envAuth.Result, &idResp)
	if idResp.Identifier != providerName {
		t.Fatalf("expected identifier %s, got %s", providerName, idResp.Identifier)
	}

	// 执行器标识
	rawExec, err := handleMethod(pluginabi.MethodExecutorIdentifier, nil)
	if err != nil {
		t.Fatalf("executor identifier failed: %v", err)
	}
	var envExec envelope
	_ = json.Unmarshal(rawExec, &envExec)
	_ = json.Unmarshal(envExec.Result, &idResp)
	if idResp.Identifier != providerName {
		t.Fatalf("expected identifier %s, got %s", providerName, idResp.Identifier)
	}

	// 静态模型列表
	rawModels, err := handleMethod(pluginabi.MethodModelStatic, nil)
	if err != nil {
		t.Fatalf("model static failed: %v", err)
	}
	var envModels envelope
	_ = json.Unmarshal(rawModels, &envModels)
	var modelResp pluginapi.ModelResponse
	_ = json.Unmarshal(envModels.Result, &modelResp)
	if len(modelResp.Models) == 0 {
		t.Fatalf("expected non-empty static models")
	}
}

func TestMain_Reconfigure(t *testing.T) {
	configYAML := `
api_key: "cfg-test-key.secret"
base_url: "https://test.upstream.ai"
timeout_seconds: 60
`
	req := struct {
		ConfigYAML []byte `json:"config_yaml"`
	}{
		ConfigYAML: []byte(configYAML),
	}
	reqBytes, _ := json.Marshal(req)

	raw, err := handleMethod(pluginabi.MethodPluginReconfigure, reqBytes)
	if err != nil {
		t.Fatalf("reconfigure failed: %v", err)
	}
	var env envelope
	_ = json.Unmarshal(raw, &env)
	if !env.OK {
		t.Fatalf("reconfigure envelope not OK")
	}

	cfg := currentConfig()
	if cfg.APIKey != "cfg-test-key.secret" {
		t.Fatalf("expected APIKey cfg-test-key.secret, got %s", cfg.APIKey)
	}
	if cfg.BaseURL != "https://test.upstream.ai" {
		t.Fatalf("expected BaseURL https://test.upstream.ai, got %s", cfg.BaseURL)
	}
	if cfg.TimeoutSeconds != 60 {
		t.Fatalf("expected TimeoutSeconds 60, got %d", cfg.TimeoutSeconds)
	}
}
