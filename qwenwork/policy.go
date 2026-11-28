// policy.go is the pure decision layer for credit-driven lifecycle actions:
// given an account's region and current credits, decide whether to disable
// CN: disable when exhausted, re-enable after check-in restores credits, or
// leave it alone. No I/O happens here — reconcileOneAccount consumes these
// decisions and applies them via the lifecycle.go authfile helpers.
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
	lifecycleDelete
	lifecycleReenable
)

func (a lifecycleAction) String() string {
	switch a {
	case lifecycleDisable:
		return "disable"
	case lifecycleDelete:
		return "delete"
	case lifecycleReenable:
		return "reenable"
	default:
		return "none"
	}
}

// lifecycleAuto gates automatic disable/delete/reenable. Default false.
var (
	lifecycleAuto   = false
	lifecycleAutoMu sync.RWMutex
)

func lifecycleEnabled() bool {
	lifecycleAutoMu.RLock()
	defer lifecycleAutoMu.RUnlock()
	return lifecycleAuto
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

// defaultMinRoutableCredits 是安全缓冲额度红线默认值（20 点）。
// 剩余额度低于该阈值的账号不再承载新请求，防止推理中途断开。
const defaultMinRoutableCredits int64 = 20

var (
	minRoutableCredits   int64 = defaultMinRoutableCredits
	minRoutableCreditsMu sync.RWMutex
)

// loadedMinRoutableCredits 返回当前生效的安全缓冲额度红线。
func loadedMinRoutableCredits() int64 {
	minRoutableCreditsMu.RLock()
	defer minRoutableCreditsMu.RUnlock()
	return minRoutableCredits
}

// setMinRoutableCredits 设置安全缓冲额度红线并返回清理函数，供单测或动态配置使用。
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

// isCreditsExhausted 判定积分快照是否归零。
// [参数] cr：积分快照。
// [返回] 上游明确标记 exceeded、或有消耗/额度池信号且剩余为 0 时返回 true；
// 缺失数据视为未知返回 false。
// 最近修改时间：2026-10-18 17:10:00；改动原因：纳入上游 quota.exceeded 权威信号。
func isCreditsExhausted(cr *creditsSummary) bool {
	if cr == nil {
		return false
	}
	if cr.TotalRemain > 0 {
		return false
	}
	// 上游明确标记超额：权威判据。免费版账号没有额度包，
	// total_used/total_size 恒为 0，仅靠数值会被误判成「未知」。
	if cr.Exceeded {
		return true
	}
	// remain==0: exhausted only when we know there was/is a package total
	// (used>0, size>0, or packages present). Pure zero with no packages = no data.
	if cr.TotalUsed > 0 || cr.TotalSize > 0 {
		return true
	}
	return len(cr.Packages) > 0
}

// isCreditsLowOrExhausted 判定账号是否额度耗尽或低于安全缓冲红线。
// [参数] cr：积分快照。
// [返回] 耗尽或正积分低于安全红线时返回 true；未知额度返回 false。
// 最近修改时间：2026-10-18 17:10:00；改动原因：耗尽账号不得再参与路由。
func isCreditsLowOrExhausted(cr *creditsSummary) bool {
	if cr == nil {
		return false
	}
	if isCreditsExhausted(cr) {
		return true
	}
	return cr.TotalRemain < loadedMinRoutableCredits()
}

// isCreditsLowForRouting 判定账号是否因额度不足暂停路由。
// [参数] cr：积分快照。
// [返回] 耗尽（含上游 exceeded）或正积分低于安全红线时返回 true。
// 最近修改时间 2026-11-27（付费模型判据保留；免费模型在调度层按模型放行）
func isCreditsLowForRouting(cr *creditsSummary) bool {
	return isCreditsLowOrExhausted(cr)
}

// isDailyCountExceeded 判定上游是否返回 Qoder 专属的 110 日请求次数上限限制。
// [参数] status: HTTP 响应状态码；body: 错误响应体文本。
// [返回] 命中 110 限额错误时返回 true。
// 最近修改时间 2026-10-13（新增 Qoder 日调用上限专用判定）
func isDailyCountExceeded(status int, body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "billing daily count exceeded") ||
		strings.Contains(lower, "daily count exceeded") ||
		strings.Contains(body, `"110"`) ||
		strings.Contains(body, `code: 110`) ||
		strings.Contains(body, `code: "110"`) ||
		strings.Contains(body, `110:`)
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

// httpStatusBadGateway 是上游流读取中断对外呈现的状态码。
const httpStatusBadGateway = 502

// httpStatusServiceUnavailable 是排队帧对外的状态码（见 queueFrameHTTPStatus）。
const httpStatusServiceUnavailable = 503

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
// 软限流文案或上游零字节断流。2026-10-16 起这类失败与硬失败共用
// recordAccountFailure 的计数与阶梯冷却路径，不再单独豁免计数；本函数仅用于
// 把「HTTP 200 承载的零字节断流文案」等不命中 isAccountFailure 状态码判据的
// 失败纳入可记账范围。硬额度错误优先排除：429 + 额度标记属账号耗尽。
// 历史沿革：2026-09-06 曾因网关抖动连续零字节失败误隔离 4 个健康账号而把
// 这类失败改为「只冷却不计数」；异常池于 2026-09-08 移除后计数不再触发
// 冻结，2026-10-16 起软失败重新并入阶梯计数，代价是抖动期间账号会沿阶梯
// 冷却更久，由「一次成功即清零」兜底回落。
func isTransientThrottle(status int, body string) bool {
	if isHardCreditError(status, body) {
		return false
	}
	return isSoftRateLimit(status, body) || isEmptyStreamBody(body)
}

// queueBusinessCode 是 Qoder 上游「排队 / 容量受限」的专属业务码。
const queueBusinessCode = "10605"

// modelPoolBusyMarker 是上游「模型池暂时没有实例」的文案特征。
//
// 社区口径（dsh-qwenwork-connector src/adapter/stream.ts:167-181 记录
// 「实测多轮循环第 4 轮就会撞上，而账号额度明明还很充足」）：这类 429 反映
// 的是模型池容量而不是账号健康，换任何账号都会撞同一堵墙，因此必须归入
// 排队类——对外 503、不冷却账号、不在同请求内换号。
// 本插件此前把裸 429 一律计入账号阶梯冷却，24 小时内 27 次 minimax 429
// 足以把健康号逐个推进冷却，流量被迫集中到更少的账号上，进一步加剧排队。
const modelPoolBusyMarker = "all models failed"

// serviceUnavailableMarker 是排队帧里 serviceAvailable:false 的键值形态。
//
// 社区三份实现一致把 serviceAvailable===false 也当作排队判据
// （dsh-qwenwork-connector stream.ts:137、dsh-qwenwork-connect errors.ts:194、
// modelmux upstream.ts:636），本插件此前只匹配 isQueued / queueCount / 10605。
const serviceUnavailableMarker = "\"serviceAvailable\":false"

// queueFrameHTTPStatus 是排队帧对外呈现的 HTTP 状态码。
//
// 选 503 而非 403：宿主 MarkResult 对 403 会给凭据打 30 分钟冷却
// （conductor_cooldown.go:856），而排队是分钟级现象；503 只触发瞬时冷却
// （默认 1 分钟，conductor_cooldown.go:949），且同样在宿主跨 provider 轮转
// 白名单（isCredentialRetryRoundStatus）内，能触发换道。
const queueFrameHTTPStatus = httpStatusServiceUnavailable

// isQueueFrame reports whether an upstream error text describes Qoder's
// capacity-queue state rather than an account fault.
//
// 生产证据（2026-10-15）：排队帧为 `403: 10605: {"isQueued":true,
// "queueCount":14250,"retryAfterSeconds":30,"serviceAvailable":true,
// "waitTime":478}`。serviceAvailable:true 证明账号本身健康、饱和的只是上游
// 队列；多账号同时命中同一队列（queueCount 单调递增）亦证明排队是请求级 +
// 时段性。该帧此前被 remap 成 403 记账 + 15s 冷却，把上游容量问题放大成整池
// auth_unavailable，故单独识别以跳过账号级记账。
//
// [参数] body：上游错误文本（含 sseErrorFrame 展开后的排队帧）。
// [返回] 命中排队/容量语义时返回 true。
// 最近修改时间 2026-11-28（补 serviceAvailable:false 与模型池忙 429 判据）
func isQueueFrame(body string) bool {
	if strings.TrimSpace(body) == "" {
		return false
	}
	if strings.Contains(body, "isQueued") ||
		strings.Contains(body, "queueCount") ||
		strings.Contains(body, queueBusinessCode) {
		return true
	}
	// serviceAvailable:false 是排队帧的另一种形态；子串匹配要容忍冒号后的空格。
	if strings.Contains(strings.ReplaceAll(body, " ", ""), serviceUnavailableMarker) {
		return true
	}
	return isModelPoolBusyBody(body)
}

// isModelPoolBusyBody 判定错误文本是否描述「模型池暂时没有实例」。
//
// [参数] body：上游错误文本。
// [返回] 命中模型池忙语义时返回 true。
// 最近修改时间：2026-11-28；改动原因：新增，模型池忙归排队类不罚账号。
func isModelPoolBusyBody(body string) bool {
	if strings.TrimSpace(body) == "" {
		return false
	}
	return strings.Contains(strings.ToLower(body), modelPoolBusyMarker)
}

// envelopeHTTPStatusForError 决定失败信封对外携带的 HTTP 状态码。
//
// 宿主 statusCodeFromError（conductor_cooldown.go:1448）只认 StatusCode() int
// 数字接口，纯文本错误恒得 0，跨 provider 轮转（isCredentialRetryRoundStatus）
// 永不触发；排队帧据此带上 503，让宿主把请求换到另一家上游。
// 断流补 502 属同类网关失败信号：本插件内部换号已用尽或已泄漏分片时，
// 让宿主看到明确的网关类状态而不是成功帧。
//
// [参数] message：失败信封的错误文本。
// [返回] 排队帧返回 503，断流返回 502；其余返回 0，保持既有行为。
// 最近修改时间 2026-10-17 21:30:00 断流信封补 502。
func envelopeHTTPStatusForError(message string) int {
	if isQueueFrame(message) {
		return queueFrameHTTPStatus
	}
	if isStreamReadError(message) {
		return httpStatusBadGateway
	}
	return 0
}

// displayNote builds a one-line note for CPAMP Auth cards.
func displayNote(sa *storedAuth, cr *creditsSummary, disabled bool) string {
	region := strings.ToUpper("cn")
	if region == "CN" {
		region = "CN"
	} else {
		region = "CN"
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

// labelForAuth 构造宿主展示用的账号标签。
//
// [参数] sa 账号凭据。
// [返回] 形如 "昵称 [CN]" 的展示标签；昵称为空时用 "QwenWork"。
// 最近修改时间：2026-10-18 02:05:00；改动原因：删除恒假的 region 分支（QwenWork 只有 CN）。
func labelForAuth(sa *storedAuth) string {
	base := "QwenWork"
	if sa != nil && strings.TrimSpace(sa.Account.Nickname) != "" {
		base = strings.TrimSpace(sa.Account.Nickname)
	}
	return base + " [CN]"
}
