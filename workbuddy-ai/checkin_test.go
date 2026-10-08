// checkin_test.go 覆盖 WorkBuddy AI 国际版签到链路：状态解析（snake_case /
// camelCase 双形态与端点回退）、领取调用、业务错误码映射（DEC-004）、
// 手动签到单账号/批量入口、字符串兜底与签到保活调度合并。全部通过
// httptest 本地桩 + 假 hostCall 完成，不触碰真实上游与宿主。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// withFakeHostCall 用内存桩替换签到链路的 host 读取接缝（hostAuthListFn /
// hostAuthGetFn），不触碰 hostCall 本身；HTTP 桥在 hostAPI 为 nil 时走
// 测试直连回退。
// [参数] t：测试上下文；files：账号列表；docs：auth_index → 凭据 JSON。
// [返回] 无（测试结束自动恢复原接缝）。
// 最近修改时间：2026-10-09；改动原因：国际版签到链路新建。
func withFakeHostCall(t *testing.T, files []pluginapi.HostAuthFileEntry, docs map[string][]byte) {
	t.Helper()
	prevList := hostAuthListFn
	prevGet := hostAuthGetFn
	hostAuthListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		return files, nil
	}
	hostAuthGetFn = func(authIndex string) (*storedAuth, error) {
		doc, ok := docs[authIndex]
		if !ok {
			return nil, fmt.Errorf("no auth for %s", authIndex)
		}
		return parseStored(doc)
	}
	t.Cleanup(func() {
		hostAuthListFn = prevList
		hostAuthGetFn = prevGet
	})
}

// testAuthDoc 构造一份最小可解析的凭据 JSON（嵌套 auth/account 形态）。
// [参数] uid：账号唯一标识；nickname：账号昵称。
// [返回] 可直接写入内存桩的凭据 JSON 字节。
// 最近修改时间：2026-10-09；改动原因：国际版签到链路新建。
func testAuthDoc(uid, nickname string) []byte {
	doc := map[string]any{
		"auth": map[string]any{
			"accessToken":  "tok-" + uid,
			"refreshToken": "ref-" + uid,
			"expiresAt":    int64(4102444800),
			"domain":       "www.workbuddy.ai",
		},
		"account": map[string]any{
			"uid":      uid,
			"nickname": nickname,
		},
	}
	raw, _ := json.Marshal(doc)
	return raw
}

// cleanupTestAccount 清理测试写入的全局缓存与锁，避免测试间串扰。
// [参数] t：测试上下文；authID：缓存键；authIndex：签到锁键。
// [返回] 无（测试结束自动清理）。
// 最近修改时间：2026-10-09；改动原因：国际版签到链路新建。
func cleanupTestAccount(t *testing.T, authID, authIndex string) {
	t.Helper()
	t.Cleanup(func() {
		accountCache.Delete(authID)
		checkinLocks.Delete(authIndex)
	})
}

// TestFetchCheckinStatus_SnakeCase: 验证 snake_case 状态响应全字段解析与主端点单次调用。
func TestFetchCheckinStatus_SnakeCase(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"active":true,"today_checked_in":false,"streak_days":5,"daily_credit":100,"today_credit":0,"total_credits":500,"week_checkin_days":3,"activity_name":"daily","season":2,"checkin_dates":["2026-10-01","2026-10-02"]}}`))
	}))
	defer srv.Close()
	restore := setBillingBase(srv.URL)
	defer restore()

	sa := &storedAuth{Auth: storedTokens{Domain: "www.workbuddy.ai"}}
	ci, err := fetchCheckinStatus(sa)
	if err != nil {
		t.Fatalf("fetchCheckinStatus: %v", err)
	}
	if !ci.Active || ci.TodayCheckedIn {
		t.Fatalf("active/today mismatch: %+v", ci)
	}
	if ci.StreakDays != 5 || ci.DailyCredit != 100 || ci.TotalCredits != 500 || ci.WeekCheckinDays != 3 || ci.Season != 2 {
		t.Fatalf("numeric fields mismatch: %+v", ci)
	}
	if ci.ActivityName != "daily" || len(ci.CheckinDates) != 2 {
		t.Fatalf("string fields mismatch: %+v", ci)
	}
	if len(paths) != 1 || paths[0] != "/v2/billing/meter/checkin-activity-status" {
		t.Fatalf("expected single call to primary endpoint, got %v", paths)
	}
}

// TestFetchCheckinStatus_CamelCaseAndFallback: 验证 camelCase 字段解析与主端点失败后自动回退备用端点。
func TestFetchCheckinStatus_CamelCaseAndFallback(t *testing.T) {
	var primaryCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v2/billing/meter/checkin-activity-status" {
			atomic.AddInt32(&primaryCalls, 1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":400,"msg":"bad request"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"Active":true,"todayCheckedIn":true,"streakDays":9}}`))
	}))
	defer srv.Close()
	restore := setBillingBase(srv.URL)
	defer restore()

	sa := &storedAuth{Auth: storedTokens{Domain: "www.workbuddy.ai"}}
	ci, err := fetchCheckinStatus(sa)
	if err != nil {
		t.Fatalf("fetchCheckinStatus fallback: %v", err)
	}
	if !ci.TodayCheckedIn || ci.StreakDays != 9 {
		t.Fatalf("camelCase fields mismatch: %+v", ci)
	}
	if atomic.LoadInt32(&primaryCalls) != 1 {
		t.Fatalf("expected 1 primary attempt, got %d", primaryCalls)
	}
}

