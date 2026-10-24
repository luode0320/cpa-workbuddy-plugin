package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 本文件实现「每日登录积分」的自动领取。
//
// 上游机制（2026-10-24 生产九账号对照实验实证）：
// 扣子个人版每日登录发放 1500 活动积分，有效期仅 1 天，官方文档口径是
// 「登录后发放」，但实测**真正触发发放的是网页端加载积分时调用的
// credit/balance/check 接口**——只调 token beat 续期不发、只访问 /home 也不发，
// 唯一命中的就是 check。该接口无参数、无积分消耗、幂等（同一天连打多次
// 只发一条），因此可以安全地跟随积分刷新循环高频调用。
//
// **响应语义的坑（2026-10-24 全账号复核推翻初版假设）**：
// check 返回的 `data.pass` / `deny_reason` 是「**余额是否够用**」的判定，
// 不是「本次是否发放」。实测 9 个账号中 b3f25e 与 f18297 返回
// `{"pass":false,"deny_reason":1}`，但两者当日「每日登录」条目同样到账
// 且 remain=1500；返回 `pass:true` 的账号也一样是 1500。
// 因此**绝不能凭 pass 判定领取结果**——初版按 pass 判失败会把已经成功的
// 领取显示成「未领」，并让面板长期挂着误导性的失败文案。
// 真实判据只能是「明细里是否存在当天、有余额的每日登录条目」（见
// dailyCreditClaimedOn），前提是 check 已调用过。
//
// 为什么必须由插件做：这些是活动积分、当天不领即归零，而插件此前只调
// balance / balance/detail 两个只读端点，从不触发发放，导致账号池长期
// 0 分（生产实测 9 个账号里 8 个在 10-22 后就没再拿到过每日积分）。

const (
	// dailyCreditRequestTimeout 是单次领取请求超时。
	//
	// 比积分查询短：领取是「顺手做」的动作，失败不影响本轮余额刷新，
	// 不值得为它长时间占住并发槽位。
	dailyCreditRequestTimeout = 8 * time.Second
)

// dailyCreditState 记录各账号最近一次领取结果，供面板展示。
type dailyCreditState struct {
	mu      sync.Mutex
	entries map[string]dailyCreditEntry
}

// dailyCreditEntry 是单账号的领取状态。
type dailyCreditEntry struct {
	// claimedAt 是最近一次确认「今日额度已到账」的时刻，零值表示尚未确认。
	claimedAt time.Time
	// amount 是最近一次确认到的当日额度，0 表示上游未返回该信息。
	amount int64
	// errorText 是最近一次领取失败的原文，空表示无错误。
	//
	// 只记录传输层与业务码失败；上游 pass=false 属于余额判定，
	// 不是失败，绝不写进这里（见文件头）。
	errorText string
	// passHint 记录上游对「余额是否够用」的判定，仅供排查。
	passHint bool
	// attemptedAt 是最近一次尝试的时刻（无论成败）。
	attemptedAt time.Time
}

var dailyCredits = dailyCreditState{entries: map[string]dailyCreditEntry{}}

// dailyCreditAccountKey 返回账号在领取状态表里的键。
//
// 与积分缓存同口径（AuthID 优先、SecUserID 兜底）：两者描述同一个账号，
// 用不同键会让面板读不到闸门用的那份状态。
// [参数] sa: 账号。
// [返回] 状态键；无法派生时返回空串。
func dailyCreditAccountKey(sa *StoredAuth) string {
	if sa == nil {
		return ""
	}
	if key := strings.TrimSpace(sa.AuthID); key != "" {
		return key
	}
	return strings.TrimSpace(sa.SecUserID)
}

