package cpa

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginapi"
)

// resetPublicOriginForTest 清空包级 origin 缓存。
//
// origin 缓存是包级全局变量，跨用例共享会互相污染：一旦某个用例把
// https://cpa.example.com 写进去，后面断言回环兜底的用例就会拿到脏值。
// 这里用白盒包（package cpa）直接操作变量，避免为测试往生产代码里加
// 导出的重置函数。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，隔离 origin 缓存的用例间污染。
func resetPublicOriginForTest() {
	publicOriginMu.Lock()
	publicOrigin = ""
	publicOriginMu.Unlock()
}

// TestSplitOrigin 校验 origin 拆分只接受 http/https 且丢掉路径部分。
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestSplitOrigin(t *testing.T) {
	cases := []struct {
		in         string
		wantScheme string
		wantHost   string
	}{
		{"https://cpa.example.com", "https", "cpa.example.com"},
		{"https://cpa.example.com/", "https", "cpa.example.com"},
		{"https://cpa.example.com/v0/resource?x=1", "https", "cpa.example.com"},
		{"HTTP://CPA.EXAMPLE.COM:8317/app", "http", "CPA.EXAMPLE.COM:8317"},
		{"ftp://cpa.example.com", "", ""},
		{"cpa.example.com", "", ""},
		{"", "", ""},
	}
	for _, item := range cases {
		scheme, host := splitOrigin(item.in)
		if scheme != item.wantScheme || host != item.wantHost {
			t.Fatalf("splitOrigin(%q) = (%q, %q), want (%q, %q)", item.in, scheme, host, item.wantScheme, item.wantHost)
		}
	}
}

// TestIsLoopbackHost 校验回环判定覆盖 IPv4、IPv6 与端口形式。
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestIsLoopbackHost(t *testing.T) {
	loopback := []string{"127.0.0.1", "127.0.0.1:8317", "localhost", "localhost:8317", "0.0.0.0", "[::1]:8317", "::1"}
	for _, host := range loopback {
		if !isLoopbackHost(host) {
			t.Fatalf("%q should be treated as loopback", host)
		}
	}
	for _, host := range []string{"cpa.example.com", "cpa.example.com:8317", "10.0.0.1:8317"} {
		if isLoopbackHost(host) {
			t.Fatalf("%q should not be treated as loopback", host)
		}
	}
}

