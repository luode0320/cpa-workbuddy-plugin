// policy.go is the pure decision layer for credit-driven lifecycle actions:
// given an account's current credits, decide whether to disable or re-enable
// it after a check-in restores credits, or leave it alone. No I/O happens
// here — reconcileOneAccount consumes these decisions and applies them via the
// lifecycle.go authfile helpers.
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// lifecycleAction is the policy decision for one account.
type lifecycleAction int

const (
	lifecycleNone lifecycleAction = iota
	lifecycleDisable
	lifecycleReenable
)

func (a lifecycleAction) String() string {
	switch a {
	case lifecycleDisable:
		return "disable"
	case lifecycleReenable:
		return "reenable"
	default:
		return "none"
	}
}

// lifecycleAuto gates automatic disable/reenable. Default false.
var (
	lifecycleAuto   = false
	lifecycleAutoMu sync.RWMutex
)

func lifecycleEnabled() bool {
	lifecycleAutoMu.RLock()
	defer lifecycleAutoMu.RUnlock()
	return lifecycleAuto
}

// shouldActOnCredits is true only when credits are *known* exhausted.
// nil / empty (no packages, no used) is unknown → false.
func shouldActOnCredits(cr *creditsSummary) bool {
	return isCreditsExhausted(cr)
}

// hardCreditMarkers are case-insensitive substrings in upstream error bodies.
var hardCreditMarkers = []string{
	"insufficient credit",
	"insufficient credits",
	"no credit",
	"no credits",
	"credit exhausted",
	"credits exhausted",
	"out of credit",
	"out of credits",
	"quota exceeded",
	"quota exhaust",
	"exceeded the quota",
	"14018",
	"payment required",
	"积分不足",
	"额度不足",
	"余额不足",
	"积分用完",
	"额度用尽",
	"没有积分",
	"credit not enough",
	"not enough credit",
}

// defaultMinRoutableCredits 是保活线默认值（400 点）。
// 剩余额度低于该阈值的账号仍可承载请求，但排在健康号之后——它是软降级，
// 不再是硬排除（2026-10-23 按用户裁定改造）：低于禁用线的号才写盘停用，
// 禁用线与保活线之间的号属于「保活」档，保留路由能力但优先级最低。
// 最近修改时间：2026-10-23；改动原因：硬排除改为保活降级，与三档分级对齐。
const defaultMinRoutableCredits int64 = 400

// defaultLowCreditDisableBelow 是硬禁用线默认值（100 点）。
// 剩余额度低于该值的账号写盘 disabled:true，由宿主路由判据直接停用；
// 恢复要求积分回到该值以上且探活成功（见 credit_disable.go）。
// 最近修改时间：2026-10-23；改动原因：新增三档分级中的硬禁用线。
const defaultLowCreditDisableBelow int64 = 100

var (
	minRoutableCredits   int64 = defaultMinRoutableCredits
	minRoutableCreditsMu sync.RWMutex

	lowCreditDisableBelow   int64 = defaultLowCreditDisableBelow
	lowCreditDisableBelowMu sync.RWMutex
)

// loadedLowCreditDisableBelow 返回当前生效的硬禁用线。
// [参数] 无。
// [返回] 积分低于该值的账号会被写盘停用；0 表示关闭该机制。
// 最近修改时间：2026-10-23；改动原因：新增。
func loadedLowCreditDisableBelow() int64 {
	lowCreditDisableBelowMu.RLock()
	defer lowCreditDisableBelowMu.RUnlock()
	return lowCreditDisableBelow
}

// setLowCreditDisableBelow 设置硬禁用线并返回清理函数，供单测使用。
// [参数] n：新的硬禁用线，负数按 0 处理。
// [返回] 还原旧值的函数。
// 最近修改时间：2026-10-23；改动原因：新增。
func setLowCreditDisableBelow(n int64) func() {
	if n < 0 {
		n = 0
	}
	lowCreditDisableBelowMu.Lock()
	old := lowCreditDisableBelow
	lowCreditDisableBelow = n
	lowCreditDisableBelowMu.Unlock()
	return func() {
		lowCreditDisableBelowMu.Lock()
		lowCreditDisableBelow = old
		lowCreditDisableBelowMu.Unlock()
	}
}

// loadedMinRoutableCredits 返回当前生效的保活线。
// [参数] 无。
// [返回] 保活线阈值；低于该值的账号降级到保活档。
// 最近修改时间：2026-10-23；改动原因：语义由硬排除红线改为保活线。
func loadedMinRoutableCredits() int64 {
	minRoutableCreditsMu.RLock()
	defer minRoutableCreditsMu.RUnlock()
	return minRoutableCredits
}

