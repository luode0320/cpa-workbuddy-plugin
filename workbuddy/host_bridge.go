// host_bridge.go routes every upstream HTTP call through the CPA host's
// http bridge (host.http.do / host.http.do_stream / stream_read / stream_close).
// Production traffic always uses the bridge so request-log captures outbound
// calls and host transport policy (proxy, timeout) applies. The *Direct
// variants are the test-only fallback used when the bridge is unavailable
// (unit tests, or hosts older than v7.2.x without the http bridge RPC).
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// sharedHTTPClient is the fallback HTTP client used ONLY when the host HTTP
// bridge is unavailable (unit tests, or hosts older than v7.2.x without
// host.http.* RPC). All production upstream calls should route via hostHTTPDo
// / hostHTTPDoStream so request-log captures them and host transport policy
// applies. Direct use of this client in new code is a compliance bug.
func sharedHTTPClient() *http.Client {
	httpClientOnce.Do(func() {
		// No cookie jar here: auth is carried by Bearer headers, and a shared
		// jar would leak upstream set-cookie state across accounts (multi-account
		// deployments could cross-contaminate sessions). Only the short-lived
		// login clients get a jar.
		//
		// 分阶段超时（2026-11-07 补）：只设 Timeout 是不够的 —— Timeout 是端到端
		// 总上限，一旦命中就把已经产出正文的合法长流一起掐断；而首字之前的静默
		// 期（半开 TCP、服务端排队）在总超时内部仍可无限挂起。生产实证：usage
		// feed 中 TTFT 最长 150.5s、单请求总耗时最长 694s，全部 status_code=0
		// （传输级），说明缺的正是「首字节 / TLS 握手」这两道前置闸门。
		// 三个前置超时的取值参考 linguo2625469/workbuddy2api-panel 的
		// internal/upstream/transport.go（其实测半开 TCP 单次 TTFB 卡 936s）。
		sharedClient = &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					// 半死连接的第一道闸：默认 Dialer 无上限。
					Timeout:   upstreamDialTimeout,
					KeepAlive: 15 * time.Second,
				}).DialContext,
				// TLS 握手前置上限（此前完全缺失）。
				TLSHandshakeTimeout: upstreamTLSHandshakeTimeout,
				// 首字节（响应头）硬上限：聊天 SSE 在吐出第一个字节前长时间静默
				// 时快速失败并交给换号，而不是让客户端干等。
				ResponseHeaderTimeout: upstreamResponseHeaderTimeout,
				// 禁 h2：ForceAttemptHTTP2=false 不够，ALPN 仍会协商出 h2，
				// 半死流上表现为 "http2: timeout awaiting response headers"；
				// 置空 TLSNextProto 才能真正禁掉。
				TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
				MaxIdleConns:        20,
				IdleConnTimeout:     90 * time.Second,
				MaxIdleConnsPerHost: 5,
			},
		}
	})
	return sharedClient
}

// 上游 HTTP 分阶段超时。三者的语义分工见 sharedHTTPClient 的说明：
// 拨号与 TLS 握手挡住「连不上」，ResponseHeaderTimeout 挡住「连上了但首字节
// 迟迟不来」，而 http.Client.Timeout 仍是整体兜底上限。
const (
	// upstreamDialTimeout 限制 TCP 建连耗时。
	upstreamDialTimeout = 10 * time.Second
	// upstreamTLSHandshakeTimeout 限制 TLS 握手耗时。
	upstreamTLSHandshakeTimeout = 10 * time.Second
	// upstreamResponseHeaderTimeout 限制「请求已发出 → 响应头到达」的耗时。
	// 取值 60s：生产观测到正常请求 TTFB 最坏约 16s（大 input prefill），
	// 60s 既不会误杀健康慢请求，又能把 150s 级别的静默快速收敛。
	upstreamResponseHeaderTimeout = 60 * time.Second
)

