package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestQoderAI_GlobalEndpointsContract verifies that Qoder AI uses dedicated
// global domain constants and correct endpoints.
func TestQoderAI_GlobalEndpointsContract(t *testing.T) {
	if providerName != "qoder-ai-provider" {
		t.Fatalf("providerName=%q; want 'qoder-ai-provider'", providerName)
	}
	if authFileName != "qoderai.json" {
		t.Fatalf("authFileName=%q; want 'qoderai.json'", authFileName)
	}
	if authFilePrefix != "qoderai-" {
		t.Fatalf("authFilePrefix=%q; want 'qoderai-'", authFilePrefix)
	}
	if upstreamBase != "https://openapi.qoder.sh" {
		t.Fatalf("upstreamBase=%q; want 'https://openapi.qoder.sh'", upstreamBase)
	}
	if gatewayBase != "https://openapi.qoder.sh" {
		t.Fatalf("gatewayBase=%q; want 'https://openapi.qoder.sh'", gatewayBase)
	}
	// 国际版签到走 campaigns（活动）API，不再使用 CN 专有的 daily-check-in 端点
	// （后者在 openapi.qoder.sh 恒返回 404 NotFound）。
	if endpointCampaigns != "https://openapi.qoder.sh/sash/api/v1/me/campaigns" {
		t.Fatalf("endpointCampaigns=%q; want .../sash/api/v1/me/campaigns", endpointCampaigns)
	}
	if !strings.HasPrefix(endpointCampaignClaim, "https://openapi.qoder.sh/sash/api/v1/me/campaigns/") {
		t.Fatalf("endpointCampaignClaim=%q; want prefix .../campaigns/", endpointCampaignClaim)
	}
	if qoderWebsite != "https://qoder.com" {
		t.Fatalf("qoderWebsite=%q; want 'https://qoder.com'", qoderWebsite)
	}
}

// TestPickCheckinCampaign_ClaimBenefit 验证从 campaigns 列表里正确挑出
// CLAIM_BENEFIT 的 CREDITS 活动，并识别已领取状态。
func TestPickCheckinCampaign_ClaimBenefit(t *testing.T) {
	// 生产真实响应形状（2026-10-11 实测）。
	raw := `{
      "uid":"01a125f7-f6f1-73ff-9928-1ae6a554edc3",
      "showCampaign":true,"claimable":false,
      "campaignUrl":"https://openapi.qoder.sh/growth-page/activity-iframe",
      "campaigns":[
        {"campaignId":"01a120ff-e293-7c9c-864c-c21392e9a771","campaignKey":"act-20261009-118",
         "actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMED",
         "startAt":1000,"endAt":9999999999,
         "benefit":{"kind":"CREDITS","amount":100}},
        {"campaignId":"01a05bce-e800-7494-a002-806e4438f483","campaignKey":"act-20260901-493",
         "actionType":"VIEW_DETAILS","claimStatus":"CLAIMED",
         "startAt":1000,"endAt":9999999999,"benefit":{}}
      ]}`
	var q campaignStatusResponse
	if err := json.Unmarshal([]byte(raw), &q); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	active, checked, cid, credit := pickCheckinCampaign(&q, time.Unix(2000, 0))
	if !active {
		t.Fatal("active=false; want true (CLAIM_BENEFIT CREDITS campaign in window)")
	}
	if !checked {
		t.Fatal("todayChecked=false; want true (claimStatus=CLAIMED)")
	}
	if cid != "01a120ff-e293-7c9c-864c-c21392e9a771" {
		t.Fatalf("campaignID=%q; want the CLAIM_BENEFIT campaign id", cid)
	}
	if credit != 100 {
		t.Fatalf("dailyCredit=%d; want 100", credit)
	}
}

// TestPickCheckinCampaign_OutOfWindow 验证窗口外的活动不算 active。
func TestPickCheckinCampaign_OutOfWindow(t *testing.T) {
	raw := `{"campaigns":[{"campaignId":"x","actionType":"CLAIM_BENEFIT",
      "claimStatus":"CLAIMABLE","startAt":100,"endAt":200,
      "benefit":{"kind":"CREDITS","amount":100}}]}`
	var q campaignStatusResponse
	if err := json.Unmarshal([]byte(raw), &q); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	active, checked, _, _ := pickCheckinCampaign(&q, time.Unix(300, 0))
	if active {
		t.Fatal("active=true; want false (now past endAt)")
	}
	if checked {
		t.Fatal("todayChecked=true; want false")
	}
}

// TestPickCheckinCampaign_NoCampaign 验证无 CLAIM_BENEFIT 活动时 active=false
// （对应国际版活动未开放或账号不适用）。
func TestPickCheckinCampaign_NoCampaign(t *testing.T) {
	raw := `{"showCampaign":false,"claimable":false,"campaigns":[]}`
	var q campaignStatusResponse
	if err := json.Unmarshal([]byte(raw), &q); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	active, checked, cid, credit := pickCheckinCampaign(&q, time.Unix(2000, 0))
	if active || checked || cid != "" || credit != 0 {
		t.Fatalf("want all zero for empty campaigns; got active=%v checked=%v cid=%q credit=%d", active, checked, cid, credit)
	}
}
