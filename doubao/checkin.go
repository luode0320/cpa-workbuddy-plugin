package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本文件实现豆包每日签到与活动赠送额度的查询/提交。
//
// 渠道判据（2026-11-14 生产实测 + 真实客户端取证）：落地页 chosen_type=0 走网页签到可成功；
// =3 表示平台把该账号签到入口限定为桌面端，真人与真实客户端点击同样回 710010702，故只在 0 且未签时提交。

const (
	// checkinCampaignGroup 是「免费用豆包工作」签到活动组标识。
	checkinCampaignGroup = "pc_incentive_task_mvp_v3"

	// checkinLandingPath 是活动落地页端点，响应里的 chosen_type 是渠道判据。
	checkinLandingPath = "/alice/ug/campaign/landing/get"

	// checkinDetailPath 是活动详情端点，checked_in_today 是签到结果的唯一可信来源。
	checkinDetailPath = "/alice/ug/campaign/v3/detail/"

	// checkinRunPath 是签到提交端点。
	checkinRunPath = "/alice/ug/campaign/landing/checkin/"

	// quotaSummaryPath 是订阅额度汇总端点，活动赠送额度埋在其中。
	quotaSummaryPath = "/alice/commerce/sale/subscription/quota/summary/"

	// dailySigninTaskCode 是每日签到任务的 campaign_code。
	dailySigninTaskCode = "daily_signin"

	// checkinChosenWeb 表示账号当前被分到网页签到渠道。
	checkinChosenWeb = 0

	// checkinTimeout 是单账号签到链路的整体超时（含提交后的回查）。
	checkinTimeout = 30 * time.Second

	// checkinConfirmDelay 是提交签到后回查详情的等待时间，给上游结算留窗口。
	checkinConfirmDelay = 2 * time.Second

	// checkinConcurrency 是定时轮次的单账号并发数。
	checkinConcurrency = 2
)

// 签到状态取值。面板按它上色，故用稳定英文枚举而非中文句子。
const (
	checkinStateDone        = "done"
	checkinStateReady       = "ready"
	checkinStateDesktopOnly = "desktop_only"
	checkinStateExpired     = "expired"
	checkinStateError       = "error"
)

// checkinHours 是每日签到槽位（本地时区整点）；两轮冗余，重复提交由上游幂等。
var checkinHours = []int{10, 22}

var (
	checkinSchedulerMu sync.Mutex
	checkinSchedulerOn bool
)

// checkinQuotaGroup 是一组额度窗口的汇总。
type checkinQuotaGroup struct {
	// Name 是额度分组名（活动赠送额度等）。
	Name string `json:"name"`
	// Total 是该窗口的总额度。
	Total float64 `json:"total"`
	// Used 是该窗口的已用额度。
	Used float64 `json:"used"`
	// Percent 是已用百分比。
	Percent float64 `json:"percent"`
	// EndTime 是额度到期时间（毫秒时间戳）。
	EndTime int64 `json:"end_time,omitempty"`
}

// campaignTask 是活动详情里的一个任务项。
type campaignTask struct {
	CampaignCode string `json:"campaign_code"`
	TaskCode     string `json:"task_code"`
	TaskStatus   int    `json:"task_status"`
	Title        string `json:"title"`
	// DailyCheckin 只有每日签到任务才带，额度嵌在 reward 对象里。
	DailyCheckin struct {
		CheckedInToday bool `json:"checked_in_today"`
		ContinuousDays int  `json:"continuous_days"`
		TotalDays      int  `json:"total_days"`
		Reward         struct {
			RewardAmount float64 `json:"reward_amount"`
			RewardType   string  `json:"reward_type"`
		} `json:"reward"`
	} `json:"daily_checkin"`
}

// codeOf 返回任务项的 campaign_code，缺失时回落到 task_code。
// [参数] 无。
// [返回] 任务码。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，任务码兼容两种字段名。
func (t campaignTask) codeOf() string {
	if code := strings.TrimSpace(t.CampaignCode); code != "" {
		return code
	}
	return strings.TrimSpace(t.TaskCode)
}