// hostHTTPResponse is the plugin-side view of an HTTP response that came back
// through the host bridge. Body is fully buffered (matches the historical
// io.ReadAll(resp.Body) usage pattern in billing / models / usage callers).
type hostHTTPResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// rpcHostHTTPRequestWire mirrors internal/pluginhost/host_callbacks.go's
// rpcHostHTTPRequest on the wire. The "request" sub-object is the actual HTTP
// call; the flat method/url/headers/body fields are an alternate form we don't
// use (host prefers Request when present).
// rpcHostHTTPRequestWire 对齐宿主偏好的嵌套 HTTP 请求协议。
// HostCallbackID 位于外层，用于恢复异步请求上下文；Request 承载实际 HTTP 请求。
// [最近修改时间] 2026-11-07 21:50:00；改动原因：补 HostCallbackID 外层字段，
// 此前结构体只有 Request，等于把宿主下发的 callback context 丢弃。
type rpcHostHTTPRequestWire struct {
	HostCallbackID string            `json:"host_callback_id,omitempty"` // 关联 CPA 异步执行的 callback context。
	Request        *rpcHostHTTPInner `json:"request,omitempty"`          // 承载方法、地址、请求头和请求体。
}

type rpcHostHTTPInner struct {
	Method  string              `json:"method,omitempty"`
	URL     string              `json:"url,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
}

type rpcHostHTTPStreamResponseWire struct {
	StatusCode int                         `json:"status_code"`
	Headers    map[string][]string         `json:"headers,omitempty"`
	StreamID   string                      `json:"stream_id,omitempty"`
	Chunks     []pluginapi.HTTPStreamChunk `json:"chunks,omitempty"`
}

type rpcHostHTTPStreamReadResponseWire struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

// hostBridgeUnwrap unwraps the pluginabi.Envelope returned by host RPC and
// returns the inner Result payload. Returns an error when the envelope itself
// signals failure (ok=false) or is malformed.
func hostBridgeUnwrap(raw []byte, method string) (json.RawMessage, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%s: decode envelope: %w", method, err)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: host error %s: %s", method, env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("%s: host returned not-ok", method)
	}
	return env.Result, nil
}

// hostBridgeAvailable reports whether host.http.* RPC is wired up. False in
// unit tests (no hostAPI) and when the host binary predates the bridge.
func hostBridgeAvailable() bool {
	return hostAPI != nil && hostAPI.call != nil
}

// hostHTTPDo performs a non-streaming upstream call via the host's http bridge.
// Request body is read eagerly (callers already have []byte or a small buffer).
// The response body is likewise read eagerly — all existing call sites consume
// it via io.ReadAll then Close, so we keep that shape and discard the closer.
//
// Fallback: when the host bridge is unavailable (unit tests, host older than
// v7.2.x without the http bridge), we route through sharedHTTPClient directly.
// This keeps the plugin functional in dev/test contexts while preferring the
// compliant path in production.
func hostHTTPDo(req *http.Request) (*hostHTTPResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}
	var bodyBytes []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
		_ = req.Body.Close()
		bodyBytes = b
	}
	// Windows stack movement mitigation: nested host calls during synchronous
	// RPCs (model.for_auth, management.handle) cause the host stack to move,
	// rendering the stack response pointer dangling and causing "unexpected
	// end of JSON input". Bypass host bridge on Windows and make direct HTTP
	// calls.
	if !hostBridgeAvailable() || runtime.GOOS == "windows" {
		return hostHTTPDoDirect(req, bodyBytes)
	}
	wire := rpcHostHTTPRequestWire{
		Request: &rpcHostHTTPInner{
			Method:  req.Method,
			URL:     req.URL.String(),
			Headers: map[string][]string(req.Header),
			Body:    bodyBytes,
		},
	}
	raw, err := hostCall(pluginabi.MethodHostHTTPDo, mustJSON(wire))
	if err != nil {
		// Bridge exists but the call failed — fall back to direct so a transient
		// host RPC error doesn't take down the executor.
		log.Printf("[workbuddy] host.http.do transport error (fallback direct): %v", err)
		return hostHTTPDoDirect(req, bodyBytes)
	}
	result, err := hostBridgeUnwrap(raw, pluginabi.MethodHostHTTPDo)
	if err != nil {
		log.Printf("[workbuddy] host.http.do bad envelope (fallback direct): %v", err)
		return hostHTTPDoDirect(req, bodyBytes)
	}
	return parseHostHTTPDoResult(result)
}

// parseHostHTTPDoResult decodes the inner Result payload of a host.http.do
// RPC into a hostHTTPResponse. Extracted as a pure function so the wire
// contract is regression-testable without the CGO host channel.
//
// Wire-format note: the host serializes pluginapi.HTTPResponse WITHOUT
// json tags (v7.2.x), producing PascalCase keys: {"StatusCode":200,...}.
// A tagged `json:"status_code"` here can never match that key (exact tag
// name and case-insensitive name both differ due to the underscore), which
// silently zeroed every non-stream bridge response's status code while
// Headers/Body still matched case-insensitively. Parse the real shape, and
// defensively accept a snake_case variant should the host add tags later.
func parseHostHTTPDoResult(result json.RawMessage) (*hostHTTPResponse, error) {
	var resp struct {
		StatusCode      int                 // PascalCase: host's untagged default
		StatusCodeSnake int                 `json:"status_code"` // defensive: tagged host variant
		Headers         map[string][]string `json:"headers,omitempty"`
		Body            []byte              `json:"body,omitempty"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("decode host.http.do response: %w", err)
	}
	sc := resp.StatusCode
	if sc == 0 {
		sc = resp.StatusCodeSnake
	}
	return &hostHTTPResponse{
		StatusCode: sc,
		Headers:    http.Header(resp.Headers),
		Body:       resp.Body,
	}, nil
}

