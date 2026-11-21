package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 本文件覆盖续期换发判据与 Cookie 合并语义（TASK-01）。
//
// 为什么单独成文件：这两条判据是 2026-11-21 生产复盘的核心修复点——
// 旧实现按「有没有下发 Cookie」判成功、按「新建容器」存结果，导致埋点
// Cookie 一到就把会话主项抹掉，面板「最近续期」一路前进而会话早已失效。
// 用例全部走 httptest，不依赖真实账号与网络。

// TestRotatedSessionCookies_IgnoresTrackingCookies 验证只有埋点 Cookie 时不算换发。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，埋点 Cookie 不得判成功。
func TestRotatedSessionCookies_IgnoresTrackingCookies(t *testing.T) {
	prev := "sessionid=old; sid_guard=guard-old"
	fresh := []*http.Cookie{
		{Name: "ttwid", Value: "t-1", Path: "/"},
		{Name: "s_v_web_id", Value: "v-1", Path: "/"},
		{Name: "Hm_lvt_x", Value: "1", Path: "/"},
	}
	if rotatedSessionCookies(prev, fresh) {
		t.Fatal("只下发埋点 Cookie 时必须判为未换发")
	}
}

// TestRotatedSessionCookies_DetectsNewSessionCookie 验证会话主项变化即为换发。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，换发正例。
func TestRotatedSessionCookies_DetectsNewSessionCookie(t *testing.T) {
	prev := "sessionid=old; sid_guard=guard-old"
	fresh := []*http.Cookie{{Name: "sessionid", Value: "new", Path: "/"}}
	if !rotatedSessionCookies(prev, fresh) {
		t.Fatal("会话主项值变化时必须判为已换发")
	}
}

// TestRotatedSessionCookies_SameValueIsNotRotation 验证同名同值不算换发。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，重复下发旧值不得刷新续期时间。
func TestRotatedSessionCookies_SameValueIsNotRotation(t *testing.T) {
	prev := "sessionid=old"
	fresh := []*http.Cookie{{Name: "sessionid", Value: "old", Path: "/"}}
	if rotatedSessionCookies(prev, fresh) {
		t.Fatal("同名同值必须判为未换发，否则 LastRenewAt 会空转前进")
	}
}

// TestRotatedSessionCookies_CaseInsensitiveName 验证 Cookie 名大小写不敏感。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，上游大小写不固定。
func TestRotatedSessionCookies_CaseInsensitiveName(t *testing.T) {
	prev := "sessionid=old"
	fresh := []*http.Cookie{{Name: "SessionID", Value: "new", Path: "/"}}
	if !rotatedSessionCookies(prev, fresh) {
		t.Fatal("Cookie 名应大小写不敏感比对")
	}
}

// TestMergeSessionCookies_KeepsSessionOnTrackingOnly 验证埋点下发不会抹掉会话主项。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，回归「整份替换丢 sessionid」缺陷。
func TestMergeSessionCookies_KeepsSessionOnTrackingOnly(t *testing.T) {
	store := mergeSessionCookies("sessionid=keep-me; sid_guard=guard-keep", []*http.Cookie{
		{Name: "ttwid", Value: "t-1", Path: "/"},
		{Name: "s_v_web_id", Value: "v-1", Path: "/"},
	})
	header := store.header()
	if !strings.Contains(header, "sessionid=keep-me") {
		t.Fatalf("原有会话主项必须保留，实际 %q", header)
	}
	if !strings.Contains(header, "sid_guard=guard-keep") {
		t.Fatalf("原有 sid_guard 必须保留，实际 %q", header)
	}
	if !strings.Contains(header, "ttwid=t-1") {
		t.Fatalf("本轮下发的埋点项必须补齐，实际 %q", header)
	}
}

// TestMergeSessionCookies_NewValueOverridesOld 验证会话主项被新值覆盖。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，换发后必须落新值。
func TestMergeSessionCookies_NewValueOverridesOld(t *testing.T) {
	store := mergeSessionCookies("sessionid=old; odin_tt=keep", []*http.Cookie{
		{Name: "sessionid", Value: "new", Path: "/"},
	})
	header := store.header()
	if strings.Contains(header, "sessionid=old") {
		t.Fatalf("旧会话主项必须被覆盖，实际 %q", header)
	}
	if !strings.Contains(header, "sessionid=new") {
		t.Fatalf("新会话主项必须写入，实际 %q", header)
	}
	if !strings.Contains(header, "odin_tt=keep") {
		t.Fatalf("未下发的既有项必须保留，实际 %q", header)
	}
}

