package main

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"
)

// 本文件集中定义豆包上游协议的常量与 query 参数集构造。
//
// 协议事实来源（2026-10-17 初版）：Lxapk/DouBao-CPA-Plugin 的 protocol.go 源码 +
// 本仓库浏览器实测网络日志交叉验证。关键结论：
//   - 业务 API 必须携带完整 query 参数集，缺参会返回 {"code":0,"data":null}；
//   - `/chat/completion` 必须携带 msToken 与 a_bogus 两个风控签名参数。
//     2026-11-15 控制实验（同一 URL/body，逐项增删）确认：两者都缺回
//     710022002，只缺 a_bogus 回 710022004，填随机值等同缺失；msToken 取自
//     页面 localStorage.xmst（7 天有效），a_bogus 每次请求现算（约 60~120 秒
//     窗口）。签名实现在 sign.go，用内嵌 bdms-sdk 在进程内生成。
//     旧记载「上游接受无签名调用」已证伪：当时样本账号恰好被投放为免签名。
//   - IM 系端点（/im/*）的 Content-Type 必须是 "application/json; encoding=utf-8"；
//     而 /chat/completion 相反，须用纯 "application/json"。
//
//   - 设备指纹是更前置的判据（2026-11-15 四象限实测）：`fp` 必须是账号在浏览器里
//     真实用过的 `s_v_web_id`，自己拼的假值一律回 710022004；真实 fp 下不带签名也能
//     出字，因此上面那条「签名必需」只在假 fp 的前提下成立，签名在真实 fp 下的独立
//     影响尚未分离。采集链路见 login.go 的 callbackSVWebIDHeader，读取入口见
//     fingerprintOf。
//
//   - 工作模式（agent_mode=1）整条协议已随「只做视频」删除（2026-11-17）：
//     该管线依赖账号被投放 pc_incentive_task_mvp_v3 活动，且必须做会话续接与
//     异步产物回灌，与视频的产物模型无关。删掉的常量与 atoiSafe
//     只服务那条管线，任何一处都不再被引用。
//
// 最近修改时间：2026-11-17 21:40:00；改动原因：视频四个参数降为缺省值，
// 新增 videoParams 与归一函数，供首轮提示词与确认轮共用同一套取值。

const (
	// webUserAgent 是模拟网页端请求的 User-Agent。
	//
	// 主版本号必须与内置签名 SDK 的口径一致：签名算法把它们编码进 a_bogus，
	// 与请求头版本不一致时上游按「协议不符」直接回 -1（2026-11-16 实测：
	// 改成 Chrome/157 + 3.45.7 后两个账号同时从可用变为 -1）。
	webUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

	// doubaoAID 是豆包网页端的应用标识。
	doubaoAID = "497858"

	// doubaoVersionCode 是客户端版本编码。
	doubaoVersionCode = "20800"

	// doubaoPCVersion 是网页端版本号，必须与内置签名 SDK 口径一致。
	doubaoPCVersion = "3.41.4"

	// defaultBotID 是豆包默认助手 ID（realm 级常量）。
	// 网页客户端从 /alice/basic/launch 响应的 assistant_bot_id 获取，
	// 此处作为兜底常量；凭据校验成功后可被上游返回值覆盖。
	defaultBotID = "7338286299411103781"

	// accountInfoSDKVersion 是判据端点使用的账号 SDK 版本。
	accountInfoSDKVersion = "2.2.10"

	// contentTypeJSON 是 /chat/completion 唯一接受的 Content-Type。
	//
	// 该端点与 /im/* 族相反：/im/* 要求 "application/json; encoding=utf-8"，
	// 而 /chat/completion 只接受纯 "application/json"；带 encoding 参数会被
	// 上游按参数错误拒绝（实测 710020202）。
	contentTypeJSON = "application/json"

	// contentTypeIM 是 /im/* 族端点要求的 Content-Type。
	//
	// 与 contentTypeJSON 相反：/im/* 必须显式声明 encoding=utf-8，缺了会被上游
	// 以 712012002「不支持编码类型」直接拒绝（2026-11-17 拉消息链时实测）。
	contentTypeIM = "application/json; encoding=utf-8"

	// blockTypeText 是消息文本块类型（BlockType.java: BLOCK_TYPE_TEXT）。
	blockTypeText = 10000

	// blockTypeDeepThink 是深度思考块类型。
	blockTypeDeepThink = 10040

	// blockTypeCreation 是生成结果块类型（视频）。
	blockTypeCreation = 2074

	// skillVideoGen 是视频生成能力的上游 skill id。
	skillVideoGen = 5

	// abilityTypeVideoGen 是 chat_ability.ability_type 的视频生成取值。
	//
	// 视频确认轮靠它把「一段确认文本」升级成异步视频任务；首轮没有该字段，
	// 因此只回参数确认、不产生任何产物（2026-11-17 抓包对比确认）。
	abilityTypeVideoGen = 17

	// videoConfirmModel 是视频确认轮提交的模型代号。
	//
	// 真实网页把它写在首轮确认文本里，次轮原样回传。本版不让调用方指定：
	// 换模型属于能力变更，要先确认账号被投放了哪个模型，不在当前范围。
	videoConfirmModel = "seedance_v2.0_mini"

	// 视频参数的缺省值：调用方不传时按这一套处理，与 0.1.27 行为一致。
	defaultVideoRatio      = "auto"
	defaultVideoResolution = "720p"
	defaultVideoDuration   = 10

	// videoDurationMin / videoDurationMax 界定合法的目标时长（秒），越界按缺省处理。
	// deferral: 上界取实测最长档 15 秒；待 v2.5 多模型时再建规格矩阵（FEAS-004）。
	videoDurationMin = 1
	videoDurationMax = 15
)

