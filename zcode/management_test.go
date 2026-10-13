package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func unwrapMgmtResp(t *testing.T, raw []byte) pluginapi.ManagementResponse {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope failed: %v", err)
	}
	if !env.OK {
		t.Fatalf("envelope not OK: %+v", env.Error)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("unmarshal ManagementResponse failed: %v", err)
	}
	return resp
}

func TestManagement_ImportAndList(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "zcode_mgmt_test_*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	setAccountsDir(tempDir)

	// 1. 导入两个账号
	importContent := "testkey1.secret----账号一\ntestkey2.secret----账号二"
	reqBody, _ := json.Marshal(map[string]any{"content": importContent})
	mgmtReq := pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/zcode-provider/import",
		Body:   reqBody,
	}
	rawReq, _ := json.Marshal(mgmtReq)

	rawResp, err := handleManagement(rawReq)
	if err != nil {
		t.Fatalf("handleManagement import failed: %v", err)
	}

	httpResp := unwrapMgmtResp(t, rawResp)
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", httpResp.StatusCode)
	}
	if gjson.GetBytes(httpResp.Body, "imported").Int() != 2 {
		t.Fatalf("expected 2 imported, got %s", httpResp.Body)
	}

	// 2. 列出账号
	listReq := pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/zcode-provider/accounts",
	}
	rawListReq, _ := json.Marshal(listReq)
	rawListResp, err := handleManagement(rawListReq)
	if err != nil {
		t.Fatalf("handleManagement list failed: %v", err)
	}
	httpResp = unwrapMgmtResp(t, rawListResp)
	if gjson.GetBytes(httpResp.Body, "count").Int() != 2 {
		t.Fatalf("expected 2 accounts, got %s", httpResp.Body)
	}

	// 3. 导出账号
	exportReq := pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/zcode-provider/export",
	}
	rawExportReq, _ := json.Marshal(exportReq)
	rawExportResp, _ := handleManagement(rawExportReq)
	httpResp = unwrapMgmtResp(t, rawExportResp)
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("expected export 200, got %d", httpResp.StatusCode)
	}

	// 4. 禁用一个账号
	accID := gjson.GetBytes(httpResp.Body, "0.auth_id").String()
	disableReq := pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/zcode-provider/disable",
		Body:   []byte(`{"auth_id":"` + accID + `"}`),
	}
	rawDisReq, _ := json.Marshal(disableReq)
	rawDisResp, _ := handleManagement(rawDisReq)
	httpResp = unwrapMgmtResp(t, rawDisResp)
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("expected disable 200, got %d", httpResp.StatusCode)
	}

	// 5. 校验该账号已被禁用
	accounts, _ := listAllAuthFiles()
	for _, acc := range accounts {
		if acc.AuthID == accID && !acc.Disabled {
			t.Fatalf("expected account %s to be disabled", accID)
		}
	}

	// 6. 测试 /models?auth_index= 查询
	modelsReq := pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/zcode-provider/models",
		Query:  map[string][]string{"auth_index": {accID}},
	}
	rawModReq, _ := json.Marshal(modelsReq)
	rawModResp, _ := handleManagement(rawModReq)
	httpResp = unwrapMgmtResp(t, rawModResp)
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("expected models 200, got %d", httpResp.StatusCode)
	}
}

func TestAuthParse_ZCodeCredential(t *testing.T) {
	parseReq := pluginapi.AuthParseRequest{
		FileName: "zcode-12345678.json",
		RawJSON:  []byte(`{"type":"zcode-provider","api_key":"test.key","label":"My ZCode","disabled":false}`),
	}
	rawReq, _ := json.Marshal(parseReq)
	rawResp, err := handleParseAuth(rawReq)
	if err != nil {
		t.Fatalf("handleParseAuth failed: %v", err)
	}

	var env envelope
	_ = json.Unmarshal(rawResp, &env)
	var res pluginapi.AuthParseResponse
	_ = json.Unmarshal(env.Result, &res)

	if !res.Handled || res.Auth.Provider != zcodeProviderID || res.Auth.Label != "My ZCode" {
		t.Fatalf("unexpected AuthParseResponse: %+v", res)
	}
}

