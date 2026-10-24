// policy.go is the pure decision layer for credit-driven lifecycle actions:
// given an account's credits, decide whether to delete (Global exhausted) or
// leave it alone. No I/O happens here -- reconcileOneAccount consumes these
// decisions and applies them via the lifecycle.go authfile helpers.
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// lifecycleAction is the maintenance decision for one account.
type lifecycleAction int

const (
	lifecycleNone lifecycleAction = iota
	lifecycleReenable
)

func (a lifecycleAction) String() string {
	switch a {
	case lifecycleReenable:
		return "reenable"
	default:
		return "none"
	}
}

// lifecycleAuto gates the automatic note/legacy-flag maintenance loop.
// Default false.
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
	"点数不足",
	"额度不足",
	"余额不足",
	"配额用尽",
	"点数耗尽",
	"没有积分",
	"credit not enough",
	"not enough credit",
}

const httpStatusPaymentRequired = 402

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
// zero-byte stream.
func isEmptyStreamBody(body string) bool {
	return strings.Contains(body, "closed before first payload") ||
		strings.Contains(body, "empty stream") ||
		strings.Contains(body, "invalid SSE response: missing output and done event")
}

// streamReadErrorLeakedMarker 标记已向客户端发出正文后的断流，抑制换号与状态码分类。
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

// isStreamReadError 判定错误文本是否描述上游 SSE 读取中断。
// [参数] body：错误文本。
// [返回] 命中可重试断流语义时返回 true。
// 最近修改时间 2026-10-18 03:40:00 补齐断流分级换号与状态码分类（此前完全缺失）。
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
// 最近修改时间 2026-10-18 03:40:00 新增，与 qoderwork/qoder-ai/workbuddy/qwenwork 同构。
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
// 仅思考分片（reasoning_content / reasoning）不算内容泄漏：该上游在推理模式下
// 会先持续输出思考摘要，此时断流重试不会产生两份正文。
// [参数] chunkJSON：上游 OpenAI 兼容分片 JSON。
// [返回] 含非空 delta.content、delta.refusal 或 tool_calls 时返回 true。
// 最近修改时间 2026-10-18 03:40:00 新增，用于区分思考泄漏与正文泄漏。
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
// 宿主 statusCodeFromError 只认 StatusCode() int 数字接口，纯文本错误恒得 0，
// 跨 provider 轮转永不触发；断流补 502 属网关失败信号，让宿主看到明确的
// 网关类状态而不是成功帧。
// [参数] message：失败信封的错误文本。
// [返回] 断流返回 502；其余返回 0，保持既有行为。
// 最近修改时间 2026-10-18 03:40:00 新增，补齐断流状态码通道。
func envelopeHTTPStatusForError(message string) int {
	if isStreamReadError(message) {
		return httpStatusBadGateway
	}
	return 0
}

// streamChunksHaveContent 判定已收集分片里是否含正文或工具调用。
//
// 同步路径只拿到清理后的分片切片，无法回看原始 JSON，故按同样的 delta 结构判定。
// [参数] chunks：已收集的宿主分片。
// [返回] 任一分片含非空 content / refusal 或 tool_calls 时返回 true。
// 最近修改时间 2026-10-18 03:40:00 新增，用于区分思考泄漏与正文泄漏。
func streamChunksHaveContent(chunks []pluginapi.ExecutorStreamChunk) bool {
	for _, chunk := range chunks {
		if streamChunkHasContent(string(chunk.Payload)) {
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
func isTransientThrottle(status int, body string) bool {
	if isHardCreditError(status, body) {
		return false
	}
	return isSoftRateLimit(status, body) || isEmptyStreamBody(body)
}

// displayNote builds a one-line note for CPAMP Auth cards.
// 积分只作展示，不参与任何可用性判定：零积分账号仍可正常推理，因此这里不产出
// 「耗尽」措辞，避免与真实可用状态冲突。
func displayNote(sa *storedAuth, cr *creditsSummary, disabled bool) string {
	parts := []string{"Global"}
	if disabled {
		parts = append(parts, "已禁用")
	}
	switch {
	case cr == nil:
		parts = append(parts, "余额未知")
	default:
		if cr.TotalSize > 0 {
			parts = append(parts, fmt.Sprintf("余%d 已用%d 总%d", cr.TotalRemain, cr.TotalUsed, cr.TotalSize))
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
