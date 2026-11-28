// upstream_timeout_test.go 锁定 traework 上游 HTTP 客户端的分阶段超时与 h2 禁用。
//
// 生产背景（2026-11-08）：本插件 sharedHTTPClient 只有 120s 整体兜底，流式客户端
// ResponseHeaderTimeout 仅 30s；而生产实测 TTFT p90 已达 26.6s、p99 达 55.2s
// （大 input prefill），30s 会切断 p90 以上的合法慢请求。同类开源反代
// （connectedgraph/trae2api-web）取 120s。
package main

import (
	"net/http"
	"testing"
	"time"
)

// TestSharedHTTPClientStagedTimeouts 验证上游客户端配置了分阶段超时并禁用 h2。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 21:00:00；改动原因：新增，锁住分阶段超时不回退。
func TestSharedHTTPClientStagedTimeouts(t *testing.T) {
	client := sharedHTTPClient()
	if client == nil {
		t.Fatal("sharedHTTPClient() returned nil")
	}
	if client.Timeout != 120*time.Second {
		t.Fatalf("client.Timeout = %v, want 120s（整体兜底上限不得被移除）", client.Timeout)
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if tr.DialContext == nil {
		t.Fatal("DialContext 未配置：半死连接缺少建连闸门")
	}
	if tr.TLSHandshakeTimeout != upstreamTLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", tr.TLSHandshakeTimeout, upstreamTLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != upstreamResponseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, upstreamResponseHeaderTimeout)
	}
	if tr.TLSNextProto == nil || len(tr.TLSNextProto) != 0 {
		t.Fatalf("TLSNextProto 必须为非 nil 空 map 才能禁掉 h2，实际 %v", tr.TLSNextProto)
	}
}

// TestStreamHeaderTimeoutIsPerAttemptBudget 验证流式路径的超时口径已由
// 「一次干等 120s」改为「单次尝试短预算 + 换连接重试」。
//
// 推翻旧结论的依据（2026-11-29 直测上游，绕过插件与 CPA）：
//   - 旧测试假定 TTFT p90=26.6s / p99=55.2s 全是上游 prefill，故要求流式
//     ResponseHeaderTimeout >= 56s。实测 `event:timing_cost` 显示上游自身处理
//     只要 server_processing_time=901~1575ms（810KB / 152k tokens 亦然），
//     真正耗时的是**响应头到达之前**的链路拥塞。
//   - 该拥塞是连接级的：同 body 8 次对照，单次干等 p90=max=80.7s；
//     6~8s 看门狗 + 最多 3 次重发 p90=max=11.7s（恢复后单次仅 ~2s）。
//
// 所以流式路径正确的超时是「短而可重试」：短预算负责及早甩掉拥塞连接，
// 重试环负责让合法慢请求不被切断（见 stream_open_retry_test.go）。
// 非流式 sharedHTTPClient 仍保留 120s 整体兜底 + 120s 响应头超时。
//
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-29；改动原因：流式响应头超时改为单次尝试预算，
// 与 open 重试环配套；旧断言要求等于 120s，会把重试环拖成 120s × N。
func TestStreamHeaderTimeoutIsPerAttemptBudget(t *testing.T) {
	if upstreamResponseHeaderTimeout < 120*time.Second {
		t.Fatalf("upstreamResponseHeaderTimeout = %v，非流式路径仍需 >=120s",
			upstreamResponseHeaderTimeout)
	}
	orig := loadedHostBridgeOpenTimeout()
	t.Cleanup(func() { setHostBridgeOpenTimeout(orig) })
	setHostBridgeOpenTimeout(streamOpenHeaderTimeoutDefault)

	sc := streamHTTPClient()
	str, ok := sc.Transport.(*http.Transport)
	if !ok || str == nil {
		t.Fatalf("stream transport type = %T", sc.Transport)
	}
	if want := loadedHostBridgeOpenTimeout(); str.ResponseHeaderTimeout != want {
		t.Fatalf("stream ResponseHeaderTimeout = %v, want %v（必须跟随单次尝试预算）",
			str.ResponseHeaderTimeout, want)
	}
	if str.ResponseHeaderTimeout > 10*time.Second {
		t.Fatalf("stream ResponseHeaderTimeout = %v，单次尝试预算必须短到能被重试环覆盖",
			str.ResponseHeaderTimeout)
	}
}
