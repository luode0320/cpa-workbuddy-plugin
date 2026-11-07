// checkin_streak_test.go 锁住「签到摘要不得编造上游没有的事实」这条契约。
//
// 背景（2026-11-07 生产反馈「签到是不是有问题」）：上游 campaigns 响应里没有
// 连续签到天数字段，旧实现把 StreakDays 硬编码为 1，面板据此显示「连续 1 天」；
// 而签到成功分支又根本不返回该字段，面板显示「连续 ? 天」。两种表现都让用户
// 以为签到断签。本文件确保：
//
//  1. 已签与未签两种情况下 StreakDays 都是 0（未知），不得再出现编造值；
//  2. 今日积分只从活动 amount 取，不参与任何推算；
//  3. 无 CREDITS 活动 / 窗口外活动仍正确判定为不可签（既有语义不回归）。
package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fetchCheckinStatusWithBody 用一次性 httptest 上游跑 fetchCheckinStatus。
func fetchCheckinStatusWithBody(t *testing.T, body string) *checkinSummary {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	prev := campaignsURLForTest
	campaignsURLForTest = srv.URL
	t.Cleanup(func() { campaignsURLForTest = prev })

	sa := &storedAuth{}
	sa.Account.UID = "u-streak-test"
	sa.Auth.AccessToken = "dt-test-token"

	sum, err := fetchCheckinStatus(sa)
	if err != nil {
		t.Fatalf("fetchCheckinStatus: %v", err)
	}
	if sum == nil {
		t.Fatal("fetchCheckinStatus returned nil summary")
	}
	return sum
}

// TestCheckinSummary_NoFabricatedStreak 是本次修复的核心回归：
// 任何情况下都不得凭空给出「连续 N 天」。
func TestCheckinSummary_NoFabricatedStreak(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name       string
		body       string
		wantActive bool
		wantToday  bool
	}{
		{
			name: "窗口内已领取",
			body: fmt.Sprintf(`{"showCampaign":true,"campaigns":[{"campaignId":"c1","actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMED","startAt":%d,"endAt":%d,"benefit":{"kind":"CREDITS","amount":100}}]}`,
				now-3600, now+3600),
			wantActive: true,
			wantToday:  true,
		},
		{
			name: "窗口内待领取",
			body: fmt.Sprintf(`{"showCampaign":true,"campaigns":[{"campaignId":"c2","actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMABLE","startAt":%d,"endAt":%d,"benefit":{"kind":"CREDITS","amount":100}}]}`,
				now-3600, now+3600),
			wantActive: true,
			wantToday:  false,
		},
		{
			name:       "只有推广活动",
			body:       `{"showCampaign":true,"campaigns":[{"campaignId":"c3","actionType":"VIEW_DETAILS","claimStatus":"CLAIMABLE"}]}`,
			wantActive: false,
			wantToday:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sum := fetchCheckinStatusWithBody(t, tc.body)
			if sum.Active != tc.wantActive {
				t.Fatalf("Active = %v, want %v", sum.Active, tc.wantActive)
			}
			if sum.TodayCheckedIn != tc.wantToday {
				t.Fatalf("TodayCheckedIn = %v, want %v", sum.TodayCheckedIn, tc.wantToday)
			}
			if sum.StreakDays != 0 {
				t.Fatalf("StreakDays = %d, want 0（上游不提供，禁止编造）", sum.StreakDays)
			}
			if sum.TotalCredits != 0 || sum.WeekCheckinDays != 0 || sum.Season != 0 {
				t.Fatalf("占位字段应为 0，实际 total=%d week=%d season=%d",
					sum.TotalCredits, sum.WeekCheckinDays, sum.Season)
			}
		})
	}
}

// TestCheckinSummary_TodayCreditFromCampaignAmount 锁「今日积分只取活动金额」。
func TestCheckinSummary_TodayCreditFromCampaignAmount(t *testing.T) {
	now := time.Now().Unix()
	sum := fetchCheckinStatusWithBody(t, fmt.Sprintf(
		`{"showCampaign":true,"campaigns":[{"campaignId":"c4","actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMED","startAt":%d,"endAt":%d,"benefit":{"kind":"CREDITS","amount":100}}]}`,
		now-3600, now+3600))
	if sum.TodayCredit != 100 {
		t.Fatalf("TodayCredit = %d, want 100", sum.TodayCredit)
	}
	if sum.DailyCredit != 100 {
		t.Fatalf("DailyCredit = %d, want 100", sum.DailyCredit)
	}
	if sum.CampaignID != "c4" {
		t.Fatalf("CampaignID = %q, want c4", sum.CampaignID)
	}
}

// TestCheckinSummary_ExpiredWindowIsInactive 锁窗口判定不回归：
// 昨日活动在 10:00 换档后应被判为过期，不能挡住今天的新活动。
func TestCheckinSummary_ExpiredWindowIsInactive(t *testing.T) {
	now := time.Now().Unix()
	sum := fetchCheckinStatusWithBody(t, fmt.Sprintf(
		`{"showCampaign":true,"campaigns":[{"campaignId":"c5","actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMABLE","startAt":%d,"endAt":%d,"benefit":{"kind":"CREDITS","amount":100}}]}`,
		now-7200, now-60))
	if sum.Active {
		t.Fatal("过期活动不应被判为可签")
	}
	if sum.TodayCheckedIn {
		t.Fatal("过期活动不应被判为今日已签")
	}
}
