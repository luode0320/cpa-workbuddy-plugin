package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// checkinDetailEmptyBody 是活动详情里没有每日签到任务时的最小响应。
const checkinDetailEmptyBody = `{"code":0,"data":{"tasks":[],"progress":{"total_amount":0}}}`

// checkinDetailNotSignedBody 是今日未签的详情响应（字段取自生产实测）。
const checkinDetailNotSignedBody = `{"code":0,"data":{"tasks":[{"campaign_code":"daily_signin","title":"每日签到","task_status":1,"daily_checkin":{"checked_in_today":false,"continuous_days":0,"total_days":0,"reward":{"reward_amount":200,"reward_type":"quota_grant"}}},{"campaign_code":"select_work_fields","title":"选择工作领域","task_status":1}],"progress":{"total_amount":0}}}`

// checkinDetailSignedBody 是今日已签的详情响应。
const checkinDetailSignedBody = `{"code":0,"data":{"tasks":[{"campaign_code":"daily_signin","title":"每日签到","task_status":3,"daily_checkin":{"checked_in_today":true,"continuous_days":1,"total_days":1,"reward":{"reward_amount":200,"reward_type":"quota_grant"}}}],"progress":{"total_amount":200}}}`

// checkinLandingWebBody 是网页渠道（chosen_type=0）的落地页响应。
const checkinLandingWebBody = `{"code":0,"data":{"chosen_type":0,"landing_cards":[]}}`

// checkinDetailCampaignNotFoundBody 是桌面端渠道账号的活动详情响应（生产实测）。
const checkinDetailCampaignNotFoundBody = `{"data":null,"code":2,"msg":"","message":"campaign not found"}`

// checkinLandingDesktopBody 是桌面端渠道（chosen_type=3）的落地页响应。
const checkinLandingDesktopBody = `{"code":0,"data":{"chosen_type":3,"landing_cards":[{"card_code":"web_pc_download_banner","task_code":"daily_signin","title":"今日签到领 200 额度","subtitle":"前往豆包桌面端即可领取"}]}}`

// checkinQuotaBody 是带活动赠送额度的订阅汇总响应（结构取自生产实测）。
const checkinQuotaBody = `{"code":0,"data":{"incentive_quota_section":{"window_limit_groups":[{"feature_group":"doubao_activity_reward_quota","feature_group_name":"活动赠送额度","window_limits":[{"total_amount":200,"used_amount":200,"used_percent":100,"end_time":1797237901995}]}]}}}`

// checkinStub 是活动端点的可编程桩，按路径返回不同响应并记录请求。
type checkinStub struct {
	// paths 记录被访问的端点路径，用于校验「桌面端渠道不提交签到」。
	paths []string
	// bodies 记录各端点的请求体，用于校验 scene 与 campaign_group_code。
	bodies map[string]string
}

// newCheckinStub 启动活动端点桩并接管业务端点基址。
// [参数] t: 测试上下文；responses: 路径到响应体的映射。
// [返回] 桩与恢复函数。
// 最近修改时间：2026-11-14 17:40:00；改动原因：新增，签到接口离线验证入口。
func newCheckinStub(t *testing.T, responses map[string]string) (*checkinStub, func()) {
	t.Helper()
	stub := &checkinStub{bodies: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. 记录访问路径与请求体，供用例断言是否真的发起了写请求。
		stub.paths = append(stub.paths, r.URL.Path)
		body := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(body)
		}
		stub.bodies[r.URL.Path] = strings.TrimSpace(string(body))

		// 2. 未登记的路径按上游缺失场景返回空对象，避免用例静默通过。
		payload, ok := responses[r.URL.Path]
		if !ok {
			payload = `{"code":0,"data":{}}`
		}
		_, _ = w.Write([]byte(payload))
	}))
	restore := swapBusinessEndpoint(srv.URL)
	return stub, func() {
		restore()
		srv.Close()
	}
}

// hasPath 判断桩是否被访问过指定端点。
// [参数] path: 端点路径。
// [返回] true 表示访问过。
// 最近修改时间：2026-11-14 17:40:00；改动原因：新增，写请求发生性断言。
func (s *checkinStub) hasPath(path string) bool {
	for _, item := range s.paths {
		if item == path {
			return true
		}
	}
	return false
}

