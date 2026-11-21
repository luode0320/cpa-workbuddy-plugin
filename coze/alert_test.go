package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// 本文件覆盖账号状态迁移告警（TASK-05）。
//
// 核心不是「能不能发出去」，而是「会不会重复发」：续期每 4 小时一轮，
// 一个死号会被反复判失效。若每轮都推，告警通道当天就没人看了。

// alertSink 是接收 webhook 的测试服务。
type alertSink struct {
	mu       sync.Mutex
	payloads []renewAlert
	status   int
}

// newAlertSink 起一个 httptest 服务并把告警地址指过去。
//
// [参数] t: 测试上下文。
// [返回] 已装配的接收器。
// 最近修改时间：2026-11-21；改动原因：新增，webhook 用例共用前置。
func newAlertSink(t *testing.T) *alertSink {
	t.Helper()
	sink := &alertSink{status: http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body renewAlert
		_ = json.NewDecoder(r.Body).Decode(&body)
		sink.mu.Lock()
		sink.payloads = append(sink.payloads, body)
		status := sink.status
		sink.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	stubAlertEndpoint(t, server.URL)
	return sink
}

// stubAlertEndpoint 临时替换告警地址与去重表。
//
// [参数] t: 测试上下文；endpoint: 替换后的地址。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，用例隔离。
func stubAlertEndpoint(t *testing.T, endpoint string) {
	t.Helper()
	prevEndpoint := alertEndpointFn
	prevNow := alertNowFn
	alertEndpointFn = func() string { return endpoint }
	// 假时钟刻意钉在 UTC 而不是 time.Local：断言里的时间戳字符串是写死的，
	// 用 time.Local 会在 CI（UTC 容器）与开发机（东八区）产出两种时区后缀，
	// 令同一条用例在两处得到相反结论（2026-11-21 CI 首跑实测 FAIL）。
	alertNowFn = func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) }
	resetAlertState()
	t.Cleanup(func() {
		alertEndpointFn = prevEndpoint
		alertNowFn = prevNow
		resetAlertState()
	})
}

// count 返回已收到的告警条数。
// [参数] 无。
// [返回] 条数。
// 最近修改时间：2026-11-21；改动原因：新增，断言辅助。
func (s *alertSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.payloads)
}

// last 返回最后一条告警。
// [参数] t: 测试上下文。
// [返回] 最后一条告警正文。
// 最近修改时间：2026-11-21；改动原因：新增，断言辅助。
func (s *alertSink) last(t *testing.T) renewAlert {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.payloads) == 0 {
		t.Fatal("未收到任何告警")
	}
	return s.payloads[len(s.payloads)-1]
}

// TestNotifyAuthStateChange_InvalidOnce 验证失效只推一条。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，去重主行为。
func TestNotifyAuthStateChange_InvalidOnce(t *testing.T) {
	sink := newAlertSink(t)
	sa := &StoredAuth{AuthID: "coze-1", Label: "1 号", Mobile: "17012345678"}

	notifyAuthStateChange(false, true, sa)
	notifyAuthStateChange(true, true, sa)
	notifyAuthStateChange(false, true, sa)

	if sink.count() != 1 {
		t.Fatalf("连续失效只应推送 1 条，实际 %d 条", sink.count())
	}
	got := sink.last(t)
	if got.Event != alertEventInvalid {
		t.Fatalf("事件类型 = %q，期望 %q", got.Event, alertEventInvalid)
	}
	if got.AuthID != "coze-1" {
		t.Fatalf("auth_id = %q，期望 coze-1", got.AuthID)
	}
	if got.Mobile != "170****5678" {
		t.Fatalf("手机号必须遮蔽，实际 %q", got.Mobile)
	}
	if got.At != "2026-11-21T12:00:00Z" {
		t.Fatalf("时间戳 = %q，期望固定假时钟值", got.At)
	}
}

// TestNotifyAuthStateChange_RecoveredOnce 验证恢复只推一条。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，恢复分支。
func TestNotifyAuthStateChange_RecoveredOnce(t *testing.T) {
	sink := newAlertSink(t)
	sa := &StoredAuth{AuthID: "coze-2"}

	notifyAuthStateChange(false, true, sa)
	notifyAuthStateChange(true, false, sa)
	notifyAuthStateChange(false, false, sa)

	if sink.count() != 2 {
		t.Fatalf("失效与恢复各 1 条，实际 %d 条", sink.count())
	}
	if got := sink.last(t).Event; got != alertEventRecovered {
		t.Fatalf("事件类型 = %q，期望 %q", got, alertEventRecovered)
	}
}