// setMinRoutableCredits 设置保活线并返回清理函数，供单测或动态配置使用。
// [参数] n：新的保活线。
// [返回] 还原旧值的函数。
// 最近修改时间：2026-10-23；改动原因：语义由硬排除红线改为保活线。
func setMinRoutableCredits(n int64) func() {
	minRoutableCreditsMu.Lock()
	old := minRoutableCredits
	minRoutableCredits = n
	minRoutableCreditsMu.Unlock()
	return func() {
		minRoutableCreditsMu.Lock()
		minRoutableCredits = old
		minRoutableCreditsMu.Unlock()
	}
}

// isCreditsRoutable 判定账号积分是否仍允许承载新请求。
//
// 三档语义（2026-10-23）：低于硬禁用线或已知耗尽 → 不可路由；
// 禁用线与保活线之间 → 可路由但降级（保活档）；保活线以上 → 正常。
// 旧实现把「低于 400 一律摘出路由」，会让 100~400 的健康账号白白闲置；
// 现在这档账号仍然接流量，只是排在正常号后面（见 scheduler.go 的排序）。
// nil（积分未知）返回 true：没有数据不等于没钱，误判会把健康号踢出路由。
// [参数] cr：积分汇总，可为 nil。
// [返回] true 表示可承载请求。
// 最近修改时间：2026-10-23；改动原因：三档分级，400 由硬排除改为保活降级。
func isCreditsRoutable(cr *creditsSummary) bool {
	if cr == nil {
		return true
	}
	if isCreditsExhausted(cr) {
		return false
	}
	disableBelow := loadedLowCreditDisableBelow()
	return disableBelow <= 0 || cr.TotalRemain >= disableBelow
}

// isCreditsLowOrExhausted 判定账号是否已不可路由（旧名保留，语义收窄）。
// 与 isCreditsRoutable 互为否定，仅供既有调用点过渡使用。
// [参数] cr：积分汇总，可为 nil。
// [返回] true 表示积分已不足以承载请求。
// 最近修改时间：2026-10-23；改动原因：语义从「低于 400」收窄为「低于禁用线或耗尽」。
func isCreditsLowOrExhausted(cr *creditsSummary) bool {
	return !isCreditsRoutable(cr)
}

// contentModerationValueMarkers 是审核判定的**字段值位置**标记，全部按小写比对。
//
// 只认带引号的字段值形态（如 `"content_filter"`），不做自由子串匹配：
// 本判据运行在每个流式分片上，而 `delta.content` 里就是用户正文——自由子串会
// 把正文中出现 "content error" / "sensitive content" 字样的**正常回答**
// 误判成审核失败，进而给健康账号记冷却（本仓库正是中文小说正文场景，
// 正文出现这些词的语义完全中性）。
//
// 覆盖三层书写形态：
//  1. 宿主直出：`"finish_reason":"content_filter"` —— 宿主转译层
//     （CLIProxyAPI/openai_openai-responses_response.go:78）把审核终止映射成该值；
//  2. OpenAI 语义帧：`"incomplete_details":{"reason":"content_filter"}`；
//  3. 错误帧：`{"error":{...}}` 且正文含审核文案（由调用点额外判定）。
var contentModerationValueMarkers = []string{
	`"content_filter"`,
	`"content filter"`,
}