// claimDailyCredit 调用上游 check 端点触发当日登录积分发放。
//
// 该端点无论是否发积分都返回 HTTP 200 + code=0，业务结果在 data 里：
//   - {"pass":true}                —— 本日额度已到账（可能是本次触发的，也可能早就有）
//   - {"pass":false,"deny_reason":1} —— 上游拒绝，通常是该账号当日已达上限
//
// 因此「调用成功」不等于「本次新领到」，函数只负责触发与记录，
// 是否真的到账由随后的余额/明细查询确认。
// [参数] client: HTTP 客户端；cookieHeader: 会话 Cookie。
// [返回] 上游 data 段与错误。
func claimDailyCredit(client httpDoer, cookieHeader string) (*dailyCreditResponse, error) {
	req, errReq := http.NewRequest(http.MethodGet, resolveDailyCreditURL(), nil)
	if errReq != nil {
		return nil, fmt.Errorf("构造每日积分请求失败：%w", errReq)
	}
	applyWebHeaders(req, cookieHeader)

	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("每日积分请求失败：%w", errDo)
	}
	defer resp.Body.Close()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, fmt.Errorf("读取每日积分响应失败：%w", errRead)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("每日积分端点返回 HTTP %d", resp.StatusCode)
	}

	var parsed dailyCreditResponse
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return nil, fmt.Errorf("每日积分响应解析失败：%w", errUnmarshal)
	}
	if parsed.Code != 0 {
		return nil, fmt.Errorf("每日积分端点返回 code=%d msg=%s", parsed.Code, parsed.Message)
	}
	return &parsed, nil
}

// claimDailyCreditForAccount 为单个账号领取当日登录积分并记录请求结果。
//
// 注意这里记录的是「请求是否成功」，不是「积分是否到账」：
// 上游 pass 字段与发放无关（见文件头说明），到账事实只能由随后查到的
// 明细确认。因此本函数只负责触发与记录传输层成败，claim 状态由
// confirmDailyCreditBySummary 在拿到明细后落定。
//
// 失败只记录不返回错误给上层：领取是余额刷新的附带动作，
// 一次网络抖动不该让整轮积分刷新失败、把好号的余额也一起丢掉。
// [参数] sa: 账号。
// [返回] 本次请求是否被上游正常受理（HTTP 与业务码均成功）。
func claimDailyCreditForAccount(sa *StoredAuth) bool {
	key := dailyCreditAccountKey(sa)
	if key == "" {
		return false
	}

	resp, errClaim := claimDailyCredit(newUpstreamClient(dailyCreditRequestTimeout), sa.CookieHeader)
	now := time.Now()

	dailyCredits.mu.Lock()
	entry := dailyCredits.entries[key]
	entry.attemptedAt = now
	if errClaim != nil {
		entry.errorText = errClaim.Error()
		dailyCredits.entries[key] = entry
		dailyCredits.mu.Unlock()
		hostLogf("warn", "每日登录积分领取失败", map[string]any{
			"auth_id": sa.AuthID,
			"error":   errClaim.Error(),
		})
		return false
	}

	entry.errorText = ""
	// 记录上游的余额判定，仅供排查：它不是发放结果。
	entry.passHint = resp.Data.Pass
	dailyCredits.entries[key] = entry
	dailyCredits.mu.Unlock()
	return true
}

// confirmDailyCreditBySummary 用刚查到的积分明细确认当日领取结果。
//
// 判据是「明细里存在当天、仍有余额的每日登录条目」——这是唯一可信的
// 到账事实。上游 pass 字段与之无关（实测 pass=false 的账号照样到账），
// 因此不能拿它判定。
//
// 只有在 check 请求本身成功时才做确认：请求失败时明细里的旧条目可能
// 属于前一天，据此标记「今日已领」会让面板与闸门都误判。
// [参数] sa: 账号；summary: 刚查询到的积分摘要（含明细）。
// [返回] 无。
func confirmDailyCreditBySummary(sa *StoredAuth, summary *creditSummary) {
	key := dailyCreditAccountKey(sa)
	if key == "" || summary == nil {
		return
	}

	dailyCredits.mu.Lock()
	entry := dailyCredits.entries[key]
	// 本次请求失败时保留原状态，不据旧明细做确认。
	if entry.errorText != "" {
		dailyCredits.mu.Unlock()
		return
	}
	amount, ok := dailyCreditAmountIn(summary, time.Now())
	if ok {
		entry.claimedAt = time.Now()
		entry.amount = amount
	}
	dailyCredits.entries[key] = entry
	dailyCredits.mu.Unlock()

	// 确认到账后立刻落进快照：内存态在热重载 / 重启后丢失，而当日额度
	// 一旦被消耗光就会从明细里消失，纯明细反推会把已领误报成未领。
	if ok {
		recordDailyClaimedSnapshot(sa.AuthID, amount)
	}
}

