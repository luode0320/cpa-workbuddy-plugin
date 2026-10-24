package main

import (
	"fmt"
	"strings"
)

// 本文件集中声明扣子上游端点与协议常量。
//
// 扣子网页端迭代频繁，所有路径、请求头、判定口径都收敛到本文件，
// 改版时只需改这一处；各调用点不再散落字符串字面量。

const (
	// cozeSiteHost 是网页端主域，业务与护照接口都在此域下。
	cozeSiteHost = "https://www.coze.cn"

	// tokenBeatPath 是令牌双向换发端点。
	//
	// 双向契约（2026-10-17 真实账号实测）：
	//   - 带会话 Cookie 调用 → 响应头返回 x-tt-token（Set-Cookie 0 条）；
	//   - 带 x-tt-token 请求头调用（不带 Cookie）→ 返回 13 条 Set-Cookie。
	// 因此它同时承担「首次授权换 Cookie」与「滚动续期」两个职责。
	tokenBeatPath = "/passport/token/beat/v2/"

	// accountInfoPath 是登录态判据端点。
	//
	// 判据口径：message == "success" 且 data.sec_user_id 非空。
	// 实测该端点对无会话请求返回 message == "error" 且无 sec_user_id，
	// 区分度足够；不像豆包的 /alice/basic/launch 那样对未登录也返回 code:0。
	accountInfoPath = "/passport/account/info/v2/"

	// tokenBeatSDKVersion 是令牌端点要求的 SDK 版本头值，缺失会被拒。
	tokenBeatSDKVersion = "2"

	// webUserAgent 是网页端 UA，护照接口按 UA 做基础风控。
	webUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

	// tokenBeatContentType 是令牌端点接受的 Content-Type。
	//
	// 必须精确为 application/json：本仓库豆包插件的 IM 网关要求
	// application/json; encoding=utf-8，两者不同，不要互相套用。
	tokenBeatContentType = "application/json"

	// authErrorCodeSessionExpired 是令牌端点「会话过期」的业务错误码。
	//
	// 该端点无论成败都返回 HTTP 200，必须读 data.error_code 判定。
	authErrorCodeSessionExpired = 401

	// modelListPath 是模型目录端点。
	//
	// 契约（2026-10-18 真实账号实测）：POST，body 里 model_type 必须是
	// **字符串**且只能取枚举值，传数字会返回 InvalidRequest：
	//   - "llm"              → 3 条对话模型（doubao-seed-2-0-pro/lite/mini）
	//   - "image_generation" → 2 条生图模型
	//   - "video_generation" → 1 条生视频模型
	//   - "tts" / "asr" / "embedding" 各 1 条
	// 未知枚举值不报错，只返回空列表——因此逐类型探测是安全的。
	modelListPath = "/v1/model/list"

	// multimodalCatalogPath 是多模态模型目录端点。
	//
	// 比 modelListPath 更完整：返回能力分组（image_gen / generate_video），
	// 每组带 default_model_name 与模型列表。生视频模型（Seedance 2.5/2.0、
	// MiniMax H3）只在这里出现，model_list 的 video_generation 只有 1 条。
	multimodalCatalogPath = "/api/coze_claw/resource/get_multimodal_model_catalog"

	// creditBalancePath 是积分余额端点。
	//
	// 契约（2026-10-18 真实账号实测）：GET，无需任何 query 即返回
	// {"data":{"items":[{"remaining":3000,"source":24,"total":3000}],
	//   "total_balance":3000,"total_remain":3000}}。
	// 带上 account_type 会返回 788770703（下游调用失败），因此**不要传参数**。
	creditBalancePath = "/api/marketplace/trade/credit/balance"

	// creditDetailPath 是积分逐笔明细端点（含过期时间）。
	//
	// 该端点必须带 account_type，且只有 account_type=2 有数据：
	//   - 0 / 不传 → 788770202 Invalid parameter
	//   - 1        → 成功但 items 为空（个人空间下无数据）
	//   - 2        → 返回逐笔明细，每笔含 remaining / total / expired_at /
	//                source / show_name / display_status
	// display_status=2 表示「即将过期」，与面板「1,500 积分即将过期」对应。
	creditDetailPath = "/api/marketplace/trade/credit/balance/detail"

	// creditDetailAccountType 是明细端点唯一有数据的 account_type 取值。
	creditDetailAccountType = "2"

	// creditDetailDisplayExpiring 是「即将过期」的 display_status 取值。
	creditDetailDisplayExpiring = "2"

	// creditDetailPageSize 是明细单页条数。
	creditDetailPageSize = "100"

	// dailyCreditCheckPath 是触发每日登录积分发放的端点。
	//
	// 契约（2026-10-24 生产九账号对照实验实证）：GET，无需参数，无积分消耗。
	// 响应恒为 HTTP 200 + code=0，业务结果在 data：
	//   - {"pass":true}                 → 当日 1500 活动积分已发放
	//   - {"pass":false,"deny_reason":1} → 上游拒绝（当日已达上限等）
	//
	// 关键事实：这是**唯一**能触发每日登录积分发放的入口。只调 token beat
	// 续期不发、只访问 /home 不发、activity/info 与 incentive/user_task/list
	// 都不发；网页端也是在加载积分页时调它才拿到当天额度。
	// 幂等：同日连打多次只发一条，可跟随积分刷新周期高频调用。
	dailyCreditCheckPath = "/api/marketplace/trade/credit/balance/check"

	// dailyCreditAccountType 是领取端点携带的账户类型。
	//
	// 与明细端点同口径取 2（个人空间）；实测不带该参数也能触发发放，
	// 显式携带是为了与网页端行为一致，避免上游按默认类型判定后不发放。
	dailyCreditAccountType = "2"
)

