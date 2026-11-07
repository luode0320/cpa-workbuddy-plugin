// billing.go owns the upstream billing API surface: check-in status, user
// resource (credits / packages), payment type, and the perform-* call wrappers
// for daily check-in. Includes the shared JSON helpers used to tolerate the
// upstream's loosely-typed response shapes.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// billingRetryDelays 是计费类 GET/POST 的瞬时错误退避序列（对齐 workbuddy
// management.go 的同名常量）：仅 5xx 与传输错误重试，4xx 与业务错误立即返回。
var billingRetryDelays = []time.Duration{300 * time.Millisecond, 900 * time.Millisecond}

// campaignsURLForTest / campaignClaimURLForTest 供单元测试注入 mock 端点；为空时使用生产常量。
var (
	campaignsURLForTest     string
	campaignClaimURLForTest string
)

// checkinLogTag 返回签到日志使用的账号标识：优先 UID，缺失时回退昵称。
// [参数] sa: 目标账号凭据。
// [返回] 非空账号标识字符串。
// 最近修改时间：2026-10-17 01:30:00 新增，用于签到失败日志的账号归属。
func checkinLogTag(sa *storedAuth) string {
	// 1. 优先 UID，其次昵称，均缺失时返回占位
	if sa == nil {
		return "unknown"
	}
	if uid := strings.TrimSpace(sa.Account.UID); uid != "" {
		return uid
	}
	if name := strings.TrimSpace(sa.Account.Nickname); name != "" {
		return name
	}
	return "unknown"
}

// logCheckinFailure 记录签到链路失败，便于在生产日志中定位账号级问题。
// [参数] op: 操作名（campaigns / claim）；sa: 目标账号凭据；err: 失败原因。
// [返回] 无。
// 最近修改时间：2026-10-17 01:30:00 新增，补齐签到失败可观测性。
func logCheckinFailure(op string, sa *storedAuth, err error) {
	log.Printf("[qoderwork] checkin %s failed for %s: %v", op, checkinLogTag(sa), err)
}

// billingDo 发送一次计费类请求，对上游 5xx / 传输错误做有限退避重试。
// 计费链路（quota/plan/campaigns）的偶发 5xx 或 host 桥接抖动若不做重试，
// 会让面板的积分与计划整行空白（stale-while-error 仅在已有前值时才兜底，
// 首刷与 force 刷新无保护）。
// [参数] req: 待发送请求；reqBody: 请求体字节（用于每次重试重建 Body，
// 因 hostHTTPDo 会消费 req.Body）。
// [返回] hostHTTPResponse: 最终响应；error: 传输错误（含重试后仍失败）。
// 最近修改时间：2026-10-14；改动原因：补齐计费接口瞬时重试（对齐 workbuddy billingCall）
func billingDo(req *http.Request, reqBody []byte) (*hostHTTPResponse, error) {
	var (
		resp *hostHTTPResponse
		err  error
	)
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			time.Sleep(billingRetryDelays[attempt-1])
		}
		// hostHTTPDo 读取并关闭 req.Body，重试前必须重建。
		if reqBody != nil {
			req.Body = io.NopCloser(bytes.NewReader(reqBody))
		}
		resp, err = hostHTTPDo(req)
		transient := err != nil || resp.StatusCode >= 500
		if !transient || attempt >= len(billingRetryDelays) {
			break
		}
	}
	if err != nil {
		// 可观测性：计费链路静默失败会让账号 note 停留在「积分未知」且无线索。
		log.Printf("[qoderwork] billing %s failed: %v", req.URL.Path, err)
		return nil, err
	}
	if resp.StatusCode >= 500 {
		log.Printf("[qoderwork] billing %s upstream http %d after retries", req.URL.Path, resp.StatusCode)
	}
	return resp, nil
}

