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

// TestResponseHeaderTimeoutCoversSlowPrefill 验证响应头超时不低于生产 TTFT p99。
//
// 30s 的旧值会切断 p90 以上的合法慢请求（生产 TTFT p90 26.6s / p99 55.2s）。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 21:00:00；改动原因：新增。
func TestResponseHeaderTimeoutCoversSlowPrefill(t *testing.T) {
	const productionTTFTP99 = 56 * time.Second
	if upstreamResponseHeaderTimeout < productionTTFTP99 {
		t.Fatalf("ResponseHeaderTimeout = %v，低于生产 TTFT p99（%v）会切断合法慢请求",
			upstreamResponseHeaderTimeout, productionTTFTP99)
	}
	// 流式客户端必须复用同一取值，避免两条路径口径漂移。
	sc := streamHTTPClient()
	str, ok := sc.Transport.(*http.Transport)
	if !ok || str == nil {
		t.Fatalf("stream transport type = %T", sc.Transport)
	}
	if str.ResponseHeaderTimeout != upstreamResponseHeaderTimeout {
		t.Fatalf("stream ResponseHeaderTimeout = %v, want %v", str.ResponseHeaderTimeout, upstreamResponseHeaderTimeout)
	}
}