// tokenBeatURL 返回令牌端点的完整 URL。
// [参数] 无。
// [返回] 完整 URL。
// 最近修改时间：2026-10-18；改动原因：新增，端点集中出口。
func tokenBeatURL() string {
	return cozeSiteHost + tokenBeatPath
}

// accountInfoURL 返回登录态判据端点的完整 URL。
//
// 实测无需任何 query 参数：带上会话 Cookie 即返回完整 data，
// 与豆包 /passport/account/info/v2/ 需要 device_platform / aid 等常量块不同。
// [参数] 无。
// [返回] 完整 URL。
// 最近修改时间：2026-10-18；改动原因：新增，端点集中出口。
func accountInfoURL() string {
	return cozeSiteHost + accountInfoPath
}

// modelListURL 返回模型目录端点的完整 URL。
// [参数] 无。
// [返回] 完整 URL。
// 最近修改时间：2026-10-18；改动原因：新增，模型发现端点出口。
func modelListURL() string {
	return cozeSiteHost + modelListPath
}

// multimodalCatalogURL 返回多模态模型目录端点的完整 URL。
// [参数] 无。
// [返回] 完整 URL。
// 最近修改时间：2026-10-18；改动原因：新增，生视频模型来源。
func multimodalCatalogURL() string {
	return cozeSiteHost + multimodalCatalogPath
}

// tokenBeatResponse 是令牌端点响应体。
type tokenBeatResponse struct {
	Message string `json:"message"`
	Data    struct {
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	} `json:"data"`
}

// isSuccess 判断响应是否为业务成功。
//
// 该端点始终返回 HTTP 200：未授权时 body 为
// {"message":"error","data":{"error_code":401,"description":"会话过期，请重新登录"}}，
// 只看状态码会把失败判成成功（2026-10-17 实测）。
// [参数] r: 解析后的响应。
// [返回] 成功时返回 true。
// 最近修改时间：2026-10-18；改动原因：新增，业务成败判定。
func (r *tokenBeatResponse) isSuccess() bool {
	if r == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Message), "success") && r.Data.ErrorCode == 0
}

// describe 把失败响应转成可展示的错误说明。
// [参数] r: 解析后的响应。
// [返回] 错误说明；成功响应返回空串。
// 最近修改时间：2026-10-18；改动原因：新增，错误文案统一。
func (r *tokenBeatResponse) describe() string {
	if r == nil {
		return "上游无响应"
	}
	desc := strings.TrimSpace(r.Data.Description)
	if desc != "" {
		return desc
	}
	return fmt.Sprintf("上游返回 message=%q error_code=%d", r.Message, r.Data.ErrorCode)
}

// accountInfoResponse 是登录态判据端点的响应体。
type accountInfoResponse struct {
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

// isSuccess 判断判据是否通过（message 为 success 且 sec_user_id 非空）。
// [参数] r: 解析后的响应。
// [返回] 通过时返回 true。
// 最近修改时间：2026-10-18；改动原因：新增，登录态判据。
func (r *accountInfoResponse) isSuccess() bool {
	if r == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(r.Message), "success") {
		return false
	}
	return strings.TrimSpace(r.secUserID()) != ""
}