// videoParams 是一次视频请求归一后的参数集合。
//
// 为什么要归一而不是直接透传：这三个字段是可选项，缺省值、空串、越界值都要
// 收敛成同一套可比较的值，否则「首轮提示词」与「确认轮 chat_ability」很容易
// 各写一份判断而出现分歧——0.1.27 的偏差正是这么来的（上游对用户承诺 5 秒，
// 确认轮却恒提交 10 秒）。
type videoParams struct {
	// Duration 是目标时长，单位秒。
	Duration int
	// Resolution 是目标清晰度，如 720p。
	Resolution string
	// Ratio 是画幅比例，如 16:9、9:16、auto。
	Ratio string
}

// normalizeVideoParams 把可选入参收敛成一份确定的视频参数。
//
// 三种情况都归一到缺省值：未传（零值）、空白字符串、时长越界。理由见
// videoParams 的注释——这些字段是可选增强，不该因为一个非法值让整条请求失败。
// [参数] duration: 目标时长，0 表示未传；resolution: 目标清晰度，空串表示未传；
// ratio: 画幅比例，空串表示未传。
// [返回] 归一后的参数集合。
// 最近修改时间：2026-11-17 21:36:00；改动原因：新增，视频参数由调用方决定。
func normalizeVideoParams(duration int, resolution, ratio string) videoParams {
	// 1. 时长按闭区间校验，越界一律回落缺省值。
	params := videoParams{Duration: defaultVideoDuration}
	if duration >= videoDurationMin && duration <= videoDurationMax {
		params.Duration = duration
	}

	// 2. 两个字符串参数去空白后为空即视为未传。
	params.Resolution = defaultVideoResolution
	if trimmed := strings.TrimSpace(resolution); trimmed != "" {
		params.Resolution = trimmed
	}
	params.Ratio = defaultVideoRatio
	if trimmed := strings.TrimSpace(ratio); trimmed != "" {
		params.Ratio = trimmed
	}
	return params
}

// isDefault 判断参数是否为缺省值。
//
// 用途是让「调用方根本没提要求」与「调用方要求和缺省一致」走同一条路径：
// 两种情况下首轮提示词都不该多出一段参数要求。
// [参数] 无。
// [返回] 三项全为缺省值时返回 true。
// 最近修改时间：2026-11-17 21:36:00；改动原因：新增，缺省路径必须逐字节不变。
func (p videoParams) isDefault() bool {
	return p.Duration == defaultVideoDuration &&
		p.Resolution == defaultVideoResolution &&
		p.Ratio == defaultVideoRatio
}

// generationMode 描述一次请求期望的回复形态。
//
// 只剩一条上游管线（网页端技能管线，agent_mode=2）：靠 Skill 激活
// 生视频。缺 Skill 时「画一只猫」只会被模型描述而不会真正绘制——这是静默
// 降级，不报错，因此调用方必须从目录别名取值，不能自己拼。
type generationMode struct {
	// Skill 是上游 skill id，0 表示未声明能力。
	Skill int
	// Kind 是回复解析类型：video。
	Kind string
}

// textMode 返回不带任何 skill 的空模式。
//
// 保留它是因为大量测试要一份「最小的合法 generationMode」；生产路径上模型名
// 一律经 modeForModel 解析，未知名直接失败，不会落到这里。
// [参数] 无。
// [返回] Skill 为 0 的文本模式。
// 最近修改时间：2026-11-17 00:52:00；改动原因：工作模式管线删除，本函数只服务于测试基线。
func textMode() generationMode {
	return generationMode{Skill: 0, Kind: "text"}
}

