package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// 本文件集中定义上游 HTTP 传输层设施：共享 Transport 与幂等请求重试。
//
// 背景（2026-11-15 实测）：插件原先三处都写 &http.Client{Timeout: ...}，
// 全部落到 http.DefaultTransport，其 TLSHandshakeTimeout 固定 10s。豆包 CDN
// （accounts.doubao.com 共 15 个 A 记录）偶尔会有节点握手黑洞，单次 10s
// 超时即把整条授权判死——生产日志里 callback 耗时 10.128s 报
// net/http: TLS handshake timeout，而同一时刻 15 个节点逐个握手全部成功，
// 说明是节点级偶发，不是链路故障。默认 Transport 还共用全局连接池，
// 任何一个调用方改它会波及全进程，故此处自建独立实例。

const (
	// upstreamDialTimeout 是上游建连（含 TCP 三次握手）超时。
	upstreamDialTimeout = 10 * time.Second

	// upstreamTLSHandshakeTimeout 是上游 TLS 握手超时。
	//
	// 必须显著大于默认 Transport 的 10s：被判死的下一次握手往往几十毫秒就完成，
	// 放宽到 20s 足以让慢节点跑完，而不会让真正的黑洞请求无限挂着。
	upstreamTLSHandshakeTimeout = 20 * time.Second

	// upstreamIdleConnTimeout 是上游空闲连接保活时长。
	//
	// 授权与探活都是低频请求，保活太久只会占着半开连接，取 Go 默认的 90s。
	upstreamIdleConnTimeout = 90 * time.Second

	// idempotentAttempts 是幂等请求（GET 判据）的最大尝试次数。
	idempotentAttempts = 3

	// idempotentRetryDelay 是幂等请求两次尝试之间的间隔。
	idempotentRetryDelay = 400 * time.Millisecond
)

// sharedUpstreamTransport 是所有上游请求共用的传输层。
//
// 独立实例而非 http.DefaultTransport：默认实例是进程级全局单例，任何调用方
// 修改都会影响整进程；自建实例才能安全地放宽握手超时。
var sharedUpstreamTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   upstreamDialTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          64,
	MaxIdleConnsPerHost:   8,
	IdleConnTimeout:       upstreamIdleConnTimeout,
	TLSHandshakeTimeout:   upstreamTLSHandshakeTimeout,
	ExpectContinueTimeout: 1 * time.Second,
}

// newUpstreamHTTPClient 构造带共享传输层的 HTTP 客户端。
//
// 替换各调用点原来的 &http.Client{Timeout: ...} 字面量：那个写法会让客户端
// 落到默认 Transport，握手超时无法调整。
// [参数] timeout: 整请求超时；0 表示不设，由 context 约束（流式请求用）。
// [返回] 新的 HTTP 客户端。
// 最近修改时间：2026-11-15 13:25:00；改动原因：新增，统一上游传输层，修正握手超时。
func newUpstreamHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: sharedUpstreamTransport,
		Timeout:   timeout,
	}
}

// doIdempotent 执行幂等请求，传输层失败时按固定次数重试。
//
// 只对「没有拿到任何响应」的传输错误重试：TLS 握手超时、连接被重置、
// 临时 DNS 失败都属于节点级偶发，换一次解析结果通常就通了。一旦上游已经
// 回了响应（哪怕 5xx），重试就不再是传输层问题，交给调用方按业务判定处理
// —— 上游对无效登录态也回 HTTP 200，重试只会拿到同样的结果。
// [参数] req: 待发送请求，必须是 GET / HEAD 这类幂等请求；client: 执行客户端。
// [返回] 响应与错误；全部尝试失败时返回最后一次的错误。
// 最近修改时间：2026-11-15 13:25:00；改动原因：新增，吸收 CDN 节点级握手抖动。
func doIdempotent(req *http.Request, client *http.Client) (*http.Response, error) {
	return doWithRetry(req, client, idempotentAttempts, nil)
}

// doUnsentSafe 执行非幂等请求，仅在「请求肯定没发出去」时重试。
//
// 用于一次性令牌兑换这类不许重复消费的 POST：连接根本没建立（拨号失败、
// TLS 握手超时）时上游不可能收到请求，此时重试安全；一旦握手完成、字节
// 已经写出，响应丢失也无法判断上游是否已消费令牌，只能如实报错交给用户。
// [参数] req: 待发送请求；client: 执行客户端。
// [返回] 响应与错误。
// 最近修改时间：2026-11-15 13:27:00；改动原因：新增，令牌兑换只重试握手阶段失败。
func doUnsentSafe(req *http.Request, client *http.Client) (*http.Response, error) {
	return doWithRetry(req, client, idempotentAttempts, func(err error) bool { return requestNeverSent(err) })
}

// doWithRetry 是重试内核：按 attempts 次尝试，shouldRetry 为 nil 时对任意传输错误重试。
// [参数] req: 待发送请求；client: 执行客户端；attempts: 最大尝试次数；shouldRetry: 额外的重试判据。
// [返回] 响应与错误。
// 最近修改时间：2026-11-15 13:27:00；改动原因：新增，把重试循环从两个入口收敛到一处。
func doWithRetry(req *http.Request, client *http.Client, attempts int, shouldRetry func(error) bool) (*http.Response, error) {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		// 1. 每次尝试都取一份请求副本：同一个 *http.Request 复用会把已关闭的
		//    Body 与上次的连接状态带进来；Clone 只复制头部，带 Body 的请求
		//    必须靠 GetBody 重新开一个读指针，否则第二次尝试会读到 EOF。
		attemptReq := req.Clone(req.Context())
		if attempt > 1 && req.GetBody != nil {
			body, errBody := req.GetBody()
			if errBody != nil {
				return nil, fmt.Errorf("重试时重建请求体失败：%w", errBody)
			}
			attemptReq.Body = body
		}

		resp, errDo := client.Do(attemptReq)
		if errDo == nil {
			return resp, nil
		}
		lastErr = errDo

		// 2. 上层主动取消不该重试：用户关页面或插件卸载时立即退出。
		if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
			return nil, errDo
		}

		// 3. 调用方给出的额外判据不成立（如请求可能已送达）时立即放弃。
		if shouldRetry != nil && !shouldRetry(errDo) {
			return nil, errDo
		}

		// 4. 最后一次失败直接返回，不再空等。
		if attempt == attempts {
			break
		}
		time.Sleep(idempotentRetryDelay)
	}
	return nil, fmt.Errorf("重试 %d 次后仍然失败：%w", attempts, lastErr)
}

// requestNeverSent 判断错误是否发生在请求字节写出之前。
//
// 两类可判定：拨号阶段失败（*net.OpError 的 Op 为 dial）与 TLS 握手超时
// （transport 的 errTimeout，错误文本为 "net/http: TLS handshake timeout"）。
// 这两种情况下上游一定没收到请求，非幂等请求重试也不会造成重复消费。
// [参数] err: client.Do 返回的错误。
// [返回] 判定为「请求未送达」时返回 true。
// 最近修改时间：2026-11-15 13:27:00；改动原因：新增，为非幂等请求划定安全重试边界。
func requestNeverSent(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	// TLS 握手超时没有可识别的错误类型，只能按 transport 的固定错误文本判定。
	return strings.Contains(err.Error(), "TLS handshake timeout")
}