// isContentModerationFailure 判定一次失败是否为上游内容审核拦截。
//
// 形态取证（2026-11-12，生产会话 session-3304f06f）：42 次审核失败在客户端侧统一
// 呈现为 `Response incomplete: content_filter`，workbuddy 收到的是宿主下发的
// `finish_reason=content_filter` 终止分片 —— 它是**正常终止**而非错误帧，
// 因此 sseErrorFrame 捕获不到，必须由本判据在分片层面识别。
//
// 精度要求（关键）：本判据在每个流式分片上执行，必须**零误判正常正文**。
// 因此审核文案（sensitive content 等）只在同时存在 `"error"` 包裹时才算命中——
// 正文里出现同名词组属于正常内容，不能据此罚号。
//
// [参数] body：分片原文或错误文本。
// [返回] 命中内容审核语义时返回 true。
// 最近修改时间：2026-11-12；改动原因：新增，内容审核未进入失败分类导致 100% 被审模型无法退出路由。
func isContentModerationFailure(body string) bool {
	if strings.TrimSpace(body) == "" {
		return false
	}
	lower := strings.ToLower(body)
	// 1. 长度类截断必须排除：与审核共用 incomplete_details / finish 语义，
	//    按 finish_reason 与 reason 双口径精确排除，避免把长度截断记成审核。
	if strings.Contains(lower, "max_output_tokens") ||
		strings.Contains(lower, "max_tokens") ||
		strings.Contains(lower, `"finish_reason":"length"`) ||
		strings.Contains(lower, `"finish_reason": "length"`) ||
		strings.Contains(lower, `"reason":"length"`) ||
		strings.Contains(lower, `"reason": "length"`) {
		return false
	}
	// 2. 字段值位置命中：finish_reason / incomplete_details.reason 取审核值。
	for _, marker := range contentModerationValueMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	// 3. 错误文本形态：审核文案出现在**错误上下文**中才成立。
	//
	//    上一层的字段值命中已覆盖"分片里带 content_filter 值"的正常路径；
	//    本层处理 sseErrorFrame 产出的裸错误文本（"upstream 200: sensitive
	//    content error finish reason: sensitive"）—— 那是错误帧抽取结果，
	//    本身就是错误上下文。
	//
	//    区分依据是错误语境标记而非词本身：正常分片是
	//    `{"choices":[{"delta":{"content":"..."}}]}`，不含 "upstream N:" 前缀
	//    也不含 reason/finish 等错误措辞；正文里的同名词组因此不会被误判。
	if strings.Contains(lower, "sensitive content") || strings.Contains(lower, "content error") {
		return strings.Contains(lower, "upstream ") ||
			strings.Contains(lower, "finish reason") ||
			strings.Contains(lower, `"error"`)
	}
	return false
}

// isHardCreditError reports business "out of credits" style failures.
// 402 is treated as payment/credit. Pure 429 is not hard unless body has credit markers.
func isHardCreditError(status int, body string) bool {
	if status == httpStatusPaymentRequired {
		return true
	}
	lower := strings.ToLower(body)
	for _, m := range hardCreditMarkers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return true
		}
	}
	// Chinese markers may not lower-map usefully; also scan raw.
	for _, m := range hardCreditMarkers {
		if strings.Contains(body, m) {
			return true
		}
	}
	return false
}

const httpStatusPaymentRequired = 402

// upstreamBusyHint 是渠道级容量满载时追加给客户端的可读说明。
//
// 它明确告诉调用方「这不是你的账号问题、换号也没用」，避免下游把它当成
// 凭据故障去做无谓的重登或换号。
const upstreamBusyHint = "（上游推理集群容量已满，非账号问题，请稍后重试）"

// isSoftRateLimit is pure throttling without hard-credit semantics.
func isSoftRateLimit(status int, body string) bool {
	if isHardCreditError(status, body) {
		return false
	}
	if status == 429 {
		return true
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "throttl")
}

// isEmptyStreamBody reports whether the failure text describes an upstream
// zero-byte stream: the gateway accepted the connection and then closed it
// before delivering any payload (host-side empty_stream). The pump records
// such attempts with the canonical text "upstream stream closed before
// first payload".
func isEmptyStreamBody(body string) bool {
	return strings.Contains(body, "closed before first payload") ||
		strings.Contains(body, "empty stream") ||
		strings.Contains(body, "invalid SSE response: missing output and done event")
}

// streamTruncatedFinishReason 是正文已泄漏断流时合成的终止原因。
//
// 上游在正文中途断流后，客户端已收到部分回答；此时既不能换号（会产生两份
// 语义冲突输出），也不应发 error 事件把已生成内容一起作废。合成 length 让
// 下游转译层产出 response.incomplete 终态事件：客户端保留已生成内容，new-api
// 也能凭该终态事件与已收正文恢复计费（此前发 error 时 new-api 既不认事件、
// 又因正文无 usage 而判定 $0）。
const streamTruncatedFinishReason = "length"

// buildStreamTerminalChunk 构造只携带 finish_reason 的终止分片（纯函数，便于回归测试）。
//
// 仅复用已下发分片中的 id/model 会让调用方承担状态管理；这里统一以 chatcmpl-stream
// 兜底，下游转译层只在首帧取 id，缺省不影响 response.id 生成。
// [参数] model：客户端请求模型名；finishReason：终止原因。
// [返回] OpenAI 兼容的 chat.completion.chunk JSON；序列化失败返回错误。
// 最近修改时间 2026-10-18 05:10:00 新增，正文已泄漏断流改用 length 收尾。
func buildStreamTerminalChunk(model, finishReason string) ([]byte, error) {
	chunk := map[string]any{
		"id":      "chatcmpl-stream",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": finishReason,
			},
		},
	}
	return json.Marshal(chunk)
}