// buildBusinessQuery 构造业务 API（/alice/*、/chat/*）的 query 参数集。
//
// 实测缺参会返回 {"code":0,"data":null}（HTTP 200 但无数据），必须补齐整个常量块。
// device_id / web_id / tea_uuid 是设备级标识，需与凭据一同持久化；
// 多账号若共用同一组标识可能被上游关联（见需求 GAP-004）。
// [参数] deviceID: 设备标识；webID: web 会话标识（同时用作 tea_uuid）；
// webTabID: 标签页标识；fp: 设备指纹（账号真实 s_v_web_id，可空）；extra: 端点专属附加参数。
// [返回] 编码后的 query 字符串。
// 最近修改时间：2026-11-15 22:05:00；改动原因：补 fp，实测它是推理端点的必需项。
func buildBusinessQuery(deviceID, webID, webTabID, fp string, extra map[string]string) string {
	q := url.Values{}
	q.Set("version_code", doubaoVersionCode)
	q.Set("language", "zh")
	q.Set("device_platform", "web")
	q.Set("doubao_device_platform", "web")
	q.Set("aid", doubaoAID)
	q.Set("real_aid", doubaoAID)
	q.Set("pkg_type", "release_version")
	q.Set("device_id", deviceID)
	q.Set("pc_version", doubaoPCVersion)
	q.Set("doubao_pc_version", doubaoPCVersion)
	q.Set("web_id", webID)
	q.Set("tea_uuid", webID)
	q.Set("region", "CN")
	q.Set("sys_region", "CN")
	q.Set("samantha_web", "1")
	q.Set("web_platform", "browser")
	q.Set("use-olympus-account", "1")
	if webTabID != "" {
		q.Set("web_tab_id", webTabID)
	}
	if fp != "" {
		q.Set("fp", fp)
	}
	q.Set("flow_im_arch", "v2")
	for k, v := range extra {
		q.Set(k, v)
	}
	return q.Encode()
}

// endpointURL 拼接业务端点完整 URL。
// [参数] sa: 账号（提供设备级标识）；path: 端点路径；extra: 附加 query 参数。
// [返回] 完整 URL。
// 最近修改时间：2026-11-15 22:05:00；改动原因：随 query 一并带上账号真实 fp。
func endpointURL(sa *StoredAuth, path string, extra map[string]string) string {
	return doubaoSiteHost + path + "?" + buildBusinessQuery(sa.DeviceID, sa.WebID, sa.WebTabID, fingerprintOf(sa), extra)
}

// fingerprintOf 返回账号真实的设备指纹（浏览器 tea SDK 生成的 s_v_web_id）。
//
// 为什么不能自己拼：上游对 /chat/completion 做设备级归因，fp 必须与账号在浏览器
// 里真实用过的那一串一致。2026-11-15 四轮交替实测（全部带真实 session 组）：
//   - 假 device_id + 格式合法的随机 fp → 710022004 semantic_reasoning；
//   - 假 device_id + 账号真实 fp        → 出字（device_id 反而是次要的）；
//   - 复验两次均成功，说明 fp 是这条链路的必需项。
//
// 旧实现用 "verify_"+deviceID 拼假 fp，等于每次都告诉上游「这是一台没见过的设备」，
// 因此自 0.1.2 起面板测试长期报 710022004。老账号没有采集过该字段时返回空串，
// query 里就不带 fp（退回旧行为），由重新授权补采。
// [参数] sa: 账号。
// [返回] 非空时返回 fp；未采集时返回空串。
// 最近修改时间：2026-11-15 22:05:00；改动原因：新增，统一从账号取真实设备指纹。
func fingerprintOf(sa *StoredAuth) string {
	if sa == nil {
		return ""
	}
	return strings.TrimSpace(sa.SVWebID)
}

