package main

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// 回归：兜底 HTTP client 必须带分阶段超时。
//
// 此前只有 http.Client.Timeout（端到端总上限），拨号 / TLS 握手 / 首字节三个
// 阶段全部无上限，半开 TCP 可以让请求在「还没有任何字节」的状态下挂到总超时
// 或更久。生产实证（2026-11-07）：usage feed 中 TTFT 最长 150.5s、单请求总耗时
// 最长 694s，全部 status_code=0（传输级）。
//
// 本用例锁死三个前置闸门的存在与量级，防止后续「统一简化 transport」时被删掉。
func TestSharedHTTPClient_HasLayeredTimeouts(t *testing.T) {
	client := sharedHTTPClient()
	if client == nil {
		t.Fatal("sharedHTTPClient() = nil")
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 类型 = %T，want *http.Transport", client.Transport)
	}

	// 1. 首字节硬上限：这是本次修复的核心，缺失即无法收敛 150s 级静默。
	if tr.ResponseHeaderTimeout != upstreamResponseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %v，want %v", tr.ResponseHeaderTimeout, upstreamResponseHeaderTimeout)
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Fatal("ResponseHeaderTimeout 必须为正数，0 表示无限等待")
	}
	// 2. TLS 握手上限。
	if tr.TLSHandshakeTimeout != upstreamTLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %v，want %v", tr.TLSHandshakeTimeout, upstreamTLSHandshakeTimeout)
	}
	// 3. 拨号上限（通过 DialContext 委托给 net.Dialer，无法直接读回，改为
	//    断言 DialContext 已注入且三个常量量级合理）。
	if tr.DialContext == nil {
		t.Fatal("DialContext 未注入，拨号阶段将无上限")
	}
	// 4. 禁 h2：TLSNextProto 必须是已初始化的空 map（nil 会回落默认 ALPN 协商）。
	if tr.TLSNextProto == nil {
		t.Fatal("TLSNextProto = nil，ALPN 仍可能协商出 h2（半死流会报 http2: timeout awaiting response headers）")
	}
	if len(tr.TLSNextProto) != 0 {
		t.Fatalf("TLSNextProto 长度 = %d，want 0（完全禁用 h2）", len(tr.TLSNextProto))
	}
	// 5. 整体兜底上限仍保留，且必须大于首字节上限（否则首字节闸门永远不生效）。
	if client.Timeout <= 0 {
		t.Fatal("client.Timeout 必须为正数")
	}
	if client.Timeout <= upstreamResponseHeaderTimeout {
		t.Fatalf("client.Timeout(%v) 必须大于 ResponseHeaderTimeout(%v)，否则首字节闸门形同虚设",
			client.Timeout, upstreamResponseHeaderTimeout)
	}
}

// 回归：首字节闸门取值必须落在「不误杀健康慢请求」与「能收敛极端静默」之间。
//
// 生产观测：健康请求的 TTFB 最坏约 16s（大 input prefill），异常请求可达
// 150s+。60s 是二者之间既保守又有效的折中。本用例防止后续被随手改成
// 极小值（误杀）或极大值（等于没设）。
func TestUpstreamResponseHeaderTimeout_InHealthyRange(t *testing.T) {
	const healthyWorstObserved = 20 * time.Second  // 生产健康 TTFB 观测上界（留余量）
	const pathologicalObserved = 150 * time.Second // 生产异常 TTFT 观测

	if upstreamResponseHeaderTimeout <= healthyWorstObserved {
		t.Fatalf("ResponseHeaderTimeout(%v) 不应小于健康观测上界(%v)，否则会误杀正常大 input 请求",
			upstreamResponseHeaderTimeout, healthyWorstObserved)
	}
	if upstreamResponseHeaderTimeout >= pathologicalObserved {
		t.Fatalf("ResponseHeaderTimeout(%v) 不应达到异常观测(%v)，否则无法收敛长尾",
			upstreamResponseHeaderTimeout, pathologicalObserved)
	}
}

// 回归：hostHTTPDoStream 必须把 host_callback_id 写进线上信封的外层。
//
// 此前 rpcHostHTTPRequestWire 只有 Request 子对象，宿主下发的 callback id 被
// 静默丢弃；后果是插件在 context.Background() 上续读上游，客户端断连无法
// 传播，形成死流并长期占用宿主 stream 槽位。本用例锁死信封形状。
func TestRPCHostHTTPRequestWire_CarriesHostCallbackID(t *testing.T) {
	wire := rpcHostHTTPRequestWire{
		HostCallbackID: "callback-42",
		Request: &rpcHostHTTPInner{
			Method: http.MethodPost,
			URL:    "https://copilot.tencent.com/v2/chat/completions",
		},
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, `"host_callback_id":"callback-42"`) {
		t.Fatalf("信封缺少 host_callback_id，实际 = %s", got)
	}
	// Request 必须仍然嵌套在 "request" 子对象下（宿主优先读该形态）。
	if !strings.Contains(got, `"request":{`) {
		t.Fatalf("信封缺少嵌套 request 子对象，实际 = %s", got)
	}

	// 空 callback id 时必须完全省略，避免给宿主传空串。
	wire.HostCallbackID = ""
	raw, err = json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if strings.Contains(string(raw), "host_callback_id") {
		t.Fatalf("空 callback id 不应出现在信封中，实际 = %s", string(raw))
	}
}

