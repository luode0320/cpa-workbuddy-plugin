package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestRememberSessionAuthRoundTrip 校验粘性表的写入、读取与清空。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 00:30:00；改动原因：新增，粘性表基本读写。
func TestRememberSessionAuthRoundTrip(t *testing.T) {
	resetSessionSticky()
	t.Cleanup(resetSessionSticky)

	if got := lookupSessionAuth(""); got != "" {
		t.Errorf("空会话 id 不该命中，实际 %q", got)
	}
	if got := lookupSessionAuth("conv-missing"); got != "" {
		t.Errorf("未登记的会话不该命中，实际 %q", got)
	}

	rememberSessionAuth("conv-1", "doubao-aaa")
	if got := lookupSessionAuth("conv-1"); got != "doubao-aaa" {
		t.Errorf("应查到 doubao-aaa，实际 %q", got)
	}

	// 空值不入表：上游偶发返回空会话 id 时不能污染表。
	rememberSessionAuth("", "doubao-aaa")
	rememberSessionAuth("conv-2", "")
	if got := lookupSessionAuth("conv-2"); got != "" {
		t.Errorf("空账号不该入表，实际 %q", got)
	}
}

// TestLookupSessionAuthDropsExpired 校验过期绑定不再命中。
//
// TTL 到点必须失效，否则上游会话过期后仍会把请求钉在一个旧账号上，
// 结果只能是每次续接都超时。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 00:30:00；改动原因：新增，TTL 到期回收。
func TestLookupSessionAuthDropsExpired(t *testing.T) {
	resetSessionSticky()
	t.Cleanup(resetSessionSticky)

	original := sessionStickyTTL
	sessionStickyTTL = -time.Second
	t.Cleanup(func() { sessionStickyTTL = original })

	rememberSessionAuth("conv-expired", "doubao-aaa")
	if got := lookupSessionAuth("conv-expired"); got != "" {
		t.Errorf("已过期的绑定不该命中，实际 %q", got)
	}
}

// TestApplySessionStickyKeepsFirstAccountForNewSession 校验新会话不做粘性修正。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 00:30:00；改动原因：新增，新会话放行。
func TestApplySessionStickyKeepsFirstAccountForNewSession(t *testing.T) {
	resetSessionSticky()
	t.Cleanup(resetSessionSticky)

	// 表里明明绑了 bbb，但新会话（resume 为零值）不该受影响。
	rememberSessionAuth("conv-ignored", "doubao-bbb")

	first := &StoredAuth{AuthID: "doubao-aaa"}
	got, errSticky := applySessionSticky(first, resumePoint{})
	if errSticky != nil {
		t.Fatalf("新会话不该报错：%v", errSticky)
	}
	if got == nil || got.AuthID != "doubao-aaa" {
		t.Errorf("新会话应保留首选账号 doubao-aaa，实际 %+v", got)
	}
}

// TestApplySessionStickyRebindsToFirstAccount 校验续接请求被钉回首轮账号。
//
// 这是本轮的核心行为：宿主轮转可能把确认轮分到别的账号，粘性必须把它拉回来。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 00:30:00；改动原因：新增，续接回钉。
func TestApplySessionStickyRebindsToFirstAccount(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })
	resetSessionSticky()
	t.Cleanup(resetSessionSticky)

	writeFailoverAccount(t, dir, "doubao-aaa", false)
	writeFailoverAccount(t, dir, "doubao-bbb", false)
	rememberSessionAuth("conv-sticky", "doubao-aaa")

	// 宿主把确认轮分给了 bbb。
	hostPicked := &StoredAuth{AuthID: "doubao-bbb", CookieHeader: validCookieHeader}
	got, errSticky := applySessionSticky(hostPicked, resumePoint{ConversationID: "conv-sticky"})
	if errSticky != nil {
		t.Fatalf("粘性修正失败：%v", errSticky)
	}
	if got == nil || got.AuthID != "doubao-aaa" {
		t.Fatalf("续接请求应被拉回首轮账号 doubao-aaa，实际 %+v", got)
	}
	// 取回的必须是本地池里的完整凭据，不是空壳：否则后续请求会因缺 cookie 失败。
	if got.CookieHeader == "" {
		t.Error("粘性修正后应带回完整凭据")
	}
}

// TestApplySessionStickyKeepsHostSnapshotWhenAlreadyBound 校验已在绑定账号时不重复查盘。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 00:30:00；改动原因：新增，命中即复用宿主快照。
func TestApplySessionStickyKeepsHostSnapshotWhenAlreadyBound(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })
	resetSessionSticky()
	t.Cleanup(resetSessionSticky)

	rememberSessionAuth("conv-same", "doubao-aaa")

	// 宿主给的就是绑定账号，且带快照：应原样返回这个指针（宿主快照比本地文件新）。
	hostPicked := &StoredAuth{AuthID: "doubao-aaa", CookieHeader: validCookieHeader, Label: "宿主快照"}
	got, errSticky := applySessionSticky(hostPicked, resumePoint{ConversationID: "conv-same"})
	if errSticky != nil {
		t.Fatalf("不该报错：%v", errSticky)
	}
	if got != hostPicked {
		t.Errorf("应原样返回宿主快照，实际 %+v", got)
	}
}