// TestPerformCheckinCall_Success: 验证领取成功响应的 success 布尔化、credit/streak 解析与端点路径。
func TestPerformCheckinCall_Success(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"status":"claimed","credit":100,"streak_days":6,"is_streak_day":true}}`))
	}))
	defer srv.Close()
	restore := setBillingBase(srv.URL)
	defer restore()

	sa := &storedAuth{Auth: storedTokens{Domain: "www.workbuddy.ai"}}
	res, err := performCheckinCall(sa)
	if err != nil {
		t.Fatalf("performCheckinCall: %v", err)
	}
	if res["success"] != true {
		t.Fatalf("success = %v; want true", res["success"])
	}
	if jsonI64(res, "credit") != 100 || jsonI64(res, "streak_days") != 6 {
		t.Fatalf("credit/streak mismatch: %v", res)
	}
	if path != "/v2/billing/meter/daily-checkin" {
		t.Fatalf("path = %q; want /v2/billing/meter/daily-checkin", path)
	}
}

// TestPerformCheckinCall_ErrorCodeMapping: 验证业务错误码 1001/1002/1003 分别映射为 already/not_eligible/event_ended。
func TestPerformCheckinCall_ErrorCodeMapping(t *testing.T) {
	var failCode int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"code":%d,"msg":"business failure"}`, atomic.LoadInt32(&failCode))
	}))
	defer srv.Close()
	restore := setBillingBase(srv.URL)
	defer restore()

	sa := &storedAuth{Auth: storedTokens{Domain: "www.workbuddy.ai"}}
	cases := []struct {
		code int32
		want string
	}{
		{1001, "already"},
		{1002, "not_eligible"},
		{1003, "event_ended"},
	}
	for _, tc := range cases {
		atomic.StoreInt32(&failCode, tc.code)
		res, err := performCheckinCall(sa)
		if err != nil {
			t.Fatalf("code=%d: unexpected error: %v", tc.code, err)
		}
		if res["success"] != false {
			t.Fatalf("code=%d: success = %v; want false", tc.code, res["success"])
		}
		if reason, _ := res["reason"].(string); reason != tc.want {
			t.Fatalf("code=%d: reason = %q; want %q", tc.code, reason, tc.want)
		}
	}
}

// TestCheckinErrorResult_FallbackKeepsMessage: 验证未匹配错误码不设 reason 且保留原始 message。
func TestCheckinErrorResult_FallbackKeepsMessage(t *testing.T) {
	out := checkinErrorResult("code=9999 msg=other failure")
	if out["success"] != false {
		t.Fatalf("success = %v; want false", out["success"])
	}
	if _, hasReason := out["reason"]; hasReason {
		t.Fatalf("unmatched code must not set reason: %v", out)
	}
	if out["message"] != "code=9999 msg=other failure" {
		t.Fatalf("message = %v; want original text", out["message"])
	}
}

