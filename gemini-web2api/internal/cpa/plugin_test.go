package cpa_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/cpa"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/gemini"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginabi"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginapi"
)

func TestPluginLifecycleAndCapabilities(t *testing.T) {
	tmpDir := t.TempDir()
	var hostCalls []string
	p := cpa.NewPlugin(func(method string, payload []byte) ([]byte, error) {
		hostCalls = append(hostCalls, method)
		return json.Marshal(pluginabi.Envelope{
			OK:     true,
			Result: json.RawMessage(`{}`),
		})
	})
	defer func() {
		_, _ = p.HandleMethod(pluginabi.MethodPluginShutdown, nil)
	}()

	cfgYAML := []byte("data_dir: " + filepath.ToSlash(tmpDir) + "\nauto_start: false\n")
	regReq, _ := json.Marshal(map[string]any{
		"config_yaml":    cfgYAML,
		"schema_version": pluginabi.SchemaVersion,
	})

	// 1. plugin.register
	regRes, err := p.HandleMethod(pluginabi.MethodPluginRegister, regReq)
	if err != nil {
		t.Fatalf("plugin.register failed: %v", err)
	}
	regJSON, _ := json.Marshal(regRes)
	if !strings.Contains(string(regJSON), `"Name":"gemini-web2api"`) {
		t.Fatalf("unexpected registration metadata: %s", string(regJSON))
	}
	if !strings.Contains(string(regJSON), `"Version":"`+expectedPluginVersion(t)+`"`) || !strings.Contains(string(regJSON), `"continue_session"`) {
		t.Fatalf("expected version %s and continue_session config field: %s", expectedPluginVersion(t), string(regJSON))
	}
	if !strings.Contains(string(regJSON), `"executor":true`) || !strings.Contains(string(regJSON), `"auth_provider":true`) {
		t.Fatalf("missing expected capabilities: %s", string(regJSON))
	}

	// 2. auth.identifier & auth.parse
	authIDRes, err := p.HandleMethod(pluginabi.MethodAuthIdentifier, nil)
	if err != nil {
		t.Fatalf("auth.identifier failed: %v", err)
	}
	authIDJSON, _ := json.Marshal(authIDRes)
	if !strings.Contains(string(authIDJSON), `"identifiers":["gemini-web2api"]`) {
		t.Fatalf("expected single auth identifier to avoid duplicate OAuth cards, got: %s", string(authIDJSON))
	}

	// Unrelated provider should return Handled: false
	unrelatedParseReq, _ := json.Marshal(pluginapi.AuthParseRequest{
		Provider: "openai",
		FileName: "openai-key.json",
		RawJSON:  []byte(`{"type":"openai"}`),
	})
	unrelatedRes, err := p.HandleMethod(pluginabi.MethodAuthParse, unrelatedParseReq)
	if err != nil {
		t.Fatalf("auth.parse unrelated failed: %v", err)
	}
	if unrelatedRes.(pluginapi.AuthParseResponse).Handled {
		t.Fatalf("expected Handled=false for unrelated provider")
	}

	// Matching gemini-web2api auth file
	matchingParseReq, _ := json.Marshal(pluginapi.AuthParseRequest{
		Provider: "gemini-web2api",
		FileName: "gemini-web2api-user@example.com.json",
		RawJSON:  []byte(`{"type":"gemini-web2api","account_id":"user@example.com","tier":"PRO"}`),
	})
	matchingRes, err := p.HandleMethod(pluginabi.MethodAuthParse, matchingParseReq)
	if err != nil {
		t.Fatalf("auth.parse matching failed: %v", err)
	}
	parsedAuth := matchingRes.(pluginapi.AuthParseResponse)
	if !parsedAuth.Handled || parsedAuth.Auth.Provider != "gemini-web2api" || parsedAuth.Auth.Attributes["account_id"] != "user@example.com" {
		t.Fatalf("unexpected parsed auth: %+v", parsedAuth)
	}

	// 3. model.static & model.for_auth & model.route
	staticRes, err := p.HandleMethod(pluginabi.MethodModelStatic, []byte(`{}`))
	if err != nil {
		t.Fatalf("model.static failed: %v", err)
	}
	modelResp := staticRes.(pluginapi.ModelResponse)
	if modelResp.Provider != "gemini-web2api" || len(modelResp.Models) == 0 {
		t.Fatalf("expected non-empty models from model.static, got: %+v", modelResp)
	}
	hasPrefixed := false
	hasUnprefixed := false
	for _, m := range modelResp.Models {
		if m.ID == "gemini-3.8-flash" || m.ID == "gemini-4-carbon" || m.ID == "gemini-4-argon" {
			hasUnprefixed = true
		}
		if strings.HasPrefix(m.ID, "gemini-web/") {
			hasPrefixed = true
		}
	}
	if !hasUnprefixed || hasPrefixed {
		t.Fatalf("expected unprefixed gemini models without duplicate gemini-web/* prefix in catalog")
	}

	routeReq, _ := json.Marshal(pluginapi.ModelRouteRequest{
		RequestedModel: "gemini-web/gemini-3.0-pro",
		Body:           []byte(`{"model":"gemini-web/gemini-3.0-pro","messages":[{"role":"user","content":"hi"}]}`),
	})
	routeRes, err := p.HandleMethod(pluginabi.MethodModelRoute, routeReq)
	if err != nil {
		t.Fatalf("model.route failed: %v", err)
	}
	rResp := routeRes.(pluginapi.ModelRouteResponse)
	if !rResp.Handled || rResp.TargetKind != pluginapi.ModelRouteTargetSelf || rResp.TargetModel != "gemini-3.0-pro" {
		t.Fatalf("unexpected model.route response: %+v", rResp)
	}

	// 4. scheduler.pick
	schedReq, _ := json.Marshal(pluginapi.SchedulerPickRequest{
		Provider: "gemini-web2api",
		Model:    "gemini-3.0-pro",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{
				ID:         "gemini-web2api-user@example.com.json",
				Provider:   "gemini-web2api",
				Status:     "active",
				Attributes: map[string]string{"provider": "gemini-web2api", "account_id": "user@example.com"},
			},
		},
	})
	schedRes, err := p.HandleMethod(pluginabi.MethodSchedulerPick, schedReq)
	if err != nil {
		t.Fatalf("scheduler.pick failed: %v", err)
	}
	sResp := schedRes.(pluginapi.SchedulerPickResponse)
	if !sResp.Handled {
		t.Fatalf("expected scheduler.pick Handled=true, got: %+v", sResp)
	}

	// 5. quota.describe & quota.fetch
	qDesc, err := p.HandleMethod(pluginabi.MethodQuotaDescribe, nil)
	if err != nil {
		t.Fatalf("quota.describe failed: %v", err)
	}
	if !qDesc.(pluginapi.QuotaDescribeResponse).SupportsReset {
		t.Fatalf("expected SupportsReset=true")
	}

	qFetch, err := p.HandleMethod(pluginabi.MethodQuotaFetch, []byte(`{"provider":"gemini-web2api"}`))
	if err != nil {
		t.Fatalf("quota.fetch failed: %v", err)
	}
	if qFetch.(pluginapi.QuotaFetchResponse).Subscription == nil {
		t.Fatalf("expected Subscription in quota.fetch")
	}

	// 6. management.register & management.handle (Web UI & Status API)
	mgmtReg, err := p.HandleMethod(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("management.register failed: %v", err)
	}
	mgmtRegJSON, _ := json.Marshal(mgmtReg)
	if !strings.Contains(string(mgmtRegJSON), `"/accounts"`) {
		t.Fatalf("missing resource route in management.register: %s", string(mgmtRegJSON))
	}

	uiReq, _ := json.Marshal(pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/gemini-web2api/accounts",
	})
	uiRes, err := p.HandleMethod(pluginabi.MethodManagementHandle, uiReq)
	if err != nil {
		t.Fatalf("management.handle UI failed: %v", err)
	}
	uiResp := uiRes.(pluginapi.ManagementResponse)
	if uiResp.StatusCode != http.StatusOK || !strings.Contains(string(uiResp.Body), "启动本机 Chrome 无痕登录") {
		t.Fatalf("unexpected UI response: status=%d len=%d", uiResp.StatusCode, len(uiResp.Body))
	}

	resStatusReq, _ := json.Marshal(pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/gemini-web2api/accounts",
		Query:  map[string][]string{"view": {"status"}},
	})
	resStatusRes, err := p.HandleMethod(pluginabi.MethodManagementHandle, resStatusReq)
	if err != nil {
		t.Fatalf("management.handle resource with query failed: %v", err)
	}
	resStatusResp := resStatusRes.(pluginapi.ManagementResponse)
	if resStatusResp.StatusCode != http.StatusOK || !strings.Contains(resStatusResp.Headers.Get("Content-Type"), "text/html") || strings.Contains(string(resStatusResp.Body), `"routing_strategy":"round-robin"`) {
		t.Fatalf("expected static HTML only on /v0/resource/plugins/..., got content-type=%q", resStatusResp.Headers.Get("Content-Type"))
	}

	statusReq, _ := json.Marshal(pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/gemini-web2api/status",
	})
	statusRes, err := p.HandleMethod(pluginabi.MethodManagementHandle, statusReq)
	if err != nil {
		t.Fatalf("management.handle status failed: %v", err)
	}
	statusResp := statusRes.(pluginapi.ManagementResponse)
	if statusResp.StatusCode != http.StatusOK || !strings.Contains(string(statusResp.Body), `"routing_strategy":"round-robin"`) || !strings.Contains(string(statusResp.Body), `"chrome_available"`) {
		t.Fatalf("unexpected status response: %s", string(statusResp.Body))
	}

	t.Setenv("GEMINI_WEB2API_NO_AUTO_BROWSER", "1")
	startRes, err := p.HandleMethod(pluginabi.MethodAuthLoginStart, []byte(`{"provider":"gemini-web2api","base_url":"http://127.0.0.1:8317/v8/management/oauth/callback"}`))
	if err != nil {
		t.Fatalf("auth.login_start failed: %v", err)
	}
	startResp := startRes.(pluginapi.AuthLoginStartResponse)
	// 登录入口改为资源路由上的桥接页（一键授权：扩展推送 + 手工粘贴 Cookie），
	// 不再直指账号面板，因此这里断言的是登录桥接页地址。
	if startResp.URL != "http://127.0.0.1:8317/v0/resource/plugins/gemini-web2api/login" {
		t.Fatalf("expected bridge login URL in AuthLoginStartResponse, got %q", startResp.URL)
	}

	// 7. executor.identifier & executor.count_tokens
	execIDRes, err := p.HandleMethod(pluginabi.MethodExecutorIdentifier, nil)
	if err != nil {
		t.Fatalf("executor.identifier failed: %v", err)
	}
	execIDJSON, _ := json.Marshal(execIDRes)
	if !strings.Contains(string(execIDJSON), `"gemini-web2api"`) {
		t.Fatalf("unexpected executor.identifier: %s", string(execIDJSON))
	}

	countReq, _ := json.Marshal(pluginapi.ExecutorRequest{
		Model:   "gemini-2.5-flash",
		Format:  "chat-completions",
		Payload: []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"Hello world from Gemini Web2API plugin!"}]}`),
	})
	countRes, err := p.HandleMethod(pluginabi.MethodExecutorCountTokens, countReq)
	if err != nil {
		t.Fatalf("executor.count_tokens failed: %v", err)
	}
	countResp := countRes.(pluginapi.ExecutorResponse)
	if !strings.Contains(string(countResp.Payload), `"totalTokens"`) {
		t.Fatalf("expected totalTokens from SentencePiece tokenizer, got %s", string(countResp.Payload))
	}
}

func TestAuthFileDisabledSyncOnServiceStopAndShutdown(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	authDir := filepath.Join(tmpHome, ".cli-proxy-api")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("mkdir authDir: %v", err)
	}
	dataDir := filepath.ToSlash(filepath.Join(tmpHome, ".gemini-web2api"))
	authFilePath := filepath.Join(authDir, "gemini-web2api-testuser@gmail.com.json")
	initialPayload := `{
		"type": "gemini-web2api",
		"provider": "gemini-web2api",
		"account_id": "testuser@gmail.com",
		"email": "testuser@gmail.com",
		"label": "testuser@gmail.com",
		"disabled": true,
		"storage_state": {
			"cookies": [
				{"name": "__Secure-1PSID", "value": "test-1psid-val", "domain": ".google.com", "path": "/", "secure": true, "sameSite": "None"},
				{"name": "__Secure-1PSIDTS", "value": "test-1psidts-val", "domain": ".google.com", "path": "/", "secure": true, "sameSite": "None"},
				{"name": "__Secure-3PSID", "value": "test-3psid-val", "domain": ".google.com", "path": "/", "secure": true, "sameSite": "None"},
				{"name": "SID", "value": "test-sid-val", "domain": ".google.com", "path": "/", "sameSite": "Lax"}
			],
			"origins": []
		}
	}`
	if err := os.WriteFile(authFilePath, []byte(initialPayload), 0o600); err != nil {
		t.Fatalf("write initial auth file: %v", err)
	}

	p := cpa.NewPlugin(nil)
	defer func() {
		_, _ = p.HandleMethod(pluginabi.MethodPluginShutdown, []byte(`{}`))
	}()

	cfgYAML := []byte("data_dir: \"" + dataDir + "\"\nauto_start: false\nguest_enabled: false\ninit_timeout: 500ms\nsync_auth_files: true\n")
	regReq, _ := json.Marshal(map[string]any{
		"config_yaml":    cfgYAML,
		"schema_version": pluginabi.SchemaVersion,
	})
	if _, err := p.HandleMethod(pluginabi.MethodPluginRegister, regReq); err != nil {
		t.Fatalf("plugin.register failed: %v", err)
	}
	// Simulate auto_start=true runtime state for startup sync test
	cfgYAMLAuto := []byte("data_dir: \"" + dataDir + "\"\nauto_start: true\nguest_enabled: false\ninit_timeout: 500ms\nsync_auth_files: true\n")
	regReqAuto, _ := json.Marshal(map[string]any{
		"config_yaml":    cfgYAMLAuto,
		"schema_version": pluginabi.SchemaVersion,
	})
	if _, err := p.HandleMethod(pluginabi.MethodPluginReconfigure, regReqAuto); err != nil {
		t.Fatalf("plugin.reconfigure failed: %v", err)
	}

	// 1. Parse auth during startup window -> should import account as enabled and flip disabled=false on disk
	parseReq, _ := json.Marshal(pluginapi.AuthParseRequest{
		FileName: "gemini-web2api-testuser@gmail.com.json",
		RawJSON:  []byte(initialPayload),
	})
	parseRes, err := p.HandleMethod(pluginabi.MethodAuthParse, parseReq)
	if err != nil {
		t.Fatalf("auth.parse failed: %v", err)
	}
	parseResp := parseRes.(pluginapi.AuthParseResponse)
	if !parseResp.Handled || parseResp.Auth.Disabled {
		t.Fatalf("expected auth to be enabled on plugin startup, got Disabled=%v", parseResp.Auth.Disabled)
	}

	checkDiskDisabled := func(wantDisabled bool, wantStopped bool, step string) {
		t.Helper()
		raw, err := os.ReadFile(authFilePath)
		if err != nil {
			t.Fatalf("%s: read auth file: %v", step, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: unmarshal auth file: %v", step, err)
		}
		gotDisabled, _ := doc["disabled"].(bool)
		gotStopped, _ := doc["service_stopped"].(bool)
		if gotDisabled != wantDisabled || gotStopped != wantStopped {
			t.Fatalf("%s: disk auth file disabled=%v service_stopped=%v, want disabled=%v service_stopped=%v", step, gotDisabled, gotStopped, wantDisabled, wantStopped)
		}
	}
	checkDiskDisabled(false, false, "after startup auth.parse")

	// 2. Stop service via management API -> should mark disk auth file disabled=true, service_stopped=true
	stopReq, _ := json.Marshal(pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/gemini-web2api/service/stop",
	})
	if _, err := p.HandleMethod(pluginabi.MethodManagementHandle, stopReq); err != nil {
		t.Fatalf("service/stop failed: %v", err)
	}
	checkDiskDisabled(true, true, "after service/stop")

	// 3. Re-enable plugin (reconfigure with auto_start=true) -> should restore disabled=false, service_stopped=false
	if _, err := p.HandleMethod(pluginabi.MethodPluginReconfigure, regReqAuto); err != nil {
		t.Fatalf("plugin.reconfigure failed: %v", err)
	}
	checkDiskDisabled(false, false, "after plugin reconfigure/enable")

	// 4. Quiesce/Shutdown plugin -> should mark disk auth file disabled=true, service_stopped=true
	if _, err := p.HandleMethod(pluginabi.MethodPluginQuiesce, []byte(`{}`)); err != nil {
		t.Fatalf("plugin.quiesce failed: %v", err)
	}
	checkDiskDisabled(true, true, "after plugin.quiesce")
}

func TestPluginReloadAndSchedulerFiltering(t *testing.T) {
	tmpDir := t.TempDir()
	p := cpa.NewPlugin(nil)
	defer func() {
		_, _ = p.HandleMethod(pluginabi.MethodPluginShutdown, []byte(`{}`))
	}()

	cfg1 := []byte("data_dir: " + filepath.ToSlash(tmpDir) + "\nauto_start: false\nguest_enabled: false\nrouting_strategy: fill-first\naccount_order: acc2@example.com,acc1@example.com\nmodel_accounts: gemini-4-carbon:acc1@example.com\nfirst_event_hedge: 15s\nfirst_event_hedge_models: gemini-4-carbon,gemini-4-argon\n")
	regReq, _ := json.Marshal(map[string]any{
		"config_yaml":    cfg1,
		"schema_version": pluginabi.SchemaVersion,
	})
	if _, err := p.HandleMethod(pluginabi.MethodPluginRegister, regReq); err != nil {
		t.Fatalf("plugin.register failed: %v", err)
	}

	parsedCfg := p.Config()
	if parsedCfg.AccountOrder != "acc2@example.com,acc1@example.com" || parsedCfg.ModelAccounts != "gemini-4-carbon:acc1@example.com" {
		t.Fatalf("unexpected parsed config fields: %+v", parsedCfg)
	}

	rt, err := p.Runtime()
	if err != nil {
		t.Fatalf("Runtime() failed: %v", err)
	}
	for _, accID := range []string{"acc1@example.com", "acc2@example.com"} {
		client, err := gemini.NewClient(gemini.AccountSource{ID: accID, Concurrency: 1}, "", false)
		if err != nil {
			t.Fatalf("NewClient(%s) failed: %v", accID, err)
		}
		rt.Pool.Add(client, accID)
	}

	if !rt.Pool.AllowsAccountForModel(t.Context(), "acc1@example.com", "gemini-4-carbon") {
		t.Fatalf("expected acc1 to be allowed for gemini-4-carbon")
	}
	if rt.Pool.AllowsAccountForModel(t.Context(), "acc2@example.com", "gemini-4-carbon") {
		t.Fatalf("expected acc2 to be excluded for gemini-4-carbon")
	}

	pickModel := func(model string) string {
		t.Helper()
		schedReq, _ := json.Marshal(pluginapi.SchedulerPickRequest{
			Provider: "gemini-web2api",
			Model:    model,
			Candidates: []pluginapi.SchedulerAuthCandidate{
				{
					ID:         "gemini-web2api-acc1.json",
					Provider:   "gemini-web2api",
					Status:     "active",
					Attributes: map[string]string{"provider": "gemini-web2api", "account_id": "acc1@example.com"},
				},
				{
					ID:         "gemini-web2api-acc2.json",
					Provider:   "gemini-web2api",
					Status:     "active",
					Attributes: map[string]string{"provider": "gemini-web2api", "account_id": "acc2@example.com"},
				},
			},
		})
		res, err := p.HandleMethod(pluginabi.MethodSchedulerPick, schedReq)
		if err != nil {
			t.Fatalf("scheduler.pick(%s) failed: %v", model, err)
		}
		resp := res.(pluginapi.SchedulerPickResponse)
		if !resp.Handled || resp.Reject {
			t.Fatalf("unexpected scheduler.pick(%s) response: %+v", model, resp)
		}
		return resp.AuthID
	}

	// fill-first + account_order puts acc2 first, so unrestricted model picks acc2.
	if got := pickModel("gemini-3.8-flash"); got != "gemini-web2api-acc2.json" {
		t.Fatalf("expected acc2 first under fill-first account_order, got %s", got)
	}
	// model_accounts restricts gemini-4-carbon to acc1 only.
	if got := pickModel("gemini-4-carbon"); got != "gemini-web2api-acc1.json" {
		t.Fatalf("expected acc1 for model_accounts-restricted gemini-4-carbon, got %s", got)
	}

	// Reconfigure live via plugin.reconfigure (ApplyConfig path) and verify updated bindings, continue_session + round-robin take effect.
	cfg2 := []byte("data_dir: " + filepath.ToSlash(tmpDir) + "\nauto_start: false\nguest_enabled: false\ncontinue_session: true\nrouting_strategy: round-robin\naccount_order: acc1@example.com,acc2@example.com\nmodel_accounts: gemini-4-carbon:acc2@example.com\nfirst_event_hedge: off\n")
	reconfReq, _ := json.Marshal(map[string]any{
		"config_yaml":    cfg2,
		"schema_version": pluginabi.SchemaVersion,
	})
	if _, err := p.HandleMethod(pluginabi.MethodPluginReconfigure, reconfReq); err != nil {
		t.Fatalf("plugin.reconfigure failed: %v", err)
	}
	if reconfCfg := p.Config(); reconfCfg.ContinueSession == nil || !*reconfCfg.ContinueSession {
		t.Fatalf("expected continue_session=true after reconfigure, got: %+v", reconfCfg.ContinueSession)
	}
	if got := pickModel("google/gemini-4-carbon"); got != "gemini-web2api-acc2.json" {
		t.Fatalf("expected acc2 for google/gemini-4-carbon after reconfigure, got %s", got)
	}
	googleRouteReq, _ := json.Marshal(pluginapi.ModelRouteRequest{
		RequestedModel: "google/gemini-4-carbon",
		Body:           []byte(`{"model":"google/gemini-4-carbon","messages":[{"role":"user","content":"hi"}]}`),
	})
	googleRouteRes, err := p.HandleMethod(pluginabi.MethodModelRoute, googleRouteReq)
	if err != nil {
		t.Fatalf("model.route google/ failed: %v", err)
	}
	grResp := googleRouteRes.(pluginapi.ModelRouteResponse)
	if !grResp.Handled || grResp.TargetKind != pluginapi.ModelRouteTargetSelf || grResp.TargetModel != "google/gemini-4-carbon" {
		t.Fatalf("unexpected model.route google/ response: %+v", grResp)
	}
	if rt.Pool.AllowsAccountForModel(t.Context(), "acc1@example.com", "gemini-4-carbon") {
		t.Fatalf("expected acc1 to be excluded after reconfigure")
	}
	if !rt.Pool.AllowsAccountForModel(t.Context(), "acc2@example.com", "gemini-4-carbon") {
		t.Fatalf("expected acc2 to be allowed after reconfigure")
	}
	if got := pickModel("gemini-4-carbon"); got != "gemini-web2api-acc2.json" {
		t.Fatalf("expected acc2 for gemini-4-carbon after reconfigure, got %s", got)
	}

	// Under round-robin, unrestricted picks should alternate between acc1 and acc2.
	first := pickModel("gemini-3.8-flash")
	second := pickModel("gemini-3.8-flash")
	if first == second {
		t.Fatalf("expected round-robin to alternate between acc1 and acc2, got %s twice", first)
	}
}

func TestRouteUnprefixedModelsAndHostCallbacks(t *testing.T) {
	tmpDir := t.TempDir()
	var hostMethods []string
	p := cpa.NewPlugin(func(method string, payload []byte) ([]byte, error) {
		hostMethods = append(hostMethods, method)
		return json.Marshal(pluginabi.Envelope{OK: true, Result: json.RawMessage(`{}`)})
	})
	defer func() {
		_, _ = p.HandleMethod(pluginabi.MethodPluginShutdown, []byte(`{}`))
	}()

	cfg := []byte("data_dir: " + filepath.ToSlash(tmpDir) + "\nauto_start: false\nguest_enabled: false\nroute_unprefixed_models: false\nsync_auth_files: true\n")
	regReq, _ := json.Marshal(map[string]any{
		"config_yaml":    cfg,
		"schema_version": pluginabi.SchemaVersion,
	})
	if _, err := p.HandleMethod(pluginabi.MethodPluginRegister, regReq); err != nil {
		t.Fatalf("plugin.register failed: %v", err)
	}

	rt, err := p.Runtime()
	if err != nil {
		t.Fatalf("Runtime() failed: %v", err)
	}
	client, err := gemini.NewClient(gemini.AccountSource{ID: "acc1@example.com", Concurrency: 1}, "", false)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	rt.Pool.Add(client, "acc1@example.com")

	// With route_unprefixed_models: false, unprefixed model must NOT be claimed, while prefixed model MUST be routed.
	unprefixedReq, _ := json.Marshal(pluginapi.ModelRouteRequest{
		RequestedModel: "gemini-4-carbon",
	})
	unprefixedRes, err := p.HandleMethod(pluginabi.MethodModelRoute, unprefixedReq)
	if err != nil {
		t.Fatalf("model.route unprefixed failed: %v", err)
	}
	if unprefixedRes.(pluginapi.ModelRouteResponse).Handled {
		t.Fatalf("expected Handled=false when route_unprefixed_models is false")
	}

	prefixedReq, _ := json.Marshal(pluginapi.ModelRouteRequest{
		RequestedModel: "gemini-web/gemini-4-carbon",
	})
	prefixedRes, err := p.HandleMethod(pluginabi.MethodModelRoute, prefixedReq)
	if err != nil {
		t.Fatalf("model.route prefixed failed: %v", err)
	}
	if !prefixedRes.(pluginapi.ModelRouteResponse).Handled {
		t.Fatalf("expected Handled=true for gemini-web/ prefix even when route_unprefixed_models is false")
	}

	// SyncAccountIDToHostAuth without callbackID must still invoke host.auth.save over ABI.
	hostMethods = nil
	if err := p.SyncAccountIDToHostAuth("", "acc1@example.com"); err != nil {
		t.Fatalf("SyncAccountIDToHostAuth failed: %v", err)
	}
	foundSave := false
	for _, m := range hostMethods {
		if m == pluginabi.MethodHostAuthSave {
			foundSave = true
		}
	}
	if !foundSave {
		t.Fatalf("expected %s host call, got %v", pluginabi.MethodHostAuthSave, hostMethods)
	}

	// Quota reset must also notify host.routing.reset_cooldown.
	hostMethods = nil
	resetReq, _ := json.Marshal(pluginapi.QuotaResetRequest{
		Attributes: map[string]string{"account_id": "acc1@example.com"},
	})
	if _, err := p.HandleMethod(pluginabi.MethodQuotaReset, resetReq); err != nil {
		t.Fatalf("quota.reset failed: %v", err)
	}
	foundCooldownReset := false
	for _, m := range hostMethods {
		if m == pluginabi.MethodHostRoutingResetCooldown {
			foundCooldownReset = true
		}
	}
	if !foundCooldownReset {
		t.Fatalf("expected %s host call on quota.reset, got %v", pluginabi.MethodHostRoutingResetCooldown, hostMethods)
	}

	// Guest stop management endpoint must succeed and return status JSON.
	guestStopReq, _ := json.Marshal(pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/gemini-web2api/guest/stop",
	})
	guestStopRes, err := p.HandleMethod(pluginabi.MethodManagementHandle, guestStopReq)
	if err != nil {
		t.Fatalf("guest/stop failed: %v", err)
	}
	if guestStopRes.(pluginapi.ManagementResponse).StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from guest/stop, got %d", guestStopRes.(pluginapi.ManagementResponse).StatusCode)
	}
}

func TestExecutorExecuteStreamFakeStreamDoesNotDeadlockOver16Chunks(t *testing.T) {
	tmpDir := t.TempDir()

	const (
		hostQueueCap = 16
		totalChunks  = 30
	)

	type streamBridge struct {
		mu       sync.Mutex
		cond     *sync.Cond
		draining bool
		closed   bool
		queue    [][]byte
		emitted  [][]byte
	}
	bridge := &streamBridge{}
	bridge.cond = sync.NewCond(&bridge.mu)

	p := cpa.NewPlugin(func(method string, payload []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostStreamEmit:
			var req struct {
				StreamID string `json:"stream_id"`
				Payload  []byte `json:"payload"`
			}
			if err := json.Unmarshal(payload, &req); err != nil {
				return nil, err
			}
			bridge.mu.Lock()
			for !bridge.draining && len(bridge.queue) >= hostQueueCap && !bridge.closed {
				bridge.cond.Wait()
			}
			if bridge.draining {
				bridge.emitted = append(bridge.emitted, append([]byte(nil), req.Payload...))
			} else {
				bridge.queue = append(bridge.queue, append([]byte(nil), req.Payload...))
			}
			bridge.mu.Unlock()
		case pluginabi.MethodHostStreamClose:
			bridge.mu.Lock()
			bridge.closed = true
			bridge.cond.Broadcast()
			bridge.mu.Unlock()
		}
		return json.Marshal(pluginabi.Envelope{OK: true, Result: json.RawMessage(`{}`)})
	})
	defer func() {
		bridge.mu.Lock()
		bridge.closed = true
		bridge.cond.Broadcast()
		bridge.mu.Unlock()
		_, _ = p.HandleMethod(pluginabi.MethodPluginShutdown, []byte(`{}`))
	}()

	cfgYAML := []byte("data_dir: " + filepath.ToSlash(tmpDir) + "\nauto_start: false\nguest_enabled: false\nfake_stream: true\n")
	regReq, _ := json.Marshal(map[string]any{
		"config_yaml":    cfgYAML,
		"schema_version": pluginabi.SchemaVersion,
	})
	if _, err := p.HandleMethod(pluginabi.MethodPluginRegister, regReq); err != nil {
		t.Fatalf("plugin.register failed: %v", err)
	}
	rt, err := p.Runtime()
	if err != nil {
		t.Fatalf("Runtime() failed: %v", err)
	}

	rt.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/responses" && r.Header.Get("X-Test-Error") == "429" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for i := range totalChunks {
			_, _ = fmt.Fprintf(w, "event: response.output_text.delta\ndata: {\"seq\":%d}\n\n", i)
		}
	})

	streamReq, _ := json.Marshal(map[string]any{
		"stream_id": "stream-over-16",
		"model":     "gemini-4-neon",
		"format":    "openai-responses",
		"payload":   []byte(`{"model":"gemini-4-neon","input":"hi","stream":true}`),
	})

	type execResult struct {
		res any
		err error
	}
	done := make(chan execResult, 1)
	go func() {
		res, execErr := p.HandleMethod(pluginabi.MethodExecutorExecuteStream, streamReq)
		done <- execResult{res: res, err: execErr}
	}()

	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("executor.execute_stream failed: %v", out.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("executor.execute_stream deadlocked before returning headers with fake_stream=true and >16 chunks")
	}

	// Start host stream consumption only AFTER executor.execute_stream returns, matching CPA streamBridgeStream.
	bridge.mu.Lock()
	bridge.draining = true
	bridge.emitted = append(bridge.emitted, bridge.queue...)
	bridge.queue = nil
	bridge.cond.Broadcast()
	deadline := time.Now().Add(3 * time.Second)
	for !bridge.closed && time.Now().Before(deadline) {
		bridge.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		bridge.mu.Lock()
	}
	closed := bridge.closed
	gotCount := len(bridge.emitted)
	bridge.mu.Unlock()

	if !closed {
		t.Fatalf("expected host.stream.close after draining, got closed=%v emitted=%d", closed, gotCount)
	}
	if gotCount != totalChunks {
		t.Fatalf("expected %d emitted SSE chunks, got %d", totalChunks, gotCount)
	}

	// Fallback without StreamID must still return inline chunks cleanly.
	inlineReq, _ := json.Marshal(map[string]any{
		"model":   "gemini-4-neon",
		"format":  "openai-responses",
		"payload": []byte(`{"model":"gemini-4-neon","input":"hi","stream":true}`),
	})
	inlineRes, err := p.HandleMethod(pluginabi.MethodExecutorExecuteStream, inlineReq)
	if err != nil {
		t.Fatalf("inline executor.execute_stream failed: %v", err)
	}
	inlineJSON, _ := json.Marshal(inlineRes)
	var parsedInline struct {
		Chunks []pluginapi.ExecutorStreamChunk `json:"chunks"`
	}
	if err := json.Unmarshal(inlineJSON, &parsedInline); err != nil {
		t.Fatalf("unmarshal inline response: %v", err)
	}
	if len(parsedInline.Chunks) != totalChunks {
		t.Fatalf("expected %d inline chunks when stream_id is empty, got %d", totalChunks, len(parsedInline.Chunks))
	}

	// Pre-header error under fake_stream=true must still propagate as HTTP error.
	errReq, _ := json.Marshal(map[string]any{
		"stream_id": "stream-err-429",
		"model":     "gemini-4-neon",
		"format":    "openai-responses",
		"headers":   map[string][]string{"X-Test-Error": {"429"}},
		"payload":   []byte(`{"model":"gemini-4-neon","input":"hi","stream":true}`),
	})
	_, err = p.HandleMethod(pluginabi.MethodExecutorExecuteStream, errReq)
	if err == nil {
		t.Fatal("expected error for 429 response")
	}
	pluginErr, ok := err.(*pluginabi.Error)
	if !ok || pluginErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("expected *pluginabi.Error with status 429, got %T: %v", err, err)
	}
}

func TestBuiltDynamicLibraryExists(t *testing.T) {
	ext := ".so"
	switch runtime.GOOS {
	case "darwin":
		ext = ".dylib"
	case "windows":
		ext = ".dll"
	}
	libPath := filepath.Join("..", "..", "dist", "plugins", runtime.GOOS, runtime.GOARCH, "gemini-web2api"+ext)
	info, err := os.Stat(libPath)
	if err != nil {
		t.Skipf("shared library not found at %s: %v", libPath, err)
	}
	if info.Size() < 1*1024*1024 {
		t.Fatalf("expected built shared library > 1MB, got %d bytes", info.Size())
	}
}
