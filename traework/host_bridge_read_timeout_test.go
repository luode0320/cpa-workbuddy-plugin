package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// 测试 hostHTTPStream.Read 在宿主桥 read 挂起（hostBridgeReadTimeout）时降级到
// 直连流，同一 attempt 继续读完全部数据，而不是让协调器永久悬挂。
// 这是 0.1.29 的核心回归：生产 qwen3.8-max 长推理在桥 read 阶段卡死
// （stream_id=1945 两分钟无日志、客户端 499），而插件直连客户端正常流式。

// TestHostStreamReadDegradesToDirectOnTimeout 用 seam 注入一个永久挂起的
// hostStreamReadFn，触发 errHostBridgeReadTimeout 后验证：流降级到内存直连，
// 后续 Read 全部来自直连体，且桥侧 late read 不影响结果。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-02 10:00:00；改动原因：为桥 read 超时降级加确定性回归。
func TestHostStreamReadDegradesToDirectOnTimeout(t *testing.T) {
	// 1. 保存并注入 seam；测试结束恢复，避免污染其它用例。
	origRead := hostStreamReadFn
	origDirect := hostStreamDirectFn
	origTimeout := hostBridgeReadTimeout
	t.Cleanup(func() {
		hostStreamReadFn = origRead
		hostStreamDirectFn = origDirect
		hostBridgeReadTimeout = origTimeout
	})
	hostBridgeReadTimeout = 100 * time.Millisecond

	// 2. 桥 read 永久挂起：阻塞在 channel 上，模拟宿主同步 cgo 卡死。
	hangStarted := make(chan struct{})
	var once sync.Once
	hostStreamReadFn = func(streamID string) ([]byte, error) {
		once.Do(func() { close(hangStarted) })
		<-make(chan struct{})
		return nil, io.EOF
	}

	// 3. 直连 seam 返回内存 SSE 流，含一个 output 分片和 done。
	directBody := []byte("event: output\ndata: {\"response\":\"直连长推理内容\"}\n\nevent: done\ndata: {}\n\n")
	hostStreamDirectFn = func(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
		return &hostHTTPStream{
			liveBody: io.NopCloser(bytes.NewReader(directBody)),
		}, 200, http.Header{}, nil
	}

	// 4. 构造 bridged 流；req/bodyBytes 保存在 stream 上供降级重开。
	req, err := http.NewRequest(http.MethodPost, "https://trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build req: %v", err)
	}
	stream := &hostHTTPStream{
		streamID:  "stream-1",
		req:       req,
		bodyBytes: []byte(`{"stream":true}`),
	}

	// 5. 并发等 seam 被调用（挂起确认），主流程直接读取触发超时降级。
	seamCalled := make(chan struct{})
	go func() {
		<-hangStarted
		close(seamCalled)
	}()
	reader := newHostStreamReader(stream)
	all, readErr := io.ReadAll(reader)
	<-seamCalled
	if readErr != nil {
		t.Fatalf("read after degrade: %v", readErr)
	}
	got := string(all)
	if !strings.Contains(got, "直连长推理内容") {
		t.Fatalf("degraded stream missing direct content: %q", got)
	}
	if !strings.Contains(got, "done") {
		t.Fatalf("degraded stream missing done frame: %q", got)
	}
	// 6. 降级后 liveBody 分支生效，streamID 已清空。
	if stream.streamID != "" {
		t.Fatalf("streamID = %q; want cleared after degrade", stream.streamID)
	}
	if stream.liveBody == nil {
		t.Fatalf("liveBody nil after degrade")
	}
	stream.Close()
}

// TestHostStreamReadTimeoutWaitsForHostRead 验证：桥 read 正常返回时（未超时），
// Read 直接透传宿主分片，不触发降级路径。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-02 10:00:00；改动原因：锁定非超时路径不被降级误伤。
func TestHostStreamReadTimeoutWaitsForHostRead(t *testing.T) {
	origRead := hostStreamReadFn
	origDirect := hostStreamDirectFn
	origTimeout := hostBridgeReadTimeout
	t.Cleanup(func() {
		hostStreamReadFn = origRead
		hostStreamDirectFn = origDirect
		hostBridgeReadTimeout = origTimeout
	})
	hostBridgeReadTimeout = 100 * time.Millisecond

	// 1. 桥 read 立即返回一个正常宿主分片（base64 "hello"）。
	hostStreamReadFn = func(streamID string) ([]byte, error) {
		raw := `{"payload":"aGVsbG8=","done":false}`
		env := envelope{OK: true, Result: json.RawMessage(raw)}
		envRaw, _ := json.Marshal(env)
		return envRaw, nil
	}
	directCalled := false
	hostStreamDirectFn = func(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
		directCalled = true
		return nil, 0, nil, nil
	}

	stream := &hostHTTPStream{streamID: "stream-2"}
	payload, done, err := stream.Read()
	if err != nil {
		t.Fatalf("bridged read: %v", err)
	}
	if done {
		t.Fatalf("done=true on first chunk")
	}
	if string(payload) != "hello" {
		t.Fatalf("payload = %q; want hello", string(payload))
	}
	// 2. 未触发降级：streamID 保留，liveBody 未设置，直连 seam 未调用。
	if stream.streamID != "stream-2" {
		t.Fatalf("streamID = %q; want stream-2 (no degrade)", stream.streamID)
	}
	if stream.liveBody != nil {
		t.Fatalf("liveBody set without degrade")
	}
	if directCalled {
		t.Fatalf("direct seam called on healthy read")
	}
}

// TestHostStreamReadTimeoutHasNoDirectFallbackRequest 验证：bridged 流缺少
// req（理论上只出现在测试直接构造的流）时，超时返回明确错误而不是 panic。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-02 10:00:00；改动原因：覆盖降级前置条件缺失的边界。
func TestHostStreamReadTimeoutHasNoDirectFallbackRequest(t *testing.T) {
	origRead := hostStreamReadFn
	origDirect := hostStreamDirectFn
	origTimeout := hostBridgeReadTimeout
	t.Cleanup(func() {
		hostStreamReadFn = origRead
		hostStreamDirectFn = origDirect
		hostBridgeReadTimeout = origTimeout
	})
	hostBridgeReadTimeout = 100 * time.Millisecond

	// 永久挂起 + 直连 seam 不应被调用。
	hostStreamReadFn = func(streamID string) ([]byte, error) {
		<-make(chan struct{})
		return nil, io.EOF
	}
	directCalled := false
	hostStreamDirectFn = func(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
		directCalled = true
		return nil, 0, nil, nil
	}

	stream := &hostHTTPStream{streamID: "stream-3"}
	_, _, err := stream.Read()
	if err == nil {
		t.Fatalf("expected error for missing fallback request")
	}
	if !strings.Contains(err.Error(), "no direct fallback request") {
		t.Fatalf("error = %v; want no-direct-fallback-request", err)
	}
	if directCalled {
		t.Fatalf("direct seam called without req")
	}
}