// handleCheckinStatus 返回账号的今日签到状态与活动赠送额度（只读）。
//
// 不做任何写操作，可直接被面板轮询。
// [参数] req: 管理请求；query 参数 auth_index 可只查单账号。
// [返回] {accounts, count, campaign, checked_at}。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，签到状态只读查询入口。
func handleCheckinStatus(req pluginapi.ManagementRequest) map[string]any {
	authIndex := ""
	if vals := req.Query["auth_index"]; len(vals) > 0 {
		authIndex = strings.TrimSpace(vals[0])
	}

	// 1. 单账号查询走同一个探测函数，保证与批量口径一致。
	if authIndex != "" {
		sa, errFind := findAccountForTest(authIndex)
		if errFind != nil {
			return map[string]any{"error": errFind.Error()}
		}
		item := checkinStatusFor(sa)
		return map[string]any{
			"accounts":   []any{item},
			"count":      1,
			"campaign":   checkinCampaignGroup,
			"checked_at": time.Now().Format(time.RFC3339),
		}
	}

	// 2. 批量查询：逐账号串行探测，签到是低频只读动作，无需并发压上游。
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return map[string]any{"error": fmt.Sprintf("读取账号失败：%v", errList)}
	}
	list := make([]any, 0, len(accounts))
	for _, acc := range accounts {
		list = append(list, checkinStatusFor(acc))
	}
	return map[string]any{
		"accounts":   list,
		"count":      len(list),
		"campaign":   checkinCampaignGroup,
		"checked_at": time.Now().Format(time.RFC3339),
	}
}

// handleCheckinRun 对指定账号（或全部账号）执行一次真实签到。
//
// 请求体：{auth_index?}。空表示全部账号；已签到与桌面端渠道的账号直接跳过，
// 不提交签到请求。
// [参数] req: 管理请求。
// [返回] {results, count, campaign, checked_at}。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，手动签到入口。
func handleCheckinRun(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)

	// 1. 定位待签账号：单账号需存在，批量取全部账号。
	var accounts []*StoredAuth
	if authIndex != "" {
		sa, errFind := findAccountForTest(authIndex)
		if errFind != nil {
			return map[string]any{"error": errFind.Error()}
		}
		accounts = []*StoredAuth{sa}
	} else {
		list, errList := listAllAuthFiles()
		if errList != nil {
			return map[string]any{"error": fmt.Sprintf("读取账号失败：%v", errList)}
		}
		accounts = list
	}

	// 2. 逐个执行：跳过禁用账号，其余交给 runCheckinForAccount 判定渠道与幂等。
	results := make([]any, 0, len(accounts))
	for _, acc := range accounts {
		if acc.Disabled {
			results = append(results, map[string]any{
				"auth_id": acc.AuthID,
				"label":   accountLabelOf(acc),
				"state":   "skipped",
				"message": "账号已禁用，跳过签到",
			})
			continue
		}
		results = append(results, runCheckinForAccount(acc))
	}
	return map[string]any{
		"results":    results,
		"count":      len(results),
		"campaign":   checkinCampaignGroup,
		"checked_at": time.Now().Format(time.RFC3339),
	}
}

// accountLabelOf 返回账号的展示名，优先备注再回落昵称与 id。
// [参数] sa: 账号。
// [返回] 展示名。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，结果回显统一命名口径。
func accountLabelOf(sa *StoredAuth) string {
	if label := strings.TrimSpace(sa.Label); label != "" {
		return label
	}
	if name := strings.TrimSpace(sa.ScreenName); name != "" {
		return name
	}
	return sa.AuthID
}

