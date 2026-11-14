package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestCheckinLiveStatus 用真实账号只读探测签到状态与活动赠送额度。
//
// 验证：① 活动端点 query 参数集被上游接受；② chosen_type 判据可读；
// ③ 额度汇总能提取出窗口。默认跳过，设 DOUBAO_LIVE_TEST=1 才执行。
func TestCheckinLiveStatus(t *testing.T) {
	if os.Getenv("DOUBAO_LIVE_TEST") != "1" {
		t.Skip("设 DOUBAO_LIVE_TEST=1 才执行真实上游探测")
	}
	sa := liveAccountForTest(t)
	if sa == nil {
		t.Skip("未提供 .local_cookie.txt（真实 Cookie），跳过签到只读探测")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	detail, errDetail := fetchCampaignDetail(ctx, sa)
	if errDetail != nil {
		t.Fatalf("真实活动详情失败：%v", errDetail)
	}
	if !detail.TaskFound {
		t.Fatalf("真实响应里应含 %s 任务，实际任务数 %d", dailySigninTaskCode, len(detail.Tasks))
	}

	chosenType, errLanding := fetchLandingChosenType(ctx, sa)
	if errLanding != nil {
		t.Fatalf("真实落地页失败：%v", errLanding)
	}

	groups, errQuota := fetchQuotaGroups(ctx, sa)
	if errQuota != nil {
		t.Fatalf("真实额度汇总失败：%v", errQuota)
	}
	t.Logf("真实签到探测：已签=%v 连续=%d 今日额度=%s 渠道=%d 额度组=%d",
		detail.CheckedInToday, detail.ContinuousDays, formatAmount(detail.RewardAmount), chosenType, len(groups))
}
