package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHostHTTPDoStreamWatchdogDoesNotResend 验证看门狗到点时**不重发**上游请求，
// 而是把错误如实返回给调用方（由换号逻辑决定下一步）。
//
// 背景（2026-11-28 生产取证）：旧语义是「超时即降级直连并重发一次完整请求」，
// 导致客户端总耗时 = 看门狗预算 + 第二次上游耗时（生产 8/8 降级事件均为此形态，
// 表现为 33~36s / 123~126s 的人造台阶），且同一逻辑请求在上游变成两份。
// 新语义：看门狗只兜「永久挂起」，到点即如实报错，绝不重发。
//
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-11-28 17:00:00；改动原因：看门狗语义改为不重发。
func TestHostHTTPDoStreamWatchdogDoesNotResend(t *testing.T) {
	prevAvailable := hostBridgeAvailableFn
	prevOpen := hostStreamOpenFn
	prevDirect := hostStreamDirectFn
	t.Cleanup(func() {
		hostBridgeAvailableFn = prevAvailable
		hostStreamOpenFn = prevOpen
		hostStreamDirectFn = prevDirect
	})
	// 宿主流桥"可用"但打开立即返回超时错误（等价于等到预算上限，测试不真实等待）。
	hostBridgeAvailableFn = func() bool { return true }
	hostStreamOpenFn = func([]byte) ([]byte, error) {
		return nil, errHostBridgeOpenTimeout
	}
	// 直连 seam 绝不能被调用：调用即代表发生了重发。
	directCalled := false
	hostStreamDirectFn = func(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
		directCalled = true
		return &hostHTTPStream{liveBody: io.NopCloser(strings.NewReader(""))}, 200, http.Header{}, nil
	}

	req, err := http.NewRequest(http.MethodPost, "https://trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	stream, _, _, err := hostHTTPDoStream(req, "cb-watchdog")
	if err == nil {
		t.Fatalf("hostHTTPDoStream err = nil, want watchdog error (no resend)")
	}
	if !errors.Is(err, errHostBridgeOpenTimeout) {
		t.Fatalf("err = %v, want it to wrap errHostBridgeOpenTimeout", err)
	}
	if directCalled {
		t.Fatalf("direct seam called on watchdog timeout; must not resend the upstream request")
	}
	if stream != nil {
		t.Fatalf("stream = %#v, want nil on watchdog timeout", stream)
	}
}

// TestHostHTTPDoStreamDegradesToDirectOnBridgeFailure 验证非超时的桥失败
// （宿主 RPC 本身出错 / 信封损坏）仍然降级直连：这类失败说明桥这条路走不通，
// 而不是上游慢，直连只会发一次请求，不构成重发放大。
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-11-28 17:00:00；改动原因：把「降级」限定到真正的桥故障。
func TestHostHTTPDoStreamDegradesToDirectOnBridgeFailure(t *testing.T) {
	prevAvailable := hostBridgeAvailableFn
	prevOpen := hostStreamOpenFn
	t.Cleanup(func() {
		hostBridgeAvailableFn = prevAvailable
		hostStreamOpenFn = prevOpen
	})
	hostBridgeAvailableFn = func() bool { return true }
	// 非超时错误：宿主 RPC 失败。
	hostStreamOpenFn = func([]byte) ([]byte, error) {
		return nil, errors.New("host call host.http.do_stream returned 1")
	}

	sse := "event: output\ndata: {\"response\":\"SENTINEL_DEGRADED_COMPLETE\"}\n\nevent: done\ndata: {}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	stream, statusCode, _, err := hostHTTPDoStream(req, "cb-degrade")
	if err != nil {
		t.Fatalf("hostHTTPDoStream err = %v, want direct fallback success", err)
	}
	if statusCode != 200 {
		t.Fatalf("statusCode = %d, want 200", statusCode)
	}
	// 直连模式是 live 流：应能读完整 SSE。
	all, err := io.ReadAll(newHostStreamReader(stream))
	if err != nil {
		t.Fatalf("read degraded stream: %v", err)
	}
	if !bytes.Contains(all, []byte("SENTINEL_DEGRADED_COMPLETE")) || !bytes.Contains(all, []byte("event: done")) {
		t.Fatalf("degraded stream incomplete: %q", all)
	}
	stream.Close()
}

// TestHostHTTPDoStreamDirectStreamsIncrementally 验证直连降级流是边读边发，
// 上游先发前缀后发剩余时，第一次 Read 就能拿到前缀（不等待完整 body）。
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-02 00:55:00；改动原因：确保长推理降级直连时首包不延迟到生成结束。
func TestHostHTTPDoStreamDirectStreamsIncrementally(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: output\ndata: {\"response\":\"PREFIX\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		<-release // 等待测试确认已读到前缀后再发剩余
		_, _ = io.WriteString(w, "event: done\ndata: {}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	stream, _, _, err := hostHTTPDoStreamDirect(req, nil)
	if err != nil {
		t.Fatalf("hostHTTPDoStreamDirect err = %v", err)
	}
	defer stream.Close()

	// 第一次 Read 应在 release 前返回 PREFIX。
	done := make(chan struct{})
	var first []byte
	go func() {
		first, _, _ = stream.Read()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("first Read did not return before the rest of the body was sent")
	}
	if !bytes.Contains(first, []byte("PREFIX")) {
		t.Fatalf("first chunk = %q, want PREFIX", first)
	}
	close(release)
	// 读完剩余。
	all, err := io.ReadAll(newHostStreamReader(stream))
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	if !bytes.Contains(all, []byte("event: done")) {
		t.Fatalf("missing done in rest: %q", all)
	}
}
