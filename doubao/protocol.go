package main

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// 本文件集中定义豆包上游协议的常量与 query 参数集构造。
//
// 协议事实来源（2026-10-17）：Lxapk/DouBao-CPA-Plugin 的 protocol.go 源码 +
// 本仓库浏览器实测网络日志交叉验证。关键结论：
//   - 业务 API 必须携带完整 query 参数集，缺参会返回 {"code":0,"data":null}；
//   - msToken 与 a_bogus 均不在必需参数中——上游接受无签名调用，
//     这消除了重实现字节风控签名算法的必要；
//   - IM 系端点（/im/*）的 Content-Type 必须是 "application/json; encoding=utf-8"；
//     而 /chat/completion 相反，须用纯 "application/json"。

const (
	// webUserAgent 是模拟网页端请求的 User-Agent。
	webUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

	// doubaoAID 是豆包网页端的应用标识。
	doubaoAID = "497858"

	// doubaoVersionCode 是客户端版本编码。
	doubaoVersionCode = "20800"

	// doubaoPCVersion 是 PC 客户端版本号。
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

	// clientUserAgent 是豆包 PC 客户端（Samantha 桌面端）的 User-Agent。
	//
	// 工作模式的 /chat/completion 只对客户端形态放行：用网页端 UA 会被上游
	// 按普通网页对话处理，agent_mode 被忽略且不进入 agent 管线。
	clientUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.7727.149 SamanthaDoubao/2.30.1"

	// doubaoClientAID 是 PC 客户端的应用标识（网页端为 497858，两者不同）。
	doubaoClientAID = "582478"

	// doubaoClientPCVersion 是 PC 客户端版本号；工作模式能力按客户端版本放行。
	doubaoClientPCVersion = "2.30.1"

	// doubaoClientRuntimeVersion 是客户端内置 runtime 的版本号。
	doubaoClientRuntimeVersion = "3.37.4"

	// doubaoClientChromiumVersion 是客户端 Chromium 内核版本。
	doubaoClientChromiumVersion = "147.0.7727.149"

	// flowTraceHeader 是客户端固定下发的链路追踪头；上游不校验其真实性，
	// 缺失时部分风控分支会拒绝请求。
	flowTraceHeader = "04-0000000000000000000000000000000-0000000000000000-01"

	// clientLastEventID 是客户端固定下发的 last-event-id 头（字面量 "undefined"）。
	clientLastEventID = "undefined"

	// clientAgwJsConv 是客户端 agw-js-conv 头取值（逗号后带空格，与网页端 "str" 不同）。
	clientAgwJsConv = "str, str"

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

	// workClientOS 是工作模式 client_option.os 的取值（上游只认 Windows 客户端）。
	workClientOS = "Windows"

	// workClientFallbackName 是取不到主机名时上报的客户端设备名。
	workClientFallbackName = "USER"

	// workWorkspaceMarker 是客户端工作目录标记，home_dir 取它之前的部分。
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
// webTabID: 标签页标识；extra: 端点专属附加参数。
// [返回] 编码后的 query 字符串。
// 最近修改时间：2026-10-17；改动原因：新增，业务 API 参数集。
func buildBusinessQuery(deviceID, webID, webTabID string, extra map[string]string) string {
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
	q.Set("flow_im_arch", "v2")
	for k, v := range extra {
		q.Set(k, v)
	}
	return q.Encode()
}

// endpointURL 拼接业务端点完整 URL。
// [参数] sa: 账号（提供设备级标识）；path: 端点路径；extra: 附加 query 参数。
// [返回] 完整 URL。
// 最近修改时间：2026-10-17；改动原因：新增，业务端点地址构造。
func endpointURL(sa *StoredAuth, path string, extra map[string]string) string {
	return doubaoSiteHost + path + "?" + buildBusinessQuery(sa.DeviceID, sa.WebID, sa.WebTabID, extra)
}

// buildClientQuery 构造 PC 客户端形态的 query 参数集。
//
// 与 buildBusinessQuery 的差异不是风格问题而是协议事实：客户端的 aid / 版本号 /
// device_platform / web_platform 全部不同，且强制要求 fp、runtime、chromium_version
// 等网页端没有的键；把这些键塞进网页端参数集会让 /alice/* 业务端点判为非法客户端。
// 因此两条参数集并存：业务端点走网页端，/chat/completion 走客户端。
//
// device_id / fp / tea_uuid 三者同源（fp 为 "verify_" 前缀），web_tab_id 每次请求
// 换新 UUID——它们共同构成客户端的设备指纹三元组。
// [参数] deviceID: 设备标识；webID: web 会话标识。
// [返回] 编码后的 query 字符串。
// 最近修改时间：2026-11-14 01:35:00；改动原因：新增，工作模式客户端参数集。
func buildClientQuery(deviceID, webID string) string {
	// 1. 账号与渠道：aid 必须是客户端口径，网页端 aid 会被判为非客户端请求。
	q := url.Values{}
	q.Set("aid", doubaoClientAID)
	q.Set("real_aid", doubaoClientAID)
	q.Set("channel", "win")
	q.Set("client_platform", "pc_client")
	q.Set("device_platform", "web")
	q.Set("doubao_device_platform", "desktop")
	q.Set("doubao_pc_version", doubaoClientPCVersion)
	q.Set("pc_version", doubaoClientPCVersion)
	q.Set("pkg_type", "release_version")
	q.Set("language", "zh")
	q.Set("region", "CN")
	q.Set("sys_region", "CN")
	// 2. 运行时与版本：上游按版本放行模型清单，版本号必须与 UA 一致。
	q.Set("runtime", "web")
	q.Set("runtime_version", doubaoClientRuntimeVersion)
	q.Set("samantha_web", "1")
	q.Set("use-olympus-account", "1")
	q.Set("version_code", doubaoVersionCode)
	q.Set("web_platform", "desktop")
	// 3. 设备级标识：fp / tea_uuid 均派生自 device_id，web_tab_id 每轮换新。
	q.Set("chromium_version", doubaoClientChromiumVersion)
	q.Set("device_id", deviceID)
	q.Set("fp", "verify_"+deviceID)
	q.Set("tea_uuid", deviceID)
	if webID != "" {
		q.Set("web_id", webID)
	}
	q.Set("web_tab_id", randomUUID())
	return q.Encode()
}

// clientEndpointURL 拼接客户端形态端点完整 URL。
// [参数] sa: 账号（提供设备级标识）；path: 端点路径。
// [返回] 完整 URL。
// 最近修改时间：2026-11-14 01:35:00；改动原因：新增，工作模式端点地址构造。
func clientEndpointURL(sa *StoredAuth, path string) string {
	return doubaoSiteHost + path + "?" + buildClientQuery(deviceIDFor(sa), sa.WebID)
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

// isRateLimit 判断错误是否意味着上游限流。
// [参数] 无。
// [返回] 属于限流时返回 true。
// 最近修改时间：2026-10-17；改动原因：新增，限流识别（供阶梯冷却使用）。
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
// 710022004 的提示必须点明「账号级风控」而不是笼统的「稍后重试」：2026-10-17
// 对照实测确认它由 shark_admin/verify/semantic_reasoning 下发，同一 Cookie 换
// 出口 IP、换设备标识、换深度思考参数都照样触发，而账号信息端点仍返回 success。
// 也就是说这不是网络抖动，等几分钟通常无效，只有换账号或等风控窗口解除。
// [参数] code: 上游业务码；desc: 上游原始描述（可空）。
// [返回] 中文提示。
// 最近修改时间：2026-11-14 21:42:00；改动原因：补 -1（账号级输入协议异常）与 710022002（账号级频率限制）两条实测码的可操作文案。
func statusCodeHint(code int, desc string) string {
	// 1. 已知业务码给可操作提示：区分「换账号可恢复」与「请求本身有问题」。
	switch code {
	case 0:
		return ""
	case 710012001:
		return "登录态已失效，请在面板重新导入 Cookie"
	case 710022004, 710022005:
		return "账号触发上游风控（semantic_reasoning），非网络问题；该账号的推理请求被临时拦截，请换用其他账号或等待风控解除（账号信息与登录态本身仍有效）"
	case 710022002:
		return "上游提示该账号访问过于频繁（账号级频率限制），非插件问题；换用其他账号即可恢复，稍后重试同一账号通常仍被拒"
	case -1:
		return "上游判定该账号的输入协议异常（delegated input protocol violation），属账号级限制；该账号的 Cookie 与登录态仍有效，但推理请求被稳定拒绝，请换用其他账号（2026-11-14 实测：同账号跨模型稳定复现，与插件请求构造无关）"
	case 710020202:
		return "上游拒绝了请求参数：请确认 Content-Type 为 application/json 且请求体结构完整"
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

// workDeviceName 返回工作模式上报的客户端设备名。
//
// 上游把 device_name 当作沙箱主机标识；取不到主机名时用固定占位，
// 不虚构随机值——同一账号每次换设备名会被风控归因为多设备登录。
// [参数] 无。
// [返回] 设备名。
// 最近修改时间：2026-11-14 03:05:00；改动原因：新增，工作模式设备名来源。
func workDeviceName() string {
	// 1. 取宿主主机名，取不到就用固定回退名。
	name, errHost := os.Hostname()
	if errHost != nil {
		return workClientFallbackName
	}

	// 2. 空主机名同样回退，避免把空串报给上游。
	name = strings.TrimSpace(name)
	if name == "" {
		return workClientFallbackName
	}
	return name
}

// workHomeDir 从工作目录推导客户端 home_dir。
//
// 客户端把 home_dir 报成工作目录中「\Doubao」段之前的部分；没有该标记时留空，
// 与客户端在自定义目录下的行为一致。
// [参数] workspace: 工作目录。
// [返回] home_dir；无法推导时为空串。
// 最近修改时间：2026-11-14 03:05:00；改动原因：新增，工作模式任务输入的 home_dir。
func workHomeDir(workspace string) string {
	// 1. 未声明工作目录时 home_dir 留空，不虚构路径。
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return ""
	}

	// 2. 有「\Doubao」标记时取其前段，其余情况留空。
	if idx := strings.Index(workspace, workWorkspaceMarker); idx > 0 {
		return workspace[:idx]
	}
	return ""
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