// hostHTTPDoDirect executes the request via the plugin's own http.Client.
// Used as a fallback when the host bridge is unavailable (unit tests).
func hostHTTPDoDirect(req *http.Request, bodyBytes []byte) (*hostHTTPResponse, error) {
	// Rebuild the request since req.Body was already consumed.
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header.Clone()
	resp, err := sharedHTTPClient().Do(newReq)
	if err != nil {
		log.Printf("[workbuddy] direct http %s %s failed: %v", req.Method, req.URL.Host, err)
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[workbuddy] direct http %s %s read body failed: %v", req.Method, req.URL.Host, err)
		return nil, err
	}
	return &hostHTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header.Clone(),
		Body:       raw,
	}, nil
}

// hostHTTPStream is a handle for an in-flight host-bridged stream. Read returns
// the next chunk; Close aborts the upstream.
//
// Two modes:
//   - Bridged: streamID set, Read/Close forward to host RPC.
//   - Direct (test fallback): direct holds the full buffered body, Read drains
//     it once then reports done. Close is a no-op.
type hostHTTPStream struct {
	streamID string
	liveBody io.ReadCloser
	direct   []byte
	directAt int
}

// buildHostHTTPStreamWire 构造宿主流桥 open 的线上信封（纯函数，便于单测）。
//
// 抽成独立函数的原因：hostHTTPDoStream 在单测环境里 hostBridgeAvailable()
// 恒为 false（没有 cgo host 通道），会直接走直连兜底，导致「调用点是否真的
// 把 hostCallbackID 写进信封」这条最关键的回归无法被测试捕获——变异验证已
// 证实该缺口：删掉调用点的赋值后，只测结构体的用例依然全绿。
// [参数] req：上游 HTTP 请求；bodyBytes：已读取的请求体；hostCallbackID：
//
//	CPA 异步执行的 callback 标识，空串表示不绑定。
//
// [返回] 序列化后的 RPC 信封字节。
// 最近修改时间：2026-11-07 21:55:00；改动原因：新增，承载 callback 透传回归。
func buildHostHTTPStreamWire(req *http.Request, bodyBytes []byte, hostCallbackID string) []byte {
	wire := rpcHostHTTPRequestWire{
		HostCallbackID: hostCallbackID, // 绑定 CPA 异步执行保留到输出流结束的 callback context。
		Request: &rpcHostHTTPInner{
			Method:  req.Method,
			URL:     req.URL.String(),
			Headers: map[string][]string(req.Header),
			Body:    bodyBytes,
		},
	}
	return mustJSON(wire)
}