// TestHandleManualCheckin_AlreadyCheckedIn: 验证单账号已签到返回 skipped/already 并回写缓存。
func TestHandleManualCheckin_AlreadyCheckedIn(t *testing.T) {
	files := []pluginapi.HostAuthFileEntry{{ID: "id-1", AuthIndex: "idx-1", Name: "workbuddyai-test.json"}}
	docs := map[string][]byte{"idx-1": testAuthDoc("uid-1", "Nick1")}
	withFakeHostCall(t, files, docs)
	cleanupTestAccount(t, "id-1", "idx-1")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"active":true,"today_checked_in":true,"streak_days":4}}`))
	}))
	defer srv.Close()
	restore := setBillingBase(srv.URL)
	defer restore()

	resp := handleManualCheckin(pluginapi.ManagementRequest{Body: []byte(`{"auth_index":"idx-1"}`)})
	results, _ := resp["results"].([]map[string]any)
	if len(results) != 1 {
		t.Fatalf("results len = %d; want 1 (%v)", len(results), resp)
	}
	r0 := results[0]
	if r0["success"] != true || r0["skipped"] != true || r0["reason"] != "already" {
		t.Fatalf("result mismatch: %v", r0)
	}
	summary, _ := resp["summary"].(map[string]any)
	if summary["total"] != 1 || summary["already"] != 1 || summary["success"] != 0 {
		t.Fatalf("summary mismatch: %v", summary)
	}
	v, ok := accountCache.Load("id-1")
	if !ok {
		t.Fatal("expected cache entry after already-checked-in")
	}
	if e, ok2 := v.(*accountCacheEntry); !ok2 || e.checkin == nil || !e.checkin.TodayCheckedIn {
		t.Fatalf("cache checkin mismatch: %v", v)
	}
}

// TestHandleManualCheckin_ClaimSuccess: 验证单账号领取成功的结果、summary 与领取后状态刷新。
func TestHandleManualCheckin_ClaimSuccess(t *testing.T) {
	files := []pluginapi.HostAuthFileEntry{{ID: "id-2", AuthIndex: "idx-2", Name: "workbuddyai-test.json"}}
	docs := map[string][]byte{"idx-2": testAuthDoc("uid-2", "Nick2")}
	withFakeHostCall(t, files, docs)
	cleanupTestAccount(t, "id-2", "idx-2")

	var statusCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/billing/meter/daily-checkin":
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"status":"claimed","credit":100,"streak_days":3,"is_streak_day":true}}`))
		default:
			// 第一次探测：未签到；领取后的刷新：已签到。
			checked := atomic.AddInt32(&statusCalls, 1) > 1
			fmt.Fprintf(w, `{"code":0,"msg":"OK","data":{"active":true,"today_checked_in":%v,"streak_days":3}}`, checked)
		}
	}))
	defer srv.Close()
	restore := setBillingBase(srv.URL)
	defer restore()

	resp := handleManualCheckin(pluginapi.ManagementRequest{Body: []byte(`{"auth_index":"idx-2"}`)})
	results, _ := resp["results"].([]map[string]any)
	if len(results) != 1 {
		t.Fatalf("results len = %d; want 1 (%v)", len(results), resp)
	}
	r0 := results[0]
	if r0["success"] != true {
		t.Fatalf("result mismatch: %v", r0)
	}
	if jsonI64(r0, "credit") != 100 {
		t.Fatalf("credit mismatch: %v", r0)
	}
	summary, _ := resp["summary"].(map[string]any)
	if summary["success"] != 1 || summary["already"] != 0 || summary["fail"] != 0 {
		t.Fatalf("summary mismatch: %v", summary)
	}
	v, ok := accountCache.Load("id-2")
	if !ok {
		t.Fatal("expected cache entry after claim")
	}
	if e, ok2 := v.(*accountCacheEntry); !ok2 || e.checkin == nil || !e.checkin.TodayCheckedIn {
		t.Fatalf("post-call cache checkin mismatch: %v", v)
	}
}

// TestHandleManualCheckin_BatchAndStringFallback: 验证批量签到与无错误码字符串兜底归入 already。
func TestHandleManualCheckin_BatchAndStringFallback(t *testing.T) {
	files := []pluginapi.HostAuthFileEntry{
		{ID: "id-3", AuthIndex: "idx-3", Name: "workbuddyai-a.json"},
		{ID: "id-4", AuthIndex: "idx-4", Name: "workbuddyai-b.json"},
	}
	docs := map[string][]byte{
		"idx-3": testAuthDoc("uid-3", "Nick3"),
		"idx-4": testAuthDoc("uid-4", "Nick4"),
	}
	withFakeHostCall(t, files, docs)
	cleanupTestAccount(t, "id-3", "idx-3")
	cleanupTestAccount(t, "id-4", "idx-4")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/billing/meter/daily-checkin":
			if r.Header.Get("X-User-Id") == "uid-4" {
				// 无显式错误码，仅业务文案——触发 checkinOneAccount 的字符串兜底。
				_, _ = w.Write([]byte(`{"code":9999,"msg":"you have already checked in today"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"status":"claimed","credit":100,"streak_days":1}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"active":true,"today_checked_in":false,"streak_days":1}}`))
		}
	}))
	defer srv.Close()
	restore := setBillingBase(srv.URL)
	defer restore()

	resp := handleManualCheckin(pluginapi.ManagementRequest{Body: []byte(`{}`)})
	results, _ := resp["results"].([]map[string]any)
	if len(results) != 2 {
		t.Fatalf("results len = %d; want 2 (%v)", len(results), resp)
	}
	for _, r0 := range results {
		if r0["success"] != true {
			t.Fatalf("batch result not success: %v", r0)
		}
	}
	// uid-4 走字符串兜底：应标记为 already（skipped）而不是失败。
	var fallback map[string]any
	for _, r0 := range results {
		if r0["auth_index"] == "idx-4" {
			fallback = r0
		}
	}
	if fallback == nil || fallback["reason"] != "already" || fallback["skipped"] != true {
		t.Fatalf("string fallback mismatch: %v", fallback)
	}
	summary, _ := resp["summary"].(map[string]any)
	if summary["total"] != 2 || summary["success"] != 1 || summary["already"] != 1 || summary["fail"] != 0 {
		t.Fatalf("summary mismatch: %v", summary)
	}
}

