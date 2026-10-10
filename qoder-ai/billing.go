// billing.go owns the upstream billing API surface: check-in status, user
// resource (credits / packages), payment type, and the perform-* call wrappers
// for daily check-in. Includes the shared JSON helpers used to tolerate the
// upstream's loosely-typed response shapes.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func billingHeaders(req *http.Request, sa *storedAuth) {
	// Qoder AI billing endpoints authenticate with the active token as a
	// plain Bearer — jobToken (jt-) or device token (dt-), both accepted
	// upstream (verified live 2026-07-27). No COSY signing (KNOWLEDGE §2).
	req.Header.Set("Authorization", "Bearer "+sa.Auth.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
}

// campaignHeaders adds the international-client headers the campaigns API
// requires. Without Cosy-ClientType the list comes back empty
// (showCampaign:false, campaigns:[]) even for accounts that do have an active
// activity — verified live 2026-10-11 against openapi.qoder.sh.
func campaignHeaders(req *http.Request, sa *storedAuth) {
	billingHeaders(req, sa)
	req.Header.Set("Cosy-ClientType", "10")
	req.Header.Set("User-Agent", "Qoder")
}

// campaignStatusResponse mirrors GET /sash/api/v1/me/campaigns (plain JSON).
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
	ClaimStatus string  `json:"claimStatus"` // CLAIMED | ...
	StartAt     int64   `json:"startAt"`     // s epoch
	EndAt       int64   `json:"endAt"`       // s epoch
	Benefit     benefit `json:"benefit"`
}

type benefit struct {
	Kind   string `json:"kind"`   // CREDITS
	Amount int64  `json:"amount"` // 100
}

// fetchCheckinStatus maps the international campaigns API onto the panel's
// check-in summary. The daily 100-Credit activity is a CLAIM_BENEFIT campaign
// (campaignKey like act-YYYYMMDD-NNN); the account is "checked in today" when
// that campaign is already CLAIMED within its window.
func fetchCheckinStatus(sa *storedAuth) (*checkinSummary, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpointCampaigns, nil)
	if err != nil {
		return nil, err
	}
	campaignHeaders(req, sa)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("campaigns http %d body=%s", resp.StatusCode, truncateRedacted(string(resp.Body), 200))
	}
	var q campaignStatusResponse
	if err := json.Unmarshal(resp.Body, &q); err != nil {
		return nil, fmt.Errorf("campaigns parse: %w", err)
	}
	active, todayChecked, campaignID, dailyCredit := pickCheckinCampaign(&q, time.Now())
	sum := &checkinSummary{
		Active:         active,
		TodayCheckedIn: todayChecked,
		DailyCredit:    dailyCredit,
		ActivityName:   "每日签到",
		CampaignID:     campaignID,
	}
	if todayChecked {
		sum.TodayCredit = dailyCredit
		sum.StreakDays = 1
	}
	return sum, nil
}

// pickCheckinCampaign selects the daily check-in campaign (CLAIM_BENEFIT with a
// CREDITS benefit) that is currently within its [startAt,endAt] window, and
// reports whether it has already been claimed. Returns active=false when no
// such campaign is open for this account (e.g. activity not enabled).
func pickCheckinCampaign(q *campaignStatusResponse, now time.Time) (active, todayChecked bool, campaignID string, dailyCredit int64) {
	ts := now.Unix()
	for _, c := range q.Campaigns {
		if c.ActionType != "CLAIM_BENEFIT" || c.Benefit.Kind != "CREDITS" {
			continue
		}
		if c.StartAt > 0 && ts < c.StartAt {
			continue // window not open yet
		}
		if c.EndAt > 0 && ts > c.EndAt {
			continue // window closed
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

// fetchUserResource queries Qoder AI's quota endpoint and aggregates base +
// add-on credits into the panel's creditsSummary shape.
func fetchUserResource(sa *storedAuth) (*creditsSummary, error) {
	req, err := http.NewRequest(http.MethodGet, upstreamBase+"/api/v2/quota/usage", nil)
	if err != nil {
		return nil, err
	}
	billingHeaders(req, sa)
	resp, err := hostHTTPDo(req)
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
	req, err := http.NewRequest(http.MethodGet, upstreamBase+"/api/v2/user/plan", nil)
	if err != nil {
		return ""
	}
	billingHeaders(req, sa)
	resp, err := hostHTTPDo(req)
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

// performCheckinCall 调用国际版活动领取接口（campaigns claim）。
// [参数] sa: 目标账号的落盘认证信息；campaignID: 待领取活动的 campaignId。
// [返回] 面板使用的 success 布尔语义响应 map；响应非法时返回错误。
// 最近修改时间：2026-10-11；改动原因：国际版签到改走 campaigns claim（原 CN daily-check-in 端点 404）
func performCheckinCall(sa *storedAuth, campaignID string) (map[string]any, error) {
	if campaignID == "" {
		return map[string]any{"success": false, "message": "no claimable check-in campaign"}, nil
	}
	req, err := http.NewRequest(http.MethodPost, endpointCampaignClaim+campaignID+"/claim", strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	campaignHeaders(req, sa)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return map[string]any{"success": false, "message": err.Error()}, nil
	}
	if resp.StatusCode >= 400 {
		return map[string]any{"success": false, "message": fmt.Sprintf("http %d: %s", resp.StatusCode, truncateRedacted(string(resp.Body), 200))}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(resp.Body, &m); err != nil {
		return nil, err
	}
	// 国际版 claim 返回 {"grantId":..,"status":"CLAIMED","replayed":bool,
	// "benefit":{"kind":"CREDITS","amount":100,...},"campaignId":..}。
	// status=CLAIMED 视为成功（replayed=true 表示本轮已领过）。
	status, _ := m["status"].(string)
	if status != "CLAIMED" {
		return map[string]any{"success": false, "message": fmt.Sprintf("unexpected claim status: %v", m["status"])}, nil
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

// isCreditsExhausted is the shared "耗尽" definition for panel + scheduler.
// Exhausted = we have usage signal and no remaining credits.
// Missing credits data is NOT exhausted (unknown).
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
