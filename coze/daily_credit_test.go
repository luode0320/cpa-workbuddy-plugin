package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 本文件覆盖每日登录积分领取：端点契约、状态记录、跨日判定、
// 以及「先领后查」的顺序约束。

// dailyCreditStub 模拟每日积分 check 端点。
type dailyCreditStub struct {
	pass       bool
	denyReason int
	code       int
	// calls 记录被调用次数，用于确认幂等语义与调用次数。
	calls int
	// failStatus 非 0 时返回该 HTTP 状态码。
	failStatus int
}

// ServeHTTP 实现领取端点桩。
// [参数] w: 响应写入器；r: 请求。
// [返回] 无。
// 最近修改时间：2026-10-24；改动原因：新增，领取端点桩。
func (s *dailyCreditStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.calls++
	w.Header().Set("Content-Type", "application/json")
	if s.failStatus != 0 {
		w.WriteHeader(s.failStatus)
		return
	}
	if s.code != 0 {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": s.code, "message": "领取端点失败"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 0,
		"data": map[string]any{
			"pass":        s.pass,
			"deny_reason": s.denyReason,
		},
	})
}

// newDailyCreditTestAuth 构造一个带 Cookie 的测试账号。
// [参数] id: 账号标识。
// [返回] 账号。
func newDailyCreditTestAuth(id string) *StoredAuth {
	return &StoredAuth{AuthID: id, CookieHeader: "sessionid=test"}
}

// withDailyCreditEndpoint 把领取端点指向测试桩，返回恢复函数。
// [参数] url: 桩地址。
// [返回] 恢复函数。
func withDailyCreditEndpoint(url string) func() {
	prev := dailyCreditCheckOverride
	dailyCreditCheckOverride = url
	resetDailyCreditState()
	return func() {
		dailyCreditCheckOverride = prev
		resetDailyCreditState()
	}
}

// TestClaimDailyCredit_ParsesPass 验证 pass=true 被识别为已发放。
// [参数] t: 测试上下文。
// [返回] 无。
// TestClaimDailyCredit_RequestsEndpointOnce 验证一次领取只发一次请求。
//
// 领取链路自身不做重试：上游幂等但重试会放大风控暴露面，
// 失败交给下一个刷新周期自然重来。
// [参数] t: 测试上下文。
// [返回] 无。
func TestClaimDailyCredit_RequestsEndpointOnce(t *testing.T) {
	stub := &dailyCreditStub{pass: true}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	defer withDailyCreditEndpoint(srv.URL)()

	sa := newDailyCreditTestAuth("coze-pass")
	if !claimDailyCreditForAccount(sa) {
		t.Fatal("HTTP 与业务码正常时应返回请求成功")
	}
	if dailyCreditClaimedToday("coze-pass") {
		t.Fatal("仅调用请求不足以判定已领取，必须由明细确认")
	}
	if stub.calls != 1 {
		t.Fatalf("应调用端点 1 次，实际 %d", stub.calls)
	}
}

// TestClaimDailyCredit_RecordsPassHintOnly 验证 pass 字段只作为排查线索。
//
// 上游 pass 描述「余额是否够用」，与发放无关（实测 pass=false 同样到账），
// 因此它既不能决定 claimed 状态，也不能写进错误字段。
// [参数] t: 测试上下文。
// [返回] 无。
func TestClaimDailyCredit_RecordsPassHintOnly(t *testing.T) {
	stub := &dailyCreditStub{pass: false, denyReason: 1}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	defer withDailyCreditEndpoint(srv.URL)()

	sa := newDailyCreditTestAuth("coze-deny")
	if !claimDailyCreditForAccount(sa) {
		t.Fatal("pass=false 但 HTTP 与业务码正常时，请求本身应算成功")
	}
	entry, ok := dailyCreditSnapshot("coze-deny")
	if !ok {
		t.Fatal("应记录一次尝试")
	}
	if entry.errorText != "" {
		t.Fatalf("pass=false 不应记成错误，实际 %q", entry.errorText)
	}
	if entry.passHint {
		t.Fatal("pass=false 应如实记录为 false")
	}
	if dailyCreditClaimedToday("coze-deny") {
		t.Fatal("仅调用请求不足以判定已领取，必须由明细确认")
	}
}

