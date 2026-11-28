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

// TestHostHTTPDoStreamWatchdogResendsOnNewConnection 验证看门狗到点后**换一条新连接
// 重发**，而不是干等或直接失败。
//
// 背景（2026-11-29 直测上游，绕过插件与 CPA）：`event:timing_cost` 显示上游自身
// 处理只要 server_processing_time=901~1575ms（810KB / 152k tokens 亦然），
// 而客户端实测响应头延迟会随机飙到 20~110s —— 说明长尾发生在**响应头到达之前**
// 的链路上，且是**连接级**的。同 body 8 次对照：单次干等 p90=max=80.7s；
// 6~8s 看门狗 + 最多 3 次重发 p90=max=11.7s，恢复后单次仅 ~2s。
//
// 因此看门狗到点的正确响应是「判定这条连接废了，换连接重试」。
// 旧测试断言的「绝不重发」会把拥塞全量转嫁给客户端（干等 20~110s）。
//
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-11-29；改动原因：语义由「超时即失败」改为「超时即换连接重试」。
func TestHostHTTPDoStreamWatchdogResendsOnNewConnection(t *testing.T) {
	prevAvailable := hostBridgeAvailableFn
	prevOpen := hostStreamOpenFn
	prevDirect := hostStreamDirectFn
	prevAttempts := streamOpenMaxAttempts
	t.Cleanup(func() {
		hostBridgeAvailableFn = prevAvailable
		hostStreamOpenFn = prevOpen
		hostStreamDirectFn = prevDirect
		streamOpenMaxAttempts = prevAttempts
	})
	// 宿主流桥"可用"但打开立即返回超时错误（等价于等到预算上限，测试不真实等待）。
	hostBridgeAvailableFn = func() bool { return true }
	streamOpenMaxAttempts = 3
	hostStreamOpenFn = func([]byte) ([]byte, error) {
		return nil, errHostBridgeOpenTimeout
	}
	// 直连 seam 必须被调用一次：那就是「换连接重发」，且本次应当成功。
	directCalls := 0
	hostStreamDirectFn = func(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
		directCalls++
		return &hostHTTPStream{liveBody: io.NopCloser(strings.NewReader(""))}, 200, http.Header{}, nil
	}

	req, err := http.NewRequest(http.MethodPost, "https://trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	stream, status, _, err := hostHTTPDoStream(req, "cb-watchdog")
	if err != nil {
		t.Fatalf("hostHTTPDoStream err = %v, want retry to recover", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if directCalls != 1 {
		t.Fatalf("direct attempts = %d, want 1 (watchdog must retry on a new connection)", directCalls)
	}
	if stream == nil {
		t.Fatal("stream = nil, want the recovered stream")
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
