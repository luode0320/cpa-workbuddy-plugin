// qwenwork_contract_test.go 覆盖 QwenWork 上游契约的三个关键解析点：
// Device Flow 授权 URL 构造、MaaS 签发/轮换响应解析、chat-modes 模型目录解析。
package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestDeviceFlowURLConstruction 验证授权 URL 携带全部必需参数，且域名是
// gateway（qwenwork.cn 上同路径 404，2026-10-17 对照实测）。
func TestDeviceFlowURLConstruction(t *testing.T) {
	verifier, challenge := makePKCE()
	if verifier == "" || challenge == "" {
		t.Fatal("makePKCE must return non-empty verifier and challenge")
	}
	if len(verifier) < 43 || len(verifier) > 128 {
		t.Fatalf("PKCE verifier length = %d, want 43..128 per RFC 7636", len(verifier))
	}
	// challenge 必须是 verifier 的 S256 摘要，不是原值。
	if challenge == verifier {
		t.Fatal("challenge must be the S256 digest, not the verifier")
	}

	q := url.Values{}
	q.Set("challenge", challenge)
	q.Set("challenge_method", "S256")
	q.Set("nonce", "nonce-1")
	q.Set("machine_id", "machine-1")
	q.Set("client_id", qwenworkClientID)
	q.Set("redirect_uri", qwenworkRedirectURI)
	authURL := qwenworkAuthBase + "/device/selectAccounts?" + q.Encode()

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("auth URL must parse: %v", err)
	}
	if parsed.Host != "gateway.qwenwork.cn" {
		t.Fatalf("auth host = %q, want gateway.qwenwork.cn", parsed.Host)
	}
	if parsed.Path != "/device/selectAccounts" {
		t.Fatalf("auth path = %q, want /device/selectAccounts", parsed.Path)
	}
	got := parsed.Query()
	for _, key := range []string{"challenge", "challenge_method", "nonce", "machine_id", "client_id", "redirect_uri"} {
		if got.Get(key) == "" {
			t.Fatalf("auth URL missing query param %q", key)
		}
	}
	if got.Get("challenge_method") != "S256" {
		t.Fatalf("challenge_method = %q, want S256", got.Get("challenge_method"))
	}
	if got.Get("client_id") != qwenworkClientID {
		t.Fatalf("client_id = %q, want %q", got.Get("client_id"), qwenworkClientID)
	}
	if got.Get("redirect_uri") != "qwenwork-cn://" {
		t.Fatalf("redirect_uri = %q, want qwenwork-cn://", got.Get("redirect_uri"))
	}
}