// isTransientThrottle 判定失败是否属于上游瞬时过载信号：429 无额度标记、
// 软限流文案，或上游零字节断流。这类失败仍会进入 recordAccountFailure 的
// 计数与阶梯冷却路径——它的唯一作用是把「HTTP 200 承载的零字节断流文案」
// 这类不命中 isAccountFailure 状态码判据的失败纳入可记账范围。
// 硬额度错误优先排除：429 + 额度标记仍属账号耗尽，走 isHardCreditError。
// 历史沿革：2026-09-06 曾因网关抖动连续零字节失败误隔离 4 个健康账号而把
// 这类失败改为「只冷却不计数」；异常池于 2026-09-08 移除后计数不再触发
// 冻结，2026-10-16 起软失败重新并入阶梯计数，代价是抖动期间账号会沿阶梯
// 冷却更久，由「一次成功即清零」兜底回落。
// [参数] status: 上游 HTTP 状态码；body: 上游响应正文。
// [返回] true 表示属于可记账的瞬时过载类失败。
// 最近修改时间：2026-10-16 23:29:50；改动原因：软失败并入阶梯计数，注释同步新语义。
func isTransientThrottle(status int, body string) bool {
	if isHardCreditError(status, body) {
		return false
	}
	return isSoftRateLimit(status, body) || isEmptyStreamBody(body)
}

// streamReadErrorLeakedMarker 标记已向客户端发出分片后的断流，抑制换号与状态码分类。
const streamReadErrorLeakedMarker = "after partial output"

// streamReadErrorReasoningOnlyMarker 标记断流时客户端只收到思考分片、正文尚未开始。
//
// 与 streamReadErrorLeakedMarker 的区别：思考摘要可以重放（客户端把它当折叠的推理过程
// 展示，第二次请求是同一问题的重新推理），正文一旦发出就不能重放，否则会出现两份
// 语义冲突的回答。2026-10-18 生产实证：qwen-3.8-flash 在 reasoning_effort=max 下思考
// 33.99s 后断流，正文未开始，旧逻辑按「已泄漏」拒绝换号，导致整条请求失败且 new-api
// 零计费。本标记让这类断流仍可换号，同时保留状态码抑制语义。
const streamReadErrorReasoningOnlyMarker = "reasoning only, no content"

// httpStatusBadGateway 是上游流读取中断对外呈现的状态码。
const httpStatusBadGateway = 502

// httpStatusBadRequest 是上游拦截（11128）对外的状态码。
//
// 选 400：宿主 IsRequestFault(400) 为真，会把该失败判成请求级故障，
// 既不冷却凭据也不跨凭据轮转。
const httpStatusBadRequest = 400

// isStreamReadError 判定错误文本是否描述上游 SSE 读取中断。
// [参数] body：错误文本。
// [返回] 命中可重试断流语义时返回 true。
// 最近修改时间 2026-10-18 03:20:00 思考阶段断流不再抑制，见 streamReadErrorReasoningOnlyMarker。
func isStreamReadError(body string) bool {
	// 1. 空文本与已泄漏正文标记不参与断流分类
	if strings.TrimSpace(body) == "" || strings.Contains(body, streamReadErrorLeakedMarker) {
		return false
	}
	// 2. 命中传输层断流关键字
	return strings.Contains(body, "stream read error") ||
		strings.Contains(body, "connection reset by peer") ||
		strings.Contains(body, "INTERNAL_ERROR")
}

// shouldRotateOnStreamReadError 判定断流失败是否允许换号重试。
// [参数] emitted：是否已向客户端发出分片；contentEmitted：其中是否已发出正文/工具调用分片；
// attempt：当前尝试序号；budget：换号预算；hasEncodedBody：能否重建请求体；hasNextAuth：是否存在可用候选。
// [返回] 允许换号重试时返回 true。
// 最近修改时间 2026-10-18 03:20:00 细化 emitted 语义：仅思考分片时仍允许换号。
func shouldRotateOnStreamReadError(emitted, contentEmitted bool, attempt, budget int, hasEncodedBody, hasNextAuth bool) bool {
	// 1. 正文已泄漏、无法重建或没有候选时只透传错误
	if contentEmitted || !hasEncodedBody || !hasNextAuth {
		return false
	}
	// 2. 预算未耗尽才允许继续换号
	return attempt < budget
}

