// upstream_timeout_test.go 锁定上游 HTTP 客户端的分阶段超时与 h2 禁用。
//
// 生产背景（2026-11-08）：只设 http.Client.Timeout 时，首字节之前的静默期
// （半开 TCP、上游排队）仍可在总超时内部挂起，命中总超时又会把已产出正文的
// 长流一起掐断。本插件此前只有 120s 整体超时，兄弟插件 workbuddy 0.21.14
// 已按同一口径补齐。
package main

import (
	"net/http"
	"testing"
	"time"
)

// TestSharedHTTPClientStagedTimeouts 验证上游客户端配置了分阶段超时并禁用 h2。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 20:10:00；改动原因：新增，锁住分阶段超时不回退。
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
	// 置空 TLSNextProto 才是真正禁 h2；只设 ForceAttemptHTTP2=false 时 ALPN
	// 仍会协商出 h2，半死流上表现为响应头等待超时。
	if tr.TLSNextProto == nil {
		t.Fatal("TLSNextProto 必须为非 nil 空 map 才能禁掉 h2")
	}
	if len(tr.TLSNextProto) != 0 {
		t.Fatalf("TLSNextProto 应置空，实际 %d 项", len(tr.TLSNextProto))
	}
}
