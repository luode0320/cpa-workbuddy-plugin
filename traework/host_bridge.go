// host_bridge.go routes every upstream HTTP call through the CPA host's
// http bridge (host.http.do / host.http.do_stream / stream_read / stream_close).
// Production traffic always uses the bridge so request-log captures outbound
// calls and host transport policy (proxy, timeout) applies. The *Direct
// variants are the test-only fallback used when the bridge is unavailable
// (unit tests, or hosts older than v7.2.x without the http bridge RPC).
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// sharedHTTPClient is the fallback HTTP client used ONLY when the host HTTP
// bridge is unavailable (unit tests, or hosts older than v7.2.x without
// host.http.* RPC). All production upstream calls should route via hostHTTPDo
// / hostHTTPDoStream so request-log captures them and host transport policy
// applies. Direct use of this client in new code is a compliance bug.
// 上游 HTTP 分阶段超时。
//
// ResponseHeaderTimeout 取 120s：生产实测 traework 的 TTFT p90 达 26.6s、
// p99 达 55.2s（大 input prefill），30s 会切断 p90 以上的合法慢请求；
// 同类开源反代（connectedgraph/trae2api-web）同样取 120s。
const (
	// upstreamDialTimeout 限制 TCP 建连耗时。
	upstreamDialTimeout = 10 * time.Second
	// upstreamTLSHandshakeTimeout 限制 TLS 握手耗时。
	upstreamTLSHandshakeTimeout = 10 * time.Second
	// upstreamResponseHeaderTimeout 限制「请求已发出 → 响应头到达」的耗时。
	upstreamResponseHeaderTimeout = 120 * time.Second
)

func sharedHTTPClient() *http.Client {
	httpClientOnce.Do(func() {
		// No cookie jar here: auth is carried by Bearer headers, and a shared
		// jar would leak upstream set-cookie state across accounts (multi-account
		// deployments could cross-contaminate sessions). Only the short-lived
		// login clients get a jar.
		// 分阶段超时：只设 Timeout 时，首字节之前的静默期（半开 TCP、上游排队）
		// 仍可在总超时内部无限挂起，而命中总超时会把已产出正文的长流一起掐断。
		sharedClient = &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   upstreamDialTimeout,
					KeepAlive: 15 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   upstreamTLSHandshakeTimeout,
				ResponseHeaderTimeout: upstreamResponseHeaderTimeout,
				// 置空 TLSNextProto 才能真正禁 h2；只设 ForceAttemptHTTP2=false 时
				// ALPN 仍会协商出 h2，半死流上表现为响应头等待超时。
				TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
				// 连接池容量按账号池规模设定：生产 traework 有 28 个可用账号，
				// 每个账号在同一上游 host 上独立承流。MaxIdleConnsPerHost=5 时，
				// 28 个并发请求会频繁触发新建连接（TCP + TLS 握手各一次），
				// 高并发窗口下这部分开销直接叠加到 TTFT 上。
				// 生产实测（2026-11-10）：同分钟并发 16-30 时慢请求占比 26.5%，
				// 而并发 1-5 时仅 9.9%，呈明显正相关。
				MaxIdleConns:        200,
				IdleConnTimeout:     90 * time.Second,
				MaxIdleConnsPerHost: 64,
			},
		}
	})
	return sharedClient
}

// hostHTTPResponse is the plugin-side view of an HTTP response that came back
// through the host bridge. Body is fully buffered (matches the historical
// io.ReadAll(resp.Body) usage pattern in billing / models / usage callers).
type hostHTTPResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// rpcHostHTTPRequestWire 对齐宿主偏好的嵌套 HTTP 请求协议。
// HostCallbackID 位于外层，用于恢复异步请求上下文；Request 承载实际 HTTP 请求。
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

// streamDirectForced 报告是否强制流式请求走插件直连（跳过宿主桥）。
//
// 用途：生产流式请求默认走宿主桥 `host.stream.do`，插件的连接池配置对它无效。
// 本开关让运维可以做「直连 vs 桥接」的 A/B 对照，验证延迟差异来自哪一层。
// 默认 false（走宿主桥，与既有行为一致）。
// [返回] true 表示强制直连。
// 最近修改时间：2026-11-10 21:30:00；改动原因：新增，支持路径对照实验。
func streamDirectForced() bool {
	streamDirectMu.RLock()
	defer streamDirectMu.RUnlock()
	return streamDirectForce
}

// hostBridgeAvailable reports whether host.http.* RPC is wired up. False in
// unit tests (no hostAPI) and when the host binary predates the bridge.
func hostBridgeAvailable() bool {
	return hostBridgeAvailableFn()
}