// TestMaasSessionUsable 验证票据可用性判定：剩余时间必须超过刷新提前量。
func TestMaasSessionUsable(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		sess *maasSession
		want bool
	}{
		{"nil session", nil, false},
		{"empty key", &maasSession{ExpiresAtMs: now.Add(2 * time.Hour).UnixMilli()}, false},
		{"no expiry", &maasSession{AccessKey: "sk-mm-v1.x"}, false},
		{"expires soon", &maasSession{
			AccessKey:   "sk-mm-v1.x",
			ExpiresAtMs: now.Add(10 * time.Minute).UnixMilli(),
		}, false},
		{"fresh", &maasSession{
			AccessKey:   "sk-mm-v1.x",
			ExpiresAtMs: now.Add(2 * time.Hour).UnixMilli(),
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sess.usable(now); got != tc.want {
				t.Fatalf("usable() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMaasMintResponseParsing 验证签发响应解析：snake_case 字段、2xx 语义、
// base_url 兜底与尾部斜杠裁剪。
func TestMaasMintResponseParsing(t *testing.T) {
	raw := []byte(`{"access_token":"sk-mm-v1.abc","base_url":"https://maas.example.com/",` +
		`"session_id":"sess-1","expires_at_ms":1767225600000,"ttl_ms":7200000}`)
	var out maasMintResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("mint response parse: %v", err)
	}
	if out.AccessToken != "sk-mm-v1.abc" {
		t.Fatalf("access_token = %q", out.AccessToken)
	}
	if strings.TrimRight(out.BaseURL, "/") != "https://maas.example.com" {
		t.Fatalf("base_url = %q", out.BaseURL)
	}
	if out.TTLMs != 7200000 {
		t.Fatalf("ttl_ms = %d, want 7200000 (2h)", out.TTLMs)
	}
	if out.SessionID != "sess-1" {
		t.Fatalf("session_id = %q", out.SessionID)
	}
}

// TestMaasExchangeResponseParsing 验证轮换响应解析（expires_at 是毫秒时间戳）。
func TestMaasExchangeResponseParsing(t *testing.T) {
	raw := []byte(`{"access_token":"sk-mm-v1.new","expires_at":1767225600000}`)
	var out maasExchangeResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("exchange response parse: %v", err)
	}
	if out.AccessToken != "sk-mm-v1.new" {
		t.Fatalf("access_token = %q", out.AccessToken)
	}
	if out.ExpiresAt != 1767225600000 {
		t.Fatalf("expires_at = %d", out.ExpiresAt)
	}
}

// TestMaasInferenceURL 验证推理端点拼接：优先票据下发的 base_url，缺省时
// 回落到客户端同款默认域名，且不会出现双斜杠。
func TestMaasInferenceURL(t *testing.T) {
	withBase := maasInferenceURL(&maasSession{BaseURL: "https://maas.example.com/"}, "/v1/chat/completions")
	if withBase != "https://maas.example.com/v1/chat/completions" {
		t.Fatalf("url = %q", withBase)
	}
	fallback := maasInferenceURL(&maasSession{}, "/v1/chat/completions")
	if fallback != maasDefaultBaseURL+"/v1/chat/completions" {
		t.Fatalf("fallback url = %q", fallback)
	}
}

// TestMaasCacheKey 验证缓存键：优先 UID，UID 缺失时退化为 token 指纹，
// 且指纹不泄露 token 原值。
func TestMaasCacheKey(t *testing.T) {
	withUID := &storedAuth{Account: storedAccount{UID: "user-1"}, Auth: storedTokens{AccessToken: "dt-secret"}}
	if got := maasCacheKey(withUID); got != "user-1" {
		t.Fatalf("cache key = %q, want user-1", got)
	}
	noUID := &storedAuth{Auth: storedTokens{AccessToken: "dt-secret"}}
	got := maasCacheKey(noUID)
	if !strings.HasPrefix(got, "token:") {
		t.Fatalf("cache key = %q, want token: prefix", got)
	}
	if strings.Contains(got, "dt-secret") {
		t.Fatal("cache key must not contain the raw access token")
	}
	if got == maasCacheKey(&storedAuth{Auth: storedTokens{AccessToken: "dt-other"}}) {
		t.Fatal("different tokens must produce different cache keys")
	}
}

// TestParseChatModesCatalog 验证模型目录解析：过滤禁用与非 openai 协议，
// 显示名按 zh→en→key 回退，context 取 max_input_tokens。
func TestParseChatModesCatalog(t *testing.T) {
	raw := []byte(`{"schema_version":2,"catalog":[` +
		`{"key":"flash","enable":true,"format":"openai/chat-completions","max_input_tokens":1000000,` +
		`"i18n":{"display_name":{"zh":"标准","en":"Standard"}}},` +
		`{"key":"pro","enable":true,"format":"openai/chat-completions","max_input_tokens":1000000,` +
		`"i18n":{"display_name":{"en":"Pro"}}},` +
		`{"key":"disabled-model","enable":false,"format":"openai/chat-completions"},` +
		`{"key":"anthropic-model","enable":true,"format":"anthropic/messages"},` +
		`{"key":"","enable":true,"format":"openai/chat-completions"}` +
		`]}`)
	models, err := parseChatModesCatalog(raw)
	if err != nil {
		t.Fatalf("parseChatModesCatalog: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2 (disabled/anthropic/empty filtered)", len(models))
	}
	if models[0].ID != "flash" || models[0].Name != "标准" {
		t.Fatalf("models[0] = %+v, want flash/标准", models[0])
	}
	if models[1].ID != "pro" || models[1].Name != "Pro" {
		t.Fatalf("models[1] = %+v, want pro/Pro (en fallback)", models[1])
	}
	if models[0].ContextLength != 1000000 {
		t.Fatalf("context = %d, want 1000000", models[0].ContextLength)
	}
	if models[0].MaxCompletionTokens < minMaxTokensFloor {
		t.Fatalf("MaxCompletionTokens = %d, want >= %d", models[0].MaxCompletionTokens, minMaxTokensFloor)
	}
}

// TestParseChatModesCatalog_NoUsableModels 验证没有可用模型时返回错误，
// 让调用方回退静态列表而不是返回空目录。
func TestParseChatModesCatalog_NoUsableModels(t *testing.T) {
	raw := []byte(`{"schema_version":2,"catalog":[{"key":"x","enable":false}]}`)
	if _, err := parseChatModesCatalog(raw); err == nil {
		t.Fatal("expected error when no enabled openai models")
	}
	if _, err := parseChatModesCatalog([]byte(`not-json`)); err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

// TestOfficialSpecsCoverQwenWorkTiers 验证上游真实下发的三档模型都拿到统一规格。
//
// 2026-10-30 起所有模型共用统一上限（ContextWindow 固定 1048576、
// 推理 128k / 非推理 64k），不再区分已收录与未收录。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：对齐全局统一上限。
func TestOfficialSpecsCoverQwenWorkTiers(t *testing.T) {
	for _, id := range []string{"flash", "pro", "qwen3.8-max-preview"} {
		spec := resolveOfficialModelSpec(id)
		if spec.ContextWindow != defaultContextWindow {
			t.Errorf("%s ContextWindow = %d, want %d", id, spec.ContextWindow, defaultContextWindow)
		}
		if spec.ReasoningMax != defaultReasoningMaxTokens {
			t.Errorf("%s ReasoningMax = %d, want %d", id, spec.ReasoningMax, defaultReasoningMaxTokens)
		}
		if spec.MaxOutputTokens != defaultMaxOutputTokens {
			t.Errorf("%s MaxOutputTokens = %d, want %d", id, spec.MaxOutputTokens, defaultMaxOutputTokens)
		}
	}
}