// checkinStatusFor 探测单个账号的签到状态与额度（只读）。
// [参数] sa: 账号。
// [返回] 面板展示项。
// 最近修改时间：2026-11-14 17:52:00；改动原因：判据顺序改为落地页先行，修桌面端账号被误判成活动下线。
func checkinStatusFor(sa *StoredAuth) map[string]any {
	item := map[string]any{
		"auth_id": sa.AuthID,
		"label":   accountLabelOf(sa),
	}

	// 1. 登录态失效直接返回，省掉两次必然失败的上游请求。
	if authStateOf(sa) != "ok" {
		item["state"] = checkinStateExpired
		item["message"] = "登录态已过期，请重新导入 Cookie"
		return item
	}

	ctx, cancel := context.WithTimeout(context.Background(), checkinTimeout)
	defer cancel()

	// 2. 渠道判据先行：桌面端账号的活动详情回 code=2（该账号无 web 活动），
	// 先查详情会把它误判成「活动已下线」，故必须先看落地页 chosen_type。
	chosenType, errLanding := fetchLandingChosenType(ctx, sa)
	if errLanding != nil {
		item["state"] = checkinStateError
		item["message"] = errLanding.Error()
		return item
	}
	item["chosen_type"] = chosenType
	if chosenType != checkinChosenWeb {
		item["state"] = checkinStateDesktopOnly
		item["state_text"] = checkinStateText(checkinStateDesktopOnly)
		return item
	}

	// 3. 网页渠道账号才拉活动详情：它是今日是否已签的唯一可信判据。
	detail, errDetail := fetchCampaignDetail(ctx, sa)
	if errDetail != nil {
		item["state"] = checkinStateError
		item["message"] = errDetail.Error()
		return item
	}

	// 4. 两态判定：已签 → 可签。
	state := checkinStateReady
	if detail.CheckedInToday {
		state = checkinStateDone
	}
	item["state"] = state
	item["state_text"] = checkinStateText(state)
	item["checked_in_today"] = detail.CheckedInToday
	item["continuous_days"] = detail.ContinuousDays
	item["total_days"] = detail.TotalDays
	item["reward_amount"] = detail.RewardAmount
	item["rewarded_total"] = detail.RewardedTotal
	item["tasks"] = detail.Tasks

	// 5. 额度失败不影响签到状态展示，单独降级。
	groups, errQuota := fetchQuotaGroups(ctx, sa)
	if errQuota != nil {
		item["quota_error"] = errQuota.Error()
		return item
	}
	item["quota"] = groups
	return item
}

// checkinStateText 把状态枚举翻成面板文案。
// [参数] state: 状态枚举。
// [返回] 中文说明。
// 最近修改时间：2026-11-14 19:30:00；改动原因：桌面端投放状态文案改为平台投放口径，不再暗示可在桌面端手动领取。
func checkinStateText(state string) string {
	// 1. 枚举到中文文案唯一映射，未知状态回落失败态。
	switch state {
	case checkinStateDone:
		return "今日已签到"
	case checkinStateReady:
		return "今日未签，可签到"
	case checkinStateDesktopOnly:
		return "平台限桌面端"
	case checkinStateExpired:
		return "登录态过期"
	case "skipped":
		return "已跳过"
	}
	return "签到失败"
}

// campaignDetail 是一次活动详情的解析结果。
type campaignDetail struct {
	// TaskFound 表示详情里存在每日签到任务。
	TaskFound bool
	// CheckedInToday 是今日是否已签（唯一可信判据）。
	CheckedInToday bool
	// ContinuousDays 是连续签到天数。
	ContinuousDays int
	// TotalDays 是累计签到天数。
	TotalDays int
	// RewardAmount 是今日签到可得额度。
	RewardAmount float64
	// RewardedTotal 是活动累计已得额度。
	RewardedTotal float64
	// Tasks 是精简后的任务清单，供面板只读展示。
	Tasks []map[string]any
}