// TestApplySessionStickyRejectsMissingBoundAccount 校验绑定账号消失时明确报错。
//
// 不能悄悄回落到当前账号：那会让一次注定失败的续接打到健康账号上，
// 既拿不到结果，又把它记进冷却。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 00:30:00；改动原因：新增，绑定账号缺失即失败。
func TestApplySessionStickyRejectsMissingBoundAccount(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })
	resetSessionSticky()
	t.Cleanup(resetSessionSticky)

	writeFailoverAccount(t, dir, "doubao-bbb", false)
	rememberSessionAuth("conv-gone", "doubao-aaa")

	_, errSticky := applySessionSticky(&StoredAuth{AuthID: "doubao-bbb"}, resumePoint{ConversationID: "conv-gone"})
	if errSticky == nil {
		t.Fatal("绑定账号已不存在时应报错")
	}
	if !strings.Contains(errSticky.Error(), "已不存在或已停用") {
		t.Errorf("错误应说明账号已不可用，实际 %q", errSticky.Error())
	}
}

// TestRunWithFailoverBindsSessionOnFirstTurn 校验首轮成功后登记粘性。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 00:30:00；改动原因：新增，首轮写入绑定。
func TestRunWithFailoverBindsSessionOnFirstTurn(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })
	resetFailoverStates()
	t.Cleanup(resetFailoverStates)
	resetSessionSticky()
	t.Cleanup(resetSessionSticky)

	writeFailoverAccount(t, dir, "doubao-aaa", false)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: SSE_ACK\n" +
			`data: {"ack_client_meta":{"conversation_id":"conv-bind"}}` + "\n\n" +
			"event: STREAM_MSG_NOTIFY\n" +
			`data: {"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"参数确认"}}}]}}` + "\n\n" +
			"event: SSE_REPLY_END\ndata: {}\n\n"))
	}))
	restore := swapCompletionEndpoint(srv.URL)
	t.Cleanup(func() {
		restore()
		srv.Close()
	})

	first := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       "doubao-aaa",
		CookieHeader: validCookieHeader,
		BotID:        defaultBotID,
		DeviceID:     "7300000000000000001",
		WebID:        "7300000000000000002",
		WebTabID:     "00000000-0000-4000-8000-000000000001",
	}
	if _, _, errRun := runWithFailover(first, 5*time.Second, textMode(), "生成一段短视频", resumePoint{}, nil); errRun != nil {
		t.Fatalf("首轮应成功：%v", errRun)
	}
	if got := lookupSessionAuth("conv-bind"); got != "doubao-aaa" {
		t.Errorf("首轮成功后应把会话绑到 doubao-aaa，实际 %q", got)
	}
}

// TestRunWithFailoverNeverRotatesOnResume 校验续接请求失败时不换号。
//
// 会话绑在某账号上，换号后 conversation_id 在新账号上不存在：
// 换过去只会把一次注定失败的追问打成健康账号的失败记录。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 00:30:00；改动原因：新增，续接禁换号回归。
func TestRunWithFailoverNeverRotatesOnResume(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })
	resetFailoverStates()
	t.Cleanup(resetFailoverStates)
	resetSessionSticky()
	t.Cleanup(resetSessionSticky)

	writeFailoverAccount(t, dir, "doubao-aaa", false)
	writeFailoverAccount(t, dir, "doubao-bbb", false)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":710022004,"message":"rate limited"}`))
	}))
	restore := swapCompletionEndpoint(srv.URL)
	t.Cleanup(func() {
		restore()
		srv.Close()
	})

	first := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       "doubao-aaa",
		CookieHeader: validCookieHeader,
		BotID:        defaultBotID,
		DeviceID:     "7300000000000000001",
		WebID:        "7300000000000000002",
		WebTabID:     "00000000-0000-4000-8000-000000000001",
	}
	_, _, errRun := runWithFailover(first, 5*time.Second, textMode(), "确认，开始生成",
		resumePoint{ConversationID: "conv-resume", SectionID: "sec-1", MessageIndex: 1}, nil)
	if errRun == nil {
		t.Fatal("429 应原样返回失败")
	}
	// 429 本来是「立刻换号」的信号，但续接请求必须放弃换号。
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("续接请求不该换号，实际请求 %d 次", got)
	}
	if !isAccountCoolingDown("doubao-aaa") {
		t.Error("失败的账号仍应记账冷却")
	}
	if isAccountCoolingDown("doubao-bbb") {
		t.Error("续接请求不该波及另一个账号")
	}
}

// TestExecutorStickyBindsConfirmToFirstAccount 校验确认轮被钉回首轮账号。
//
// 端到端回归：宿主在确认轮把 auth_id 换成了 bbb，粘性表必须把它拉回 aaa。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 00:30:00；改动原因：新增，decode 入口粘性回归。
func TestExecutorStickyBindsConfirmToFirstAccount(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })
	resetSessionSticky()
	t.Cleanup(resetSessionSticky)

	writeFailoverAccount(t, dir, "doubao-aaa", false)
	writeFailoverAccount(t, dir, "doubao-bbb", false)
	rememberSessionAuth("conv-e2e", "doubao-aaa")

	raw, _ := json.Marshal(map[string]any{
		"auth_id": "doubao-bbb",
		"payload": []byte(`{"model":"doubao/video","conversation_id":"conv-e2e","messages":[{"role":"user","content":"确认，开始生成"}]}`),
	})
	_, sa, _, errDecode := decodeExecutorRequest(raw)
	if errDecode != nil {
		t.Fatalf("解析失败：%v", errDecode)
	}
	if sa == nil || sa.AuthID != "doubao-aaa" {
		t.Errorf("确认轮应被拉回首轮账号 doubao-aaa，实际 %+v", sa)
	}
}

// 保持 pluginapi 导入被使用：本文件里 scheduler 相关用例共用该包。
var _ = pluginapi.ExecutorRequest{}