// TestRenewSession_TrackingOnlyIsNotRenewed 验证死会话只下发埋点时不判续期成功。
//
// 这是本轮最关键的一条：旧实现会用这 3 条埋点 Cookie 新建容器、标
// Renewed=true 并刷新 LastRenewAt，于是面板「最近续期」一路前进而会话
// 其实早已被上游吊销。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，生产复盘核心回归用例。
func TestRenewSession_TrackingOnlyIsNotRenewed(t *testing.T) {
	token := &tokenBeatStub{
		t:           t,
		issuedToken: "renewed-token",
		cookieNames: []string{"ttwid", "s_v_web_id", "biz_trace_id"},
	}
	cleanup := setupAuthStubs(t, token, &accountInfoStub{valid: true, secUserID: "sec-user"})
	defer cleanup()

	res, errRenew := renewSession(&StoredAuth{CookieHeader: "sessionid=dead; sid_guard=dead"})
	if errRenew != nil {
		t.Fatalf("埋点响应不应报错，实际 %v", errRenew)
	}
	if res == nil {
		t.Fatal("必须返回结果对象，供调用方读取未换发说明")
	}
	if res.Renewed {
		t.Fatalf("只下发埋点 Cookie 时不得标记为已换发，实际 %+v", res)
	}
	if !strings.Contains(res.Note, "未换到会话主 Cookie") && !strings.Contains(res.Note, "未下发会话 Cookie") {
		t.Fatalf("说明应指出未换到会话主项，实际 %q", res.Note)
	}
	if !strings.Contains(res.Note, "无会话主项") && !strings.Contains(res.Note, "0 条 Cookie") {
		t.Fatalf("说明应保留下发条数供诊断，实际 %q", res.Note)
	}
	if strings.Contains(res.CookieHeader, "sessionid") {
		t.Fatalf("未换发时不得给出主凭据 Cookie 头，实际 %q", res.CookieHeader)
	}
}

// TestRenewSession_KeepsOldCookiesOnRotation 验证真实换发时会话叠加而非替换。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，回归「整份替换丢旧 Cookie」缺陷。
func TestRenewSession_KeepsOldCookiesOnRotation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-tt-token", "step2-token")
		if r.Header.Get("x-tt-token") != "" {
			http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "rotated", Path: "/"})
		}
		_, _ = w.Write([]byte(`{"message":"success","data":{"error_code":0}}`))
	}))
	defer server.Close()
	restore := swapEndpoints(server.URL, "")
	defer restore()

	res, errRenew := renewSession(&StoredAuth{CookieHeader: "sessionid=old; odin_tt=keep-me; sid_guard=guard"})
	if errRenew != nil {
		t.Fatalf("续期应成功，实际失败：%v", errRenew)
	}
	if res == nil || !res.Renewed {
		t.Fatalf("会话主项被替换时必须标记已换发，实际 %+v", res)
	}
	if !strings.Contains(res.CookieHeader, "sessionid=rotated") {
		t.Fatalf("应写入新会话主项，实际 %q", res.CookieHeader)
	}
	if !strings.Contains(res.CookieHeader, "odin_tt=keep-me") {
		t.Fatalf("既有 Cookie 不得丢失，实际 %q", res.CookieHeader)
	}
	if !strings.Contains(res.CookieHeader, "sid_guard=guard") {
		t.Fatalf("既有会话项不得丢失，实际 %q", res.CookieHeader)
	}
}

// TestUnattemptedNote_DistinguishesZeroFromTracking 验证说明文案能区分两种未换发。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，上游无下发与只给埋点必须可区分。
func TestUnattemptedNote_DistinguishesZeroFromTracking(t *testing.T) {
	zero := unattemptedNote("上游未返回令牌", 0)
	if !strings.Contains(zero, "0 条 Cookie") {
		t.Fatalf("零下发应写明条数 0，实际 %q", zero)
	}
	tracking := unattemptedNote("上游未返回令牌", 9)
	if !strings.Contains(tracking, "9 条 Cookie") || !strings.Contains(tracking, "无会话主项") {
		t.Fatalf("埋点下发应写明条数与缺少主项，实际 %q", tracking)
	}
}
