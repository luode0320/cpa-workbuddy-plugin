package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// 本文件实现每日登录积分的面板侧契约：领取状态视图与手动补领入口。
//
// 领取内核在 daily_credit.go，这里只负责把结果翻译成管理 API 与面板
// 需要的形状；拆开是因为 management.go 已近 500 行，继续堆放会让
// 「每日积分」这条链路被淹没在账号管理的大文件里。

// dailyCreditView 是面板可见的领取状态。
//
// 只暴露结论与时间，不暴露内部错误栈：面板要回答的是
// 「今天这笔领没领到」，失败原因的完整原文留在插件日志里。
type dailyCreditView struct {
	// ClaimedToday 表示今日已确认拿到登录积分。
	ClaimedToday bool `json:"claimed_today"`
	// ClaimedAt 是最近一次确认领取的时间（RFC3339，北京时间）。
	ClaimedAt string `json:"claimed_at,omitempty"`
	// Amount 是确认到账的当日额度，0 表示上游未返回该信息。
	Amount int64 `json:"amount,omitempty"`
	// Error 是最近一次领取失败的简述。
	Error string `json:"error,omitempty"`
}

// buildDailyCreditView 组装账号的领取状态视图。
//
// 从未尝试过领取时返回 nil：面板据此显示「未知」而不是「今天没领」，
// 否则刚热重载、循环还没跑第一轮时用户会误以为功能失效。
//
// 内存无记录但快照里有确认时刻时仍要出视图：热重载会清空内存态，
// 而当日额度可能早已用完、从明细里消失，返回 nil 会让面板显示「未知」。
// [参数] authID: 账号标识。
// [返回] 视图；内存与快照都无记录时返回 nil。
// 最近修改时间：2026-10-25；改动原因：补快照回落，避免热重载后显示未知。
func buildDailyCreditView(authID string) *dailyCreditView {
	entry, ok := dailyCreditSnapshot(authID)
	if !ok {
		// 回落到快照里的签到确认记录。
		claimedAt, amount := dailyClaimedFromSnapshot(authID)
		if claimedAt == "" {
			return nil
		}
		view := &dailyCreditView{
			ClaimedToday: dailyCreditClaimedToday(authID),
			ClaimedAt:    claimedAt,
			Amount:       amount,
		}
		return view
	}
	view := &dailyCreditView{
		ClaimedToday: dailyCreditClaimedToday(authID),
		Error:        entry.errorText,
		Amount:       entry.amount,
	}
	if !entry.claimedAt.IsZero() {
		view.ClaimedAt = entry.claimedAt.In(cstZone).Format(time.RFC3339)
	}
	return view
}

// handleClaimDaily 立即为指定账号领取当日登录积分。
//
// 与后台循环里的自动领取共用同一条链路，区别只是「跳过调度、立刻执行」：
// 面板的补领按钮与当天的首次排查都靠它，不必等下一个刷新周期。
// 领取结果与随后刷新到的余额一起返回，用户点完就能看到数字变化。
// [参数] body: 含 auth_id 的请求体。
// [返回] 管理响应。
// 最近修改时间：2026-10-24；改动原因：新增，面板手动补领入口。
func handleClaimDaily(body []byte) ([]byte, error) {
	authID := extractAuthID(body)
	if authID == "" {
		return makeHTTPResponse(http.StatusBadRequest, []byte(`{"error":"auth_id 不能为空"}`))
	}
	sa, errFind := findAccountForTest(authID)
	if errFind != nil {
		return makeHTTPResponse(http.StatusNotFound, []byte(fmt.Sprintf(`{"error":%q}`, errFind.Error())))
	}

	accepted := claimDailyCreditForAccount(sa)
	// 领取后强制刷新积分：不刷新的话返回给面板的仍是领取前的旧余额，
	// 用户会以为按钮没生效。同时这是唯一能确认「到账」的时点——
	// 上游 check 响应不携带发放结果（见 daily_credit.go）。
	summary, errFetch := cachedCreditSummary(sa, true)
	if errFetch == nil {
		confirmDailyCreditBySummary(sa, summary)
	}

	claimed := dailyCreditClaimedToday(dailyCreditAccountKey(sa))
	payload := map[string]any{
		"ok":      accepted,
		"claimed": claimed,
		"message": dailyCreditClaimMessage(accepted, claimed),
	}
	if summary != nil {
		payload["remain"] = summary.Remain
		payload["total"] = summary.Total
	}
	if errFetch != nil {
		payload["error"] = errFetch.Error()
	}
	encoded, _ := json.Marshal(payload)
	return makeHTTPResponse(http.StatusOK, encoded)
}

// dailyCreditClaimMessage 生成面向用户的领取结果说明。
//
// 三种情况必须区分：请求失败、请求成功但明细未确认到账、已确认到账。
// 注意不能用上游 pass 判定——它描述余额是否够用，与发放无关。
// [参数] accepted: 请求是否成功；claimed: 明细是否确认到账。
// [返回] 结果说明。
// 最近修改时间：2026-10-24；改动原因：新增，领取结果文案统一。
func dailyCreditClaimMessage(accepted, claimed bool) string {
	switch {
	case claimed:
		return "当日登录积分已到账"
	case !accepted:
		return "领取请求失败，请查看插件日志"
	default:
		return "领取请求已发送，但明细未确认到账"
	}
}