// 回归：executorStreamRequest 必须能解析宿主的 host_callback_id。
//
// 非流式入口（handleExecExecute）此前用裸 pluginapi.ExecutorRequest 解析，
// 该结构没有 HostCallbackID 字段，导致宿主对 executor.execute 下发的 callback
// id 被丢弃。改解析 executorStreamRequest 后两条路径共用同一形状。
func TestExecutorStreamRequest_ParsesHostCallbackID(t *testing.T) {
	raw := []byte(`{"stream_id":"7","host_callback_id":"cb-9","model":"deepseek-v4.1-flash"}`)
	var req executorStreamRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.HostCallbackID != "cb-9" {
		t.Fatalf("HostCallbackID = %q，want cb-9", req.HostCallbackID)
	}
	if req.StreamID != "7" {
		t.Fatalf("StreamID = %q，want 7", req.StreamID)
	}
	if req.Model != "deepseek-v4.1-flash" {
		t.Fatalf("Model = %q，want deepseek-v4.1-flash", req.Model)
	}
}

// 回归（端到端）：hostHTTPDoStream 实际发出的 RPC 信封必须带 host_callback_id。
//
// 上一条用例只验证了结构体本身的 JSON 形状，无法捕获「结构体有字段但调用点
// 忘了赋值」这种最真实的回归形态——变异验证已证实：把调用点的 HostCallbackID
// 删掉后，只测结构体的用例依然全绿。这里通过 hostStreamOpenFn 接缝拦截真实
// 信封，才能锁死调用点。
func TestBuildHostHTTPStreamWire_CarriesCallbackID(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://copilot.tencent.com/v2/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer secret")

	got := string(buildHostHTTPStreamWire(req, []byte(`{"model":"m"}`), "cb-live-1"))
	if !strings.Contains(got, `"host_callback_id":"cb-live-1"`) {
		t.Fatalf("信封缺少 host_callback_id，实际 = %s", got)
	}
	if !strings.Contains(got, `"request":{`) {
		t.Fatalf("信封缺少嵌套 request，实际 = %s", got)
	}
	// body 字段类型是 []byte，JSON 编码为 base64（宿主侧同样按 []byte 解码）。
	if !strings.Contains(got, `"body":"eyJtb2RlbCI6Im0ifQ=="`) {
		t.Fatalf("信封未携带 base64 请求体，实际 = %s", got)
	}

	// 空 callback id 必须省略字段，不给宿主传空串。
	empty := string(buildHostHTTPStreamWire(req, nil, ""))
	if strings.Contains(empty, "host_callback_id") {
		t.Fatalf("空 callback id 不应出现在信封中，实际 = %s", empty)
	}
}

// 回归（接线级）：两条执行入口必须把 req.HostCallbackID 传下去。
//
// 上面几条用例覆盖了「信封形状」与「wire 构造」，但都无法捕获入口处忘记
// 传参这种回归——变异验证已证实：把 handleExecStream 里的 req.HostCallbackID
// 换成 "" 后全部用例依然全绿。这里用源码级断言锁死接线，与
// test/traework/credits_sweep_test.go 读源码核对的做法同源。
func TestExecutors_PassHostCallbackIDToUpstream(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	text := string(src)

	// 1. 流式入口：pumpUpstreamStream 的最后一参必须是 req.HostCallbackID。
	if !strings.Contains(text, "accountLabel, sessionKey, req.HostCallbackID)") {
		t.Error("handleExecStream 未把 req.HostCallbackID 传给 pumpUpstreamStream")
	}
	// 2. 同步流式入口：collectUpstreamStream 必须带 req.HostCallbackID。
	if !strings.Contains(text, "collector, upstreamModel, req.HostCallbackID)") {
		t.Error("handleExecStream 的同步分支未把 req.HostCallbackID 传给 collectUpstreamStream")
	}
	// 3. 非流式入口：doExecuteOnce 必须带 req.HostCallbackID。
	if !strings.Contains(text, "upstreamModel, req.HostCallbackID)") {
		t.Error("handleExecExecute 未把 req.HostCallbackID 传给 doExecuteOnce")
	}
	// 4. 非流式入口必须用 executorStreamRequest 解析（裸 ExecutorRequest 没有该字段）。
	if strings.Contains(text, "var req pluginapi.ExecutorRequest") {
		t.Error("handleExecExecute 仍用裸 pluginapi.ExecutorRequest 解析，会丢失 host_callback_id")
	}
}

// 确保 tls.Conn 类型仍在视野内（TLSNextProto 的签名依赖它），
// 避免后续重构时误删 import 而让上面的断言失去编译期保护。
var _ = func(c *tls.Conn) {}
