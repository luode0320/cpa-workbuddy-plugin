package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// withTraeModelServer 把动态模型请求定向到本地测试服务，并在用例结束后恢复全局配置。
//
// [参数] t: 当前测试；handler: 本地上游处理器。
// [返回] 无。
// 最近修改时间：2026-08-30 02:43:13；改动原因：隔离动态模型测试，避免访问真实 Trae 上游。
func withTraeModelServer(t *testing.T, handler http.HandlerFunc) {
	// 1. 启动本地上游并保存原配置，测试结束时关闭服务和恢复配置。
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	cfgMu.Lock()
	previous := *traeCfg
	traeCfg.APIHost = server.URL
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		*traeCfg = previous
		cfgMu.Unlock()
	})
}

// decodeTraeModelResponse 解开插件 envelope，返回模型响应供断言使用。
//
// [参数] t: 当前测试；raw: handleModelForAuth 返回的 envelope JSON。
// [返回] 已解析的模型响应。
// 最近修改时间：2026-08-30 02:43:13；改动原因：集中校验 envelope，避免各用例重复解析代码。
func decodeTraeModelResponse(t *testing.T, raw []byte) pluginapi.ModelResponse {
	// 1. 先校验外层执行结果，再解析宿主模型响应。
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("envelope not ok: %+v", env.Error)
	}
	var resp pluginapi.ModelResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("decode model response: %v", err)
	}
	return resp
}

// modelRequestWithToken 构造包含最小可用账号凭据的 model.for_auth 请求。
//
// [参数] t: 当前测试。
// [返回] 已序列化的账号级模型请求。
// 最近修改时间：2026-08-30 02:43:13；改动原因：所有动态模型用例使用同一最小凭据形态。
func modelRequestWithToken(t *testing.T) []byte {
	// 1. 先序列化账号存储，再组装宿主账号级模型请求。
	t.Helper()
	storageJSON, err := json.Marshal(traeAuth{Token: "test-token", UserID: "test-user"})
	if err != nil {
		t.Fatalf("marshal storage: %v", err)
	}
	raw, err := json.Marshal(pluginapi.AuthModelRequest{StorageJSON: storageJSON})
	if err != nil {
		t.Fatalf("marshal model request: %v", err)
	}
	return raw
}

// TestHandleModelForAuthDynamicSuccess 验证账号模型接口返回精确 config_name、展示名回退和重复去重。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-08-30 02:43:13；改动原因：覆盖动态模型发现的请求契约和成功解析路径。
func TestHandleModelForAuthDynamicSuccess(t *testing.T) {
	// 1. 配置本地动态模型上游并核对请求契约。
	resetTraeConfiguredModels(t)
	withTraeModelServer(t, func(w http.ResponseWriter, r *http.Request) {
		// 1. 验证动态发现使用正确路径、认证头和完整模型池请求参数。
		if r.Method != http.MethodPost || r.URL.Path != traeModelDetailPath {
			t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.Path, traeModelDetailPath)
		}
		if got := r.Header.Get("Authorization"); got != "Cloud-IDE-JWT test-token" {
			t.Errorf("Authorization = %q", got)
		}
		var payload traeModelDetailRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Function != soloWorkLite || payload.ConfigNames != nil || !payload.PolyPrompt {
			t.Errorf("unexpected payload: %+v", payload)
		}

		// 2. 返回重复、缺失展示名和空 ID，验证解析层只保留有效唯一模型。
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config_info_list":[{"config_name":"Doubao-Seed-2.1-Pro","display_config":{"display_name":"Seed 2.1 Pro"}},{"config_name":"qwen3.8-max","display_config":{}},{"config_name":"Doubao-Seed-2.1-Pro","display_config":{"display_name":"重复项"}},{"config_name":"","display_config":{"display_name":"空项"}}]}`))
	})

	raw, err := handleModelForAuth(modelRequestWithToken(t))
	if err != nil {
		t.Fatalf("handleModelForAuth: %v", err)
	}
	resp := decodeTraeModelResponse(t, raw)
	if len(resp.Models) != 2 {
		t.Fatalf("model count = %d, want 2: %+v", len(resp.Models), resp.Models)
	}
	if resp.Models[0].ID != "Doubao-Seed-2.1-Pro" || resp.Models[0].Name != "Seed 2.1 Pro" {
		t.Fatalf("models[0] = %+v", resp.Models[0])
	}
	if resp.Models[1].ID != "qwen3.8-max" || resp.Models[1].Name != "qwen3.8-max" {
		t.Fatalf("models[1] = %+v", resp.Models[1])
	}
}

// TestHandleModelForAuthFallbacks 验证无效凭据和各类上游异常均回退到配置模型。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-08-30 02:43:13；改动原因：锁定动态模型不可用时不清空客户端模型的降级语义。
func TestHandleModelForAuthFallbacks(t *testing.T) {
	// 1. 枚举请求解析、凭据解析、上游状态和响应内容的失败形态。
	tests := []struct {
		name       string
		request    func(*testing.T) []byte
		statusCode int
		body       string
	}{
		{name: "invalid request", request: func(*testing.T) []byte { return []byte("{") }},
		{name: "missing storage", request: func(t *testing.T) []byte {
			raw, err := json.Marshal(pluginapi.AuthModelRequest{})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			return raw
		}},
		{name: "non 2xx", request: modelRequestWithToken, statusCode: http.StatusUnauthorized, body: `{"error":"unauthorized"}`},
		{name: "invalid json", request: modelRequestWithToken, statusCode: http.StatusOK, body: `{`},
		{name: "empty list", request: modelRequestWithToken, statusCode: http.StatusOK, body: `{"config_info_list":[]}`},
	}

	// 2. 每个失败形态都必须返回 config_yaml 配置的同一兜底模型。
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// 2.1 每个异常用例都独立重置配置，并断言返回同一兜底模型。
			resetTraeConfiguredModels(t)
			parseModelsConfig([]any{"configured-fallback"})
			if tc.statusCode != 0 {
				withTraeModelServer(t, func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.statusCode)
					_, _ = w.Write([]byte(tc.body))
				})
			}

			raw, err := handleModelForAuth(tc.request(t))
			if err != nil {
				t.Fatalf("handleModelForAuth: %v", err)
			}
			resp := decodeTraeModelResponse(t, raw)
			if len(resp.Models) != 1 || resp.Models[0].ID != "configured-fallback" {
				t.Fatalf("fallback models = %+v", resp.Models)
			}
		})
	}
}