var (
	// streamDirectMu 保护 streamDirectForce。
	streamDirectMu sync.RWMutex
	// streamDirectForce 为 true 时流式请求强制走插件直连，跳过宿主桥。
	streamDirectForce bool
)

// setStreamDirectForce 设置直连开关（配置加载与测试使用）。
// [参数] on：true 表示强制直连。
// [返回] 无。
// 最近修改时间：2026-11-10 21:30:00；改动原因：新增。
func setStreamDirectForce(on bool) {
	streamDirectMu.Lock()
	defer streamDirectMu.Unlock()
	streamDirectForce = on
}

// hostBridgeAvailableFn is the availability probe seam, injectable in tests to
// exercise the host-bridge branch without a real hostAPI.
var hostBridgeAvailableFn = func() bool {
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
	return decodeBridgeHTTPResponse(result)
}

// decodeBridgeHTTPResponse decodes the host.http.do result envelope with a
// tolerant status extraction: host versions differ in the JSON key used for
// the upstream status (status_code / statusCode / status / code), and an
// unrecognized key silently decoded as StatusCode 0, which callers treated
// as a hard HTTP failure even when the body was intact — traework check-in
// and points broke exactly this way on Linux (bridge path) while the Windows
// direct path kept working, hiding the bug in dev.
func decodeBridgeHTTPResponse(result json.RawMessage) (*hostHTTPResponse, error) {
	var resp struct {
		StatusCode      int                 `json:"status_code"`
		StatusCodeCamel int                 `json:"statusCode"`
		Status          json.RawMessage     `json:"status"`
		Code            json.RawMessage     `json:"code"`
		Headers         map[string][]string `json:"headers,omitempty"`
		Body            []byte              `json:"body,omitempty"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("decode host.http.do response: %w", err)
	}
	status := resp.StatusCode
	if status == 0 {
		status = resp.StatusCodeCamel
	}
	if status == 0 {
		if n, ok := bridgeJSONInt(resp.Status); ok {
			status = int(n)
		}
	}
	if status == 0 {
		if n, ok := bridgeJSONInt(resp.Code); ok {
			status = int(n)
		}
	}
	return &hostHTTPResponse{
		StatusCode: status,
		Headers:    http.Header(resp.Headers),
		Body:       resp.Body,
	}, nil
}

// bridgeJSONInt extracts an integer from a raw JSON value (number or numeric
// string); ok=false when absent or non-numeric. Used by the tolerant status
// decode below.
func bridgeJSONInt(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		var v int64
		if _, err := fmt.Sscanf(s, "%d", &v); err == nil {
			return v, true
		}
	}
	return 0, false
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
// Three modes:
//   - Bridged: streamID set, Read/Close forward to host RPC.
//   - Live: direct holds an open http.Response.Body; Read drains it as the
//     upstream produces bytes (used when the host stream bridge hangs and the
//     async path degrades to the plugin's own HTTP client).
//   - Direct (test fallback): direct holds the full buffered body, Read drains
//     it once then reports done. Close is a no-op.
//
// The bridged mode keeps the original request (req/bodyBytes) so a read that
// stalls past hostBridgeReadTimeout can be aborted and re-opened as a direct
// live stream on the same attempt — mirroring the open-phase degradation in
// hostHTTPDoStreamBridged, but covering the read phase that the open-phase
// protection misses. Reads are strictly sequential (single consumer), so once
// degraded the liveBody branch at the top of Read serves the remainder of the
// attempt.
type hostHTTPStream struct {
	streamID string
	liveBody io.ReadCloser
	direct   []byte
	directAt int

	// Bridged read-phase degradation state (nil outside bridged mode).
	req       *http.Request
	bodyBytes []byte

	// releaseAccountLoad 在流关闭时归还账号在跑请求计数，nil 表示无需归还。
	// 计数必须在整条流读完才释放：上游的并行槽位占用时长等于响应流生命周期，
	// 提前释放会让调度层低估负载并继续往同一个号上堆并发。
	releaseAccountLoad func()

	// cancelOpen 释放 open 阶段的 context（见 hostHTTPDoStream 的 timer 说明）。
	// 拿到响应头后 timer 已停表，但 context 仍需在流结束时取消以回收资源；
	// nil 表示本次尝试没有独立的可取消 context（测试或直连 seam 直接构造的流）。
	cancelOpen func()
}

// streamOpenHeaderTimeoutDefault 是「请求已发出 → 上游响应头到达」的**单次尝试**
// 预算，也是 open 重试环的判据。
//
// 取证结论（2026-11-28，绕过插件与 CPA 直测上游 llm_utils_chat）：
//
//  1. 上游**自身处理**只要 1.0~1.6s。`event:timing_cost` 里
//     server_processing_time=901~1575ms、first_sse_event_time=1006~1509ms，
//     即使 body 810KB / 152k tokens 也是如此。queue_timing 仅 3~24ms。
//  2. 也就是说 20~110s 的「首字长尾」里，真正属于模型的只有 1 秒多，其余全部
//     发生在**响应头到达之前**的链路上（CDN/网关把响应头压住）。
//  3. 该长尾是**瞬时拥塞窗口**而非稳定状态：同一进程随机交错测 4 档 body × 9 轮
//     （n=36）时，慢事件全部落在前 4 轮，后 5 轮全快；空闲窗口内 header 只要
//     1.4~3.1s。
//  4. 体积决定**暴露面**：8KB 0/9 慢、128KB 0/9 慢、512KB 1/9(11%)、
//     810KB 4/9(44%)。700~730KB 仍全快，760KB 起开始出现 16~40s。
//  5. **关键**：卡住后中止并换新连接重发，几乎立刻恢复。同 body 8 次对照：
//     单次干等 p90=80.7s max=80.7s；6~8s 看门狗 + 最多 3 次重发
//     p90=11.7s max=11.7s（trial1 第 1 次 8s 超时、第 2 次 2.0s 成功）。
//     即「卡住」是**连接级**的，换连接就能绕开。
//
// 因此正确策略不是「如实等 180s」（那是把上游拥塞全量转嫁给客户端），而是
// 「短预算 + 换连接重试」：把最坏 20~110s 压到 ~8~18s。取 6s —— 健康窗口
// header 实测最大 4.6s，6s 能覆盖健康 p99 又足够早地甩掉拥塞连接。
// 可用配置项 host_bridge_open_timeout_seconds 覆盖。
const streamOpenHeaderTimeoutDefault = 6 * time.Second

// hostBridgeOpenTimeoutDefault 保留为旧名，语义已收敛为「单次尝试预算」。
// 历史名保留是为了让既有配置项与测试继续可用（它驱动的本来就是同一个看门狗）。
const hostBridgeOpenTimeoutDefault = streamOpenHeaderTimeoutDefault

// streamOpenMaxAttemptsDefault 是 open 重试环的最大尝试次数。
//
// 依据：单次尝试失败（卡住）后换连接的成功率很高（见上，重发链实测 2s 内恢复）。
// 上游拥塞窗口内单次卡住概率约 25%~44%，3 次尝试后仍全部卡住的概率降到
// 0.44^3 ≈ 8.5%（810KB 最坏档），而总耗时上界 = 3 × 预算 ≈ 18s。
// 取 3 是「最坏耗时」与「残余卡死率」的折中；可用配置项 stream_open_max_attempts 覆盖。
const streamOpenMaxAttemptsDefault = 3

// streamOpenMaxAttempts 是当前生效的最大尝试次数（可被配置覆盖）。
var streamOpenMaxAttempts = streamOpenMaxAttemptsDefault

// loadedStreamOpenMaxAttempts 读取当前生效的最大尝试次数。
//
// [参数] 无。
// [返回] 当前生效的最大尝试次数（恒 >= 1）。
// 最近修改时间：2026-11-29；改动原因：新增 open 重试环的运行时旋钮。
func loadedStreamOpenMaxAttempts() int {
	if streamOpenMaxAttempts < 1 {
		return 1
	}
	return streamOpenMaxAttempts
}

// setStreamOpenMaxAttempts 设置最大尝试次数；非正值忽略，保持既有值。
//
// [参数] n: 新的最大尝试次数，必须 > 0。
// [返回] 无。
// 最近修改时间：2026-11-29；改动原因：新增，配合 stream_open_max_attempts 配置项。
func setStreamOpenMaxAttempts(n int) {
	if n <= 0 {
		return
	}
	streamOpenMaxAttempts = n
}

// hostBridgeOpenTimeout 是当前生效的 open 看门狗预算（可被配置覆盖）。
// 保留为 var 而非 const：测试需要把它缩短到毫秒级来驱动降级路径。
var hostBridgeOpenTimeout = hostBridgeOpenTimeoutDefault

// errHostBridgeOpenTimeout reports that the host stream bridge did not open the
// upstream within hostBridgeOpenTimeout; callers should fall back to the direct
// HTTP client so the request still completes.
var errHostBridgeOpenTimeout = fmt.Errorf("host stream bridge open timed out after %s", hostBridgeOpenTimeout)

// setHostBridgeOpenTimeout 设置 open 单次尝试预算；非正值忽略，保持既有值。
//
// [参数] d: 新的单次尝试预算，必须 > 0。
// [返回] 无。
// 最近修改时间：2026-11-29；改动原因：默认值由 180s 下调到 6s。
// 180s 的依据是「上游长尾要如实等」，但后续取证推翻了该前提——长尾里只有 ~1.2s
// 属于上游处理，其余是响应头到达前的链路拥塞，且换连接重发即可绕开（详见
// streamOpenHeaderTimeoutDefault 注释）。因此预算改为「尽早判定 + 重试」。
func setHostBridgeOpenTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	hostBridgeOpenTimeout = d
	errHostBridgeOpenTimeout = fmt.Errorf("host stream bridge open timed out after %s", d)
	// transport 上的 ResponseHeaderTimeout 是静态字段，直连重试要跟随同一个
	// 预算就必须重建客户端。
	resetStreamHTTPClient()
}

// loadedHostBridgeOpenTimeout 读取当前 open 看门狗预算，供测试与日志使用。
//
// [参数] 无。
// [返回] 当前生效的看门狗预算。
// 最近修改时间：2026-11-28 14:30:00；改动原因：与 setHostBridgeOpenTimeout 配对。
func loadedHostBridgeOpenTimeout() time.Duration {
	return hostBridgeOpenTimeout
}

// hostBridgeReadTimeout bounds how long a single bridged Read RPC may block
// before the stream degrades to the plugin's own direct HTTP client. The read
// RPC is another synchronous cgo call that waits on the host's unbuffered chunk
// channel; a long qwen3.8-max generation can stall it for minutes (observed
// 2026-09-02: stream_id=1945 scheduled then 2 min with zero open/pump/pseudo/
// done logs, client got gin 499) while the plugin's direct client streams the
// same upstream fine. 90s is comfortably longer than a normal chunk gap for a
// healthy stream but still bounds the hang. Kept as a var (not const) so tests
// can shrink it to drive the degradation path deterministically.
var hostBridgeReadTimeout = 90 * time.Second

// errHostBridgeReadTimeout reports that a bridged Read RPC did not return
// within hostBridgeReadTimeout; the stream re-opens directly so the request
// completes instead of hanging.
var errHostBridgeReadTimeout = fmt.Errorf("host stream bridge read timed out after %s", hostBridgeReadTimeout)

// hostHTTPDoStream 打开上游流式请求，并在「响应头迟迟不到」时换连接重试。
//
// 重试环的形状（取证见 streamOpenHeaderTimeoutDefault 注释）：
//   - 第 1 次走宿主桥（host.http.do_stream），保留 request-log 与宿主传输策略；
//   - 第 2..N 次走插件直连客户端，因为它能被 ResponseHeaderTimeout 真正中断，
//     不会每重试一次就在宿主侧多漏一份仍在等待的上游请求；
//   - 上游已给出 4xx/5xx 明确结论时不重试（那是账号/参数问题）；
//   - 每次尝试都落一条 [traework] stream open retry 日志，恢复时另落一条
//     stream open recovered，使「卡住—换连接—恢复」成为可直接观测的事件。
//
// [参数] req: 上游 HTTP 请求；hostCallbackID: CPA 为当前异步执行注册的 callback 标识。
// [返回] hostHTTPStream: 可按块读取的响应流；int: HTTP 状态码；http.Header: 响应头；
// error: 全部尝试都失败时的最后一次错误。
// 最近修改时间：2026-11-29；改动原因：由「超时即放弃/静默降级」改为
// 「短预算 + 换连接重试」，把上游拥塞造成的 20~110s 首字压到 ~8~18s。
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
	// 直连开关：用于实测「插件直连」与「宿主桥」两条路径的 TTFT 差异。
	// 生产流量默认全部走宿主桥（host.http.do_stream RPC），插件的连接池配置因此
	// 对生产无效 —— 这个开关让运维可以在不改代码的前提下做 A/B 对照。
	useBridge := hostBridgeAvailable() && !streamDirectForced()
	attempts := loadedStreamOpenMaxAttempts()
	var lastErr error
	var lastStatus int
	var lastHeaders http.Header
	for attempt := 1; attempt <= attempts; attempt++ {
		// 只有第 1 次走宿主桥：桥 open 是同步 cgo 调用、宿主侧无任何超时，
		// 看门狗到点后无法真正取消宿主那次请求（会漏一个仍在等待的宿主流）。
		// 重试因此全部走插件自己的直连客户端——它能被 ResponseHeaderTimeout
		// 真正中断，不会每重试一次就多漏一份上游请求。
		viaBridge := useBridge && attempt == 1
		started := time.Now()
		// 每次尝试套一个真正覆盖「建连 + 上传请求体 + 等响应头」的硬 deadline。
		//
		// 为什么不能只靠 ResponseHeaderTimeout（2026-11-29 生产实测）：
		// ResponseHeaderTimeout 的计时起点是**请求体写完**之后，上传本身不受它约束。
		// 生产 body 1.2MB 时上传要 ~19s，于是日志出现
		// `elapsed_ms=25193 budget_ms=6000`、`elapsed_ms=32019 budget_ms=6000`——
		// 单次尝试实际耗时是预算的 4~5 倍，重试环的上界（预算×次数）完全失效。
		//
		// 实现细节：用 WithCancel + 手工 timer 而不是 WithTimeout。WithTimeout 的
		// deadline 会一直有效，6s 后会把已经拿到响应头、正在正常生成的长流一起掐断；
		// 这里只约束 open 阶段——拿到响应头就停表（stopTimer），cancel 转交给
		// hostHTTPStream.Close，流生命周期结束时才释放 context。
		attemptCtx, cancelAttempt := context.WithCancel(req.Context())
		openTimer := time.AfterFunc(loadedHostBridgeOpenTimeout(), cancelAttempt)
		stream, status, headers, err := openStreamOnce(attemptCtx, req, bodyBytes, hostCallbackID, viaBridge)
		if !openTimer.Stop() {
			// timer 已触发：本次尝试被判超时，cancel 已生效，不再挂到流上。
			cancelAttempt()
		} else if stream != nil {
			stream.cancelOpen = cancelAttempt
		} else {
			cancelAttempt()
		}
		if err == nil {
			if attempt > 1 {
				log.Printf("[traework] stream open recovered after retry: url=%s attempt=%d/%d body_bytes=%d elapsed_ms=%d",
					req.URL.Redacted(), attempt, attempts, len(bodyBytes),
					time.Since(started).Milliseconds())
			}
			return stream, status, headers, nil
		}
		lastErr, lastStatus, lastHeaders = err, status, headers
		// 上游已经给出明确 HTTP 结论（4xx/5xx）时不重试：那是账号/参数问题，
		// 换连接重发只会重复同样的错误，还会多打一次上游。
		if status >= 400 {
			return nil, status, headers, err
		}
		reason := "open_error"
		if errors.Is(err, errHostBridgeOpenTimeout) {
			reason = "header_timeout"
		}
		log.Printf("[traework] stream open retry: url=%s attempt=%d/%d via_bridge=%t reason=%s elapsed_ms=%d budget_ms=%d body_bytes=%d err=%s",
			req.URL.Redacted(), attempt, attempts, viaBridge, reason,
			time.Since(started).Milliseconds(), loadedHostBridgeOpenTimeout().Milliseconds(),
			len(bodyBytes), truncateRedacted(err.Error(), 200))
	}
	return nil, lastStatus, lastHeaders, lastErr
}

// openStreamOnce 执行**一次**上游 open 尝试。
//
// 与 hostHTTPDoStream 的分工：本函数不含重试与看门狗语义，只负责「走桥」或
// 「走直连」其中的一条路径并把结果原样返回，让重试环可以干净地重复调用。
//
// [参数] ctx: 本次尝试的 context；其取消（含 open 预算到点）会中断建连、上传与
// 等待响应头，但拿到响应头后不再约束正文流（见 hostHTTPDoStream 的 timer 说明）。
// [参数] req: 上游 HTTP 请求（仅提供方法/URL/头，body 由 bodyBytes 提供）。
// [参数] bodyBytes: 已读出的请求体；重试时必须原样重发，不能复用已读的 Body。
// [参数] hostCallbackID: 宿主侧异步执行回调标识，走桥时透传。
// [参数] viaBridge: true 走宿主 host.http.do_stream，false 走插件直连客户端。
// [返回] hostHTTPStream: 可按块读取的响应流；int: HTTP 状态码；
// http.Header: 响应头；error: 本次尝试失败。
// 最近修改时间：2026-11-29；改动原因：新增 ctx 入参，使上传阶段也受 open 预算约束。
func openStreamOnce(ctx context.Context, req *http.Request, bodyBytes []byte, hostCallbackID string, viaBridge bool) (*hostHTTPStream, int, http.Header, error) {
	if !viaBridge {
		return hostStreamDirectFn(req.WithContext(ctx), bodyBytes)
	}
	wire := rpcHostHTTPRequestWire{
		HostCallbackID: hostCallbackID, // 绑定 CPA 异步执行保留到输出流结束的 callback context。
		Request: &rpcHostHTTPInner{
			Method:  req.Method,
			URL:     req.URL.String(),
			Headers: map[string][]string(req.Header),
			Body:    bodyBytes,
		},
	}
	raw, err := hostStreamOpenFn(mustJSON(wire))
	if err != nil {
		return nil, 0, nil, err
	}
	result, err := hostBridgeUnwrap(raw, pluginabi.MethodHostHTTPDoStream)
	if err != nil {
		return nil, 0, nil, err
	}
	var resp rpcHostHTTPStreamResponseWire
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, 0, nil, fmt.Errorf("decode host.http.do_stream response: %w", err)
	}
	if resp.StreamID == "" {
		return nil, resp.StatusCode, http.Header(resp.Headers), fmt.Errorf("host stream bridge unavailable")
	}
	// 保存原始请求与请求体：bridged read 若在 hostBridgeReadTimeout 内无数据，
	// 同一 attempt 可直接重开上游，无需重建认证头与 payload。
	return &hostHTTPStream{
		streamID:  resp.StreamID,
		req:       req,
		bodyBytes: bodyBytes,
	}, resp.StatusCode, http.Header(resp.Headers), nil
}

// hostStreamOpenFn is the host stream bridge open seam, injectable in tests to
// simulate a hanging MethodHostHTTPDoStream RPC.
var hostStreamOpenFn = hostHTTPDoStreamBridged

// hostStreamOpenCallFn 是宿主 open RPC 的最内层调用缝，默认执行真实的同步
// cgo 调用。与 hostStreamOpenFn 的分工：hostStreamOpenFn 替换的是「带看门狗的
// 包装」，替换它等于连看门狗一起去掉；要验证看门狗本身必须只让最内层挂起。
// [参数] wireJSON: 已序列化的宿主请求。
// [返回] 宿主原始信封；error 表示 RPC 失败。
// 最近修改时间：2026-11-28 14:30:00；改动原因：新增，使 open 看门狗可被单测驱动。
var hostStreamOpenCallFn = func(wireJSON []byte) ([]byte, error) {
	return hostCall(pluginabi.MethodHostHTTPDoStream, wireJSON)
}

// hostHTTPDoStreamBridged 在有限等待内执行宿主 host.http.do_stream RPC。
//
// 背景与语义（2026-11-28 生产取证，本函数是 33~36s / 123~126s TTFT 台阶的直接来源）：
//
// 宿主 open 是**同步阻塞到上游响应头**才返回 stream_id 的 RPC，且宿主侧
// ResponseHeaderTimeout 与 client.Timeout 双为 0。直测上游实测（绕过插件与 CPA）：
// 同一账号、同一 body、每次新建连接，响应头延迟 p50 仅 11~16s，但会**随机**出现
// 40s / 80s / 101s 的尾部。也就是说上游本身就有长尾，官方客户端遇到长尾就是
// 「等 40s」，不会变成别的数字。
//
// 而本看门狗一旦到点，调用方会**放弃宿主侧仍在等待的 open 并重发一次完整上游请求**
// （body 1.2~2.5MB 全量重传）。生产日志逐条对齐证实：
//
//	ttft ≈ 看门狗预算 + 第二次直连耗时
//	（8/8 降级事件：预算 120000ms + 3547~8053ms）
//
// 即「上游慢」被放大成「预算 + 又一次上游耗时」的人造延迟，且同一逻辑请求在上游
// 变成两份。这正是台阶随预算移动的原因（30s 预算 → 33~36s 台阶；120s 预算 →
// 123~126s 台阶）。
//
// 因此这里的语义是「**单次尝试**预算，不是最终放弃点」：到点返回
// errHostBridgeOpenTimeout，由调用方 hostHTTPDoStream 的重试环换一条新连接重发。
// 关键取证（2026-11-29，同 body 8 次对照）：卡住后换连接重发几乎立刻恢复——
// 单次干等 p90=80.7s max=80.7s，6~8s 看门狗 + 最多 3 次重发 p90/max 均为 11.7s。
// 即「卡住」是连接级的链路拥塞，不是上游真需要那么久（上游自报处理仅 ~1.2s）。
// 预算必须**短**才有效：旧的 180s 会让每次重试前面白等三分钟。
//
// [参数] wireJSON: 已序列化的宿主请求信封。
// [返回] 宿主原始信封；error 表示 RPC 失败或看门狗超时（errHostBridgeOpenTimeout）。
// 最近修改时间：2026-11-29；改动原因：看门狗语义定为「单次尝试预算」，
// 与 hostHTTPDoStream 的重试环配套，默认值 180s → 6s。
func hostHTTPDoStreamBridged(wireJSON []byte) ([]byte, error) {
	type res struct {
		raw []byte
		err error
	}
	done := make(chan res, 1)
	go func() {
		raw, err := hostStreamOpenCallFn(wireJSON)
		done <- res{raw: raw, err: err}
	}()
	select {
	case r := <-done:
		return r.raw, r.err
	case <-time.After(loadedHostBridgeOpenTimeout()):
		return nil, errHostBridgeOpenTimeout
	}
}

// hostHTTPDoStreamDirect performs the request with the plugin's own http.Client
// and streams the response body as the upstream produces it. Used when the host
// stream bridge is unavailable, or as the degradation path after the bridge
// open (hostBridgeOpenTimeout) or read (hostBridgeReadTimeout) stalled — without
// this the async coordinator would hang forever on the synchronous host RPC.
// Unlike the old test-only buffered variant, it does NOT wait for the full body,
// so long generations still stream to the client instead of blocking on a full
// read.
func hostHTTPDoStreamDirect(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, 0, nil, err
	}
	newReq.Header = req.Header.Clone()
	resp, err := streamHTTPClient().Do(newReq)
	if err != nil {
		return nil, 0, nil, err
	}
	return &hostHTTPStream{liveBody: resp.Body}, resp.StatusCode, resp.Header.Clone(), nil
}

// streamHTTPClient is the plugin's own HTTP client for direct STREAMING
// upstream calls. It deliberately has NO overall Timeout (unlike
// sharedHTTPClient's 120s): a single qwen3.8-max long reasoning generation can
// legitimately take minutes, and an overall deadline would truncate it mid-stream
// right when the bridge read degraded. Only per-operation dial/TLS deadlines
// apply so a wedged connection still fails fast while a healthy long stream
// runs to completion.
//
// 上游 HTTP 分阶段超时：拨号与 TLS 握手挡住「连不上」，ResponseHeaderTimeout
// 挡住「连上了但首字节迟迟不来」。
//
// ResponseHeaderTimeout 绑定的是**单次尝试预算**（loadedHostBridgeOpenTimeout），
// 不是历史那个 120s：直连重试必须能被真正中断，否则一次拥塞会把重试环拖成
// 120s × N，反而比不重试更糟。
func streamHTTPClient() *http.Client {
	streamHTTPClientMu.Lock()
	defer streamHTTPClientMu.Unlock()
	streamHTTPClientOnce.Do(func() {
		streamClient = &http.Client{
			Transport: &http.Transport{
				// 连接池容量按账号池规模设定：生产 traework 有 28 个可用账号，
				// 每个账号在同一上游 host 上独立承流。MaxIdleConnsPerHost=5 时，
				// 28 个并发请求会频繁触发新建连接（TCP + TLS 握手各一次），
				// 高并发窗口下这部分开销直接叠加到 TTFT 上。
				// 生产实测（2026-11-10）：同分钟并发 16-30 时慢请求占比 26.5%，
				// 而并发 1-5 时仅 9.9%，呈明显正相关。
				MaxIdleConns:        200,
				IdleConnTimeout:     90 * time.Second,
				MaxIdleConnsPerHost: 64,
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   upstreamTLSHandshakeTimeout,
				ResponseHeaderTimeout: loadedHostBridgeOpenTimeout(),
			},
		}
	})
	return streamClient
}

// resetStreamHTTPClient 丢弃已构造的流式直连客户端，让下一次调用按当前
// 单次尝试预算重建 transport。
//
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-29；改动原因：ResponseHeaderTimeout 改为跟随
// host_bridge_open_timeout_seconds，配置热更新后必须重建才生效。
func resetStreamHTTPClient() {
	streamHTTPClientMu.Lock()
	defer streamHTTPClientMu.Unlock()
	streamClient = nil
	streamHTTPClientOnce = sync.Once{}
}

// Read pulls the next chunk. Returns (payload, done, err). done=true means the
// stream ended cleanly; err non-nil means upstream or bridge error.
//
// Bridged reads are wrapped in a goroutine+select race: the read RPC is a
// synchronous cgo call with no host-side deadline, and a stalled upstream can
// block it past hostBridgeReadTimeout while the plugin's direct client streams
// the same upstream fine. On timeout the stream degrades to the direct live
// body (re-opening the upstream once) so the request completes instead of
// hanging the coordinator. Reads are sequential (single consumer), so the
// sticky liveBody branch at the top serves the rest of the attempt.
func (s *hostHTTPStream) Read() ([]byte, bool, error) {
	if s == nil {
		return nil, true, fmt.Errorf("stream closed")
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
	// Live direct mode: drain the open upstream response body as bytes arrive.
	// Once a bridged read degrades (below) this branch serves the remainder.
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
	if s.streamID == "" {
		return nil, true, fmt.Errorf("stream closed")
	}

	// Bridged read with bounded wait; on timeout re-open directly.
	raw, err := s.bridgedRead()
	if err == nil {
		return s.bridgeReadUnwrap(raw)
	}
	if err != errHostBridgeReadTimeout {
		return nil, true, err
	}
	return s.degradeToDirect()
}

// hostStreamReadFn is the host stream bridge read seam, injectable in tests to
// simulate a hanging MethodHostHTTPStreamRead RPC (the production read stall
// that the read-phase degradation guards against). Defaults to the real
// synchronous cgo call.
var hostStreamReadFn = func(streamID string) ([]byte, error) {
	return hostCall(pluginabi.MethodHostHTTPStreamRead, mustJSON(map[string]any{"stream_id": streamID}))
}

// hostStreamDirectFn is the direct-stream degradation seam, injectable in tests
// to substitute an in-memory upstream for the real plugin HTTP client. Defaults
// to the real client (hostHTTPDoStreamDirect).
var hostStreamDirectFn = hostHTTPDoStreamDirect

// bridgedRead runs one host stream read RPC under hostBridgeReadTimeout; the
// goroutine's late result is discarded if the timer wins (the host-side stream
// is closed by the eventual Close RPC, and this stream is now direct).
func (s *hostHTTPStream) bridgedRead() ([]byte, error) {
	type res struct {
		raw []byte
		err error
	}
	done := make(chan res, 1)
	go func() {
		raw, err := hostStreamReadFn(s.streamID)
		done <- res{raw: raw, err: err}
	}()
	select {
	case r := <-done:
		return r.raw, r.err
	case <-time.After(hostBridgeReadTimeout):
		return nil, errHostBridgeReadTimeout
	}
}

// bridgeReadUnwrap decodes the host stream read envelope into a chunk triple.
func (s *hostHTTPStream) bridgeReadUnwrap(raw []byte) ([]byte, bool, error) {
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

// degradeToDirect re-opens the same upstream with the plugin's own HTTP client
// and switches this stream to live direct mode, then drains the first chunk.
// Subsequent Reads hit the liveBody branch at the top of Read.
func (s *hostHTTPStream) degradeToDirect() ([]byte, bool, error) {
	if s.req == nil {
		return nil, true, fmt.Errorf("host stream bridge read timed out (%s) and no direct fallback request", hostBridgeReadTimeout)
	}
	direct, statusCode, _, err := hostStreamDirectFn(s.req, s.bodyBytes)
	if err != nil {
		return nil, true, fmt.Errorf("host stream bridge read timed out (%s); direct fallback failed: %w", hostBridgeReadTimeout, err)
	}
	if statusCode < 200 || statusCode >= 300 {
		return nil, true, fmt.Errorf("host stream bridge read timed out (%s); direct fallback status %d", hostBridgeReadTimeout, statusCode)
	}
	s.liveBody = direct.liveBody
	s.streamID = ""
	// Read from the direct body immediately.
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

// Close aborts the upstream stream. Always safe to call (idempotent on host).
func (s *hostHTTPStream) Close() {
	if s == nil {
		return
	}
	// 先归还账号在跑计数：Close 是唯一确定的流结束点，在这里释放才能与占用的
	// 上游槽位时长严格对齐；重复 Close 由 hostHTTPStream 自身的字段置空保证幂等。
	if s.releaseAccountLoad != nil {
		s.releaseAccountLoad()
		s.releaseAccountLoad = nil
	}
	// 释放 open 阶段 context：拿到响应头时 timer 已停表，但 context 本身要等到
	// 流结束才回收，否则每次请求都会漏一个 context 节点。
	if s.cancelOpen != nil {
		s.cancelOpen()
		s.cancelOpen = nil
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
