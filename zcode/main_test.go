package main

import (
	"encoding/json"
	"os"
	"testing"

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
	if !reg.Capabilities.ModelProvider || !reg.Capabilities.Executor || !reg.Capabilities.Scheduler || !reg.Capabilities.ManagementAPI {
		t.Fatalf("missing required capabilities: %+v", reg.Capabilities)
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