func TestManagement_ActivateStartAndPoll(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "zcode_act_test_*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	setAccountsDir(tempDir)

	// 1. 创建待激活存量账号
	oldAuth := &StoredAuth{
		Type:       zcodeProviderID,
		Provider:   zcodeProviderID,
		AuthID:     "zcode-target123",
		APIKey:     "old.token.value",
		Token:      "old.token.value",
		Label:      "ZCode 待激活账号",
		Disabled:   false,
		TestFailed: false,
	}
	targetPath := filepath.Join(tempDir, "zcode-target123.json")
	if err := writeAuthFileDirect(targetPath, oldAuth); err != nil {
		t.Fatalf("write target auth: %v", err)
	}

	// 使用完整且含百分号编码的授权链接，确保 JSON 往返不会改变分隔符或回调地址。
	const authorizeURL = "https://chat.z.ai/api/oauth/authorize?client_id=test-client&redirect_uri=https%3A%2F%2Fzcode.z.ai%2Fapi%2Fv1%2Foauth%2Fcli%2Fcallback%2Fzai&state=state_test_123&response_type=code"
	pollStatus := "pending"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/init" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "",
				"data": map[string]any{
					"flow_id":       "flow_xyz",
					"poll_token":    "poll_tok",
					"authorize_url": authorizeURL,
				},
			})
			return
		}
		if r.URL.Path == "/poll/flow_xyz" {
			if pollStatus == "pending" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{"status": "pending"},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"status": "ready",
					"token":  "new.jwt.token",
					"zai": map[string]any{
						"access_token": "new.access.token",
					},
					"user": map[string]any{
						"user_id": "usr_999",
						"name":    "旅行者999",
						"email":   "999@test.local",
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	origInit := mockOAuthCLIInitURL
	origPoll := mockOAuthCLIPollURL
	mockOAuthCLIInitURL = ts.URL + "/init"
	mockOAuthCLIPollURL = ts.URL + "/poll/"
	defer func() {
		mockOAuthCLIInitURL = origInit
		mockOAuthCLIPollURL = origPoll
	}()

	// 2. 发起 /activate-start
	startReq := pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/zcode-provider/activate-start",
		Body:   []byte(`{"auth_id":"zcode-target123"}`),
	}
	rawReq, _ := json.Marshal(startReq)
	rawResp, err := handleManagement(rawReq)
	if err != nil {
		t.Fatalf("activate-start failed: %v", err)
	}
	httpResp := unwrapMgmtResp(t, rawResp)
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", httpResp.StatusCode, httpResp.Body)
	}
	state := gjson.GetBytes(httpResp.Body, "state").String()
	if state != "state_test_123" {
		t.Fatalf("expected state_test_123, got %s", state)
	}
	returnedURL := gjson.GetBytes(httpResp.Body, "authorize_url").String()
	if returnedURL != authorizeURL {
		t.Fatalf("authorize_url changed during management JSON round trip: %q", returnedURL)
	}
	parsedURL, err := url.Parse(returnedURL)
	if err != nil {
		t.Fatalf("parse authorize_url: %v", err)
	}
	query := parsedURL.Query()
	for name, want := range map[string]string{
		"client_id":     "test-client",
		"redirect_uri":  "https://zcode.z.ai/api/v1/oauth/cli/callback/zai",
		"state":         state,
		"response_type": "code",
	} {
		if got := query.Get(name); got != want {
			t.Fatalf("authorize_url query %s = %q, want %q", name, got, want)
		}
	}

	// 3. 轮询 pending 态
	pollReq := pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/zcode-provider/activate-poll",
		Query:  map[string][]string{"state": {state}},
	}
	rawPollReq, _ := json.Marshal(pollReq)
	rawPollResp, _ := handleManagement(rawPollReq)
	pollHTTPResp := unwrapMgmtResp(t, rawPollResp)
	if gjson.GetBytes(pollHTTPResp.Body, "status").String() != "pending" {
		t.Fatalf("expected pending status, got %s", pollHTTPResp.Body)
	}

	// 4. 模拟官方授权完成 ready 态
	pollStatus = "ready"
	rawPollResp2, _ := handleManagement(rawPollReq)
	pollHTTPResp2 := unwrapMgmtResp(t, rawPollResp2)
	if gjson.GetBytes(pollHTTPResp2.Body, "status").String() != "success" {
		t.Fatalf("expected success status, got %s", pollHTTPResp2.Body)
	}
	if gjson.GetBytes(pollHTTPResp2.Body, "auth_id").String() != "zcode-target123" {
		t.Fatalf("expected auth_id zcode-target123, got %s", pollHTTPResp2.Body)
	}

	// 5. 校验原账号已在原地被更新且无重复卡片
	accs, errList := listAllAuthFiles()
	if errList != nil || len(accs) != 1 {
		t.Fatalf("expected exactly 1 account, got %d (err: %v)", len(accs), errList)
	}
	updated := accs[0]
	if updated.AuthID != "zcode-target123" {
		t.Fatalf("auth_id changed: %s", updated.AuthID)
	}
	if updated.Token != "new.jwt.token" || updated.AccessToken != "new.access.token" {
		t.Fatalf("tokens not updated: token=%s, access=%s", updated.Token, updated.AccessToken)
	}
	if updated.Username != "旅行者999" || updated.UserID != "usr_999" {
		t.Fatalf("user info not updated: %+v", updated)
	}
}