func billingHeaders(req *http.Request, sa *storedAuth) {
	// QoderWork billing endpoints authenticate with the active token as a
	// plain Bearer — jobToken (jt-) or device token (dt-), both accepted
	// upstream (verified live 2026-07-27). No COSY signing (KNOWLEDGE §2).
	req.Header.Set("Authorization", "Bearer "+sa.Auth.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
}

// campaignHeaders 在 billingHeaders 基础上补齐 CN 客户端调用 campaigns（活动）
// API 必需的客户端头。生产实测（2026-10-11）：缺 Cosy-ClientType 时
// /sash/api/v1/me/campaigns 会把有活动的账号掩盖为
// {"showCampaign":false,"claimable":false,"campaigns":[]}，与 CN 官方客户端
// v0.4.4 的请求头契约（Cosy-ClientType:10 + User-Agent:Qoder）一致。
// [参数] req: 待发送请求；sa: 目标账号凭据。
// [返回] 无（就地修改 req.Header）。
// 最近修改时间：2026-10-11；改动原因：CN 签到由 daily-check-in 改走 campaigns。
func campaignHeaders(req *http.Request, sa *storedAuth) {
	billingHeaders(req, sa)
	req.Header.Set("Cosy-ClientType", "10")
	req.Header.Set("User-Agent", "Qoder")
}

// campaignStatusResponse 对应 GET /sash/api/v1/me/campaigns（plain JSON）。
type campaignStatusResponse struct {
	UID          string     `json:"uid"`
	ShowCampaign bool       `json:"showCampaign"`
	Claimable    bool       `json:"claimable"`
	CampaignURL  string     `json:"campaignUrl"`
	Campaigns    []campaign `json:"campaigns"`
}

type campaign struct {
	CampaignID  string  `json:"campaignId"`
	CampaignKey string  `json:"campaignKey"`
	ActionType  string  `json:"actionType"`  // CLAIM_BENEFIT | VIEW_DETAILS
	ClaimStatus string  `json:"claimStatus"` // CLAIMABLE | CLAIMED
	StartAt     int64   `json:"startAt"`     // s epoch
	EndAt       int64   `json:"endAt"`       // s epoch
	Benefit     benefit `json:"benefit"`
}

type benefit struct {
	Kind   string `json:"kind"`   // CREDITS
	Amount int64  `json:"amount"` // 100
}

// fetchCheckinStatus 把 CN campaigns API 映射为面板的签到摘要。每日 100
// Credits 活动是一条 CLAIM_BENEFIT 活动（campaignKey 形如
// act-YYYYMMDD-NNN）；窗口内该活动 claimStatus=CLAIMED 即视为今日已签。
// [参数] sa: 目标账号凭据。
// [返回] 面板签到摘要（Active/TodayCheckedIn/CampaignID/DailyCredit 等）；上游错误。
// 最近修改时间：2026-10-17 01:30:00；改动原因：失败分支补账号级日志与测试端点注入。
func fetchCheckinStatus(sa *storedAuth) (*checkinSummary, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	target := endpointCampaigns
	if strings.TrimSpace(campaignsURLForTest) != "" {
		target = strings.TrimSpace(campaignsURLForTest)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	campaignHeaders(req, sa)
	resp, err := billingDo(req, nil)
	if err != nil {
		logCheckinFailure("campaigns", sa, err)
		return nil, err
	}
	if resp.StatusCode >= 400 {
		err := fmt.Errorf("campaigns http %d body=%s", resp.StatusCode, truncateRedacted(string(resp.Body), 200))
		logCheckinFailure("campaigns", sa, err)
		return nil, err
	}
	var q campaignStatusResponse
	if err := json.Unmarshal(resp.Body, &q); err != nil {
		err := fmt.Errorf("campaigns parse: %w", err)
		logCheckinFailure("campaigns", sa, err)
		return nil, err
	}
	active, todayChecked, campaignID, dailyCredit := pickCheckinCampaign(&q, time.Now())
	sum := &checkinSummary{
		Active:         active,
		ShowCampaign:   q.ShowCampaign,
		TodayCheckedIn: todayChecked,
		DailyCredit:    dailyCredit,
		ActivityName:   "每日签到",
		CampaignID:     campaignID,
	}
	if todayChecked {
		sum.TodayCredit = dailyCredit
		// 上游 campaigns 响应里没有「连续签到天数」字段（campaign 结构只有
		// campaignId/actionType/claimStatus/startAt/endAt/benefit）。历史实现
		// 把 StreakDays 硬编码为 1，面板据此显示「连续 1 天」——一个永远为 1
		// 的编造值比不显示更糟：用户会以为签到断签了（2026-11-07 生产反馈）。
		// 这里保持 0 表示「上游未提供」，面板不再宣称连续天数。
	}
	return sum, nil
}

// pickCheckinCampaign 从活动列表里挑出当前在 [startAt,endAt] 窗口内的
// CLAIM_BENEFIT + CREDITS 活动，并报告其是否已领取。无此类活动（活动未开放
// 或账号不适用）时返回 active=false。
// [参数] q: campaigns 响应；now: 当前时间。
// [返回] active: 是否存在可领取签到活动；todayChecked: 是否已领取；
// campaignID: 待领取活动 id；dailyCredit: 活动赠送积分。
// 最近修改时间：2026-10-11；改动原因：CN 签到改走 campaigns。
func pickCheckinCampaign(q *campaignStatusResponse, now time.Time) (active, todayChecked bool, campaignID string, dailyCredit int64) {
	ts := now.Unix()
	for _, c := range q.Campaigns {
		if c.ActionType != "CLAIM_BENEFIT" || c.Benefit.Kind != "CREDITS" {
			continue
		}
		if c.StartAt > 0 && ts < c.StartAt {
			continue // 窗口未开启
		}
		if c.EndAt > 0 && ts > c.EndAt {
			continue // 窗口已关闭
		}
		active = true
		campaignID = c.CampaignID
		dailyCredit = c.Benefit.Amount
		if c.ClaimStatus == "CLAIMED" {
			todayChecked = true
		}
		return active, todayChecked, campaignID, dailyCredit
	}
	return false, false, "", 0
}

// quotaUsageResponse mirrors GET /api/v2/quota/usage response (plain JSON,
// no envelope). Both userQuota (base credits) and addOnQuota (one-time pro
// upgrade + checkin packs) are summed for the panel.
type quotaUsageResponse struct {
	UserID               string  `json:"userId"`
	UserType             string  `json:"userType"`
	UsageType            string  `json:"usageType"`
	TotalUsagePercentage float64 `json:"totalUsagePercentage"`
	IsQuotaExceeded      bool    `json:"isQuotaExceeded"`
	ExpiresAt            int64   `json:"expiresAt"` // ms epoch
	UpgradeURL           string  `json:"upgradeUrl"`
	UserQuota            struct {
		Total     float64 `json:"total"`
		Used      float64 `json:"used"`
		Remaining float64 `json:"remaining"`
		Unit      string  `json:"unit"`
	} `json:"userQuota"`
	AddOnQuota struct {
		Total     float64 `json:"total"`
		Used      float64 `json:"used"`
		Remaining float64 `json:"remaining"`
	} `json:"addOnQuota"`
}

// fetchUserResource queries QoderWork's quota endpoint and aggregates base +
// add-on credits into the panel's creditsSummary shape.
func fetchUserResource(sa *storedAuth) (*creditsSummary, error) {
	req, err := http.NewRequest(http.MethodGet, upstreamBaseCN+"/api/v2/quota/usage", nil)
	if err != nil {
		return nil, err
	}
	billingHeaders(req, sa)
	resp, err := billingDo(req, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("quota/usage http %d body=%s", resp.StatusCode, truncateRedacted(string(resp.Body), 200))
	}
	var q quotaUsageResponse
	if err := json.Unmarshal(resp.Body, &q); err != nil {
		return nil, fmt.Errorf("quota/usage parse: %w", err)
	}
	sum := &creditsSummary{
		TotalRemain: int64(q.UserQuota.Remaining + q.AddOnQuota.Remaining),
		TotalUsed:   int64(q.UserQuota.Used + q.AddOnQuota.Used),
		TotalSize:   int64(q.UserQuota.Total + q.AddOnQuota.Total),
		PackCount:   2,
		Packages: []packageSummary{
			{Name: "基础额度", Remain: int64(q.UserQuota.Remaining), Used: int64(q.UserQuota.Used), Size: int64(q.UserQuota.Total)},
			{Name: "赠送/签到额度", Remain: int64(q.AddOnQuota.Remaining), Used: int64(q.AddOnQuota.Used), Size: int64(q.AddOnQuota.Total)},
		},
	}
	return sum, nil
}

// planResponse mirrors GET /api/v2/user/plan (plain JSON, no envelope).
type planResponse struct {
	UserType       string          `json:"user_type"`
	PlanTierName   string          `json:"plan_tier_name"`
	IsPersonal     bool            `json:"is_personal_version"`
	IsPaid         bool            `json:"is_paid_plan"`
	IsHighestTier  bool            `json:"is_highest_tier"`
	FeatureAllowed map[string]bool `json:"feature_allowed"`
	StartDate      int64           `json:"start_date"` // ms epoch
	EndDate        int64           `json:"end_date"`   // ms epoch
}

func fetchPaymentType(sa *storedAuth) string {
	req, err := http.NewRequest(http.MethodGet, upstreamBaseCN+"/api/v2/user/plan", nil)
	if err != nil {
		return ""
	}
	billingHeaders(req, sa)
	resp, err := billingDo(req, nil)
	if err != nil || resp.StatusCode >= 400 {
		return ""
	}
	var p planResponse
	if err := json.Unmarshal(resp.Body, &p); err != nil {
		return ""
	}
	// Prefer plan_tier_name (e.g. "Pro Trial") over the raw user_type string.
	if p.PlanTierName != "" {
		return p.PlanTierName
	}
	return p.UserType
}

// performCheckinCall 调用 CN 活动领取接口（campaigns claim）。
// [参数] sa: 目标账号凭据；campaignID: 待领取活动的 campaignId（来自 fetchCheckinStatus）。
// [返回] 面板使用的 success 布尔语义响应 map；响应非法时返回错误。
// 最近修改时间：2026-10-17 01:30:00；改动原因：失败分支补账号级日志与测试端点注入。
func performCheckinCall(sa *storedAuth, campaignID string) (map[string]any, error) {
	if campaignID == "" {
		return map[string]any{"success": false, "message": "no claimable check-in campaign"}, nil
	}
	claimURL := endpointCampaignClaim + campaignID + "/claim"
	if strings.TrimSpace(campaignClaimURLForTest) != "" {
		claimURL = strings.TrimSpace(campaignClaimURLForTest) + campaignID + "/claim"
	}
	req, err := http.NewRequest(http.MethodPost, claimURL, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	campaignHeaders(req, sa)
	// claim 语义幂等（重复领取返回 replayed=true），5xx 重试安全。
	resp, err := billingDo(req, []byte("{}"))
	if err != nil {
		logCheckinFailure("claim", sa, err)
		return map[string]any{"success": false, "message": err.Error()}, nil
	}
	if resp.StatusCode >= 400 {
		err := fmt.Errorf("http %d: %s", resp.StatusCode, truncateRedacted(string(resp.Body), 200))
		logCheckinFailure("claim", sa, err)
		return map[string]any{"success": false, "message": err.Error()}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(resp.Body, &m); err != nil {
		err := fmt.Errorf("claim parse: %w", err)
		logCheckinFailure("claim", sa, err)
		return nil, err
	}
	// CN claim 返回 {"grantId":..,"status":"CLAIMED","replayed":bool,
	// "benefit":{"kind":"CREDITS","amount":100,...},"campaignId":..}。
	// status=CLAIMED 视为成功（replayed=true 表示本轮已领过）。
	status, _ := m["status"].(string)
	if status != "CLAIMED" {
		err := fmt.Errorf("unexpected claim status: %v", m["status"])
		logCheckinFailure("claim", sa, err)
		return map[string]any{"success": false, "message": err.Error()}, nil
	}
	out := map[string]any{"success": true, "result": status}
	if b, ok := m["benefit"].(map[string]any); ok {
		if amt, ok := b["amount"].(float64); ok {
			out["rewardCredits"] = int64(amt)
		}
	}
	if replayed, ok := m["replayed"].(bool); ok && replayed {
		out["already"] = true
	}
	return out, nil
}

// isCreditsExhausted 判定积分快照是否归零（仅用于面板展示「0积分」标签）。
// [参数] cr：积分快照。
// [返回] 有消耗或额度池信号且剩余为 0 时返回 true；缺失数据视为未知返回 false。
// 最近修改时间 2026-10-17 15:12:00（改为纯展示语义，不再参与路由与自动禁用）
func isCreditsExhausted(cr *creditsSummary) bool {
	if cr == nil {
		return false
	}
	if cr.TotalRemain > 0 {
		return false
	}
	// remain==0: exhausted only when we know there was/is a package total
	// (used>0, size>0, or packages present). Pure zero with no packages = no data.
	if cr.TotalUsed > 0 || cr.TotalSize > 0 {
		return true
	}
	return len(cr.Packages) > 0
}