// dailyCreditAmountIn 从积分摘要中取出指定自然日的每日登录额度。
//
// 明细条目的标题是「每日登录」（show_name 为「[每日登录]活动积分」），
// 到期时间落在发放当日 23:59:59；因此「到期日等于今天」即当天那笔。
// 余额为 0 的条目是已消耗掉的旧记录，不算今日可用额度。
// [参数] summary: 积分摘要；day: 目标时刻。
// [返回] 当日可用额度与是否存在。
func dailyCreditAmountIn(summary *creditSummary, day time.Time) (int64, bool) {
	if summary == nil {
		return 0, false
	}
	for _, item := range summary.Items {
		if !isDailyCreditTitle(item.Title) || item.Remaining <= 0 {
			continue
		}
		expiredAt, errParse := time.Parse(time.RFC3339, item.ExpiredAt)
		if errParse != nil {
			continue
		}
		// 到期时间在下一天零点前，说明就是当天那笔。
		if sameDayCST(expiredAt, day) {
			return item.Remaining, true
		}
	}
	return 0, false
}

// isDailyCreditTitle 判断明细标题是否属于每日登录奖励。
// [参数] title: 明细标题。
// [返回] 属于每日登录奖励时返回 true。
func isDailyCreditTitle(title string) bool {
	return strings.Contains(title, "每日登录")
}

// dailyCreditSnapshot 返回账号的领取状态副本，供面板读取。
// [参数] authID: 账号标识。
// [返回] 状态副本与是否存在记录。
func dailyCreditSnapshot(authID string) (dailyCreditEntry, bool) {
	dailyCredits.mu.Lock()
	defer dailyCredits.mu.Unlock()
	entry, ok := dailyCredits.entries[authID]
	return entry, ok
}

// dailyCreditClaimedToday 判断账号今日是否已确认拿到登录积分。
//
// 判据是「最近一次成功领取发生在今天」，按北京时间切日：
// 上游的每日额度以自然日（UTC+8）为周期，用 UTC 会把凌晨 8 点前的
// 领取算到前一天，导致面板误报「今天还没领」并重复触发。
//
// 内存无记录时回落到落盘快照：热重载 / 重启会清空内存态，
// 而当日额度可能早已被消耗光、从明细里消失，只看内存会让面板
// 把「已领且已用完」显示成「未领」。
// [参数] authID: 账号标识。
// [返回] 今日已领取时为 true。
// 最近修改时间：2026-10-25；改动原因：内存无记录时回落到持久化快照。
func dailyCreditClaimedToday(authID string) bool {
	if entry, ok := dailyCreditSnapshot(authID); ok && !entry.claimedAt.IsZero() {
		return sameDayCST(entry.claimedAt, time.Now())
	}
	// 回落到快照里的确认时刻。
	if claimedAt, _ := dailyClaimedFromSnapshot(authID); claimedAt != "" {
		parsed, errParse := time.Parse(time.RFC3339, claimedAt)
		if errParse == nil {
			return sameDayCST(parsed, time.Now())
		}
	}
	return false
}

// sameDayCST 判断两个时刻是否落在同一个北京时间的自然日。
//
// 容器镜像里不一定装有 tzdata，不能依赖 time.LoadLocation("Asia/Shanghai")
// （缺库时会回退 UTC，静默算错日期）。北京时间是固定的 UTC+8、无夏令时，
// 直接用固定偏移即可，既准确又不引入外部依赖。
// [参数] a: 时刻一；b: 时刻二。
// [返回] 同一自然日时为 true。
func sameDayCST(a, b time.Time) bool {
	ay, am, ad := a.In(cstZone).Date()
	by, bm, bd := b.In(cstZone).Date()
	return ay == by && am == bm && ad == bd
}

// cstZone 是北京时间固定偏移（UTC+8，无夏令时）。
var cstZone = time.FixedZone("CST", 8*3600)

// resetDailyCreditState 清空领取状态（测试与凭据变更时使用）。
// [参数] 无。
// [返回] 无。
func resetDailyCreditState() {
	dailyCredits.mu.Lock()
	dailyCredits.entries = map[string]dailyCreditEntry{}
	dailyCredits.mu.Unlock()
}

// dailyCreditResponse 是每日积分 check 端点响应。
//
// 字段语义（2026-10-24 复核）：`pass` / `deny_reason` 描述的是
// 「**余额是否够用**」，与本次是否发放无关——实测 pass=false 的账号
// 同样拿到当天 1500 分。它们只用于排查，不参与领取判定。
type dailyCreditResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		// Pass 表示余额检查是否通过（不是发放结果）。
		Pass bool `json:"pass"`
		// DenyReason 是余额检查未通过的原因码，0 表示通过。
		DenyReason int `json:"deny_reason"`
	} `json:"data"`
}