// TestConfirmDailyCredit_MarksClaimedWhenTodayItemHasBalance 验证明细确认到账。
//
// 这是唯一可信的领取判据：明细里存在当天且仍有余额的「每日登录」条目。
// [参数] t: 测试上下文。
// [返回] 无。
func TestConfirmDailyCredit_MarksClaimedWhenTodayItemHasBalance(t *testing.T) {
	stub := &dailyCreditStub{pass: false, denyReason: 1}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	defer withDailyCreditEndpoint(srv.URL)()

	sa := newDailyCreditTestAuth("coze-confirmed")
	claimDailyCreditForAccount(sa)

	summary := &creditSummary{Items: []creditItem{
		{Title: "每日登录", Remaining: 1500, ExpiredAt: todayEndCST()},
	}}
	confirmDailyCreditBySummary(sa, summary)

	if !dailyCreditClaimedToday("coze-confirmed") {
		t.Fatal("明细里当天有余额条目时必须判定为已领取")
	}
	entry, _ := dailyCreditSnapshot("coze-confirmed")
	if entry.amount != 1500 {
		t.Fatalf("应记录当日额度 1500，实际 %d", entry.amount)
	}
}

// TestConfirmDailyCredit_IgnoresZeroBalanceItem 验证已消耗条目不算今日额度。
//
// 历史条目 remain=0 表示当年那笔已被用完，拿它判「今日已领」会让
// 面板显示已领、闸门继续把账号当 0 分。
// [参数] t: 测试上下文。
// [返回] 无。
func TestConfirmDailyCredit_IgnoresZeroBalanceItem(t *testing.T) {
	stub := &dailyCreditStub{pass: true}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	defer withDailyCreditEndpoint(srv.URL)()

	sa := newDailyCreditTestAuth("coze-zero")
	claimDailyCreditForAccount(sa)

	summary := &creditSummary{Items: []creditItem{
		{Title: "每日登录", Remaining: 0, ExpiredAt: todayEndCST()},
	}}
	confirmDailyCreditBySummary(sa, summary)

	if dailyCreditClaimedToday("coze-zero") {
		t.Fatal("余额为 0 的条目不应判定为今日已领")
	}
}

// TestConfirmDailyCredit_IgnoresOtherDays 验证非当日条目不算。
// [参数] t: 测试上下文。
// [返回] 无。
func TestConfirmDailyCredit_IgnoresOtherDays(t *testing.T) {
	stub := &dailyCreditStub{pass: true}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	defer withDailyCreditEndpoint(srv.URL)()

	sa := newDailyCreditTestAuth("coze-otherday")
	claimDailyCreditForAccount(sa)

	yesterday := time.Now().In(cstZone).AddDate(0, 0, -1)
	summary := &creditSummary{Items: []creditItem{
		{Title: "每日登录", Remaining: 1500, ExpiredAt: midnightCST(yesterday)},
	}}
	confirmDailyCreditBySummary(sa, summary)

	if dailyCreditClaimedToday("coze-otherday") {
		t.Fatal("非当日的条目不应当作今日已领")
	}
}

// TestConfirmDailyCredit_SkippedWhenClaimFailed 验证请求失败时不做确认。
//
// 请求失败时明细里可能还留着前一天的条目，据此标记「今日已领」
// 会让面板与闸门一起误判。
// [参数] t: 测试上下文。
// [返回] 无。
func TestConfirmDailyCredit_SkippedWhenClaimFailed(t *testing.T) {
	stub := &dailyCreditStub{failStatus: http.StatusInternalServerError}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	defer withDailyCreditEndpoint(srv.URL)()

	sa := newDailyCreditTestAuth("coze-failedconfirm")
	claimDailyCreditForAccount(sa)

	summary := &creditSummary{Items: []creditItem{
		{Title: "每日登录", Remaining: 1500, ExpiredAt: todayEndCST()},
	}}
	confirmDailyCreditBySummary(sa, summary)

	if dailyCreditClaimedToday("coze-failedconfirm") {
		t.Fatal("请求失败时不应据明细标记为已领取")
	}
}

// todayEndCST 返回今天 23:59:59（北京时间），与上游每日额度到期口径一致。
// [参数] 无。
// [返回] RFC3339 时间串。
func todayEndCST() string {
	now := time.Now().In(cstZone)
	return time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, cstZone).Format(time.RFC3339)
}

// midnightCST 返回指定时刻所在自然日的零点（北京时间）。
// [参数] t: 目标时刻。
// [返回] RFC3339 时间串。
func midnightCST(t time.Time) string {
	y, m, d := t.In(cstZone).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, cstZone).Format(time.RFC3339)
}

