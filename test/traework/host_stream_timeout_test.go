package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHostHTTPDoStreamDegradesToDirectAfterBridgeTimeout 验证宿主流桥打开挂起时，
// hostHTTPDoStream 降级到插件直连并流式读完完整 SSE。
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-02 00:55:00；改动原因：修复异步流式 open 阶段挂死导致客户端 499 的问题。
func TestHostHTTPDoStreamDegradesToDirectAfterBridgeTimeout(t *testing.T) {
	prevAvailable := hostBridgeAvailableFn
	prevOpen := hostStreamOpenFn
	t.Cleanup(func() {
		hostBridgeAvailableFn = prevAvailable
		hostStreamOpenFn = prevOpen
	})
	// 宿主流桥"可用"但打开立即返回超时错误（等价于 30s 挂起后超时，测试不真实等待）。
	hostBridgeAvailableFn = func() bool { return true }
	hostStreamOpenFn = func([]byte) ([]byte, error) {
		return nil, errHostBridgeOpenTimeout
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
