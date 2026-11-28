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
		// 分阶段超时（对齐 workbuddy 2026-11-07）：只设 Timeout 是不够的 ——
		// Timeout 是端到端总上限，首字之前的静默期（半开 TCP、服务端排队）
		// 在总超时内部仍可无限挂起。本客户端承担 MaaS 票据签发（请求关键路径，
		// ctx 只有 15s），半开连接会把 TTFT 直接顶到签发超时上限。
		sharedClient = &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				// 半死连接的第一道闸：默认 Dialer 无上限。
				DialContext: (&net.Dialer{
					Timeout:   upstreamDialTimeout,
					KeepAlive: 15 * time.Second,
				}).DialContext,
				// TLS 握手前置上限（此前完全缺失）。
				TLSHandshakeTimeout: upstreamTLSHandshakeTimeout,
				// 首字节（响应头）硬上限：SSE 在吐出第一个字节前长时间静默时
				// 快速失败并交给换号，而不是让客户端干等。
				ResponseHeaderTimeout: upstreamResponseHeaderTimeout,
				// 禁 h2：ForceAttemptHTTP2=false 不够，ALPN 仍会协商出 h2；
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

// 上游 HTTP 分阶段超时。语义分工见 sharedHTTPClient 的说明：拨号与 TLS 握手
// 挡住「连不上」，ResponseHeaderTimeout 挡住「连上了但首字节迟迟不来」，
// 而 http.Client.Timeout 仍是整体兜底上限。
const (
	// upstreamDialTimeout 限制 TCP 建连耗时。
	upstreamDialTimeout = 10 * time.Second
	// upstreamTLSHandshakeTimeout 限制 TLS 握手耗时。
	upstreamTLSHandshakeTimeout = 10 * time.Second
	// upstreamResponseHeaderTimeout 限制「请求已发出 → 响应头到达」的耗时。
	// 取值 60s：生产观测到大 input 的正常 TTFB 最坏十几秒，60s 既不误杀健康
	// 慢请求，又能把分钟级静默快速收敛。
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
//
// HostCallbackID 位于外层，用于恢复宿主侧的异步请求上下文；Request 承载实际
// HTTP 请求。缺失该字段时宿主拿不到 callback context，上游请求的 ctx 退化为
// context.Background()，客户端断连后上游请求不会被取消——上游看到的并发远
// 大于真实在途请求，上游排队随之加剧（同仓 workbuddy 2026-11-07 已补齐）。
type rpcHostHTTPRequestWire struct {
	HostCallbackID string            `json:"host_callback_id,omitempty"`
	Request        *rpcHostHTTPInner `json:"request,omitempty"`
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
		return hostHTTPDoDirect(req, bodyBytes)
	}
	result, err := hostBridgeUnwrap(raw, pluginabi.MethodHostHTTPDo)
	if err != nil {
		return hostHTTPDoDirect(req, bodyBytes)
	}
	return parseHostHTTPDoResult(result)
}

// parseHostHTTPDoResult 把 host.http.do RPC 的 Result 载荷解码为 hostHTTPResponse，
// 同时兼容宿主未加 json tag 的 PascalCase 键与加 tag 后的下划线键。
//
// [参数] result：host.http.do 的 Result 原始 JSON 载荷。
// [返回] 解码后的宿主响应；载荷非法时返回错误。
// 最近修改时间：2026-10-10 新增, 修复宿主桥状态码恒为 0 导致签到假成功
func parseHostHTTPDoResult(result json.RawMessage) (*hostHTTPResponse, error) {
	var resp struct {
		StatusCode      int                 // 宿主未加 tag 时的 PascalCase 键
		StatusCodeSnake int                 `json:"status_code"` // 宿主加 tag 后的下划线键
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
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
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

// hostHTTPDoStream opens a streaming call via the host bridge. The host owns
// the actual http.Response body; we pull chunks via hostHTTPStreamRead.
//
// hostCallbackID 用于把宿主侧的异步请求上下文绑定到这条上游流：客户端断连时
// 宿主据此取消上游请求。没有它，上游请求会一直挂着，占用上游并发槽位。
//
// Falls back to direct http.Client.Do when the bridge is unavailable (tests).
// In that case the returned hostHTTPStream wraps an in-memory copy of the
// full response body so Read/Close have the same shape.
//
// [参数] req：上游 HTTP 请求；hostCallbackID：宿主异步执行回调标识，可空。
// [返回] 流句柄、状态码、响应头、传输错误。
// 最近修改时间：2026-11-28；改动原因：补 host_callback_id 透传。
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
	raw, err := hostCall(pluginabi.MethodHostHTTPDoStream, buildHostHTTPStreamWire(req, bodyBytes, hostCallbackID))
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

// buildHostHTTPStreamWire 构造宿主流桥 open 的线上信封（纯函数，便于单测）。
//
// 抽成独立函数的原因：hostHTTPDoStream 在单测环境里 hostBridgeAvailable()
// 恒为 false（没有 cgo host 通道），会直接走直连兜底，导致「调用点是否真的
// 把 host_callback_id 写进信封」这条最关键的回归无法被测试捕获。
//
// [参数] req：上游 HTTP 请求；bodyBytes：已读取的请求体；hostCallbackID：
//
//	宿主异步执行回调标识，空串表示不绑定。
//
// [返回] 序列化后的 RPC 信封字节。
// 最近修改时间：2026-11-28；改动原因：新增，承载 callback 透传回归。
func buildHostHTTPStreamWire(req *http.Request, bodyBytes []byte, hostCallbackID string) []byte {
	wire := rpcHostHTTPRequestWire{
		HostCallbackID: hostCallbackID,
		Request: &rpcHostHTTPInner{
			Method:  req.Method,
			URL:     req.URL.String(),
			Headers: map[string][]string(req.Header),
			Body:    bodyBytes,
		},
	}
	return mustJSON(wire)
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