// TestFirstHeaderValue 校验多级反代写出的逗号列表只取最外层。
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestFirstHeaderValue(t *testing.T) {
	cases := map[string]string{
		"https, http":    "https",
		" https , http ": "https",
		"https":          "https",
		"":               "",
	}
	for in, want := range cases {
		if got := firstHeaderValue(in); got != want {
			t.Fatalf("firstHeaderValue(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestReportPublicOriginIgnoresLoopback 校验回环 origin 不会覆盖已记录的真实域名。
//
// 这条不变量很关键：容器内自测、健康检查、运维 curl 的 origin 都是
// http://127.0.0.1:8317，一旦被允许覆盖，线上授权链接就会被改回本机地址。
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestReportPublicOriginIgnoresLoopback(t *testing.T) {
	resetPublicOriginForTest()
	t.Cleanup(resetPublicOriginForTest)

	reportPublicOrigin("https://cpa.example.com/")
	if got := currentPublicOrigin(); got != "https://cpa.example.com" {
		t.Fatalf("unexpected origin after first report: %q", got)
	}

	reportPublicOrigin("http://127.0.0.1:8317")
	if got := currentPublicOrigin(); got != "https://cpa.example.com" {
		t.Fatalf("loopback origin must not overwrite real domain, got %q", got)
	}

	reportPublicOrigin("not-an-origin")
	if got := currentPublicOrigin(); got != "https://cpa.example.com" {
		t.Fatalf("invalid origin must not overwrite real domain, got %q", got)
	}

	// 冷启动时只有回环上报的话仍要接受，否则首屏完全没有可用 origin。
	resetPublicOriginForTest()
	reportPublicOrigin("http://127.0.0.1:8317")
	if got := currentPublicOrigin(); got != "http://127.0.0.1:8317" {
		t.Fatalf("loopback origin should be accepted when cache is empty, got %q", got)
	}
}

// TestEffectiveLoginOriginPriority 校验 origin 取值优先级：转发头 > 缓存 > 宿主 BaseURL。
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestEffectiveLoginOriginPriority(t *testing.T) {
	resetPublicOriginForTest()
	t.Cleanup(resetPublicOriginForTest)

	// 1) 请求头带真实域名时优先用请求头，并覆盖缓存。
	headers := http.Header{}
	headers.Set("X-Forwarded-Host", "cpa.example.com")
	headers.Set("X-Forwarded-Proto", "https")
	if got := effectiveLoginOrigin(headers, "http://127.0.0.1:8317"); got != "https://cpa.example.com" {
		t.Fatalf("header origin should win, got %q", got)
	}

	// 2) 请求头只有回环时不能采信，必须回落到浏览器上报的缓存域名。
	// effectiveLoginOrigin 是纯函数、不写缓存，所以这里显式模拟一次浏览器上报。
	reportPublicOrigin("https://cpa.example.com")
	loopbackHeader := http.Header{}
	loopbackHeader.Set("Host", "127.0.0.1:8317")
	if got := effectiveLoginOrigin(loopbackHeader, "http://127.0.0.1:8317"); got != "https://cpa.example.com" {
		t.Fatalf("loopback header must fall back to cached origin, got %q", got)
	}

	// 3) 缓存为空且请求头为回环时，退回宿主给的 BaseURL。
	resetPublicOriginForTest()
	if got := effectiveLoginOrigin(loopbackHeader, "http://10.0.0.1:8317/cb"); got != "http://10.0.0.1:8317" {
		t.Fatalf("expected BaseURL fallback, got %q", got)
	}
}

// TestOriginFromHeadersRefererFallback 校验请求头里没有主机信息时用 Referer 兜底。
//
// 生产链路上宿主 cloneHeader 不包含 r.Host，nginx 也没设 X-Forwarded-Host，
// 所以前两级恒为空；此时唯一还能还原真实域名的线索就是 Referer —— 控制台
// 用自己的页面地址发同源请求时一定会带上它。丢掉这条兜底，授权链接就又会
// 回落成 127.0.0.1。
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增 Referer 兜底后补齐断言。
func TestOriginFromHeadersRefererFallback(t *testing.T) {
	// 1) 只有 Referer：直接采用它的 scheme 与 host。
	refererOnly := http.Header{}
	refererOnly.Set("Referer", "https://cpa.luode.vip/management.html?key=x")
	if got := originFromHeaders(refererOnly); got != "https://cpa.luode.vip" {
		t.Fatalf("expected referer origin, got %q", got)
	}

	// 2) 转发头存在时优先于 Referer，避免代理链上的 Referer 抢占真实域名。
	both := http.Header{}
	both.Set("X-Forwarded-Host", "cpa.example.com")
	both.Set("X-Forwarded-Proto", "https")
	both.Set("Referer", "https://ignored.example.org/management.html")
	if got := originFromHeaders(both); got != "https://cpa.example.com" {
		t.Fatalf("forwarded host must win over referer, got %q", got)
	}

	// 3) Referer 是相对值 / 非法值时不能瞎猜，返回空让上层继续回落。
	badReferer := http.Header{}
	badReferer.Set("Referer", "/management.html")
	if got := originFromHeaders(badReferer); got != "" {
		t.Fatalf("relative referer must yield empty origin, got %q", got)
	}

	// 4) 完全没有主机线索时返回空。
	if got := originFromHeaders(http.Header{}); got != "" {
		t.Fatalf("empty headers must yield empty origin, got %q", got)
	}
}

// TestHandleLoginOriginWritesCache 校验 origin 上报接口的成功与失败分支。
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestHandleLoginOriginWritesCache(t *testing.T) {
	resetPublicOriginForTest()
	t.Cleanup(resetPublicOriginForTest)

	empty := handleLoginOrigin(rpcManagementRequest{})
	if empty.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing origin, got %d", empty.StatusCode)
	}

	invalid := handleLoginOrigin(rpcManagementRequest{ManagementRequest: pluginapi.ManagementRequest{Query: url.Values{"origin": {"cpa.example.com"}}}})
	if invalid.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for relative origin, got %d", invalid.StatusCode)
	}

	ok := handleLoginOrigin(rpcManagementRequest{ManagementRequest: pluginapi.ManagementRequest{Query: url.Values{"origin": {"https://cpa.example.com"}}}})
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for valid origin, got %d body=%s", ok.StatusCode, string(ok.Body))
	}
	if got := currentPublicOrigin(); got != "https://cpa.example.com" {
		t.Fatalf("origin cache not updated, got %q", got)
	}
}