// TestClaimDailyCredit_HTTPFailureRecorded 验证传输层失败被记录。
// [参数] t: 测试上下文。
// [返回] 无。
func TestClaimDailyCredit_HTTPFailureRecorded(t *testing.T) {
	stub := &dailyCreditStub{failStatus: http.StatusInternalServerError}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	defer withDailyCreditEndpoint(srv.URL)()

	sa := newDailyCreditTestAuth("coze-fail")
	if claimDailyCreditForAccount(sa) {
		t.Fatal("HTTP 500 应返回请求失败")
	}
	entry, _ := dailyCreditSnapshot("coze-fail")
	if entry.errorText == "" {
		t.Fatal("传输层失败必须记录错误文案")
	}
}

// TestClaimDailyCredit_BusinessErrorRecorded 验证 code!=0 被记录为错误。
// [参数] t: 测试上下文。
// [返回] 无。
func TestClaimDailyCredit_BusinessErrorRecorded(t *testing.T) {
	stub := &dailyCreditStub{code: 788770202}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	defer withDailyCreditEndpoint(srv.URL)()

	sa := newDailyCreditTestAuth("coze-biz")
	if claimDailyCreditForAccount(sa) {
		t.Fatal("code!=0 应返回请求失败")
	}
	entry, _ := dailyCreditSnapshot("coze-biz")
	if entry.errorText == "" {
		t.Fatal("业务错误必须记录错误文案")
	}
}

// TestClaimDailyCredit_EmptyKeySkipped 验证无法派生键的账号不发请求。
//
// 没有账号标识就无法记录状态，发请求等于白白消耗上游调用；
// 这种账号属于宿主注册异常，跳过并留给面板如实显示「未知」。
// [参数] t: 测试上下文。
// [返回] 无。
func TestClaimDailyCredit_EmptyKeySkipped(t *testing.T) {
	stub := &dailyCreditStub{pass: true}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	defer withDailyCreditEndpoint(srv.URL)()

	if claimDailyCreditForAccount(&StoredAuth{CookieHeader: "sessionid=test"}) {
		t.Fatal("无账号标识时不应判定为已领取")
	}
	if stub.calls != 0 {
		t.Fatalf("无账号标识时不应调用上游，实际调用 %d 次", stub.calls)
	}
}

// TestDailyCreditClaimedToday_CrossDayResets 验证跨自然日重置。
//
// 判据必须按北京时间切日：用 UTC 会把凌晨 8 点前的领取算到前一天，
// 面板会在每天早上误报「今天还没领」。
// [参数] t: 测试上下文。
// [返回] 无。
func TestDailyCreditClaimedToday_CrossDayResets(t *testing.T) {
	stub := &dailyCreditStub{pass: true}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	defer withDailyCreditEndpoint(srv.URL)()

	sa := newDailyCreditTestAuth("coze-day")
	claimDailyCreditForAccount(sa)

	// 直接把记录改成「昨天」，模拟跨日。
	dailyCredits.mu.Lock()
	entry := dailyCredits.entries["coze-day"]
	entry.claimedAt = time.Now().Add(-24 * time.Hour)
	dailyCredits.entries["coze-day"] = entry
	dailyCredits.mu.Unlock()

	if dailyCreditClaimedToday("coze-day") {
		t.Fatal("昨天的领取不应算作今天已领")
	}
}

// TestSameDayCST_UsesFixedZone 验证切日按 UTC+8 而不是 UTC。
//
// 北京时间 10-24 00:30 与 10-24 23:30 同属一天，但 UTC 下前者是 10-23；
// 若用 UTC 切日，同一天会被判成两天。
// [参数] t: 测试上下文。
// [返回] 无。
func TestSameDayCST_UsesFixedZone(t *testing.T) {
	early := time.Date(2026, 10, 24, 0, 30, 0, 0, cstZone)
	late := time.Date(2026, 10, 24, 23, 30, 0, 0, cstZone)
	if !sameDayCST(early, late) {
		t.Fatal("北京时间同一天应判定为同一自然日")
	}
	next := time.Date(2026, 10, 25, 0, 10, 0, 0, cstZone)
	if sameDayCST(late, next) {
		t.Fatal("跨日后应判定为不同自然日")
	}
}

// TestBuildDailyCreditView_UnknownBeforeFirstAttempt 验证从未尝试时返回 nil。
//
// 返回空结构会让面板把「还没跑过」显示成「今天没领」，
// 用户会误以为功能失效并去点补领按钮。
// [参数] t: 测试上下文。
// [返回] 无。
func TestBuildDailyCreditView_UnknownBeforeFirstAttempt(t *testing.T) {
	resetDailyCreditState()
	defer resetDailyCreditState()
	if view := buildDailyCreditView("coze-unknown"); view != nil {
		t.Fatalf("从未尝试时应返回 nil，实际 %+v", view)
	}
}
