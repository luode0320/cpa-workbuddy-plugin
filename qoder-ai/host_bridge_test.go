package main

import (
	"encoding/json"
	"testing"
)

// TestParseHostHTTPDoResult_HostUntaggedPascalCase 是宿主桥状态码解码的回归测试。
// 宿主（v7.2.x）序列化 pluginapi.HTTPResponse 时未加 json tag，线协议键名是
// PascalCase 的 {"StatusCode":200,...}；旧解析器用 `json:"status_code"` 永远匹配不上，
// 使每个非流式桥响应 StatusCode 恒为 0，404/4xx/5xx 被当作成功，
// 在 Linux 生产上导致签到假成功。
//
// 测试夹具用 json.Marshal 复刻宿主真实输出：未加 tag 的匿名结构体。
func TestParseHostHTTPDoResult_HostUntaggedPascalCase(t *testing.T) {
	hostResp := struct {
		StatusCode int
		Headers    map[string][]string
		Body       []byte
	}{
		StatusCode: 200,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       []byte(`{"code":0,"msg":"ok"}`),
	}
	wire, err := json.Marshal(hostResp)
	if err != nil {
		t.Fatalf("marshal host fixture: %v", err)
	}

	resp, err := parseHostHTTPDoResult(wire)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200 (旧 tag 解析器在此返回 0)", resp.StatusCode)
	}
	if string(resp.Body) != `{"code":0,"msg":"ok"}` {
		t.Fatalf("Body = %q, want upstream body", resp.Body)
	}
	if resp.Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", resp.Headers.Get("Content-Type"))
	}
}

// TestParseHostHTTPDoResult_SnakeCaseTaggedVariant 验证防御性兼容：
// 若未来宿主版本补上 snake_case tag（{"status_code":200,...}），解析仍须正确。
func TestParseHostHTTPDoResult_SnakeCaseTaggedVariant(t *testing.T) {
	wire := []byte(`{"status_code":401,"headers":{"Www-Authenticate":["Bearer"]},"body":"dW5hdXRo"}`)
	resp, err := parseHostHTTPDoResult(wire)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("StatusCode = %d, want 401", resp.StatusCode)
	}
	if string(resp.Body) != "unauth" {
		t.Fatalf("Body = %q, want unauth", resp.Body)
	}
	if resp.Headers.Get("Www-Authenticate") != "Bearer" {
		t.Fatalf("Www-Authenticate = %q, want Bearer", resp.Headers.Get("Www-Authenticate"))
	}
}

// TestParseHostHTTPDoResult_Malformed 验证非法载荷必须返回错误，
// 由调用方记录日志并回退直连，而不是得到零值响应。
func TestParseHostHTTPDoResult_Malformed(t *testing.T) {
	if _, err := parseHostHTTPDoResult([]byte(`{not json`)); err == nil {
		t.Fatal("expected error for malformed payload, got nil")
	}
}