// fetchCampaignDetail 拉取活动详情并解析签到任务。
// [参数] ctx: 上下文；sa: 账号。
// [返回] 解析结果与错误。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，活动详情解析。
func fetchCampaignDetail(ctx context.Context, sa *StoredAuth) (*campaignDetail, error) {
	raw, errPost := postCampaignJSON(ctx, sa, checkinDetailPath, map[string]any{
		"campaign_group_code": checkinCampaignGroup,
	})
	if errPost != nil {
		return nil, errPost
	}

	// 1. code 非 0 一律按上游拒绝处理，data 此时通常为 null。
	code := int(jsonNumber(raw, "code"))
	if code != 0 {
		return nil, fmt.Errorf("读取签到活动失败：%s", campaignCodeHint(code))
	}

	// 2. 逐任务找每日签到：它的 daily_checkin 才是签到判据。
	var payload struct {
		Data struct {
			Tasks    []campaignTask `json:"tasks"`
			Progress struct {
				TotalAmount float64 `json:"total_amount"`
			} `json:"progress"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		return nil, fmt.Errorf("解析签到活动失败：%w", errUnmarshal)
	}
	detail := &campaignDetail{RewardedTotal: payload.Data.Progress.TotalAmount}
	for _, task := range payload.Data.Tasks {
		code := task.codeOf()
		detail.Tasks = append(detail.Tasks, map[string]any{
			"campaign_code": code,
			"title":         task.Title,
			"task_status":   task.TaskStatus,
		})
		if code != dailySigninTaskCode {
			continue
		}
		detail.TaskFound = true
		detail.CheckedInToday = task.DailyCheckin.CheckedInToday
		detail.ContinuousDays = task.DailyCheckin.ContinuousDays
		detail.TotalDays = task.DailyCheckin.TotalDays
		detail.RewardAmount = task.DailyCheckin.Reward.RewardAmount
	}
	return detail, nil
}

// fetchLandingChosenType 读取活动落地页的渠道判据 chosen_type。
// [参数] ctx: 上下文；sa: 账号。
// [返回] chosen_type 与错误。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，签到渠道判据读取。
func fetchLandingChosenType(ctx context.Context, sa *StoredAuth) (int, error) {
	// 1. 落地页只读 chosen_type，业务码非 0 说明活动不可用。
	raw, errPost := postCampaignJSON(ctx, sa, checkinLandingPath, map[string]any{
		"scene": checkinCampaignGroup,
	})
	if errPost != nil {
		return 0, errPost
	}
	if code := int(jsonNumber(raw, "code")); code != 0 {
		return 0, fmt.Errorf("读取签到活动落地页失败：%s", campaignCodeHint(code))
	}
	return int(jsonNumber(raw, "data.chosen_type")), nil
}

// fetchQuotaGroups 读取订阅额度汇总里的活动赠送额度。
// [参数] ctx: 上下文；sa: 账号。
// [返回] 额度分组与错误。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，活动赠送额度读取。
func fetchQuotaGroups(ctx context.Context, sa *StoredAuth) ([]checkinQuotaGroup, error) {
	// 1. 先取原始响应并校验业务码，额度失败不影响签到状态判定。
	raw, errPost := postCampaignJSON(ctx, sa, quotaSummaryPath, map[string]any{})
	if errPost != nil {
		return nil, errPost
	}
	if code := int(jsonNumber(raw, "code")); code != 0 {
		return nil, fmt.Errorf("读取额度汇总失败：%s", campaignCodeHint(code))
	}

	// 2. window_limits 层级由上游决定，故解析成通用节点后递归提取。
	var node any
	if errUnmarshal := json.Unmarshal(raw, &node); errUnmarshal != nil {
		return nil, fmt.Errorf("解析额度汇总失败：%w", errUnmarshal)
	}
	groups := make([]checkinQuotaGroup, 0, 2)
	collectQuotaGroups(node, &groups)
	return groups, nil
}

// collectQuotaGroups 递归收集所有 window_limits 数组，分组名取同层 feature_group_name。
// [参数] node: 已解析的 JSON 节点；groups: 收集目标。
// [返回] 无。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，额度窗口递归提取。
func collectQuotaGroups(node any, groups *[]checkinQuotaGroup) {
	switch v := node.(type) {
	case map[string]any:
		// 1. 当前层有 window_limits 就直接读，不再往下钻。
		if raw, ok := v["window_limits"].([]any); ok {
			name, _ := v["feature_group_name"].(string)
			for _, item := range raw {
				window, okWindow := item.(map[string]any)
				if !okWindow {
					continue
				}
				*groups = append(*groups, checkinQuotaGroup{
					Name:    name,
					Total:   numberValue(window["total_amount"]),
					Used:    numberValue(window["used_amount"]),
					Percent: numberValue(window["used_percent"]),
					EndTime: int64(numberValue(window["end_time"])),
				})
			}
			return
		}
		// 2. 否则继续下钻，层级由上游决定。
		for _, child := range v {
			collectQuotaGroups(child, groups)
		}
	case []any:
		for _, child := range v {
			collectQuotaGroups(child, groups)
		}
	}
}

// runCheckinForAccount 对单个账号执行一次签到并回查确认。
//
// 硬约束：只在 chosen_type=0 且今日未签时提交；平台限定桌面端的账号只上报不提交。
// [参数] sa: 账号。
// [返回] 面板展示项（含 state 与 message）。
// 最近修改时间：2026-11-14 19:30:00；改动原因：桌面端拒绝口径改为平台投放策略，不再引导用户去桌面端点击。
func runCheckinForAccount(sa *StoredAuth) map[string]any {
	item := map[string]any{
		"auth_id": sa.AuthID,
		"label":   accountLabelOf(sa),
	}

	// 1. 登录态失效与未启用渠道直接返回，不产生写请求。
	if authStateOf(sa) != "ok" {
		item["state"] = checkinStateExpired
		item["message"] = "登录态已过期，请重新导入 Cookie"
		return item
	}

	ctx, cancel := context.WithTimeout(context.Background(), checkinTimeout)
	defer cancel()

	// 2. 渠道判据先行：桌面端账号的活动详情回 code=2，先查详情会被误判成活动下线。
	chosenType, errLanding := fetchLandingChosenType(ctx, sa)
	if errLanding != nil {
		item["state"] = checkinStateError
		item["message"] = errLanding.Error()
		return item
	}
	if chosenType != checkinChosenWeb {
		item["state"] = checkinStateDesktopOnly
		item["message"] = "平台把该账号的签到入口限定为豆包桌面端，网页与客户端页内点击均被拒（实测真人点击同样回 710010702），插件侧无可用通道"
		item["chosen_type"] = chosenType
		return item
	}

	// 3. 网页渠道账号先看今日是否已签，已签不再发写请求。
	before, errBefore := fetchCampaignDetail(ctx, sa)
	if errBefore != nil {
		item["state"] = checkinStateError
		item["message"] = errBefore.Error()
		return item
	}
	if before.CheckedInToday {
		item["state"] = checkinStateDone
		item["message"] = fmt.Sprintf("今日已签到（连续 %d 天，已得额度 %s）", before.ContinuousDays, formatAmount(before.RewardedTotal))
		item["continuous_days"] = before.ContinuousDays
		item["rewarded_total"] = before.RewardedTotal
		return item
	}

	// 4. 提交签到：回执只作参考，最终仍以详情回查为准。
	raw, errRun := postCampaignJSON(ctx, sa, checkinRunPath, map[string]any{
		"scene": checkinCampaignGroup,
	})
	if errRun != nil {
		item["state"] = checkinStateError
		item["message"] = errRun.Error()
		return item
	}
	if code := int(jsonNumber(raw, "code")); code != 0 {
		item["state"] = checkinStateError
		item["message"] = campaignCodeHint(code)
		return item
	}

	// 5. 回查确认：单个 code=0 不等于到账，必须看到 checked_in_today=true。
	time.Sleep(checkinConfirmDelay)
	after, errAfter := fetchCampaignDetail(ctx, sa)
	if errAfter != nil {
		item["state"] = checkinStateError
		item["message"] = fmt.Sprintf("签到已提交，但回查失败：%v", errAfter)
		return item
	}
	item["continuous_days"] = after.ContinuousDays
	item["rewarded_total"] = after.RewardedTotal
	if !after.CheckedInToday {
		item["state"] = checkinStateError
		item["message"] = "签到已提交但上游未确认到账，请稍后重新查询"
		return item
	}
	item["state"] = checkinStateDone
	item["message"] = fmt.Sprintf("签到成功（连续 %d 天，今日额度 %s，累计已得 %s）",
		after.ContinuousDays, formatAmount(before.RewardAmount), formatAmount(after.RewardedTotal))
	log.Printf("[doubao] checkin success for %s (continuous=%d, rewarded_total=%s)",
		sa.AuthID, after.ContinuousDays, formatAmount(after.RewardedTotal))
	return item
}

// postCampaignJSON 向活动端点发一次 JSON POST，query 与对话链路完全一致。
// [参数] ctx: 上下文；sa: 账号；path: 端点路径；payload: 请求体对象。
// [返回] 上游响应字节与错误。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，活动端点统一调用入口。
func postCampaignJSON(ctx context.Context, sa *StoredAuth, path string, payload any) ([]byte, error) {
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("编码签到请求失败：%w", errMarshal)
	}
	return postBusinessJSON(ctx, sa, path, raw)
}

// campaignCodeHint 把活动接口业务码翻成可操作的中文提示。
// [参数] code: 上游业务码。
// [返回] 中文提示。
// 最近修改时间：2026-11-14 19:30:00；改动原因：710010702 定性为平台侧投放拒绝，不再表述成可换渠道自救。
func campaignCodeHint(code int) string {
	// 1. 只覆盖已知可操作码，其余原样回显业务码便于排查。
	switch code {
	case 2:
		return "签到活动已下线或已换季"
	case 710012001:
		return "登录态已失效，请在面板重新导入 Cookie"
	case 710010702:
		return "平台按账号投放策略拒绝了本次签到（真人点击与真实客户端同样被拒），插件侧无可用通道"
	}
	return fmt.Sprintf("上游返回业务码 %d", code)
}

// jsonNumber 读取 JSON 里的数值字段，缺失或类型不符时返回 0。
// [参数] raw: 原始 JSON；path: 点分隔的字段路径。
// [返回] 数值。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，数值字段安全读取。
func jsonNumber(raw []byte, path string) float64 {
	// 1. 解析失败一律回落 0，调用方按缺字段处理。
	var node any
	if json.Unmarshal(raw, &node) != nil {
		return 0
	}

	// 2. 按点分路径逐层下钻，任一层不是对象即视为缺失。
	current := node
	for _, key := range strings.Split(path, ".") {
		object, okObject := current.(map[string]any)
		if !okObject {
			return 0
		}
		current = object[key]
	}
	return numberValue(current)
}

// numberValue 把数值统一转成 float64，兼容上游返回数字或数字字符串两种形态。
// [参数] v: 原始值。
// [返回] 数值；无法解析时为 0。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，额度字段双形态兼容。
func numberValue(v any) float64 {
	// 1. 数字与数字字符串都要认，字符串先去掉千分位与货币符号。
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		value, _ := n.Float64()
		return value
	case string:
		cleaned := strings.NewReplacer(",", "", " ", "", "¥", "", "$", "").Replace(strings.TrimSpace(n))
		value, errParse := strconv.ParseFloat(cleaned, 64)
		if errParse != nil {
			return 0
		}
		return value
	}
	return 0
}

// formatAmount 把额度数值格式化成不带多余小数的文本。
// [参数] value: 额度。
// [返回] 展示文本。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，额度展示口径统一。
func formatAmount(value float64) string {
	if value == float64(int64(value)) {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'f', 2, 64)
}

// ensureCheckinScheduler 幂等启动签到定时器；不提供停止函数（c-shared 拆卸期触碰 sync 会 SIGSEGV）。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，签到定时器启动入口。
func ensureCheckinScheduler() {
	// 1. 加锁判在位，保证重复 reconfigure 只起一个循环。
	checkinSchedulerMu.Lock()
	defer checkinSchedulerMu.Unlock()
	if checkinSchedulerOn {
		return
	}

	// 2. 标记后异步起循环，避免阻塞配置就绪。
	checkinSchedulerOn = true
	go checkinSchedulerLoop()
}

// nextCheckinTime 返回下一个签到槽位。
// [参数] now: 当前时间。
// [返回] 严格晚于 now 的槽位时刻。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，签到槽位排期计算。
func nextCheckinTime(now time.Time) time.Time {
	// 1. 遍历槽位取严格晚于当前的最早时刻，跨日自动顺延。
	var earliest time.Time
	for _, hour := range checkinHours {
		slot := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
		if !slot.After(now) {
			slot = slot.Add(24 * time.Hour)
		}
		if earliest.IsZero() || slot.Before(earliest) {
			earliest = slot
		}
	}
	return earliest
}

// checkinSchedulerLoop 按槽位唤醒并执行一轮全账号签到。
// [参数] 无（阻塞循环）。
// [返回] 无。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，签到定时循环。
func checkinSchedulerLoop() {
	for {
		// 1. 睡到下一个槽位；定时器只用于排期，不承载取消语义。
		timer := time.NewTimer(time.Until(nextCheckinTime(time.Now())))
		<-timer.C

		// 2. 单轮失败只记日志，不退出循环，否则一次上游抖动会终止整天的定时。
		if errRun := runAutoCheckin(); errRun != nil {
			log.Printf("[doubao] scheduled checkin round failed: %v", errRun)
		}
	}
}

// runAutoCheckin 执行一轮全账号签到。
// [参数] 无。
// [返回] 读取账号失败时的错误；单账号失败已计入结果。
// 最近修改时间：2026-11-14 17:10:00；改动原因：新增，定时轮次执行。
func runAutoCheckin() error {
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return errList
	}

	// 1. 并发上限固定，避免账号规模上来后把上游打成尖峰。
	sem := make(chan struct{}, checkinConcurrency)
	var wg sync.WaitGroup
	for _, acc := range accounts {
		if acc.Disabled {
			continue
		}
		wg.Add(1)
		go func(target *StoredAuth) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			result := runCheckinForAccount(target)
			log.Printf("[doubao] scheduled checkin %s state=%v message=%v",
				target.AuthID, result["state"], result["message"])
		}(acc)
	}
	wg.Wait()
	return nil
}