// secUserID 提取 data.sec_user_id。
// [参数] 无。
// [返回] 账号句柄；缺失时返回空串。
// 最近修改时间：2026-10-18；改动原因：新增，账号标识来源。
func (r *accountInfoResponse) secUserID() string {
	if r == nil || r.Data == nil {
		return ""
	}
	v, _ := r.Data["sec_user_id"].(string)
	return strings.TrimSpace(v)
}

// userID 提取 data.user_id（数字型，JSON 解码为 float64）。
// [参数] 无。
// [返回] 数字用户 ID；缺失时为 0。
// 最近修改时间：2026-10-18；改动原因：新增，账号展示字段。
func (r *accountInfoResponse) userID() int64 {
	if r == nil || r.Data == nil {
		return 0
	}
	if v, ok := r.Data["user_id"].(float64); ok {
		return int64(v)
	}
	return 0
}

// screenName 提取 data.screen_name。
// [参数] 无。
// [返回] 昵称；缺失时返回空串。
// 最近修改时间：2026-10-18；改动原因：新增，账号展示字段。
func (r *accountInfoResponse) screenName() string {
	if r == nil || r.Data == nil {
		return ""
	}
	v, _ := r.Data["screen_name"].(string)
	return strings.TrimSpace(v)
}

// mobile 提取 data.mobile（脱敏手机号）。
// [参数] 无。
// [返回] 手机号；缺失时返回空串。
// 最近修改时间：2026-10-18；改动原因：新增，账号展示字段。
func (r *accountInfoResponse) mobile() string {
	if r == nil || r.Data == nil {
		return ""
	}
	v, _ := r.Data["mobile"].(string)
	return strings.TrimSpace(v)
}

// creditBalanceURL 返回积分余额端点的完整 URL。
// [参数] 无。
// [返回] 完整 URL。
// 最近修改时间：2026-10-18；改动原因：新增，积分余额出口。
func creditBalanceURL() string {
	return cozeSiteHost + creditBalancePath
}

// creditDetailURL 返回积分明细端点的完整 URL（含 account_type=2）。
//
// account_type 直接拼进 URL：该端点只有 account_type=2 有数据，
// 没有其它合法取值需要保留成参数。
// [参数] 无。
// [返回] 完整 URL。
// 最近修改时间：2026-10-18；改动原因：新增，积分明细出口。
func creditDetailURL() string {
	return cozeSiteHost + creditDetailPath + "?account_type=" + creditDetailAccountType
}

// dailyCreditCheckURL 返回每日登录积分领取端点的完整 URL。
// [参数] 无。
// [返回] 完整 URL。
// 最近修改时间：2026-10-24；改动原因：新增，每日积分领取出口。
func dailyCreditCheckURL() string {
	return cozeSiteHost + dailyCreditCheckPath + "?account_type=" + dailyCreditAccountType
}

// dailyCreditCheckOverride 是领取端点的测试覆盖点；非空时优先使用。
//
// 生产路径下始终为空，端点由常量拼装。
var dailyCreditCheckOverride string

// resolveDailyCreditURL 返回实际使用的领取端点。
// [参数] 无。
// [返回] 端点 URL。
// 最近修改时间：2026-10-24；改动原因：新增，测试覆盖入口。
func resolveDailyCreditURL() string {
	if dailyCreditCheckOverride != "" {
		return dailyCreditCheckOverride
	}
	return dailyCreditCheckURL()
}

// creditBalanceResponse 是积分余额端点响应。
type creditBalanceResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		// TotalBalance 是额度池总量，TotalRemain 是当前可用余量。
		TotalBalance int64 `json:"total_balance"`
		TotalRemain  int64 `json:"total_remain"`
		Items        []struct {
			Remaining int64 `json:"remaining"`
			Total     int64 `json:"total"`
			Source    int64 `json:"source"`
		} `json:"items"`
	} `json:"data"`
}

// creditDetailResponse 是积分明细端点响应。
type creditDetailResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Items   []creditDetailItem `json:"items"`
		HasMore bool               `json:"has_more"`
	} `json:"data"`
}

// creditDetailItem 是单笔积分明细。
type creditDetailItem struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	ShowName      string `json:"show_name"`
	Remaining     int64  `json:"remaining"`
	Total         int64  `json:"total"`
	Source        int64  `json:"source"`
	ExpiredAt     int64  `json:"expired_at"`
	DisplayStatus int    `json:"display_status"`
}