// checkinTestAccount 构造一份签到用例账号（值为假数据，不落盘）。
// [参数] 无。
// [返回] 账号。
// 最近修改时间：2026-11-14 17:40:00；改动原因：新增，签到用例统一账号夹具。
func checkinTestAccount() *StoredAuth {
	return &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       doubaoAuthFilePrefix + "checkintest",
		CookieHeader: validCookieHeader,
		BotID:        defaultBotID,
		DeviceID:     "7300000000000000001",
		WebID:        "7300000000000000002",
		WebTabID:     "00000000-0000-4000-8000-000000000002",
	}
}

// TestFetchCampaignDetailParsesDailySignin 校验能从详情里解析出签到判据与今日额度。
func TestFetchCampaignDetailParsesDailySignin(t *testing.T) {
	stub, restore := newCheckinStub(t, map[string]string{
		checkinDetailPath: checkinDetailNotSignedBody,
	})
	defer restore()

	detail, err := fetchCampaignDetail(t.Context(), checkinTestAccount())
	if err != nil {
		t.Fatalf("解析活动详情失败：%v", err)
	}
	// 1. 签到任务必须被识别，否则页面会把「需桌面端」误报成「不可签」。
	if !detail.TaskFound {
		t.Fatalf("应识别出 daily_signin 任务")
	}
	if detail.CheckedInToday {
		t.Errorf("未签详情不应报告已签到")
	}
	// 2. 今日可得额度嵌在 daily_checkin.reward 里，取值层级错了会恒为 0。
	if detail.RewardAmount != 200 {
		t.Errorf("今日额度应为 200，实际 %v", detail.RewardAmount)
	}
	if len(detail.Tasks) != 2 {
		t.Errorf("应回传 2 条任务，实际 %d", len(detail.Tasks))
	}
	// 3. 请求体必须是活动组而非 scene，字段名写错上游只回空 data。
	if got := stub.bodies[checkinDetailPath]; !strings.Contains(got, `"campaign_group_code":"`+checkinCampaignGroup+`"`) {
		t.Errorf("详情请求体应携带 campaign_group_code，实际 %s", got)
	}
}

// TestFetchCampaignDetailRejectsBusinessCode 校验业务码非 0 时按失败处理。
func TestFetchCampaignDetailRejectsBusinessCode(t *testing.T) {
	_, restore := newCheckinStub(t, map[string]string{
		checkinDetailPath: `{"code":2,"msg":"campaign not found"}`,
	})
	defer restore()

	if _, err := fetchCampaignDetail(t.Context(), checkinTestAccount()); err == nil {
		t.Fatalf("业务码 2 必须返回错误")
	} else if !strings.Contains(err.Error(), "已下线或已换季") {
		t.Errorf("活动下线应有可操作提示，实际 %v", err)
	}
}

// TestFetchLandingChosenType 校验渠道判据读取。
func TestFetchLandingChosenType(t *testing.T) {
	_, restore := newCheckinStub(t, map[string]string{
		checkinLandingPath: checkinLandingDesktopBody,
	})
	defer restore()

	chosen, err := fetchLandingChosenType(t.Context(), checkinTestAccount())
	if err != nil {
		t.Fatalf("读取落地页失败：%v", err)
	}
	if chosen != 3 {
		t.Errorf("chosen_type 应为 3，实际 %d", chosen)
	}
}

