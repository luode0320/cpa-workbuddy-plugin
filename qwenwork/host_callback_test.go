// host_callback_test.go 锁定宿主异步执行回调标识（host_callback_id）的全链路透传，
// 以及共享 HTTP 客户端的分阶段超时。
//
// 生产语义（2026-11-28）：客户端断连后插件仍在读上游，宿主拿不到 callback
// context 就无法取消上游请求——上游看到的并发数远大于真实在途请求，上游排队
// 随之加剧。同仓 workbuddy 2026-11-07 已补齐同一链路，qwenwork 此前漏移植。
package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestBuildHostHTTPStreamWire_CarriesHostCallbackID 锁定流桥信封携带 callback 标识。
func TestBuildHostHTTPStreamWire_CarriesHostCallbackID(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://maas.example/v1/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	raw := buildHostHTTPStreamWire(req, []byte("{}"), "cb-42")
	if !strings.Contains(string(raw), `"host_callback_id":"cb-42"`) {
		t.Fatalf("流桥信封缺少 host_callback_id，实际 = %s", raw)
	}
	var probe struct {
		HostCallbackID string `json:"host_callback_id"`
		Request        *struct {
			Method string `json:"method"`
		} `json:"request"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	if probe.HostCallbackID != "cb-42" {
		t.Fatalf("host_callback_id = %q, want cb-42", probe.HostCallbackID)
	}
	if probe.Request == nil || probe.Request.Method != http.MethodPost {
		t.Fatalf("request 子对象必须保留真实方法，got %+v", probe.Request)
	}
}

// TestBuildHostHTTPStreamWire_OmitsEmptyCallbackID 锁定空 callback 标识不写入信封。
func TestBuildHostHTTPStreamWire_OmitsEmptyCallbackID(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://maas.example/v1/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	raw := buildHostHTTPStreamWire(req, []byte("{}"), "")
	if strings.Contains(string(raw), "host_callback_id") {
		t.Fatalf("空 callback 标识不得写入信封，实际 = %s", raw)
	}
}

// TestExecutorStreamRequest_ParsesHostCallbackID 锁定执行请求结构体能解析该字段。
func TestExecutorStreamRequest_ParsesHostCallbackID(t *testing.T) {
	raw := []byte(`{"stream_id":"7","host_callback_id":"cb-9","model":"deepseek-flash"}`)
	var req executorStreamRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.HostCallbackID != "cb-9" {
		t.Fatalf("HostCallbackID = %q, want cb-9", req.HostCallbackID)
	}
}

// TestExecutors_PassHostCallbackIDToUpstream 锁定三个执行入口都把 callback 标识传下去。
//
// 这是装配层断言：函数本身测对了，调用点仍可能传空——漏传会让客户端断连后
// 上游请求继续挂着。
func TestExecutors_PassHostCallbackIDToUpstream(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	text := string(raw)
	checks := []string{
		"doExecuteOnceQwenWork(body, curSA, req.Model, maasSessionKey, req.HostCallbackID)",
		"collectUpstreamStreamOpenAI(body, sa, maasSessionKey, sseFramed, collector, req.AuthID, req.Model, req.HostCallbackID)",
		"sessionKey, maasSessionKey, req.HostCallbackID)",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Errorf("执行入口必须把 req.HostCallbackID 传下去，缺：%s", want)
		}
	}
	// 非流式入口必须用能解析 host_callback_id 的结构体。
	if strings.Contains(text, "var req pluginapi.ExecutorRequest") {
		t.Error("handleExecExecute 不得再解析裸 pluginapi.ExecutorRequest（会丢掉 host_callback_id）")
	}
	if !strings.Contains(text, "var req executorStreamRequest") {
		t.Error("handleExecExecute 必须解析 executorStreamRequest")
	}
}

// TestSharedHTTPClient_HasStagedTimeouts 锁定共享客户端的三段前置超时。
//
// 只设 http.Client.Timeout 不够：它是端到端总上限，首字之前的静默期
// （半开 TCP、服务端排队）在总超时内部仍可无限挂起。
func TestSharedHTTPClient_HasStagedTimeouts(t *testing.T) {
	transport, ok := sharedHTTPClient().Transport.(*http.Transport)
	if !ok {
		t.Fatal("共享客户端必须使用显式 http.Transport")
	}
	if transport.DialContext == nil {
		t.Fatal("缺少拨号超时（半死连接的第一道闸）")
	}
	if transport.TLSHandshakeTimeout != upstreamTLSHandshakeTimeout {
		t.Fatalf("TLS 握手超时 = %v, want %v", transport.TLSHandshakeTimeout, upstreamTLSHandshakeTimeout)
	}
	if transport.ResponseHeaderTimeout != upstreamResponseHeaderTimeout {
		t.Fatalf("首字节超时 = %v, want %v", transport.ResponseHeaderTimeout, upstreamResponseHeaderTimeout)
	}
	if transport.TLSNextProto == nil {
		t.Fatal("必须置空 TLSNextProto 才能真正禁掉 h2")
	}
	if upstreamDialTimeout <= 0 || upstreamTLSHandshakeTimeout <= 0 || upstreamResponseHeaderTimeout <= 0 {
		t.Fatal("分阶段超时常量必须为正数")
	}
	if upstreamResponseHeaderTimeout > 120*time.Second {
		t.Fatal("首字节超时不得大于客户端整体兜底上限")
	}
}