// deviceIDFor 返回账号的稳定设备标识，缺失时就地派生并落盘。
//
// 设备标识必须在账号生命周期内保持不变：上游用它做设备级风控归因，同一账号
// 每次换 device_id 会触发风控升级。老账号（0.1.6 之前导入）没有这三个字段，
// 因此这里按 Cookie 摘要派生一次并写回账号文件。
// [参数] sa: 账号。
// [返回] 非空设备标识。
// 最近修改时间：2026-11-14 01:35:00；改动原因：新增，设备标识兜底派生。
func deviceIDFor(sa *StoredAuth) string {
	if id := strings.TrimSpace(sa.DeviceID); id != "" {
		return id
	}
	// 1. 以 Cookie 摘要派生稳定的十进制设备号：上游 device_id 是 18-19 位纯数字串，
	//    长度与形态不符会被风控判为非客户端请求。
	sum := sha256.Sum256([]byte(sa.CookieHeader + sa.AuthID))
	digits := new(strings.Builder)
	for _, b := range sum[:16] {
		fmt.Fprintf(digits, "%d", int(b%10))
	}
	sa.DeviceID = "73" + digits.String()

	// 2. web_id 与设备号同源派生，保证同一账号的三个标识彼此绑定。
	if strings.TrimSpace(sa.WebID) == "" {
		sa.WebID = sa.DeviceID
	}

	// 3. 落盘：写回失败的账号仍可用（本次请求内已生效），不阻断对话。
	if sa.PhysicalPath != "" {
		_ = writeAuthFileDirect(sa.PhysicalPath, sa)
	}
	return sa.DeviceID
}

// upstreamError 表示一次被判定为上游侧的失败。
//
// 携带状态码与上游业务码，供调用方区分「账号级故障」（需换号/冷却）
// 与「请求本身有问题」（不该换号）。
type upstreamError struct {
	// StatusCode 是 HTTP 状态码；传输层失败时为 0。
	StatusCode int
	// Code 是上游业务码（如 710012001）；无业务码时为 0。
	Code int
	// Message 是面向用户的错误说明。
	Message string
}

// Error 实现 error 接口。
// [参数] 无。
// [返回] 错误说明。
// 最近修改时间：2026-10-17；改动原因：新增，上游错误类型。
func (e *upstreamError) Error() string { return e.Message }

// isAuthFailure 判断错误是否意味着登录态失效。
// [参数] 无。
// [返回] 属于登录态失效时返回 true。
// 最近修改时间：2026-10-17；改动原因：新增，失效识别（供账号状态上报）。
func (e *upstreamError) isAuthFailure() bool {
	if e.StatusCode == 401 || e.StatusCode == 403 {
		return true
	}
	return e.Code == 710012001
}

// isRateLimit 判断错误是否意味着「换一个账号就能继续」。
//
// 名字沿用历史叫法，实际语义是「账号级、可换号绕过」。710022004 既不是限流
// 也不是设备指纹问题，而是上游对该账号弹的人机验证（语义选图）：账号进入验证态后
// 该账号的每个请求都会被拦，直到有人在浏览器里手动过掉一次。归到同一类的理由是
// 处理动作一致 —— 冷却该账号、切到其他账号，验证由人另行完成。710022002 是真正的
// 请求过频，同样靠换号与等待恢复，归入同一类。
// [参数] 无。
// [返回] 属于账号级可绕过故障时返回 true。
// 最近修改时间：2026-11-25 00:08:00；改动原因：710022002（请求过频）并入冷却换号判据。
func (e *upstreamError) isRateLimit() bool {
	if e.StatusCode == 429 {
		return true
	}
	return e.Code == 710022004 || e.Code == 710022005 || e.Code == 710022002
}