// TestFetchQuotaGroupsReadsActivityGrant 校验活动赠送额度能被递归提取出来。
//
// 生产实测结构是 window_limit_groups[].feature_group_name + 其下 window_limits[]，
// 只按固定路径取值会在上游加层级时静默读到空，故此处锁死递归行为。
func TestFetchQuotaGroupsReadsActivityGrant(t *testing.T) {
	_, restore := newCheckinStub(t, map[string]string{
		quotaSummaryPath: checkinQuotaBody,
	})
	defer restore()

	groups, err := fetchQuotaGroups(t.Context(), checkinTestAccount())
	if err != nil {
		t.Fatalf("读取额度失败：%v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("应提取 1 个额度窗口，实际 %d", len(groups))
	}
	group := groups[0]
	if group.Name != "活动赠送额度" {
		t.Errorf("分组名应取 window_limits 同层的 feature_group_name，实际 %q", group.Name)
	}
	if group.Total != 200 || group.Used != 200 {
		t.Errorf("额度数值错误：total=%v used=%v", group.Total, group.Used)
	}
	if group.EndTime != 1797237901995 {
		t.Errorf("到期时间错误：%v", group.EndTime)
	}
}

// TestCheckinStatusForDesktopAccountIgnoresDetailError 锁死生产实测的判据顺序。
//
// 桌面端渠道账号的活动详情回 code=2 campaign not found（该账号在网页侧没有活动实例），
// 只有落地页 chosen_type=3 才是真相；先查详情会把它误判成「活动已下线」。
func TestCheckinStatusForDesktopAccountIgnoresDetailError(t *testing.T) {
	stub, restore := newCheckinStub(t, map[string]string{
		checkinDetailPath:  checkinDetailCampaignNotFoundBody,
		checkinLandingPath: checkinLandingDesktopBody,
	})
	defer restore()

	item := checkinStatusFor(checkinTestAccount())
	if item["state"] != checkinStateDesktopOnly {
		t.Fatalf("桌面端账号状态应为 %s，实际 %v（%v）", checkinStateDesktopOnly, item["state"], item["message"])
	}
	if item["chosen_type"] != 3 {
		t.Errorf("应回传 chosen_type=3，实际 %v", item["chosen_type"])
	}
	// 1. 桌面端账号无 web 活动实例，不该再白跑一次详情请求。
	if stub.hasPath(checkinDetailPath) {
		t.Errorf("桌面端账号不应请求活动详情")
	}
	if stub.hasPath(checkinRunPath) {
		t.Fatalf("桌面端账号不得发起签到写请求")
	}
}

// TestRunCheckinSkipsDesktopChannel 校验桌面端渠道账号只上报、不提交签到。
//
// 这是本实现最重要的红线：上游把网页入口改投桌面端时，反复裸提交会被判异常流量，
// 因此必须完全不产生签到写请求。
func TestRunCheckinSkipsDesktopChannel(t *testing.T) {
	stub, restore := newCheckinStub(t, map[string]string{
		checkinDetailPath:  checkinDetailNotSignedBody,
		checkinLandingPath: checkinLandingDesktopBody,
	})
	defer restore()

	result := runCheckinForAccount(checkinTestAccount())
	if result["state"] != checkinStateDesktopOnly {
		t.Fatalf("状态应为 %s，实际 %v（%v）", checkinStateDesktopOnly, result["state"], result["message"])
	}
	if stub.hasPath(checkinRunPath) {
		t.Fatalf("桌面端渠道不得发起签到写请求")
	}
}

// TestRunCheckinSubmitsWhenWebChannel 校验网页渠道账号会提交签到并以回查为准确认。
func TestRunCheckinSubmitsWhenWebChannel(t *testing.T) {
	submitted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case checkinDetailPath:
			// 1. 首次回查未签，提交后再回查已签，模拟上游结算时序。
			if submitted {
				_, _ = w.Write([]byte(checkinDetailSignedBody))
			} else {
				_, _ = w.Write([]byte(checkinDetailNotSignedBody))
			}
		case checkinLandingPath:
			_, _ = w.Write([]byte(checkinLandingWebBody))
		case checkinRunPath:
			submitted = true
			_, _ = w.Write([]byte(`{"code":0,"data":{"checked_in":true,"checkin_date":"2026-11-14","continuous_days":1,"idempotent":true,"reward_amount":200,"reward_type":"quota_grant"}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		}
	}))
	defer srv.Close()
	restore := swapBusinessEndpoint(srv.URL)
	defer restore()

	result := runCheckinForAccount(checkinTestAccount())
	if result["state"] != checkinStateDone {
		t.Fatalf("状态应为 %s，实际 %v（%v）", checkinStateDone, result["state"], result["message"])
	}
	if !submitted {
		t.Fatalf("网页渠道应提交签到请求")
	}
	if result["continuous_days"] != 1 {
		t.Errorf("应回传连续天数 1，实际 %v", result["continuous_days"])
	}
}

// TestRunCheckinReportsWhenUpstreamNotConfirmed 校验提交后回查仍未到账时按失败上报。
//
// 单个 code=0 不算成功：上游可能在提交与结算之间失败。
func TestRunCheckinReportsWhenUpstreamNotConfirmed(t *testing.T) {
	stub, restore := newCheckinStub(t, map[string]string{
		checkinDetailPath:  checkinDetailNotSignedBody,
		checkinLandingPath: checkinLandingWebBody,
		checkinRunPath:     `{"code":0,"data":{"checked_in":true}}`,
	})
	defer restore()

	result := runCheckinForAccount(checkinTestAccount())
	if result["state"] != checkinStateError {
		t.Fatalf("未确认到账应为 %s，实际 %v", checkinStateError, result["state"])
	}
	if !strings.Contains(result["message"].(string), "未确认到账") {
		t.Errorf("提示应说明未确认到账，实际 %v", result["message"])
	}
	if !stub.hasPath(checkinRunPath) {
		t.Fatalf("网页渠道应提交签到请求")
	}
}

// TestRunCheckinSkipsWhenAlreadySigned 校验已签到账号不重复提交。
func TestRunCheckinSkipsWhenAlreadySigned(t *testing.T) {
	stub, restore := newCheckinStub(t, map[string]string{
		checkinDetailPath:  checkinDetailSignedBody,
		checkinLandingPath: checkinLandingWebBody,
	})
	defer restore()

	result := runCheckinForAccount(checkinTestAccount())
	if result["state"] != checkinStateDone {
		t.Fatalf("已签到状态应为 %s，实际 %v", checkinStateDone, result["state"])
	}
	if stub.hasPath(checkinRunPath) {
		t.Fatalf("已签到账号不得重复提交签到")
	}
}

// TestNextCheckinTimePicksNextSlot 校验槽位排期严格晚于当前时刻。
func TestNextCheckinTimePicksNextSlot(t *testing.T) {
	location := time.FixedZone("CST", 8*3600)
	// 1. 上午九点应排到十点。
	if got := nextCheckinTime(time.Date(2026, 11, 14, 9, 0, 0, 0, location)); got.Hour() != 10 || got.Day() != 14 {
		t.Errorf("09:00 应排到当日 10:00，实际 %v", got)
	}
	// 2. 十点整已过槽位，应排到二十二点。
	if got := nextCheckinTime(time.Date(2026, 11, 14, 10, 0, 0, 0, location)); got.Hour() != 22 || got.Day() != 14 {
		t.Errorf("10:00 应排到当日 22:00，实际 %v", got)
	}
	// 3. 晚于当天最后一个槽位应跨到次日十点。
	if got := nextCheckinTime(time.Date(2026, 11, 14, 23, 30, 0, 0, location)); got.Hour() != 10 || got.Day() != 15 {
		t.Errorf("23:30 应排到次日 10:00，实际 %v", got)
	}
}

// TestCheckinStateTextCoversAllStates 校验每个状态都有中文文案。
func TestCheckinStateTextCoversAllStates(t *testing.T) {
	for _, state := range []string{checkinStateDone, checkinStateReady, checkinStateDesktopOnly, checkinStateExpired, checkinStateError} {
		if text := checkinStateText(state); text == "" {
			t.Errorf("状态 %q 缺少文案", state)
		}
	}
}

// TestCheckinStatusForReportsQuota 校验只读探测同时回传签到状态与活动额度。
func TestCheckinStatusForReportsQuota(t *testing.T) {
	_, restore := newCheckinStub(t, map[string]string{
		checkinDetailPath:  checkinDetailSignedBody,
		checkinLandingPath: checkinLandingWebBody,
		quotaSummaryPath:   checkinQuotaBody,
	})
	defer restore()

	item := checkinStatusFor(checkinTestAccount())
	if item["state"] != checkinStateDone {
		t.Fatalf("状态应为 %s，实际 %v", checkinStateDone, item["state"])
	}
	groups, ok := item["quota"].([]checkinQuotaGroup)
	if !ok || len(groups) != 1 {
		t.Fatalf("应回传 1 个额度分组，实际 %v", item["quota"])
	}
	if _, exists := item["quota_error"]; exists {
		t.Errorf("额度读取成功时不应带 quota_error，实际 %v", item["quota_error"])
	}
}

// TestCampaignCodeHintCoversKeyCodes 校验关键业务码都有可操作提示。
func TestCampaignCodeHintCoversKeyCodes(t *testing.T) {
	cases := map[int]string{
		2:         "已下线或已换季",
		710012001: "登录态已失效",
		710010702: "投放策略拒绝",
	}
	for code, expected := range cases {
		if hint := campaignCodeHint(code); !strings.Contains(hint, expected) {
			t.Errorf("业务码 %d 的提示应含 %q，实际 %q", code, expected, hint)
		}
	}
	if hint := campaignCodeHint(999); !strings.Contains(hint, "999") {
		t.Errorf("未知业务码应回显原始码，实际 %q", hint)
	}
}

// TestNumberValueAcceptsBothShapes 校验额度字段数字与字符串两种形态都能读。
func TestNumberValueAcceptsBothShapes(t *testing.T) {
	cases := map[string]float64{
		`{"amount":200}`:      200,
		`{"amount":"1,200"}`:  1200,
		`{"amount":"¥ 88.5"}`: 88.5,
		`{"amount":null}`:     0,
	}
	for raw, expected := range cases {
		if got := jsonNumber([]byte(raw), "amount"); got != expected {
			t.Errorf("%s 应解析为 %v，实际 %v", raw, expected, got)
		}
	}
}