// streamChunkHasContent 判定单个上游分片是否已承载正文或工具调用。
//
// 仅思考分片（reasoning_content / reasoning）不算内容泄漏：Qoder 系模型在
// reasoning_effort 较高时会先持续输出思考摘要，此时断流重试不会产生两份正文。
// [参数] chunkJSON：上游 OpenAI 兼容分片 JSON。
// [返回] 含非空 delta.content、delta.refusal 或 tool_calls 时返回 true。
// 最近修改时间 2026-10-18 03:20:00 新增，用于区分思考泄漏与正文泄漏。
func streamChunkHasContent(chunkJSON string) bool {
	var obj struct {
		Choices []struct {
			Delta struct {
				Content   any `json:"content"`
				Refusal   any `json:"refusal"`
				ToolCalls any `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(chunkJSON), &obj) != nil {
		return false
	}
	for _, choice := range obj.Choices {
		if !isEmptyValue(choice.Delta.Content) || !isEmptyValue(choice.Delta.Refusal) {
			return true
		}
		if arr, ok := choice.Delta.ToolCalls.([]any); ok && len(arr) > 0 {
			return true
		}
	}
	return false
}

// envelopeHTTPStatusForError 决定失败信封对外携带的 HTTP 状态码。
//
// 宿主 statusCodeFromError 只认 StatusCode() int 数字接口，纯文本错误恒得 0；
// 断流补 502 让宿主看到明确的网关类状态而不是成功帧。该插件无排队帧识别，
// 因此不返回 503。
// [参数] message：失败信封的错误文本。
// [返回] 断流返回 502；上游拦截（11128）返回 400；其余返回 0，保持既有行为。
// 最近修改时间 2026-10-17 21:30:00 新增断流信封状态码通道。
// 最近修改时间 2026-11-08 14:40:00；改动原因：上游拦截补 400，堵住单请求扇出整池。
func envelopeHTTPStatusForError(message string) int {
	if isStreamReadError(message) {
		return httpStatusBadGateway
	}
	// 上游拦截在插件内已停止换号，但信封缺状态码时宿主仍会逐个凭据重试。
	if isModelBlockedError(httpStatusBadRequest, message) {
		return httpStatusBadRequest
	}
	return 0
}

// lifecycleActionFor chooses disable/none from credits.
// 国际版账号不再由本插件自动删号：Global 归 workbuddy-ai 插件专门承载，
// 耗尽与 CN 一致只写盘禁用，凭据保留（可人工充值或迁移后恢复）。
func lifecycleActionFor(cr *creditsSummary) lifecycleAction {
	if !shouldActOnCredits(cr) {
		return lifecycleNone
	}
	return lifecycleDisable
}

// shouldReenable is true when an auto-disabled account now has credits again.
func shouldReenable(disabled bool, cr *creditsSummary) bool {
	if !disabled {
		return false
	}
	if cr == nil {
		return false
	}
	if isCreditsExhausted(cr) {
		return false
	}
	// Known positive remain, or non-exhausted with packages still having room.
	return cr.TotalRemain > 0
}

// displayNote builds a one-line note for CPAMP Auth cards.
func displayNote(sa *storedAuth, cr *creditsSummary, disabled bool) string {
	region := strings.ToUpper(accountRegion(sa))
	if region == "CN" {
		region = "CN"
	} else {
		region = "Global"
	}
	parts := []string{region}
	if disabled {
		parts = append(parts, "已禁用")
	}
	switch {
	case cr == nil:
		parts = append(parts, "积分未知")
	case isCreditsExhausted(cr):
		parts = append(parts, fmt.Sprintf("耗尽 · 余%d 已用%d", cr.TotalRemain, cr.TotalUsed))
	default:
		// Show remain as primary (what you can still spend). Used is real cycle spend.
		// Size (capacity) grows with check-in packs — do not treat size↑ as usage↓.
		if cr.TotalSize > 0 {
			parts = append(parts, fmt.Sprintf("余%d 已用%d 池%d", cr.TotalRemain, cr.TotalUsed, cr.TotalSize))
		} else {
			parts = append(parts, fmt.Sprintf("余%d 已用%d", cr.TotalRemain, cr.TotalUsed))
		}
	}
	note := strings.Join(parts, " · ")
	if len(note) > 80 {
		note = note[:77] + "..."
	}
	return note
}

// labelForAuth adds [CN]/[Global] for host labels.
func labelForAuth(sa *storedAuth) string {
	base := "WorkBuddy"
	if sa != nil && strings.TrimSpace(sa.Account.Nickname) != "" {
		base = strings.TrimSpace(sa.Account.Nickname)
	}
	tag := "CN"
	if accountRegion(sa) == "global" {
		tag = "Global"
	}
	return base + " [" + tag + "]"
}