// hostStreamOpenFn 是宿主流桥 open 的接缝，单测可注入假实现以断言线上信封形状
// （尤其是 host_callback_id 是否真的写进去了）。生产默认走真实 host RPC。
var hostStreamOpenFn = func(request []byte) ([]byte, error) {
	return hostCall(pluginabi.MethodHostHTTPDoStream, request)
}

// hostHTTPDoStream opens a streaming call via the host bridge. The host owns
// the actual http.Response body; we pull chunks via hostHTTPStreamRead.
//
// Falls back to direct http.Client.Do when the bridge is unavailable (tests).
// In that case the returned hostHTTPStream wraps an in-memory copy of the
// full response body so Read/Close have the same shape.
//
// [参数] req：上游 HTTP 请求；hostCallbackID：CPA 为当前异步执行注册的 callback 标识，
//
//	用于把客户端取消沿宿主的 callback context 传播到上游；空串表示不绑定
//	（同步路径与测试场景）。
//
// [返回] 流句柄、状态码、响应头、错误。
// 最近修改时间：2026-11-07 21:50:00；改动原因：补 hostCallbackID 透传，
//
//	此前虽然解析了 host_callback_id 却从不传给宿主，导致客户端断连后
//	插件仍在 context.Background() 上续读上游，形成死流并占用 stream 槽位。
func hostHTTPDoStream(req *http.Request, hostCallbackID string) (*hostHTTPStream, int, http.Header, error) {
	if req == nil {
		return nil, 0, nil, fmt.Errorf("nil request")
	}
	var bodyBytes []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("read request body: %w", err)
		}
		_ = req.Body.Close()
		bodyBytes = b
	}
	if !hostBridgeAvailable() {
		return hostHTTPDoStreamDirect(req, bodyBytes)
	}
	raw, err := hostStreamOpenFn(buildHostHTTPStreamWire(req, bodyBytes, hostCallbackID))
	if err != nil {
		return hostHTTPDoStreamDirect(req, bodyBytes)
	}
	result, err := hostBridgeUnwrap(raw, pluginabi.MethodHostHTTPDoStream)
	if err != nil {
		return hostHTTPDoStreamDirect(req, bodyBytes)
	}
	var resp rpcHostHTTPStreamResponseWire
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, 0, nil, fmt.Errorf("decode host.http.do_stream response: %w", err)
	}
	if resp.StreamID == "" {
		return nil, resp.StatusCode, http.Header(resp.Headers), fmt.Errorf("host stream bridge unavailable")
	}
	return &hostHTTPStream{streamID: resp.StreamID}, resp.StatusCode, http.Header(resp.Headers), nil
}

// hostHTTPDoStreamDirect performs the request with the plugin's own http.Client
// and wraps resp.Body as a live stream so callers read chunks as they arrive
// without blocking on io.ReadAll.
// [参数] req: 上游 HTTP 请求；bodyBytes: 已读取的请求体字节。
// [返回] hostHTTPStream: 实时流句柄；int: 状态码；http.Header: 响应头；error: 传输错误。
// 最近修改时间：2026-10-11 22:35:00 改为 liveBody 实时流，消除 io.ReadAll 全量预缓冲阻塞
func hostHTTPDoStreamDirect(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, 0, nil, err
	}
	newReq.Header = req.Header.Clone()
	resp, err := sharedHTTPClient().Do(newReq)
	if err != nil {
		return nil, 0, nil, err
	}
	return &hostHTTPStream{liveBody: resp.Body}, resp.StatusCode, resp.Header.Clone(), nil
}