// TestNextCheckinTime: 验证调度合并取签到与保活时段表中最早的触发时刻，
// 覆盖跨天顺延与合并后不丢保活时段；防 GAP-004 的调度器合并回归。
func TestNextCheckinTime(t *testing.T) {
	// 07:00 → 下一触发 08:00（08:00 同时是签到与保活时段）。
	morning := time.Date(2026, 7, 24, 7, 0, 0, 0, time.UTC)
	got := nextCheckinTime(morning)
	if got.Hour() != 8 {
		t.Fatalf("want 8, got %v", got)
	}
	// 10:00 → 下一触发 12:00。
	noon := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	got = nextCheckinTime(noon)
	if got.Hour() != 12 {
		t.Fatalf("want 12, got %v", got)
	}
	// 22:00 → 次日 00:00。
	late := time.Date(2026, 7, 24, 22, 0, 0, 0, time.UTC)
	got = nextCheckinTime(late)
	if got.Hour() != 0 || got.Day() != 25 {
		t.Fatalf("want next day 00:00, got %v", got)
	}
	// 保活时段被合并：临时把保活改为 01/13 后，00:30 的下一触发应为 01:00；
	// 若合并丢失则退化为 04:00，断言失败。
	prev := keepaliveHours
	keepaliveHours = []int{1, 13}
	merged := nextCheckinTime(time.Date(2026, 7, 24, 0, 30, 0, 0, time.UTC))
	keepaliveHours = prev
	if merged.Hour() != 1 {
		t.Fatalf("merged schedule: want 1, got %v", merged)
	}
	// 保活窗口判定保留：04:00 时段 [04:00, 05:00) 内的 04:30 命中。
	if !shouldRunKeepaliveNow(time.Date(2026, 7, 24, 4, 30, 0, 0, time.UTC)) {
		t.Fatal("keepalive window not reachable from merged schedule")
	}
}

// TestJsonBool: 验证 bool/数字/字符串三形态解析与多键回退。
func TestJsonBool(t *testing.T) {
	m := map[string]any{"a": true, "b": float64(1), "c": "true", "d": "false", "e": "1", "f": float64(0)}
	if !jsonBool(m, "a") {
		t.Error("a")
	}
	if !jsonBool(m, "b") {
		t.Error("b")
	}
	if !jsonBool(m, "c") {
		t.Error("c")
	}
	if !jsonBool(m, "e") {
		t.Error("e")
	}
	if jsonBool(m, "d") {
		t.Error("d")
	}
	if jsonBool(m, "f") {
		t.Error("f")
	}
	if jsonBool(m, "missing") {
		t.Error("missing")
	}
	if !jsonBool(m, "missing", "a") {
		t.Error("fallback a")
	}
}

// TestJsonI64: 验证 float/int64/字符串解析与缺失键归零。
func TestJsonI64(t *testing.T) {
	m := map[string]any{"a": float64(42), "b": int64(7), "c": "99", "d": "abc"}
	if jsonI64(m, "a") != 42 {
		t.Error("a")
	}
	if jsonI64(m, "b") != 7 {
		t.Error("b")
	}
	if jsonI64(m, "c") != 99 {
		t.Error("c")
	}
	if jsonI64(m, "d") != 0 {
		t.Error("d")
	}
	if jsonI64(m, "missing") != 0 {
		t.Error("missing")
	}
}

// TestJsonStr: 验证类型过滤与多键回退。
func TestJsonStr(t *testing.T) {
	m := map[string]any{"a": "hello", "b": 42, "c": true}
	if jsonStr(m, "a") != "hello" {
		t.Error("a")
	}
	if jsonStr(m, "b") != "" {
		t.Error("b")
	}
	if jsonStr(m, "c") != "" {
		t.Error("c")
	}
	if jsonStr(m, "missing") != "" {
		t.Error("missing")
	}
	if jsonStr(m, "missing", "a") != "hello" {
		t.Error("fallback")
	}
}
