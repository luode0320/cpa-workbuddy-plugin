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
// 最近修改时间：2026-11-15 22:55:00；改动原因：补记设备指纹是更前置的判据。

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

	// blockTypeText 是消息文本块类型（BlockType.java: BLOCK_TYPE_TEXT）。
	blockTypeText = 10000

	// blockTypeDeepThink 是深度思考块类型。
	blockTypeDeepThink = 10040

	// blockTypeCreation 是生成结果块类型（图片/视频）。
	blockTypeCreation = 2074

	// skillImageGen 是图像生成能力的上游 skill id。
	//
	// 值来自上游 /samantha/skill/recommend。缺少它时「画一只猫」只会让模型
	// 描述画面而不会真正绘制——这是静默降级，不报错。
	skillImageGen = 3

	// skillVideoGen 是视频生成能力的上游 skill id。
	skillVideoGen = 5

	// workConversationMode 是工作模式会话的 conversation_mode 取值。
	//
	// 上游用该字段区分会话类型：2 = 工作任务（agent 管线），0 = 普通对话。
	workConversationMode = 2

	// defaultWorkModeID 是工作模式的 mode_id（mode_name 为「工作任务」）。
	defaultWorkModeID = "3"

	// defaultWorkReasoningEffort 是工作模式默认推理档位（0-4，4 为最高）。
	defaultWorkReasoningEffort = "4"

	// defaultWorkMemoryProfile 是工作模式默认记忆档位。
	defaultWorkMemoryProfile = "300k_230"

	// defaultWorkTotalWindow 是工作模式默认上下文窗口（与 memory_profile 配套）。
	defaultWorkTotalWindow = "300000"

	// needDeepThinkOn 是开启深度思考时 need_deep_think 的取值（10001 = 开启）。
	needDeepThinkOn = 10001

	// workWorkspaceMarker 是客户端工作目录标记。
	workWorkspaceMarker = "\\Doubao"
)

// generationMode 描述一次请求期望的回复形态与模型参数。
//
// 两条不兼容的上游管线共用本结构：
//   - 工作模式（WorkMode=true）：agent_mode=1，走客户端形态请求，模型由
//     ModelItemKey 决定，Skill 恒为 0；
//   - 技能模式（WorkMode=false）：agent_mode=2，走网页端形态请求，靠 Skill
//     激活生图/生视频管线。
//
// 缺 Skill 时「画一只猫」只会被模型描述而不会真正绘制；反过来，在工作模式
// 里塞 Skill 会被上游按普通对话处理。
type generationMode struct {
	// Skill 是上游 skill id，0 表示纯文本对话。
	Skill int
	// Kind 是回复解析类型：text / image / video。
	Kind string
	// ModelItemKey 是上游模型主键（agent 管线按它选模型）。
	ModelItemKey string
	// MemoryProfile 是上游记忆档位（如 300k_230），需与 TotalWindow 配套。
	MemoryProfile string
	// TotalWindow 是上下文窗口大小（十进制字符串，如 300000）。
	TotalWindow string
	// ReasoningEffort 是推理档位（0-4，4 为最高）。
	ReasoningEffort string
	// WorkMode 表示走 agent 工作模式管线。
	WorkMode bool
}

// textMode 返回普通对话模式（技能模式，无 skill）。
// [参数] 无。
// [返回] Skill 为 0 的文本模式。
// 最近修改时间：2026-11-14 01:40:00；改动原因：工作模式引入后明确本函数只表达技能管线的空模式。
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
// 名字沿用历史叫法，实际语义是「账号级、可换号绕过」：710022004 不是限流，
// 而是上游判定该请求的设备指纹与账号在浏览器里真实用过的那一组不一致
// （详见 statusCodeHint 注释）。归到同一类的理由是处理动作一致 —— 冷却该账号、
// 切到其他账号，直到有人把该账号的 `s_v_web_id` 采集回来。
// [参数] 无。
// [返回] 属于账号级可绕过故障时返回 true。
// 最近修改时间：2026-11-15 22:55:00；改动原因：710022004 的真因改为设备指纹不匹配，注释同步。
func (e *upstreamError) isRateLimit() bool {
	if e.StatusCode == 429 {
		return true
	}
	return e.Code == 710022004 || e.Code == 710022005
}

