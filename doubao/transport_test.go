package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestSharedTransportHandshakeTimeoutWidened 钉死握手超时必须显著大于 Go 默认值。
//
// 这是本轮缺陷的回归防线：默认 Transport 的 TLSHandshakeTimeout 是 10s，
// 生产曾出现 10.128s 判据判死（net/http: TLS handshake timeout），而同一时刻
// 15 个 CDN 节点逐个握手全部成功——放宽到 20s 后慢节点能跑完。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-15 13:30:00；改动原因：新增，覆盖 CDN 节点级握手黑洞。
func TestSharedTransportHandshakeTimeoutWidened(t *testing.T) {
	// 1. 必须自建实例，不能回落到全进程共用的默认 Transport。
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatal("http.DefaultTransport 不再是非 *http.Transport，本用例前提失效")
	}
	if sharedUpstreamTransport == defaultTransport {
		t.Fatal("上游传输层不能复用 http.DefaultTransport：改它会波及全进程")
	}

	// 2. 握手超时必须严格大于默认的 10s。
	if sharedUpstreamTransport.TLSHandshakeTimeout != upstreamTLSHandshakeTimeout {
		t.Fatalf("握手超时=%v，期望 %v", sharedUpstreamTransport.TLSHandshakeTimeout, upstreamTLSHandshakeTimeout)
	}
	if sharedUpstreamTransport.TLSHandshakeTimeout <= defaultTransport.TLSHandshakeTimeout {
		t.Fatalf("握手超时 %v 未超过默认 Transport 的 %v，握手抖动仍会判死请求",
			sharedUpstreamTransport.TLSHandshakeTimeout, defaultTransport.TLSHandshakeTimeout)
	}

	// 3. 拨号超时必须显式设置：默认 Transport 的该字段为 0（不限时），
	//    一旦 CDN 节点丢包，建连会挂到客户端总超时为止。
	if sharedUpstreamTransport.DialContext == nil {
		t.Fatal("必须自定义 DialContext，否则拨号阶段不受控")
	}
}

// TestNewUpstreamHTTPClientUsesSharedTransport 验证构造入口没有退回默认 Transport。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-15 13:30:00；改动原因：新增，防止调用点重新写裸字面量。
func TestNewUpstreamHTTPClientUsesSharedTransport(t *testing.T) {
	client := newUpstreamHTTPClient(5 * time.Second)
	if client.Transport != http.RoundTripper(sharedUpstreamTransport) {
		t.Fatal("客户端未使用共享传输层，握手超时配置不会生效")
	}
	if client.Timeout != 5*time.Second {
		t.Fatalf("客户端总超时=%v，期望 5s", client.Timeout)
	}
}

// countingTransport 是可控的 RoundTripper：前 failTimes 次直接返回传输错误。
type countingTransport struct {
	failTimes int32
	calls     int32
	err       error
}

// RoundTrip 实现 http.RoundTripper。
// [参数] req: 待发送请求。
// [返回] 响应与错误。
// 最近修改时间：2026-11-15 13:30:00；改动原因：新增，让重试分支可被单测覆盖。
func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	n := atomic.AddInt32(&c.calls, 1)
	if n <= c.failTimes {
		return nil, c.err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"message":"success"}`)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// TestDoIdempotentRetriesTransportError 验证幂等请求在传输层失败后会重试并最终成功。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-15 13:30:00；改动原因：新增，覆盖判据请求的重试分支。
func TestDoIdempotentRetriesTransportError(t *testing.T) {
	tr := &countingTransport{
		failTimes: 2,
		err:       errors.New("net/http: TLS handshake timeout"),
	}
	client := &http.Client{Transport: tr}

	req, errReq := http.NewRequest(http.MethodGet, "https://accounts.doubao.com/passport/account/info/v2/", nil)
	if errReq != nil {
		t.Fatalf("构造请求失败：%v", errReq)
	}

	resp, errDo := doIdempotent(req, client)
	if errDo != nil {
		t.Fatalf("前两次握手失败后第三次应成功，实际错误：%v", errDo)
	}
	defer resp.Body.Close()

	if got := atomic.LoadInt32(&tr.calls); got != idempotentAttempts {
		t.Fatalf("实际尝试 %d 次，期望 %d 次", got, idempotentAttempts)
	}
}

// TestDoUnsentSafeStopsAfterHandshakeDone 验证非幂等请求不会在「可能已送达」后重试。
//
// 令牌只能兑换一次：连接重置这类错误无法判断上游是否已消费令牌，必须一次就放弃，
// 否则重试可能把同一枚令牌打两次。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-15 13:30:00；改动原因：新增，钉死非幂等请求的重试边界。
func TestDoUnsentSafeStopsAfterHandshakeDone(t *testing.T) {
	tr := &countingTransport{
		failTimes: 3,
		err:       errors.New("read tcp 10.0.0.1:443: connection reset by peer"),
	}
	client := &http.Client{Transport: tr}

	req, errReq := http.NewRequest(http.MethodPost, "https://www.doubao.com/passport/token/beat/v2/", strings.NewReader("{}"))
	if errReq != nil {
		t.Fatalf("构造请求失败：%v", errReq)
	}

	if _, errDo := doUnsentSafe(req, client); errDo == nil {
		t.Fatal("连接重置不属于「请求未送出」，必须直接失败")
	}
	if got := atomic.LoadInt32(&tr.calls); got != 1 {
		t.Fatalf("可能已送达的请求实际重试了 %d 次，期望 1 次", got)
	}
}

// TestRequestNeverSentClassification 覆盖「请求是否已送出」的判据分类。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-15 13:30:00；改动原因：新增，固定判据口径。
func TestRequestNeverSentClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"拨号失败", &net.OpError{Op: "dial", Err: errors.New("i/o timeout")}, true},
		{"握手超时", errors.New("net/http: TLS handshake timeout"), true},
		{"连接重置", errors.New("read tcp 10.0.0.1:443: connection reset by peer"), false},
		{"读取响应失败", errors.New("unexpected EOF"), false},
		{"空错误", nil, false},
	}
	for _, c := range cases {
		if got := requestNeverSent(c.err); got != c.want {
			t.Errorf("%s：requestNeverSent=%v，期望 %v", c.name, got, c.want)
		}
	}
}

// TestProbeUsesSharedTransportAgainstRealServer 验证判据链路整体仍可跑通（httptest，无外网）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-15 13:30:00；改动原因：新增，确认接入重试后判据语义未被破坏。
func TestProbeUsesSharedTransportAgainstRealServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":"success","data":{"sec_user_id":"sec-abc","user_id":42,"screen_name":"探针账号"}}`)
	}))
	defer server.Close()

	probed, errProbe := probeAccountInfoAgainst(
		&credentials{CookieHeader: "sessionid=x; flow_cur_user_sec_id=sec-abc"},
		server.URL,
		5*time.Second,
	)
	if errProbe != nil {
		t.Fatalf("判据探测失败：%v", errProbe)
	}
	if probed.SecUserID != "sec-abc" {
		t.Fatalf("sec_user_id=%q，期望 sec-abc", probed.SecUserID)
	}
	if probed.UserID != 42 {
		t.Fatalf("user_id=%d，期望 42", probed.UserID)
	}
}
