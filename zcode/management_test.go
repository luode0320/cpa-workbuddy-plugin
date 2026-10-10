package main

import (
	"encoding/json"
	"net/http"
	"os"
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