// statusCodeHint 把上游业务码翻译成可操作的中文提示。
//
// 码值来自实测与上游客户端行为：直接回显数字对用户没有意义，
// 提示里写清「该查什么」。
//
// 710022004 的真义是「登录态是真的，但设备指纹对不上」，不是账号风控、不是环境风险、
// 也不是限流。上游对 /chat/completion 做设备级归因：`fp`（浏览器 tea SDK 写入的
// `s_v_web_id`）必须与账号在浏览器里真实用过的那一串一致，否则判定「别人拿着你的
// 会话在冒充浏览器」。2026-11-15 四象限交替实测：真实 fp + 保留 session 组稳定出字，
// 假 fp（`"verify_"+deviceID`）即使格式合法也一律 710022004。
//
// 历史教训（不要重犯）：0.1.19 曾把成因记到「出站 session Cookie 组」头上，
// 0.1.20 又记到「人工语义选图验证码」头上，两次都是把 fp 真伪与另一个变量混在一起
// 做对照得出的。0.1.21 起两条一起改：fp 改用账号真实 `s_v_web_id`，session 组恢复出站。
// [参数] code: 上游业务码；desc: 上游原始描述（可空）。
// [返回] 中文提示。
// 最近修改时间：2026-11-15 22:55:00；改动原因：710022004 / 710022002 的成因改为设备指纹不匹配，撤销上一版「人工验证码」口径。
func statusCodeHint(code int, desc string) string {
	// 1. 已知业务码给可操作提示：区分「换账号可恢复」与「请求本身有问题」。
	switch code {
	case 0:
		return ""
	case 710012001, 710012000:
		return "登录态已失效，请在面板重新导入 Cookie"
	case 710022004, 710022005:
		return "上游判定该账号的设备指纹与浏览器里真实用过的那一组不一致（semantic_reasoning），不是账号风控也不是限流：请重新点一次授权书签，让插件采集到该账号的 s_v_web_id；老账号没有这个值时请求不带 fp，会被持续拒绝"
	case 710022002:
		return "上游拒绝了这次请求：常见原因是该账号未采集 s_v_web_id（fp 缺失）、或短时间内重复请求过多。请重新点一次授权书签补采设备指纹，并等待 1~2 分钟再试"
	case -1:
		// 2026-11-16 结论修正：这个码不是「账号未投放工作模式」，而是插件把请求体
		// 构造得比真实网页少了一批字段。真实浏览器在网页「工作」标签下发的
		// agent_mode=1 请求能正常出字，逐项消融后确认上游要求 option 里的
		// click_clear_context / connector_info_list / recovery_option 等默认值字段
		// 必须显式存在，省略即判「委托输入协议不符」。插件已补齐这些字段，
		// 若仍出现本码，说明请求体被外部改回旧形状或上游再次收紧校验。
		return "上游判定请求体不符合网页端的委托输入协议：插件已按真实网页补齐默认值字段，若仍出现请确认用的是最新版本；怀疑被上游收紧校验时请附上游 log_id 反馈"
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

// atoiSafe 把纯数字字符串转成整数，遇非数字字符即停止。
//
// 上游要整数型的 reasoning_effort，而配置里存的是字符串；用 strconv.Atoi
// 遇到 "4 " 之类的脏值会整段失败，这里按前缀解析保证总能拿到一个档位。
// [参数] s: 待转换字符串。
// [返回] 解析结果；无数字前缀时为 0。
// 最近修改时间：2026-11-14 03:05:00；改动原因：新增，推理档位字符串转整数。
func atoiSafe(s string) int {
	// 1. 按字符前缀累加，遇非数字立即停止。
	value := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			break
		}
		value = value*10 + int(r-'0')
	}

	// 2. 无数字前缀时返回 0，由调用方按默认档位兜底。
	return value
}