// TestNotifyAuthStateChange_NoMigrationNoPush 验证无迁移不发告警。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，避免正常续期产生噪音。
func TestNotifyAuthStateChange_NoMigrationNoPush(t *testing.T) {
	sink := newAlertSink(t)
	sa := &StoredAuth{AuthID: "coze-3"}

	notifyAuthStateChange(false, false, sa)
	notifyAuthStateChange(true, true, sa)

	if sink.count() != 0 {
		t.Fatalf("状态未迁移不应推送，实际 %d 条", sink.count())
	}
}

// TestNotifyAuthStateChange_NoEndpoint 验证未配地址时不发请求。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，空配置分支。
func TestNotifyAuthStateChange_NoEndpoint(t *testing.T) {
	stubAlertEndpoint(t, "")
	notifyAuthStateChange(false, true, &StoredAuth{AuthID: "coze-4"})

	// 空地址时去重表仍要记下来：后续补配 webhook 不会把旧失效重推一遍。
	alertSentMu.Lock()
	recorded, ok := alertSent["coze-4"]
	alertSentMu.Unlock()
	if !ok || !recorded {
		t.Fatalf("空地址也必须记入去重表，实际 recorded=%v ok=%v", recorded, ok)
	}
}

// TestNotifyAuthStateChange_WebhookFailureDoesNotPanic 验证推送失败只记日志。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，旁路动作不得影响主流程。
func TestNotifyAuthStateChange_WebhookFailureDoesNotPanic(t *testing.T) {
	sink := newAlertSink(t)
	sink.mu.Lock()
	sink.status = http.StatusInternalServerError
	sink.mu.Unlock()

	notifyAuthStateChange(false, true, &StoredAuth{AuthID: "coze-5"})
	if sink.count() != 1 {
		t.Fatalf("失败也要记录本次投递，实际 %d 条", sink.count())
	}
	// 失败后不重试：同一状态再次进入也不该补推。
	notifyAuthStateChange(true, true, &StoredAuth{AuthID: "coze-5"})
	if sink.count() != 1 {
		t.Fatalf("推送失败不得重试，实际 %d 条", sink.count())
	}
}

// TestApplyRenewal_PushesRecoveredAlert 验证续期成功会推恢复告警。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，接线正确性。
func TestApplyRenewal_PushesRecoveredAlert(t *testing.T) {
	stubRenewCanWrite(t, true)
	sink := newAlertSink(t)
	sa, err := newWritableAuth(t)
	if err != nil {
		t.Fatalf("准备账号失败：%v", err)
	}
	sa.AuthInvalid = true
	alertSent["coze-guard"] = true

	if errApply := applyRenewal(sa, &renewResult{Renewed: true, CookieHeader: "sessionid=fresh"}); errApply != nil {
		t.Fatalf("续期落盘失败：%v", errApply)
	}
	if sink.count() != 1 {
		t.Fatalf("失效→可用应推送 1 条，实际 %d 条", sink.count())
	}
	if got := sink.last(t).Event; got != alertEventRecovered {
		t.Fatalf("事件类型 = %q，期望 %q", got, alertEventRecovered)
	}
}

// TestMarkSessionInvalid_PushesInvalidAlert 验证标记失效会推失效告警。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，接线正确性。
func TestMarkSessionInvalid_PushesInvalidAlert(t *testing.T) {
	stubRenewCanWrite(t, true)
	sink := newAlertSink(t)
	sa, err := newWritableAuth(t)
	if err != nil {
		t.Fatalf("准备账号失败：%v", err)
	}

	markSessionInvalid(sa, "续期失败（第一步）：会话过期，请重新登录")
	markSessionInvalid(sa, "续期失败（第一步）：会话过期，请重新登录")

	if sink.count() != 1 {
		t.Fatalf("连续标记失效只推 1 条，实际 %d 条", sink.count())
	}
	if got := sink.last(t).Event; got != alertEventInvalid {
		t.Fatalf("事件类型 = %q，期望 %q", got, alertEventInvalid)
	}
}