// Read pulls the next chunk. Returns (payload, done, err). done=true means the
// stream ended cleanly; err non-nil means upstream or bridge error.
func (s *hostHTTPStream) Read() ([]byte, bool, error) {
	if s == nil {
		return nil, true, fmt.Errorf("stream closed")
	}
	// Live direct mode: drain the open upstream response body as bytes arrive.
	if s.liveBody != nil {
		buf := make([]byte, 32*1024)
		n, err := s.liveBody.Read(buf)
		if n > 0 {
			return buf[:n], false, nil
		}
		if err == io.EOF {
			return nil, true, nil
		}
		if err != nil {
			return nil, true, err
		}
		return nil, false, nil
	}
	// Direct (test fallback) mode: serve the buffered body in one shot.
	if s.direct != nil {
		if s.directAt >= len(s.direct) {
			return nil, true, nil
		}
		out := s.direct[s.directAt:]
		s.directAt = len(s.direct)
		return out, false, nil
	}
	if s.streamID == "" {
		return nil, true, fmt.Errorf("stream closed")
	}
	raw, err := hostCall(pluginabi.MethodHostHTTPStreamRead, mustJSON(map[string]any{"stream_id": s.streamID}))
	if err != nil {
		return nil, true, err
	}
	result, err := hostBridgeUnwrap(raw, pluginabi.MethodHostHTTPStreamRead)
	if err != nil {
		return nil, true, err
	}
	var resp rpcHostHTTPStreamReadResponseWire
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, true, fmt.Errorf("decode host.http.stream_read response: %w", err)
	}
	if resp.Error != "" {
		return nil, true, fmt.Errorf("%s", resp.Error)
	}
	return resp.Payload, resp.Done, nil
}

// Close aborts the upstream stream. Always safe to call (idempotent on host).
func (s *hostHTTPStream) Close() {
	if s == nil {
		return
	}
	if s.liveBody != nil {
		_ = s.liveBody.Close()
		s.liveBody = nil
		return
	}
	if s.direct != nil {
		s.direct = nil
		s.directAt = 0
		return
	}
	if s.streamID == "" {
		return
	}
	_, _ = hostCall(pluginabi.MethodHostHTTPStreamClose, mustJSON(map[string]any{"stream_id": s.streamID}))
	s.streamID = ""
}

// hostStreamReader adapts a hostHTTPStream to io.Reader so existing
// bufio.Scanner / io.ReadAll call sites work unchanged. The host bridge emits
// arbitrary 32KB chunks (not SSE lines), so line framing must be re-assembled
// by the consumer — Scanner handles that for us.
type hostStreamReader struct {
	s    *hostHTTPStream
	buf  []byte // leftover from previous chunk
	done bool
	err  error
}

func newHostStreamReader(s *hostHTTPStream) *hostStreamReader {
	return &hostStreamReader{s: s}
}

func (r *hostStreamReader) Read(p []byte) (int, error) {
	// Drain buffered bytes first.
	if len(r.buf) > 0 {
		n := copy(p, r.buf)
		r.buf = r.buf[n:]
		return n, nil
	}
	if r.done {
		if r.err != nil {
			return 0, r.err
		}
		return 0, io.EOF
	}
	chunk, done, err := r.s.Read()
	if err != nil {
		r.done = true
		r.err = err
		return 0, err
	}
	if len(chunk) > 0 {
		n := copy(p, chunk)
		if n < len(chunk) {
			r.buf = append(r.buf, chunk[n:]...)
		}
		if done {
			r.done = true
		}
		return n, nil
	}
	if done {
		r.done = true
		return 0, io.EOF
	}
	// Empty chunk, not done — recurse to fetch next.
	return r.Read(p)
}

// mustJSON marshals v and panics on error — the wire structs above are always
// marshalable, so any failure here is a programming bug.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
