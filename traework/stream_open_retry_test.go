package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 回归目标：上游拥塞表现为「响应头迟迟不到」，而换一条新连接重发即可绕开。
//
// 取证（2026-11-29，同 body 810KB 交错对照，每次新建连接）：
//
//	单次干等                 p50=2.8s  p90=80.7s max=80.7s
//	6~8s 看门狗 + 最多 3 次重发 p50=3.0s  p90=11.7s max=11.7s
//
// 而 event:timing_cost 显示上游自身处理只要 server_processing_time=901~1575ms，
// 即长尾里真正属于模型的只有 1 秒多，其余全部发生在响应头到达之前。
// 因此 hostHTTPDoStream 必须「短预算 + 换连接重试」，不能干等到 180s。

// TestStreamOpenDefaultsAreShortAndBounded 守住两个默认值：
// 单次尝试预算必须是秒级（旧的 180s 会让每次重试前白等三分钟），
// 尝试次数必须有限（否则残余全卡时总耗时会失控）。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-29；改动原因：新增重试环时同步锁住默认值。
func TestStreamOpenDefaultsAreShortAndBounded(t *testing.T) {
	if got := hostBridgeOpenTimeoutDefault; got != 6*time.Second {
		t.Fatalf("hostBridgeOpenTimeoutDefault = %v, want 6s", got)
	}
	if got := streamOpenMaxAttemptsDefault; got != 3 {
		t.Fatalf("streamOpenMaxAttemptsDefault = %d, want 3", got)
	}
	if got := streamOpenHeaderTimeoutDefault; got != hostBridgeOpenTimeoutDefault {
		t.Fatalf("streamOpenHeaderTimeoutDefault = %v, want == hostBridgeOpenTimeoutDefault", got)
	}
}

// TestStreamOpenRetryRecoversAfterBridgeTimeout 覆盖主路径：第 1 次走宿主桥
// 被看门狗判定超时，第 2 次换直连新连接立刻拿到响应头，整体成功返回。
// 断言桥只被调用一次（不重复漏宿主侧请求）、直连恰好被调用一次。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-29；改动原因：为 open 重试环加确定性回归。
func TestStreamOpenRetryRecoversAfterBridgeTimeout(t *testing.T) {
	origAvailable := hostBridgeAvailableFn
	origOpen := hostStreamOpenFn
	origDirect := hostStreamDirectFn
	origAttempts := streamOpenMaxAttempts
	t.Cleanup(func() {
		hostBridgeAvailableFn = origAvailable
		hostStreamOpenFn = origOpen
		hostStreamDirectFn = origDirect
		streamOpenMaxAttempts = origAttempts
	})

	hostBridgeAvailableFn = func() bool { return true }
	streamOpenMaxAttempts = 3

	bridgeCalls := 0
	hostStreamOpenFn = func(wireJSON []byte) ([]byte, error) {
		bridgeCalls++
		return nil, errHostBridgeOpenTimeout
	}

	directCalls := 0
	directBody := []byte("event: output\ndata: {\"response\":\"ok\"}\n\nevent: done\ndata: {}\n\n")
	hostStreamDirectFn = func(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
		directCalls++
		return &hostHTTPStream{liveBody: io.NopCloser(bytes.NewReader(directBody))}, 200, http.Header{}, nil
	}

	req, err := http.NewRequest(http.MethodPost,
		"https://trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatalf("build req: %v", err)
	}

	stream, status, _, err := hostHTTPDoStream(req, "cb-1")
	if err != nil {
		t.Fatalf("hostHTTPDoStream returned error: %v", err)
	}
	if stream == nil {
		t.Fatal("hostHTTPDoStream returned nil stream")
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if bridgeCalls != 1 {
		t.Fatalf("bridge attempts = %d, want 1 (retries must not re-enter the bridge)", bridgeCalls)
	}
	if directCalls != 1 {
		t.Fatalf("direct attempts = %d, want 1", directCalls)
	}
}

// TestStreamOpenRetryStopsOnUpstreamStatus 覆盖「上游已给出明确 HTTP 结论」：
// 桥返回 429 且没有 stream_id 时不重试，直接把该状态码交回调用方。
// 重试只会把同样的账号/限流错误再打一次上游。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-29；改动原因：新增，防止重试吃掉上游的限流结论。
func TestStreamOpenRetryStopsOnUpstreamStatus(t *testing.T) {
	origAvailable := hostBridgeAvailableFn
	origOpen := hostStreamOpenFn
	origDirect := hostStreamDirectFn
	origAttempts := streamOpenMaxAttempts
	t.Cleanup(func() {
		hostBridgeAvailableFn = origAvailable
		hostStreamOpenFn = origOpen
		hostStreamDirectFn = origDirect
		streamOpenMaxAttempts = origAttempts
	})

	hostBridgeAvailableFn = func() bool { return true }
	streamOpenMaxAttempts = 3

	hostStreamOpenFn = func(wireJSON []byte) ([]byte, error) {
		// 伪造一个「有状态码、无 stream_id」的宿主信封（envelope.ok=true）。
		return []byte(`{"ok":true,"result":{"status_code":429,"headers":{},"stream_id":""}}`), nil
	}
	directCalls := 0
	hostStreamDirectFn = func(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
		directCalls++
		return nil, 0, nil, io.ErrUnexpectedEOF
	}

	req, err := http.NewRequest(http.MethodPost,
		"https://trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build req: %v", err)
	}

	_, status, _, err := hostHTTPDoStream(req, "cb-1")
	if err == nil {
		t.Fatal("want error for 429 without stream_id")
	}
	if status != 429 {
		t.Fatalf("status = %d, want 429", status)
	}
	if directCalls != 0 {
		t.Fatalf("direct attempts = %d, want 0 (4xx must not retry)", directCalls)
	}
}

// TestStreamOpenRetryExhausts attempts 次数用尽时返回最后一次错误，
// 且总尝试次数恰好等于预算（1 次桥 + N-1 次直连），不会无限重试。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-29；改动原因：新增，锁住重试上界。
func TestStreamOpenRetryExhausts(t *testing.T) {
	origAvailable := hostBridgeAvailableFn
	origOpen := hostStreamOpenFn
	origDirect := hostStreamDirectFn
	origAttempts := streamOpenMaxAttempts
	t.Cleanup(func() {
		hostBridgeAvailableFn = origAvailable
		hostStreamOpenFn = origOpen
		hostStreamDirectFn = origDirect
		streamOpenMaxAttempts = origAttempts
	})

	hostBridgeAvailableFn = func() bool { return true }
	streamOpenMaxAttempts = 3

	bridgeCalls := 0
	hostStreamOpenFn = func(wireJSON []byte) ([]byte, error) {
		bridgeCalls++
		return nil, errHostBridgeOpenTimeout
	}
	directCalls := 0
	hostStreamDirectFn = func(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
		directCalls++
		return nil, 0, nil, errHostBridgeOpenTimeout
	}

	req, err := http.NewRequest(http.MethodPost,
		"https://trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build req: %v", err)
	}

	stream, _, _, err := hostHTTPDoStream(req, "cb-1")
	if err == nil {
		t.Fatal("want error after exhausting attempts")
	}
	if stream != nil {
		t.Fatal("want nil stream after exhausting attempts")
	}
	if bridgeCalls != 1 {
		t.Fatalf("bridge attempts = %d, want 1", bridgeCalls)
	}
	if directCalls != 2 {
		t.Fatalf("direct attempts = %d, want 2 (total 3 with the bridge attempt)", directCalls)
	}
}