// statusCodeHint 把上游业务码翻译成可操作的中文提示。
//
// 码值来自实测与上游客户端行为：直接回显数字对用户没有意义，
// 提示里写清「该查什么」。
//
// 710022004 的真义是「上游对该账号弹了人机验证（语义选图）」，既不是账号风控，
// 也不是限流，更不是设备指纹问题。铁证是 STREAM_ERROR 帧里的 extra.decision：
// `{"code":"10000","from":"shark_admin","type":"verify","subtype":"semantic_reasoning",
// "log_id":"…","verify_scene":"doubao_message_web"}`，同一时刻页面中央会弹出九宫格
// 验证码，左下角 log_id 与帧里完全一致。带 decision 的帧由 streamErrorHint 走专项
// 文案，本函数只是没有 decision 时的兜底。
//
// 历史教训（不要重犯）：0.1.19 曾把成因记到「出站 session Cookie 组」头上，
// 0.1.20 记到「人工语义选图验证码」头上（这次是对的），0.1.21 又推翻成「设备指纹
// 不匹配」——三次都只看了错误码和 error_msg，没解析 extra.decision。判定这类
// 「码一样、含义不同」的失败必须读 decision，不能靠码值猜。
//
// -1 也有两次误判史：0.1.23 之前被记成「账号未投放工作模式」，0.1.23 又推翻成
// 「插件请求体缺默认值字段」，两次都只看了插件自己的请求。2026-11-16 21:20 的
// 结论以「真实浏览器同账号同请求也回 -1」为准，见下面 case -1 的取证说明。
// [参数] code: 上游业务码；desc: 上游原始描述（可空）。
// [返回] 中文提示。
// 最近修改时间：2026-11-16 21:26:00；改动原因：-1 的成因改回「该账号未投放工作模式」，附三步取证。
func statusCodeHint(code int, desc string) string {
	// 1. 已知业务码给可操作提示：区分「换账号可恢复」与「请求本身有问题」。
	switch code {
	case 0:
		return ""
	case 710012001, 710012000:
		return "登录态已失效，请在面板重新导入 Cookie"
	case 710022004, 710022005:
		return "上游要求该账号完成人机验证（语义选图）：请在浏览器里登录同一账号，在豆包网页任意发一句话，按弹出的九宫格完成一次验证；验证通过后账号即恢复，不需要重新授权"
	case 710022002:
		return "上游判定这次请求过频（当前服务访问频繁）：等待 1~2 分钟再试；同一账号短时间内被反复测试也会触发，建议降低测试频率"
	case -1:
		// 2026-11-16 21:20 结论修正（第二次）：这个码在 agent_mode=1 下是
		// 「该账号没有被投放工作模式」，不是插件把请求体写错了。
		// 铁证分三步：① 同账号在真实浏览器点网页「工作」标签发同一请求，
		// 也回同一帧 `{"error_code":-1,"error_msg":"delegated input protocol violation"}`；
		// ② 把浏览器原样抓到的 body（含 reasoning_effort=3、extra_info 等全部字段）
		// 原封不动重放，健康账号出字、本账号仍回 -1；
		// ③ launch 端点（页面原生 fetch + 页面原生签名）对两个账号的
		// data.ug_campaign_info 不同：健康账号有 campaign_group_code=
		// pc_incentive_task_mvp_v3（标题「免费用豆包工作」，schema 指向
		// /pc/agent-explore），本账号该字段为空串、material_map 也整个缺失。
		// 换 device_id、换 model_item_key、增删 cookie、改客户端参数集（aid=582478）、
		// 换签名来源都无法改变结果，故这是账号维度的投放事实，请求侧无解。
		return "该账号没有被投放到豆包工作模式（上游活动 pc_incentive_task_mvp_v3 未覆盖此账号），请求本身没有问题：请在面板改用其他账号，或在豆包电脑版参与「免费用豆包工作」活动后重试"
	case 710020202:
		return "上游拒绝了请求参数：请确认 Content-Type 为 application/json 且请求体结构完整"
	case 710020702:
		return "上游判定该请求为重放（is_replay）：签名或请求唯一标识被复用，需重新生成请求"
	case 710022013:
		return "上游拒绝了未带签名的裸请求：该路径需要补齐风控签名或改用受支持的请求形态"
	case 710010202:
		return "上游返回系统错误：通常是 Cookie 与设备标识不匹配，建议重新导入账号"
	}

	// 2. 未收录的码原样回显，保留上游描述便于定位。
	if desc == "" {
		desc = "未知错误"
	}
	return fmt.Sprintf("上游错误 %d：%s", code, desc)
}

// randomUUID 生成 UUID v4 字符串。
// [参数] 无。
// [返回] 标准 UUID v4 文本；随机源失败时返回全零 UUID。
// 最近修改时间：2026-10-17；改动原因：新增，请求体需要稳定格式的唯一标识。
func randomUUID() string {
	var b [16]byte
	if _, errRead := rand.Read(b[:]); errRead != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// buildAccountInfoQuery 构造判据端点 /passport/account/info/v2/ 的 query 参数集。
//
// 判据端点使用与业务 API 不同的参数子集（含 account_sdk_source / sdk_version /
// doubao_from），实测缺参会返回空 data。
// [参数] 无。
// [返回] 编码后的 query 字符串。
// 最近修改时间：2026-10-17；改动原因：新增，判据端点参数集。
func buildAccountInfoQuery() string {
	q := url.Values{}
	q.Set("pc_version", doubaoPCVersion)
	q.Set("version_code", doubaoVersionCode)
	q.Set("language", "zh")
	q.Set("device_platform", "web")
	q.Set("aid", doubaoAID)
	q.Set("real_aid", doubaoAID)
	q.Set("pkg_type", "release_version")
	q.Set("use-olympus-account", "1")
	q.Set("region", "CN")
	q.Set("sys_region", "CN")
	q.Set("account_sdk_source", "web")
	q.Set("sdk_version", accountInfoSDKVersion)
	q.Set("doubao_from", "inject_html")
	return q.Encode()
}
