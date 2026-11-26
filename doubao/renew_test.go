package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// renewBeatStub 模拟豆包令牌端点的两种调用形态。
type renewBeatStub struct {
	// issuedToken 是带 Cookie 调用时返回的令牌。
	issuedToken string
	// cookieNames 是带令牌调用时下发的 Cookie 名。
	cookieNames []string
	// reject 为真时返回会话过期。
	reject bool
	// calls 记录每次调用的入参形态。
	calls []renewBeatCall
}

// renewBeatCall 记录一次令牌端点调用的入参形态。
type renewBeatCall struct {
	HasToken  bool
	HasCookie bool
}

// ServeHTTP 实现令牌端点桩。
// [参数] w: 响应写入器；r: 请求。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，续期用例无外网依赖。
func (s *renewBeatStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hasToken := r.Header.Get("x-tt-token") != ""
	hasCookie := r.Header.Get("Cookie") != ""
	s.calls = append(s.calls, renewBeatCall{HasToken: hasToken, HasCookie: hasCookie})
	w.Header().Set("Content-Type", "application/json")
	if s.reject {
		_, _ = w.Write([]byte(`{"message":"error","data":{"error_code":401,"error_name":"session_expired","description":"会话过期，请重新登录"}}`))
		return
	}
	if hasCookie && !hasToken {
		w.Header().Set("x-tt-token", s.issuedToken)
		_, _ = w.Write([]byte(`{"message":"success","data":{"error_code":0}}`))
		return
	}
	if hasToken {
		for _, name := range s.cookieNames {
			http.SetCookie(w, &http.Cookie{Name: name, Value: "fresh-" + name, Path: "/"})
		}
		_, _ = w.Write([]byte(`{"message":"success","data":{"error_code":0}}`))
		return
	}
	_, _ = w.Write([]byte(`{"message":"error","data":{"error_code":401,"description":"缺少凭据"}}`))
}

// TestRotatedSessionCookiesRequiresValueChange 校验换发判据锚定会话主项值变化。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，防止埋点 Cookie 被误判为换发。
func TestRotatedSessionCookiesRequiresValueChange(t *testing.T) {
	prev := "flow_cur_user_sec_id=sec; sessionid=old_value; ttwid=tw"
	if rotatedSessionCookies(prev, []*http.Cookie{{Name: "ttwid", Value: "new_tw", Path: "/"}}) {
		t.Fatal("只有埋点 Cookie 变化不应判为换发")
	}
	if rotatedSessionCookies(prev, []*http.Cookie{{Name: "sessionid", Value: "old_value", Path: "/"}}) {
		t.Fatal("会话主项值未变不应判为换发")
	}
	if !rotatedSessionCookies(prev, []*http.Cookie{{Name: "sid_guard", Value: "new_guard", Path: "/"}}) {
		t.Fatal("会话主项值变化应判为换发")
	}
}

// TestMergeSessionCookiesKeepsOldSession 校验合并保留未被覆盖的旧会话项。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，防止整份替换丢会话主项。
func TestMergeSessionCookiesKeepsOldSession(t *testing.T) {
	prev := "sessionid=old_sid; sid_guard=old_guard; flow_cur_user_sec_id=sec"
	_, header := mergeSessionCookies(prev, []*http.Cookie{
		{Name: "sessionid", Value: "new_sid", Path: "/"},
		{Name: "ttwid", Value: "new_tw", Path: "/"},
	})
	if !strings.Contains(header, "sessionid=new_sid") {
		t.Fatalf("会话主项应被新值覆盖，实际 %q", header)
	}
	if !strings.Contains(header, "sid_guard=old_guard") {
		t.Fatalf("未下发的会话主项应保留，实际 %q", header)
	}
	if !strings.Contains(header, "flow_cur_user_sec_id=sec") {
		t.Fatalf("判据字段应保留，实际 %q", header)
	}
}

// TestRenewSessionTwoStep 校验双向换发主链路。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，覆盖 Cookie→token→Cookie。
func TestRenewSessionTwoStep(t *testing.T) {
	stub := &renewBeatStub{
		issuedToken: "issued-token",
		cookieNames: []string{"sessionid", "sid_guard"},
	}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	restore := swapTokenBeatEndpoint(srv.URL)
	defer restore()

	sa := &StoredAuth{CookieHeader: "sessionid=old_sid; sid_guard=old_guard"}
	res, errRenew := renewSession(sa)
	if errRenew != nil {
		t.Fatalf("双向换发应成功：%v", errRenew)
	}
	if !res.Renewed {
		t.Fatalf("应判定换发成功，实际 %q", res.Note)
	}
	if len(stub.calls) != 2 || !stub.calls[0].HasCookie || stub.calls[0].HasToken || !stub.calls[1].HasToken || stub.calls[1].HasCookie {
		t.Fatalf("应为带 Cookie 换 token 再带 token 换 Cookie，实际 %+v", stub.calls)
	}
	if !strings.Contains(res.CookieHeader, "sessionid=fresh-sessionid") {
		t.Fatalf("换发后应含新会话主项，实际 %q", res.CookieHeader)
	}
}

// TestRenewSessionDeadSessionNotRenewed 校验死会话不被误判为换发成功。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，防止埋点 Cookie 掩盖会话失效。
func TestRenewSessionDeadSessionNotRenewed(t *testing.T) {
	stub := &renewBeatStub{
		issuedToken: "issued-token",
		cookieNames: []string{"ttwid", "s_v_web_id"},
	}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	restore := swapTokenBeatEndpoint(srv.URL)
	defer restore()

	sa := &StoredAuth{CookieHeader: "sessionid=old_sid; sid_guard=old_guard"}
	res, errRenew := renewSession(sa)
	if errRenew != nil {
		t.Fatalf("换发调用本身不应报错：%v", errRenew)
	}
	if res.Renewed {
		t.Fatal("只下发埋点 Cookie 不应判为换发成功")
	}
}

// TestRenewSessionRejectsExpired 校验会话过期被识别为错误。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，失效账号不记成功。
func TestRenewSessionRejectsExpired(t *testing.T) {
	stub := &renewBeatStub{reject: true}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	restore := swapTokenBeatEndpoint(srv.URL)
	defer restore()

	_, errRenew := renewSession(&StoredAuth{CookieHeader: "sessionid=dead"})
	if errRenew == nil {
		t.Fatal("会话过期必须报错")
	}
	if !isSessionExpiredError(errRenew.Error()) {
		t.Fatalf("错误应被判为会话失效，实际 %q", errRenew.Error())
	}
}

// TestIsSessionExpiredError 校验失效判据只认上游明确文案。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，瞬态失败不得误标失效。
func TestIsSessionExpiredError(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
	}{
		{"续期失败（第一步）：会话过期，请重新登录", true},
		{"login invalid", true},
		{"session_expired", true},
		{"710012001", true},
		{"网络超时", false},
		{"HTTP 500", false},
	}
	for _, c := range cases {
		if got := isSessionExpiredError(c.reason); got != c.want {
			t.Errorf("%q：期望 %v，实际 %v", c.reason, c.want, got)
		}
	}
}
