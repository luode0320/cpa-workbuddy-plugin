// stream.go implements the Trae SSE → OpenAI chat-completion conversion.
// The upstream llm_utils_chat emits event:output / event:done / event:error
// SSE frames; these are folded into chat.completion.chunk deltas (streaming)
// or a chat.completion aggregate (non-streaming), mirroring the verified
// prototype's renderOpenAI* paths.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// streamEmit pushes one chunk to the host's stream channel for streamID.
func streamEmit(streamID string, payload []byte) error {
	body, _ := json.Marshal(map[string]any{
		"stream_id": streamID,
		"payload":   payload,
	})
	_, err := hostCall(pluginabi.MethodHostStreamEmit, body)
	return err
}

// marshalStreamErrorEnvelope 构造带 "error" 字段的 stream.emit 信封（纯函数，便于回归测试）。
func marshalStreamErrorEnvelope(streamID, message string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"stream_id": streamID,
		"error":     message,
	})
}

// emitStreamErrorEnvelope 通过 host.stream.emit 信封的 "error" 字段发送终态错误。
// 宿主把该字段映射为执行器流 chunk 的 Err（见 internal/pluginhost/stream_bridge.go
// rpcStreamEmitRequest.Error）：核心 conductor 只有在收到真正的错误 chunk 时才会
// 判定本次执行失败，进而轮换下一个凭据——包括另一平台（如 workbuddy）的账号。
// 若把错误当 payload 数据帧发出，宿主会视为正常流内容：请求以"成功"告终，
// 跨平台失败切换永远不会触发，客户端只收到一条内嵌错误的 SSE 事件。
func emitStreamErrorEnvelope(streamID, message string) error {
	body, err := marshalStreamErrorEnvelope(streamID, message)
	if err != nil {
		return err
	}
	_, err = hostCall(pluginabi.MethodHostStreamEmit, body)
	return err
}

// streamEmitError 以错误 chunk 形式发出终态错误并关闭宿主流。
func streamEmitError(streamID, message string) {
	_ = emitStreamErrorEnvelope(streamID, message)
	streamClose(streamID)
}

// streamClose terminates a host stream channel.
func streamClose(streamID string) {
	body, _ := json.Marshal(map[string]string{"stream_id": streamID})
	_, _ = hostCall(pluginabi.MethodHostStreamClose, body)
}

// streamHeaders is the client-facing response header set for streaming.
func streamHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	return h
}

// deltaChunk 构造一个 chat.completion.chunk 骨架：id/object/created/model 四处
// 固定字段集中在此，choices 与 usage 由调用方决定。正文帧、工具帧、终止帧三条
// 下行路径共用同一骨架，避免字段形态各自漂移。
// [参数] requestID: 逻辑请求 ID；model: 客户端模型；choices: 分片选择体；
// usageObj: OpenAI 键形态 usage 对象，nil 表示本帧不携带用量。
// [返回] 分片 JSON；调用方须自行保证 choices 非空。
// 最近修改时间：2026-11-08 13:40:00；改动原因：下行 usage 帧缺失——抽出骨架
// 让终止帧复用同一构造路径挂 usage。
func deltaChunk(requestID, model string, choices []map[string]any, usageObj map[string]any) ([]byte, error) {
	chunk := map[string]any{
		"id":      "chatcmpl-" + requestID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": choices,
	}
	if usageObj != nil {
		chunk["usage"] = usageObj
	}
	return json.Marshal(chunk)
}

// chunkDelta renders one chat.completion.chunk for an output fragment.
// 本函数只产出正文/推理帧，恒定不挂 usage：OpenAI 约定流式用量只出现在最后一帧，
// 中间帧带 usage 会被客户端按重复计费累加（见 coze/convert.go 的同款注释）。
// [参数] requestID: 逻辑请求 ID；model: 客户端模型；text: 正文片段；reasoning: 推理片段；
// finishReason: 非空时转为终止帧形态（delta 置空并带 finish_reason）。
// [返回] 分片 JSON；无内容且无终止原因时返回 (nil, nil)。
// 最近修改时间：2026-11-08 13:40:00；改动原因：骨架抽到 deltaChunk 复用。
func chunkDelta(requestID, model string, text, reasoning string, finishReason string) ([]byte, error) {
	delta := map[string]any{}
	if text != "" {
		delta["content"] = text
	}
	if reasoning != "" {
		delta["reasoning_content"] = reasoning
	}
	if len(delta) == 0 && finishReason == "" {
		return nil, nil
	}
	choice := map[string]any{
		"index": 0,
		"delta": delta,
	}
	if finishReason != "" {
		choice = map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": finishReason,
		}
	}
	return deltaChunk(requestID, model, []map[string]any{choice}, nil)
}

// usageToOpenAI 把内部 usage.Detail 翻成 OpenAI 标准 usage 对象（蛇形键）。
// 宿主 internal/runtime/executor/helps/usage_helpers.go 的
// hasOpenAIStyleUsageTokenFields 只认 total_tokens 与 prompt/completion 等标准桶
// 字段，键名写错等于没挂；cached / reasoning 也必须放在宿主实际读取的
// prompt_tokens_details.cached_tokens 与 completion_tokens_details.reasoning_tokens
// 位置上。全零 Detail 返回 nil——挂一个 0/0/0 的 usage 会被下游当成真实用量采信，
// new-api 会把该请求记成 0 token，比"未上报"更糟。
// [参数] d: 用量明细（上游 token_usage 真实值或字符估算兜底）。
// [返回] OpenAI 键形态 usage 对象；d 全零时返回 nil。
// 最近修改时间：2026-11-08 13:40:00；改动原因：插件 executor 在宿主侧没有 usage
// 观测器（ObserveOpenAIStream 不在插件路径上），用量只能由插件自己挂到 chunk 顶层。
func usageToOpenAI(d usage.Detail) map[string]any {
	if d.TotalTokens <= 0 && d.InputTokens <= 0 && d.OutputTokens <= 0 {
		return nil
	}
	total := d.TotalTokens
	if total <= 0 {
		total = d.InputTokens + d.OutputTokens
	}
	out := map[string]any{
		"prompt_tokens":     d.InputTokens,
		"completion_tokens": d.OutputTokens,
		"total_tokens":      total,
	}
	cached := d.CacheReadTokens
	if cached <= 0 {
		cached = d.CachedTokens
	}
	if cached > 0 || d.CacheCreationTokens > 0 {
		details := map[string]any{}
		if cached > 0 {
			details["cached_tokens"] = cached
		}
		if d.CacheCreationTokens > 0 {
			details["cache_creation_tokens"] = d.CacheCreationTokens
		}
		out["prompt_tokens_details"] = details
	}
	if d.ReasoningTokens > 0 {
		out["completion_tokens_details"] = map[string]any{
			"reasoning_tokens": d.ReasoningTokens,
		}
	}
	return out
}

// chunkFinishWithUsage 渲染携带 usage 的流式终止帧，是插件下行用量数据的唯一出口。
// 三条流式路径（collect / pump / async）收尾时都必须走这里而不是 chunkDelta，
// 否则宿主与 new-api 拿不到 token 数：new-api 使用日志的 Tokens 列显示 "-"、
// 该段流量零计费（2026-11-08 生产现象）。
// [参数] requestID: 逻辑请求 ID；model: 客户端模型；finishReason: 终止原因；
// detail: 本次请求最终用量（真实值或估算兜底）。
// [返回] 终止分片 JSON；detail 全零时不挂 usage，仍返回形态正常的终止帧。
// 最近修改时间：2026-11-08 13:40:00；改动原因：下行缺 usage 帧致下游零计费。
func chunkFinishWithUsage(requestID, model, finishReason string, detail usage.Detail) ([]byte, error) {
	choice := map[string]any{
		"index":         0,
		"delta":         map[string]any{},
		"finish_reason": finishReason,
	}
	return deltaChunk(requestID, model, []map[string]any{choice}, usageToOpenAI(detail))
}

// traeToolCallsToOpenAI 把上游 tool_calls 数组原始 JSON（function_call 变体
// 键）转成 OpenAI 兼容的 delta 元素形态（function 键）。取证事实
// （2026-09-03 直连 qwen3.8-max）：上游为快照式全量——单 output 事件可含多个
// index 的完整调用，每 index 只出现一次，arguments 恒为全量字符串；因此按
// 事件整块转换后由客户端 SDK 按 index 累积即得正确结果，无需自行拼接。
// arguments 缺省时回退 partial_arguments（防御上游偶发分片形态）。
// [参数] raw: normalizeOutput 过滤后的 tool_calls 数组 JSON。
// [返回] OpenAI delta tool_calls 元素数组；解析失败或空输入返回 nil。
// 最近修改时间：2026-09-03；改动原因：P0-③——下行 tool_calls 键适配。
func traeToolCallsToOpenAI(raw json.RawMessage) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var calls []map[string]any
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		el := map[string]any{}
		if idx, ok := call["index"]; ok {
			el["index"] = idx
		} else {
			el["index"] = len(out)
		}
		if id, ok := call["id"].(string); ok && id != "" {
			el["id"] = id
		}
		if typ, ok := call["type"].(string); ok && typ != "" {
			el["type"] = typ
		}
		fc, ok := call["function_call"].(map[string]any)
		if !ok {
			continue
		}
		fn := map[string]any{}
		if name, ok := fc["name"].(string); ok && name != "" {
			fn["name"] = name
		}
		args := ""
		if a, ok := fc["arguments"].(string); ok && a != "" {
			args = a
		} else if pa, ok := fc["partial_arguments"].(string); ok {
			args = pa
		}
		fn["arguments"] = args
		el["function"] = fn
		out = append(out, el)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// toolCallDeltaChunk 渲染一个携带 tool_calls 的 chat.completion.chunk 分片
// （流式工具调用响应）。与 chunkDelta 同构，delta 只挂 tool_calls。
// [参数] requestID: 逻辑请求 ID；model: 客户端模型；calls: OpenAI 键形态调用数组。
// [返回] 分片 JSON；calls 为空时返回 (nil, nil)。
// 最近修改时间：2026-09-03；改动原因：P0-③——流式 tool_calls 分片构造。
func toolCallDeltaChunk(requestID, model string, calls []map[string]any) ([]byte, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	chunk := map[string]any{
		"id":      "chatcmpl-" + requestID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{"tool_calls": calls},
			},
		},
	}
	return json.Marshal(chunk)
}

// completionAggregate renders the final chat.completion for non-streaming
// requests from the accumulated text/reasoning. toolCalls（OpenAI function 键
// 形态，可为 nil）非空时挂到 message.tool_calls，用于非流式工具调用响应。
// [参数] requestID/model/text/reasoning/finishReason: 同前；toolCalls: 聚合后的
// 工具调用；usageObj: OpenAI 键形态 usage 对象，nil 表示本次不挂用量。
// [返回] 响应体 JSON。
// 最近修改时间：2026-11-08 13:52:00；改动原因：非流式响应体同样缺 usage，
// 宿主只从顶层 usage 取 token 数，缺失时整条请求记 0 用量。
func completionAggregate(requestID, model, text, reasoning, finishReason string, toolCalls []map[string]any, usageObj map[string]any) ([]byte, error) {
	msg := map[string]any{"role": "assistant", "content": text}
	if reasoning != "" {
		msg["reasoning_content"] = reasoning
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	out := map[string]any{
		"id":      "chatcmpl-" + requestID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       msg,
				"finish_reason": finishReason,
			},
		},
	}
	if usageObj != nil {
		out["usage"] = usageObj
	}
	return json.Marshal(out)
}

// traeUsageCollector 累积最近一个 event:token_usage 事件（Trae llm_utils_chat
// 在 done 前发送，data 即 OpenAI 风格的 usage JSON 对象）。三条约流路径
// （collect / aggregate / pump）在 scanSSE 回调里喂入，成功收尾时把
// detail() 结果写入 usage feed，替代纯 content 字符估算（2026-09-04 直连
// 取证：token_usage data 形如 {"prompt_tokens":69,"completion_tokens":34,
// "reasoning_tokens":23,"total_tokens":103,"cache_read_input_tokens":0,
// "cache_creation_input_tokens":0,"cluster":"normal_context"}）。
type traeUsageCollector struct {
	last map[string]any
}

// feed 解析一条 token_usage 事件 data 并保留最近一次（容错：非 JSON /
// 空对象直接忽略）。兼容两种形态：data 直接是 usage 对象（Trae 实测），
// 或带 usage 包装键（OpenAI 惯例）。
// [参数] rawJSON: token_usage 事件的原始 data。
// [返回] 无。
// 最近修改时间：2026-09-04；改动原因：dashboard 输入/输出/思考/总 Token 列
// 需要 traework 侧真实 usage（此前 InputTokens/ReasoningTokens 恒 0）。
func (c *traeUsageCollector) feed(rawJSON string) {
	if c == nil {
		return
	}
	var obj map[string]any
	if json.Unmarshal([]byte(rawJSON), &obj) != nil {
		return
	}
	if u, ok := obj["usage"].(map[string]any); ok && len(u) > 0 {
		obj = u
	}
	if len(obj) == 0 {
		return
	}
	c.last = obj
}

// detail 返回收集到的真实 token 用量；未收到任何 token_usage 事件时为空。
// [参数] 无。
// [返回] usage.Detail: 上游真实计数（Input/Output/Reasoning/Total）。
// 最近修改时间：2026-09-04；改动原因：同 feed。
func (c *traeUsageCollector) detail() usage.Detail {
	if c == nil {
		return usage.Detail{}
	}
	return usageDetailFromTraeMap(c.last)
}

// usageDetailFromTraeMap 把 Trae token_usage 对象转成 usage.Detail，键兼容
// OpenAI 蛇形命名：prompt_tokens/input_tokens、completion_tokens/output_tokens、
// reasoning_tokens（顶层，Trae 实测形态；另兼容 completion_tokens_details
// 子对象形态）、cache_read_input_tokens、cache_creation_input_tokens、
// total_tokens。数值抖动（float64/int64/json.Number）统一容错。
// [参数] m: token_usage 对象。
// [返回] usage.Detail: 全部字段翻译；空输入返回空 Detail。
// 最近修改时间：2026-09-04；改动原因：同 feed。
func usageDetailFromTraeMap(m map[string]any) usage.Detail {
	if len(m) == 0 {
		return usage.Detail{}
	}
	num := func(keys ...string) int64 {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				switch n := v.(type) {
				case float64:
					return int64(n)
				case int64:
					return n
				case json.Number:
					i, _ := n.Int64()
					return i
				}
			}
		}
		return 0
	}
	d := usage.Detail{
		InputTokens:         num("prompt_tokens", "input_tokens"),
		OutputTokens:        num("completion_tokens", "output_tokens"),
		TotalTokens:         num("total_tokens"),
		CacheReadTokens:     num("cache_read_input_tokens"),
		CacheCreationTokens: num("cache_creation_input_tokens"),
		ReasoningTokens:     num("reasoning_tokens"),
	}
	// OpenAI 惯例形态：cached_tokens 位于 prompt_tokens_details 子对象。
	// Trae 当前不返回该字段（社区反代亦未处理），此处对齐标准，上游一旦提供即生效。
	if d.CacheReadTokens == 0 && d.CachedTokens == 0 {
		if pt, ok := m["prompt_tokens_details"].(map[string]any); ok {
			if v, ok := pt["cached_tokens"]; ok {
				switch n := v.(type) {
				case float64:
					d.CachedTokens = int64(n)
				case int64:
					d.CachedTokens = n
				case json.Number:
					i, _ := n.Int64()
					d.CachedTokens = i
				}
			}
		}
	}
	// OpenAI 惯例形态：reasoning_tokens 位于 completion_tokens_details 子对象
	// （CodeBuddy 等）。Trae 顶层已命中时跳过，避免子对象零值覆盖。
	if d.ReasoningTokens == 0 {
		if ct, ok := m["completion_tokens_details"].(map[string]any); ok {
			if v, ok := ct["reasoning_tokens"]; ok {
				switch n := v.(type) {
				case float64:
					d.ReasoningTokens = int64(n)
				case int64:
					d.ReasoningTokens = n
				case json.Number:
					i, _ := n.Int64()
					d.ReasoningTokens = i
				}
			}
		}
	}
	return d
}

// traeSSETerminal 记录本次 SSE 是否以可验证的业务事件结束。
type traeSSETerminal struct {
	hasOutput    bool
	hasDone      bool
	hasToolCalls bool
}

// recordOutput 解析输出事件，并只在可转换为客户端内容时将其标记为有效输出。
// tool_calls 载荷（非 null/[]）同样视为有效输出：工具型任务中模型可能只输出
// 推理 + 工具调用就终止，此类短流是正常中间态而非伪完成。
// [参数] data: output 事件的原始 JSON 数据。
// [返回] text: 正文片段；reasoning: 推理片段；toolCalls: 原始 tool_calls 数组
// JSON（null/[] 填充已过滤，需经 traeToolCallsToOpenAI 转客户端形态）；
// ok: 是否为有效输出。
// 最近修改时间：2026-09-03；改动原因：P0-③——把 tool_calls 原始载荷带出给
// 三条下行路径（collect/aggregate/pump），供客户端分片转换使用。
func (t *traeSSETerminal) recordOutput(data string) (text, reasoning string, toolCalls json.RawMessage, ok bool) {
	text, reasoning, toolCalls = normalizeOutput(data)
	if text == "" && reasoning == "" && len(toolCalls) == 0 {
		return "", "", nil, false
	}
	t.hasOutput = true
	if len(toolCalls) > 0 {
		t.hasToolCalls = true
	}
	return text, reasoning, toolCalls, true
}

// recordDone 标记上游已明确发送完成事件。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-08-30 21:01:25；改动原因：统一三条响应路径的有效终止判定。
func (t *traeSSETerminal) recordDone() {
	t.hasDone = true
}

// doneTerminalError 从 done 事件的 data 里提取上游终止原因，仅返回需要报错的
// 那一类（内容审核拦截）；正常完成返回空串。
//
// 上游把终止原因放在 done 的 data 中，形如
// "sensitive content error finish reason: sensitive" 或含
// finish_reason / finishReason 字段的结构化载荷。正常完成时 data 为空、
// 为 {} 或 [DONE]。
// [参数] data: done 事件的原始 data 文本。
// [返回] 需要作为错误上报的原因文本；正常完成为空串。
// 最近修改时间：2026-11-09 22:20:00；改动原因：新增，内容审核拦截需明确报错。
func doneTerminalError(data string) string {
	raw := strings.TrimSpace(data)
	if raw == "" || raw == "{}" || raw == "[DONE]" || raw == "null" {
		return ""
	}
	lower := strings.ToLower(raw)
	// 只认内容审核类终止：其余未知 payload 一律按正常完成处理，避免把上游
	// 新增的无关字段误判成失败（错误方向必须是「宁可放过」）。
	if !strings.Contains(lower, "sensitive") && !strings.Contains(lower, "content error") {
		return ""
	}
	return truncateRedacted(raw, 200)
}

// hasPayload 报告本次扫描是否已收到可交付的业务事件（正文输出或明确完成）。
// 供 scanSSE 在读取错误时判定：已有可交付内容的上游断流应按截断收尾，
// 而非把已生成内容连同错误一起丢弃。
// [参数] 无。
// [返回] 是否已收到正文输出或完成事件。
// 最近修改时间：2026-08-31 15:20:00；改动原因：读错误型断流（RST/unexpected EOF）需与干净 EOF 同款兜底。
func (t *traeSSETerminal) hasPayload() bool {
	return t.hasOutput || t.hasDone
}

// traeStreamTermination 描述一次 SSE 扫描的终结类别，供三条响应路径统一收尾。
// 区分「业务完整」「上游中途断流但有部分输出」「空响应」三类，避免把可兜底的部分输出误判为致命错误。
type traeStreamTermination int

const (
	terminationDone      traeStreamTermination = iota // 收到明确 done，业务完整。
	terminationOutputEOF                              // 有部分输出但 EOF 无 done，上游中途断流，可兜底收尾。
	terminationInvalid                                // 既无 output 也无 done，空响应。
)

// classify 依据扫描累积的 output/done 判定终结类别。
// [参数] statusCode: 上游 HTTP 状态码。
// [返回] traeStreamTermination: 终结类别；error: 空响应时的协议错误，其余类别为 nil。
// 最近修改时间：2026-08-31 02:10:00；改动原因：部分 output 后 EOF 是上游断流而非空成功，应兜底收尾而非报错中断。
func (t traeSSETerminal) classify(statusCode int) (traeStreamTermination, error) {
	if t.hasDone {
		return terminationDone, nil
	}
	if t.hasOutput {
		return terminationOutputEOF, nil
	}
	return terminationInvalid, fmt.Errorf("upstream %d: invalid SSE response: missing output and done event", statusCode)
}

// terminationLabel 返回终结类别的稳定短标签，供流路径日志区分业务完整 / 断流 / 空响应。
func terminationLabel(t traeStreamTermination) string {
	switch t {
	case terminationDone:
		return "done"
	case terminationOutputEOF:
		return "output_eof"
	case terminationInvalid:
		return "invalid"
	default:
		return "unknown"
	}
}

// collectTraeStream reads the upstream SSE stream, converts every output
// event into a chat.completion.chunk, and returns the chunks. On an
// event:error it surfaces the upstream message via fmt.Errorf with the
// canonical "upstream N:" prefix when status is known.
// [参数] r: 上游 SSE 响应；model: 客户端模型；statusCode: 上游 HTTP 状态码。
// [返回] chunks: 转换后的分片；hasToolCalls: 本次流是否含结构化工具调用（伪完成豁免信号）；
//
//	firstOutputAt: 第一个有效 output 事件到达时间（首字延迟 TTFT 观测点，
//	零值表示未观测到任何输出，如请求开启即失败）；detail: 上游 token_usage
//	事件带出的真实用量（未收到该事件时为空 Detail，调用方应回退估算）；
//	error: 上游错误、缺少 done 或传输截断时的协议错误。
//
// 最近修改时间：2026-09-04；改动原因：dashboard 输入/输出/思考/总 Token 列——
// 解析 event:token_usage 真实用量替代纯 content 估算（此前输入/思考恒 0）。
func collectTraeStream(r io.Reader, model string, statusCode int) ([]pluginapi.ExecutorStreamChunk, bool, time.Time, usage.Detail, error) {
	requestID := randomUUID()
	started := time.Now()
	var chunks []pluginapi.ExecutorStreamChunk
	var terminal traeSSETerminal
	var firstOutputAt time.Time
	var collector traeUsageCollector
	err := scanSSE(r, func(ev sseEvent) error {
		switch ev.Event {
		case "output":
			text, reasoning, toolCalls, ok := terminal.recordOutput(ev.Data)
			if !ok {
				return nil
			}
			if firstOutputAt.IsZero() {
				firstOutputAt = time.Now()
			}
			if raw, err := chunkDelta(requestID, model, text, reasoning, ""); err == nil && raw != nil {
				chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
			}
			if len(toolCalls) > 0 {
				if calls := traeToolCallsToOpenAI(toolCalls); len(calls) > 0 {
					if raw, err := toolCallDeltaChunk(requestID, model, calls); err == nil && raw != nil {
						chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
					}
				}
			}
		case "token_usage":
			collector.feed(ev.Data)
		case "done":
			// done 事件的 data 可能携带上游终止原因（如内容审核拦截）。
			// 旧实现直接丢弃 payload，把审核失败当业务完整收尾 —— 客户端会收到
			// 已生成的正文且无任何错误标记（生产实测 glm-5.3-flashx 耗时 1m34s、
			// 2946 分片后才报 sensitive content，此时错误信息已无处传达）。
			if msg := doneTerminalError(ev.Data); msg != "" {
				return fmt.Errorf("upstream %d: %s", statusCode, msg)
			}
			terminal.recordDone()
			return errTraeStreamDone
		case "error":
			msg := streamErrData(ev.Data)
			if msg == "" {
				msg = ev.Data
			}
			return fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(msg, 200))
		}
		return nil
	}, terminal.hasPayload)
	if err != nil {
		log.Printf("[traework] stream collect error: request_id=%s model=%s status=%d err=%s elapsed_ms=%d", requestID, model, statusCode, truncateRedacted(err.Error(), 200), time.Since(started).Milliseconds())
		return nil, false, time.Time{}, usage.Detail{}, err
	}
	termination, err := terminal.classify(statusCode)
	if err != nil {
		log.Printf("[traework] stream collect invalid: request_id=%s model=%s status=%d err=%s elapsed_ms=%d", requestID, model, statusCode, truncateRedacted(err.Error(), 200), time.Since(started).Milliseconds())
		return nil, false, time.Time{}, usage.Detail{}, err
	}
	// 收到 done 正常收尾；部分 output 后 EOF（上游中途断流）补 length 收尾，
	// 让客户端保留已生成内容，而不是把可兜底的中断误判为致命错误。仅空响应才真正报错。
	// 含工具调用的完整流以 tool_calls 终止（OpenAI 语义），客户端凭它触发工具执行。
	finish := "stop"
	switch {
	case termination == terminationOutputEOF:
		finish = "length"
	case terminal.hasToolCalls:
		finish = "tool_calls"
	}
	// 1. 先定稿下行用量：上游 token_usage 真实值优先，缺失时按已收集正文分片
	//    估算（与调用方 usageDetailForAttempt 同一兜底，且终止帧 delta 为空、
	//    不改变估算基数，故两侧数值必然一致）。必须在渲染终止帧之前算完——
	//    终止帧一旦发出就无法补挂 usage。
	downDetail := usageDetailForAttempt(collector.detail(), chunks)
	// 2. 终止帧携带 usage 下发，这是宿主与 new-api 取得 token 数的唯一来源。
	raw, _ := chunkFinishWithUsage(requestID, model, finish, downDetail)
	if raw != nil {
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
	}
	log.Printf("[traework] stream collect done: request_id=%s model=%s status=%d termination=%s chunks=%d finish=%s tool_calls=%v usage_in=%d usage_out=%d usage_total=%d elapsed_ms=%d",
		requestID, model, statusCode, terminationLabel(termination), len(chunks), finish, terminal.hasToolCalls, downDetail.InputTokens, downDetail.OutputTokens, downDetail.TotalTokens, time.Since(started).Milliseconds())
	// 返回值保持原契约：只回上游真实用量（空 Detail 表示未收到 token_usage 事件），
	// 由调用方 usageDetailForAttempt 统一决定是否估算兜底。
	return chunks, terminal.hasToolCalls, firstOutputAt, collector.detail(), nil
}

// aggregateTraeCompletion reads the upstream SSE stream and folds all output
// events into one chat.completion aggregate (non-streaming path).
// [参数] r: 上游 SSE 响应；model: 客户端模型；statusCode: 上游 HTTP 状态码。
// [返回] []byte: OpenAI 完成响应；usage.Detail: 上游 token_usage 事件带出的
// 真实用量（未收到该事件时为空 Detail，调用方应回退估算）；error: 上游错误、
// 缺少 done 或传输截断时的协议错误。
// 最近修改时间：2026-09-04；改动原因：dashboard 输入/输出/思考/总 Token 列——
// 非流式路径同样解析 event:token_usage 真实用量（此前输入/思考恒 0）。
func aggregateTraeCompletion(r io.Reader, model string, statusCode int) ([]byte, usage.Detail, error) {
	requestID := randomUUID()
	started := time.Now()
	var text, reasoning strings.Builder
	var terminal traeSSETerminal
	var collector traeUsageCollector
	// 工具调用按 index 合并：上游为快照式全量（单事件完整调用），跨事件同
	// index 的 arguments 按流式分片语义拼接作为防御（正常路径不会触发）。
	toolCalls := map[int]map[string]any{}
	var toolOrder []int
	err := scanSSE(r, func(ev sseEvent) error {
		switch ev.Event {
		case "output":
			t, rz, tcRaw, ok := terminal.recordOutput(ev.Data)
			if ok {
				text.WriteString(t)
				reasoning.WriteString(rz)
			}
			if len(tcRaw) > 0 {
				for _, call := range traeToolCallsToOpenAI(tcRaw) {
					idx := 0
					if v, ok := call["index"].(float64); ok {
						idx = int(v)
					}
					merged, seen := toolCalls[idx]
					if !seen {
						merged = map[string]any{"index": idx}
						toolCalls[idx] = merged
						toolOrder = append(toolOrder, idx)
					}
					mergeTraeToolCall(merged, call)
				}
			}
		case "token_usage":
			collector.feed(ev.Data)
		case "done":
			// done 事件的 data 可能携带上游终止原因（如内容审核拦截）。
			// 旧实现直接丢弃 payload，把审核失败当业务完整收尾 —— 客户端会收到
			// 已生成的正文且无任何错误标记（生产实测 glm-5.3-flashx 耗时 1m34s、
			// 2946 分片后才报 sensitive content，此时错误信息已无处传达）。
			if msg := doneTerminalError(ev.Data); msg != "" {
				return fmt.Errorf("upstream %d: %s", statusCode, msg)
			}
			terminal.recordDone()
			return errTraeStreamDone
		case "error":
			msg := streamErrData(ev.Data)
			if msg == "" {
				msg = ev.Data
			}
			return fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(msg, 200))
		}
		return nil
	}, terminal.hasPayload)
	if err != nil {
		log.Printf("[traework] stream aggregate error: request_id=%s model=%s status=%d err=%s elapsed_ms=%d", requestID, model, statusCode, truncateRedacted(err.Error(), 200), time.Since(started).Milliseconds())
		return nil, usage.Detail{}, err
	}
	termination, err := terminal.classify(statusCode)
	if err != nil {
		log.Printf("[traework] stream aggregate invalid: request_id=%s model=%s status=%d err=%s elapsed_ms=%d", requestID, model, statusCode, truncateRedacted(err.Error(), 200), time.Since(started).Milliseconds())
		return nil, usage.Detail{}, err
	}
	finish := "stop"
	switch {
	case termination == terminationOutputEOF:
		finish = "length"
	case len(toolOrder) > 0:
		finish = "tool_calls"
	}
	var calls []map[string]any
	if len(toolOrder) > 0 {
		sort.Ints(toolOrder)
		calls = make([]map[string]any, 0, len(toolOrder))
		for _, idx := range toolOrder {
			calls = append(calls, toolCalls[idx])
		}
	}
	// 1. 定稿下行用量：真实 token_usage 优先；缺失时只按正文 content 字符估算
	//    （口径与 estimateUsageFromCompletion 严格一致，不含 reasoning），保证
	//    响应体 usage 与调用方后续发布的 feed 用量数值相同。
	real := collector.detail()
	downDetail := real
	if _, ok := normalizeTraeDetail(real); !ok {
		toks := tokensFromChars(text.Len())
		downDetail = usage.Detail{OutputTokens: toks, TotalTokens: toks}
	}
	// 2. 聚合响应体必须带顶层 usage：宿主 ParseOpenAIUsage 与非流式计费都读这里。
	raw, aggErr := completionAggregate(requestID, model, text.String(), reasoning.String(), finish, calls, usageToOpenAI(downDetail))
	if aggErr != nil {
		return nil, usage.Detail{}, aggErr
	}
	log.Printf("[traework] stream aggregate done: request_id=%s model=%s status=%d termination=%s finish=%s chars=%d tool_calls=%d usage_in=%d usage_out=%d usage_total=%d elapsed_ms=%d",
		requestID, model, statusCode, terminationLabel(termination), finish, text.Len()+reasoning.Len(), len(calls), downDetail.InputTokens, downDetail.OutputTokens, downDetail.TotalTokens, time.Since(started).Milliseconds())
	// 返回值保持原契约：只回上游真实用量，估算兜底由调用方负责。
	return raw, real, nil
}

// mergeTraeToolCall 把单个 OpenAI 键形态的调用增量并入同 index 的聚合记录：
// id/type/函数名首次写入后不再覆盖；arguments 按流式分片语义拼接（上游快照
// 式全量时每 index 仅出现一次，拼接不会重复触发）。
// [参数] dst: 同 index 的聚合目标；src: 新到达的调用增量。
// [返回] 无。
// 最近修改时间：2026-09-03；改动原因：P0-③——非流式聚合工具调用合并。
func mergeTraeToolCall(dst, src map[string]any) {
	if id, ok := src["id"].(string); ok && id != "" {
		if _, exists := dst["id"]; !exists {
			dst["id"] = id
		}
	}
	if typ, ok := src["type"].(string); ok && typ != "" {
		if _, exists := dst["type"]; !exists {
			dst["type"] = typ
		}
	}
	fn, ok := src["function"].(map[string]any)
	if !ok {
		return
	}
	dfn, exists := dst["function"].(map[string]any)
	if !exists {
		dfn = map[string]any{}
		dst["function"] = dfn
	}
	if name, ok := fn["name"].(string); ok && name != "" {
		if cur, _ := dfn["name"].(string); cur == "" {
			dfn["name"] = name
		}
	}
	if args, ok := fn["arguments"].(string); ok && args != "" {
		cur, _ := dfn["arguments"].(string)
		dfn["arguments"] = cur + args
	}
}

// traeStreamPumpContext 保存异步流下发与用量发布共享的请求上下文。
type traeStreamPumpContext struct {
	StreamID      string    // 宿主流标识。
	Model         string    // 客户端请求模型。
	UpstreamModel string    // Trae 上游实际模型。
	StatusCode    int       // 上游 HTTP 状态码。
	AuthID        string    // 调度与故障核算使用的账号标识。
	AuthUID       string    // 用量维度使用的 Trae 账号 UID。
	Started       time.Time // 请求开始时间。
	// AttemptStarted 是本次账号尝试的发出时刻，TTFT 必须用它作基准。
	// Started 是请求级时间戳，跨重试 attempt 复用：账号 A 失败耗时 10s 后换到 B、
	// B 首帧 2s，用 Started 会被记成 12s，把 A 的失败耗时算到健康号 B 头上。
	// 慢速冷却判据直接吃这个样本会误禁健康账号，故单独记录。
	AttemptStarted time.Time
	InputChars     int    // 请求输入估算字符数，用于伪完成检测的输入长度判据。
	SessionKey     string // 会话亲和键，写入 usage feed 的 session_key 列（空表示无会话信号）。
}

// ttftNSBetween 返回 started → firstOutputAt 的纳秒差（首字延迟 TTFT）。
// 任一时间零值或差值为负（时钟抖动）时返回 0，与 workbuddy 的
// sseUsageCollector.ttftNS 语义一致。
// [参数] started: 本次尝试的发出时刻；firstOutputAt: 首个有效输出事件到达时间。
// [返回] 首字延迟纳秒数；不可观测时为 0。
// 最近修改时间：2026-09-03；改动原因：token-usage-tracker dashboard 的首字延迟列需要 traework 侧真实 ttft_ns。
func ttftNSBetween(started, firstOutputAt time.Time) uint64 {
	if started.IsZero() || firstOutputAt.IsZero() {
		return 0
	}
	d := firstOutputAt.Sub(started)
	if d <= 0 {
		return 0
	}
	return uint64(d)
}

// attemptTTFTNS 用「本次尝试发出时刻」计算首字延迟，回退到请求起点。
//
// attemptStarted 是每次账号尝试的发出时刻，requestStarted 是请求起点。重试发生后
// 两者相差整段前序耗时，用 requestStarted 会把上一个账号的失败耗时算进当前账号；
// 因此优先取 attemptStarted，仅在它未被注入（历史调用点）时回退到 requestStarted。
// [参数] attemptStarted：本次尝试发出时刻；requestStarted：请求起点；
//
//	firstOutputAt：本次尝试首个有效输出事件到达时间。
//
// [返回] 首字延迟纳秒数；不可观测时为 0。
// 最近修改时间：2026-10-25；改动原因：新增 per-attempt 计时基准，阻断慢速冷却的样本污染。
func attemptTTFTNS(attemptStarted, requestStarted, firstOutputAt time.Time) uint64 {
	base := attemptStarted
	if base.IsZero() {
		base = requestStarted
	}
	return ttftNSBetween(base, firstOutputAt)
}

// pumpTraeStream 读取 Trae 上游 SSE，向宿主推送分片，并在流结束时发布一次用量结果。
// [参数] r: 上游 SSE；ctx: 异步流下发、账号核算与用量发布上下文。
// [返回] 无；分片与错误通过宿主流通道发送，用量通过共享发布路径异步落地。
// 最近修改时间：2026-08-30 23:40:18；改动原因：补齐 done 终止校验与最终 stop 下发失败核算，客户端未完整收尾时禁止记成功。

// pseudoCompletionMaxChars 是伪完成检测的最大输出字符数（等价于 150 token 估算，
// 即 150*4=600 字符）。pseudoCompletionMinInputChars 是判伪完成所需的最小输入
// 长度。双重判据：输出字符低于阈值 且 输入是长任务时，才判伪完成——覆盖生产
// 观察到的伪完成（15~129 token，包括用户长任务被掐断的 120 token），同时通过
// 输入长度保护正常短答（如「你好」≈5 token）不被误判。健康长输出 215+ token
// （860+ 字符）不受影响。旧实现用 120 字符阈值，漏掉 ~120 token（≈480 字符）
// 的伪完成。用字符数而非估算 token，避免短输出 chars/4 取整归零误判。
const (
	pseudoCompletionMaxChars = 600
	// pseudoCompletionMinInputChars 是判伪完成所需的最小输入字符数。
	//
	// 取 20000（约 5000 token）：长任务不必然要求长输出——模型判断无需展开时
	// 回一句「好的」是完全正常的短答。阈值过低会把这类健康短答误判为伪完成，
	// 触发换号重试。2026-11-09 生产实测：单请求连续 pseudo retry 11 次换遍
	// 11 个账号全部判伪，最终 pool exhausted；同期 632 条伪完成样本的 input
	// 中位仅 188 字符，全部是短请求被误判。
	// 真正的伪完成（上游掐断长任务）input 通常在数万字符量级，20000 足以区分。
	pseudoCompletionMinInputChars = 20000
	// pseudoRetryBudget 是同一账号在同一逻辑请求内被判伪完成后的同号重试次数。
	// 伪完成常是上游对该账号的窗口性限流（2351 实证：同号约 30s 后恢复 18696
	// tokens），立即 noteForcedAccountFailure（打入固定 15s 冷却）+ 换号在池中
	// 只剩该健康账号时会直接 pool exhausted。同号重试一次让窗口性限流有机会自愈，
	// 仍伪才核算 + 换号；不消耗跨账号 Budget，仅受本预算约束。
	pseudoRetryBudget = 1

	// pseudoGateMaxHold 是首字闸门的最长持有时间。
	//
	// 闸门的存在目的是「长输入 + 双轴都短」时不在换号前把正文泄漏给客户端；
	// 但它按字符数攒够 pseudoCompletionMaxChars(600) 才放行，思考型模型在
	// reasoning 阶段可能持续数十秒都不满 600 字符，用户侧首字被整段拖后。
	// 生产实测（2026-11-09）：99.1% 的 traework 请求 input ≥ 5000 token 走闸门，
	// 闸门组 TTFT p50 5630ms、直通组仅 1866ms。
	// 上限设为 1200ms：超过它即无条件放行已缓冲分片 —— 伪完成防护让位于首字体感，
	// 因为「已生成内容被回滚」对用户是可接受的降级，而「长时间零输出」不可接受。
	pseudoGateMaxHold = 1200 * time.Millisecond
)

// isPseudoCompletion reports whether a done-terminated stream carried far less
// output than a long-input task warrants — the signature of an account silently
// throttled/flagged by the upstream. inputChars is the estimated prompt length;
// a short prompt with a short answer is NOT treated as a pseudo completion.
// Reasoning counts as healthy output: a thinking model that reasons at length
// then answers tersely (long reasoning + short content) is healthy, and a
// reasoning-only stream is a legitimate thinking trace, not a throttle. Only a
// stream with near-nothing on BOTH axes (short content AND short/no reasoning)
// is flagged. hasToolCalls exempts tool-invoking streams entirely: a model that
// ends its turn with a structured tool call stops with very little content by
// design — that is a normal mid-task state, not an account throttle.
// 最近修改时间：2026-09-03；改动原因：P1 止血——含结构化工具调用的短流永不判伪
// （上游取证：正常工具调用流 1.9s / 3 output 事件 / reasoning ~100 字符即 done）。
func isPseudoCompletion(chunks []pluginapi.ExecutorStreamChunk, inputChars int, hasToolCalls bool) bool {
	if hasToolCalls {
		return false
	}
	contentChars, reasoningChars := 0, 0
	for _, c := range chunks {
		var ch struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(c.Payload, &ch); err != nil {
			continue
		}
		for _, c2 := range ch.Choices {
			contentChars += len(c2.Delta.Content)
			reasoningChars += len(c2.Delta.Reasoning)
		}
	}
	// Healthy axes: content ≥ threshold, or reasoning ≥ threshold (thinking model
	// that reasoned fully), or pure reasoning trace (existing terminal contract:
	// reasoning-only is never pseudo, regardless of length).
	if contentChars >= pseudoCompletionMaxChars || reasoningChars >= pseudoCompletionMaxChars {
		return false
	}
	if contentChars <= 0 && reasoningChars > 0 {
		return false
	}
	if contentChars+reasoningChars <= 0 {
		return false
	}
	return inputChars >= pseudoCompletionMinInputChars
}

// traeStreamAttemptResult 保存单次上游流尝试的转换结果；逻辑请求协调器据此决定换号或唯一收尾。
type traeStreamAttemptResult struct {
	RequestID    string
	Chunks       []pluginapi.ExecutorStreamChunk
	Termination  traeStreamTermination
	Pseudo       bool
	Emitted      bool
	HasToolCalls bool
	// FirstOutputAt 是第一个有效 output 事件的到达时间（TTFT 观测点）；
	// 零值表示该尝试未观测到任何输出（如开启即失败）。
	FirstOutputAt time.Time
	// GateOpenedAt 是首帧真正下发给客户端的时刻；零值表示闸门从未打开
	// （分片全部留在 pending 里，由收尾逻辑统一释放）。与 FirstOutputAt 的
	// 差值即闸门缓冲给用户感知首字带来的额外延迟。
	// 最近修改时间：2026-11-09 22:50:00；改动原因：新增，量化闸门对首字延迟的贡献。
	GateOpenedAt time.Time
	// Usage 是上游 token_usage 事件带出的真实用量（未收到该事件时为空，
	// 调用方应回退 estimateUsageFromChunks 估算，见 usageDetailForAttempt）。
	// 最近修改时间：2026-09-04；改动原因：dashboard Token 列真实 usage。
	Usage usage.Detail
	Err   error
}

// traeStreamEmitter 下发一个已转换的客户端分片。
type traeStreamEmitter func(payload []byte) error

// pumpTraeStreamAttempt 读取单次 Trae SSE；长输入达到健康门槛前缓存全部分片，伪完成时不向客户端泄漏。
// [参数] r: 单次上游 SSE；ctx: 流上下文；requestID: 逻辑请求固定 ID；emit: 分片下发函数。
// [返回] 单次尝试的终结类别、分片、伪完成和下发状态；本函数不发送 finish，也不关闭宿主流。
// 最近修改时间：2026-09-02 23:40:00；改动原因：思考型长推理在 reasoning 阶段即应流式放行——
// gate 只累计 content 会把 reasoning-only 分片无限压 pending，长思考（reasoning≥600 字符）
// 时客户端/nginx 长时间零字节触发 300s 读超时 504（生产 stream 2878）；reasoning 长在
// isPseudoCompletion 已豁免为健康，放行 reasoning 不构成伪完成泄漏。
func pumpTraeStreamAttempt(r io.Reader, ctx traeStreamPumpContext, requestID string, emit traeStreamEmitter) traeStreamAttemptResult {
	result := traeStreamAttemptResult{RequestID: requestID}
	gateOpen := ctx.InputChars < pseudoCompletionMinInputChars
	healthChars := 0
	pending := make([][]byte, 0)
	// gateOpenedAt 记录闸门打开（首帧下发）时刻，用于观测闸门对首字延迟的贡献。
	var gateOpenedAt time.Time
	var terminal traeSSETerminal
	var collector traeUsageCollector

	// 1. 转换每个 output；长输入在 content+reasoning 合计达到健康门槛前只缓存，
	//    不向客户端承诺当前账号（双轴都短的真伪完成全程零泄漏）。含结构化工具调用的
	//    流立即放行——工具型任务以短输出 + tool_calls 终止是正常中间态，不受 gate 约束。
	scanErr := scanSSE(r, func(ev sseEvent) error {
		switch ev.Event {
		case "output":
			text, reasoning, tcRaw, ok := terminal.recordOutput(ev.Data)
			if !ok {
				return nil
			}
			// 一个 output 事件可同时产出正文分片与 tool_calls 分片（上游工具
			// 事件为快照式全量，整块转换后由客户端 SDK 按 index 累积）。
			var raws [][]byte
			if raw, err := chunkDelta(requestID, ctx.Model, text, reasoning, ""); err != nil {
				return err
			} else if raw != nil {
				raws = append(raws, raw)
			}
			if len(tcRaw) > 0 {
				if calls := traeToolCallsToOpenAI(tcRaw); len(calls) > 0 {
					if raw, err := toolCallDeltaChunk(requestID, ctx.Model, calls); err != nil {
						return err
					} else if raw != nil {
						raws = append(raws, raw)
					}
				}
			}
			if len(raws) == 0 {
				return nil
			}
			if result.FirstOutputAt.IsZero() {
				result.FirstOutputAt = time.Now()
			}
			healthChars += len(text) + len(reasoning)
			for _, raw := range raws {
				result.Chunks = append(result.Chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
				if !gateOpen {
					pending = append(pending, raw)
					// 双闸门：攒够健康字符（防伪完成泄漏）**或**持有超过
					// pseudoGateMaxHold（保首字体感），任一满足即放行。
					heldTooLong := !result.FirstOutputAt.IsZero() &&
						time.Since(result.FirstOutputAt) >= pseudoGateMaxHold
					if healthChars < pseudoCompletionMaxChars && !terminal.hasToolCalls && !heldTooLong {
						continue
					}
					gateOpen = true
					gateOpenedAt = time.Now()
					result.GateOpenedAt = gateOpenedAt
					for _, buffered := range pending {
						if err := emit(buffered); err != nil {
							return err
						}
						result.Emitted = true
					}
					pending = nil
					continue
				}
				if err := emit(raw); err != nil {
					return err
				}
				result.Emitted = true
			}
		case "token_usage":
			collector.feed(ev.Data)
		case "done":
			// done 事件的 data 可能携带上游终止原因（如内容审核拦截）。
			// 旧实现直接丢弃 payload，把审核失败当业务完整收尾 —— 客户端会收到
			// 已生成的正文且无任何错误标记（生产实测 glm-5.3-flashx 耗时 1m34s、
			// 2946 分片后才报 sensitive content，此时错误信息已无处传达）。
			if msg := doneTerminalError(ev.Data); msg != "" {
				return fmt.Errorf("upstream %d: %s", ctx.StatusCode, msg)
			}
			terminal.recordDone()
			return errTraeStreamDone
		case "error":
			msg := streamErrData(ev.Data)
			if msg == "" {
				msg = ev.Data
			}
			return fmt.Errorf("upstream %d: %s", ctx.StatusCode, truncateRedacted(msg, 200))
		}
		return nil
	}, terminal.hasPayload)
	// 无论成败都把真实用量带出（协调器在发布前对空 Detail 回退估算）。
	result.Usage = collector.detail()
	if scanErr != nil {
		result.Err = scanErr
		return result
	}

	// 2. 先分类并识别伪完成；命中时丢弃 pending，禁止下发正文、reasoning 和终止分片。
	result.Termination, result.Err = terminal.classify(ctx.StatusCode)
	if result.Err != nil {
		return result
	}
	result.HasToolCalls = terminal.hasToolCalls
	if result.Termination == terminationDone && isPseudoCompletion(result.Chunks, ctx.InputChars, terminal.hasToolCalls) {
		result.Pseudo = true
		return result
	}

	// 3. reasoning-only 或 output_eof 不属于伪完成，按既有兼容语义释放尚未承诺的缓冲。
	for _, buffered := range pending {
		if err := emit(buffered); err != nil {
			result.Err = err
			return result
		}
		result.Emitted = true
	}
	return result
}

// normalizeTraeDetail 判断上游是否真的报过用量，并在缺失 total 时用
// input+output 补齐。上游只给 prompt_tokens / completion_tokens 而不给
// total_tokens 时，只按 TotalTokens>0 判断会把真实值当成"未上报"丢掉、退回
// 字符估算，下行 usage 也就与上游实际计数不一致。
// [参数] d: 上游 token_usage 折出的明细。
// [返回] 归一化后的明细；三个桶全为 0 时 ok=false（视为未上报）。
// 最近修改时间：2026-11-08 13:56:00；改动原因：下行 usage 化后必须与 feed 用量
// 同源同值，total 缺失不能再误判为"无真实用量"。
func normalizeTraeDetail(d usage.Detail) (usage.Detail, bool) {
	if d.TotalTokens <= 0 && d.InputTokens <= 0 && d.OutputTokens <= 0 {
		return usage.Detail{}, false
	}
	if d.TotalTokens <= 0 {
		d.TotalTokens = d.InputTokens + d.OutputTokens
	}
	return d, true
}

// usageDetailForAttempt 返回一次流式尝试的用量 detail：优先上游 token_usage
// 真实值，缺失时回退 content 字符估算——旧上游 / 开启即失败 / 断流早于
// token_usage 事件时保持与 0.1.35 一致的估算行为。
// [参数] real: 尝试带出的真实用量（可为空）；chunks: 已收集分片（估算输入）。
// [返回] usage.Detail: 同时用于终止帧 usage 与 feed / CPAMP 的最终用量。
// 最近修改时间：2026-11-08 13:56:00；改动原因：真实用量判定放宽为
// "任一桶非零"（上游可只给 prompt/completion），并补齐 total。
func usageDetailForAttempt(real usage.Detail, chunks []pluginapi.ExecutorStreamChunk) usage.Detail {
	if d, ok := normalizeTraeDetail(real); ok {
		return d
	}
	return estimateUsageFromChunks(chunks)
}

// usageDetailForCompletion 返回非流式请求的用量 detail：优先上游 token_usage
// 真实值，缺失时回退 content 字符估算——与 usageDetailForAttempt 的流式语义
// 同构，保证非流式路径在旧上游 / 断流时保持与 0.1.35 一致的估算行为。
// [参数] real: 聚合带出的真实用量（可为空）；completion: OpenAI 完成响应 JSON（估算输入）。
// [返回] usage.Detail: 同时用于响应体 usage 与 feed 的最终用量。
// 最近修改时间：2026-11-08 13:56:00；改动原因：与流式路径同款放宽与补齐。
func usageDetailForCompletion(real usage.Detail, completion []byte) usage.Detail {
	if d, ok := normalizeTraeDetail(real); ok {
		return d
	}
	return estimateUsageFromCompletion(completion)
}

// pumpTraeStream 保留单次异步流入口；账号级协调与唯一终结由 executor 统一接管。
func pumpTraeStream(r io.Reader, ctx traeStreamPumpContext) {
	requestID := randomUUID()
	started := time.Now()
	result := pumpTraeStreamAttempt(r, ctx, requestID, func(payload []byte) error {
		return streamEmit(ctx.StreamID, payload)
	})
	if result.Err != nil {
		log.Printf("[traework] stream pump error: request_id=%s stream_id=%s model=%s status=%d err=%s elapsed_ms=%d",
			requestID, ctx.StreamID, ctx.Model, ctx.StatusCode, truncateRedacted(result.Err.Error(), 200), time.Since(started).Milliseconds())
		reconcileAfterExecutorErrorForModel(ctx.AuthID, ctx.UpstreamModel, ctx.StatusCode, result.Err.Error())
		streamEmitError(ctx.StreamID, result.Err.Error())
		publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, usageDetailForAttempt(result.Usage, result.Chunks), true, ctx.StatusCode, result.Err.Error(), "", attemptTTFTNS(ctx.AttemptStarted, ctx.Started, result.FirstOutputAt), ctx.AuthUID, ctx.SessionKey)
		return
	}
	if result.Pseudo {
		reason := "pseudo completion: upstream returned done with near-empty output"
		log.Printf("[traework] stream pump pseudo-done: request_id=%s stream_id=%s model=%s status=%d chunks=%d elapsed_ms=%d",
			requestID, ctx.StreamID, ctx.Model, ctx.StatusCode, len(result.Chunks), time.Since(started).Milliseconds())
		noteForcedAccountFailureForModel(ctx.AuthID, ctx.UpstreamModel, reason)
		evictSessionBindingsForAuth(ctx.AuthID)
		streamEmitError(ctx.StreamID, reason)
		publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, usageDetailForAttempt(result.Usage, result.Chunks), true, ctx.StatusCode, reason, "", attemptTTFTNS(ctx.AttemptStarted, ctx.Started, result.FirstOutputAt), ctx.AuthUID, ctx.SessionKey)
		return
	}

	finish := "stop"
	failed := false
	failureReason := ""
	if result.Termination == terminationOutputEOF {
		finish = "length"
		failed = true
		failureReason = "truncated: upstream stream ended without done"
	} else if result.HasToolCalls {
		finish = "tool_calls"
	}
	// 终止帧携带 usage：本函数的 result.Chunks 尚未包含终止帧，估算基数正确。
	raw, err := chunkFinishWithUsage(requestID, ctx.Model, finish, usageDetailForAttempt(result.Usage, result.Chunks))
	if err == nil && raw != nil {
		err = streamEmit(ctx.StreamID, raw)
		if err == nil {
			result.Chunks = append(result.Chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
		}
	}
	if err != nil {
		reconcileAfterExecutorErrorForModel(ctx.AuthID, ctx.UpstreamModel, ctx.StatusCode, err.Error())
		streamEmitError(ctx.StreamID, err.Error())
		publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, usageDetailForAttempt(result.Usage, result.Chunks), true, ctx.StatusCode, err.Error(), "", attemptTTFTNS(ctx.AttemptStarted, ctx.Started, result.FirstOutputAt), ctx.AuthUID, ctx.SessionKey)
		return
	}
	streamClose(ctx.StreamID)
	if failed {
		publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, usageDetailForAttempt(result.Usage, result.Chunks), true, ctx.StatusCode, failureReason, "", attemptTTFTNS(ctx.AttemptStarted, ctx.Started, result.FirstOutputAt), ctx.AuthUID, ctx.SessionKey)
		return
	}
	resetAccountFailover(ctx.AuthID)
	// 轮换打点：直通流式收尾路径。此处与 executor 的 collect/aggregate 是三个
	// 互斥的成功返回点，任何一处漏接都会让该模式的流量不计入连续承流。
	noteRotationUsage(ctx.AuthID)
	pumpTTFT := attemptTTFTNS(ctx.AttemptStarted, ctx.Started, result.FirstOutputAt)
	// 闸门延迟观测：上游首帧到客户端首帧的间隔。为 0 表示未走闸门（短输入直通），
	// 明显大于 0 说明首帧被缓冲到攒够健康字符才下发。
	if !result.FirstOutputAt.IsZero() && !result.GateOpenedAt.IsZero() {
		log.Printf("[traework] stream pump gate delay: request_id=%s stream_id=%s model=%s gate_ms=%d ttft_ms=%d",
			requestID, ctx.StreamID, ctx.Model,
			result.GateOpenedAt.Sub(result.FirstOutputAt).Milliseconds(),
			time.Duration(pumpTTFT).Milliseconds())
	}
	publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, usageDetailForAttempt(result.Usage, result.Chunks), false, 0, "", "", pumpTTFT, ctx.AuthUID, ctx.SessionKey)
	// 慢速观测：直通流式收尾路径。此处与 executor 的 collect/async 是三个互斥的
	// 成功返回点，任何一处漏接都会让该模式的流量不计入慢速判定。
	noteSlowObservation(ctx.AuthID, ctx.UpstreamModel, pumpTTFT)
}

// clientNeedsSSEFrame reports whether the client expects raw SSE framing in
// the chunks (older clients) vs plain JSON payloads (current convention).
func clientNeedsSSEFrame(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	if v, ok := metadata["sse_framed"].(bool); ok {
		return v
	}
	return false
}

// stripProviderPrefix removes a leading "<provider>/" segment from a
// CPA-facing model name, leaving the bare alias/key.
func stripProviderPrefix(model string) string {
	if i := strings.Index(model, "/"); i > 0 {
		return model[i+1:]
	}
	return model
}

// openAIRequest mirrors the chat-completions request body the host forwards.
// Tools/ToolChoice 保持原始 any 形态接收（object/string 都来自客户端 SDK），
// 上行前经 traeToolsFromOpenAI / normalizeTraeToolChoice 规整。
// 最近修改时间：2026-11-07 22:10:00；改动原因：补 max_completion_tokens 别名。
type openAIRequest struct {
	Model    string           `json:"model"`
	Messages []map[string]any `json:"messages"`
	// Stream 用 flexBool：客户端发 `"stream":"yes"` 时 bool 强类型会让整包解析失败，
	// 宿主据此遍历整池（2026-11-08 实证日志 18 条 stream of type bool）。见 model_specs.go。
	Stream flexBool `json:"stream"`
	// MaxTokens 是 OpenAI 旧字段；MaxCompletionTokens 是新别名。
	// 2026-11-07 生产实测：DSH / Codex 系新客户端只发 max_completion_tokens，
	// 结构体缺该字段时该值被 json.Unmarshal 直接丢弃，等价于客户端从未声明上限，
	// 上游按自己默认上限生成（实测请求 12 token 实际产出 1091 字符 / 1119 tokens）。
	// 两者并存时显式 max_tokens 优先，语义与 qoderwork / workbuddy 一致。
	// 用 flexNumber 而非 int：客户端发 `"max_tokens":"abc"` 这类字符串时，
	// int 会让整个 json.Unmarshal 失败，宿主据此遍历整池账号（2026-11-08 实证）。
	// flexNumber 对数字 / 数字字符串取整，其余降级为 0（=未声明），见 model_specs.go。
	MaxTokens           flexNumber `json:"max_tokens"`
	MaxCompletionTokens flexNumber `json:"max_completion_tokens"`
	// 采样参数用 flexFloat：客户端发 `"temperature":"abc"` 时 *float64 会让整包解析失败
	// （2026-11-08 实证日志 14 条 temperature + 1 条 top_p）。flexFloat 自带 set 标记，
	// 调用方用 Float() 同时拿到「值」和「客户端是否声明」。
	Temperature flexFloat        `json:"temperature"`
	TopP        flexFloat        `json:"top_p"`
	Tools       []map[string]any `json:"tools"`
	ToolChoice  any              `json:"tool_choice"`
}

// toTraeMessages normalizes OpenAI messages into the Trae messages shape.
// The upstream LLMRawMessage expects messages[].content as a content-parts
// array ([{"type":"text","text":...}]) — a plain string fails with 4001
// "cannot unmarshal string into ... []*LLMRawMessageContent". Multi-part
// content arrays are mapped 1:1 onto text parts.
//
// P0-④ 工具语义保留（2026-09-03 多轮取证）：
//   - assistant 携带 tool_calls 的消息必须保留，且键必须从 OpenAI 标准
//     function 改为上游 protobuf 要求的 function_call（function 键直发会
//     400 "ToolCall read field 4 'FunctionCall' error"）；content 为 null/缺省
//     时补空 parts 数组（上游 LLMRawMessage.content 允许空数组）。
//   - role=tool 的结果消息原样保留（role + tool_call_id + content parts）。
func toTraeMessages(msgs []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		role, _ := m["role"].(string)
		base := map[string]any{"role": role}
		switch c := m["content"].(type) {
		case string:
			base["content"] = textParts(c)
		case []any:
			var parts []map[string]any
			for _, item := range c {
				if part, ok := item.(map[string]any); ok {
					if txt, ok := part["text"].(string); ok && txt != "" {
						parts = append(parts, map[string]any{"type": "text", "text": txt})
					}
				}
			}
			if len(parts) > 0 {
				base["content"] = parts
			} else if role == "assistant" || role == "tool" {
				// 空数组：仅工具语义消息保留（assistant 工具调用消息的 content 可为空）。
				base["content"] = []map[string]any{}
			} else {
				continue
			}
		default:
			// content 为 nil/null 或缺省：仅 assistant（工具调用消息常见
			// content:null）与 tool（工具结果可能无文本）两类保留并补空 parts；
			// 其它角色无内容则丢弃。
			switch role {
			case "assistant", "tool":
				base["content"] = []map[string]any{}
			default:
				continue
			}
		}
		if tcs, ok := m["tool_calls"].([]any); ok && len(tcs) > 0 {
			base["tool_calls"] = traeToolCallsFromOpenAIHistory(tcs)
		}
		if role == "tool" {
			if id, ok := m["tool_call_id"].(string); ok && id != "" {
				base["tool_call_id"] = id
			}
		}
		out = append(out, base)
	}
	return out
}

// traeToolCallsFromOpenAIHistory 把客户端历史消息中的 OpenAI 标准 tool_calls
// （function 键）转成上游 protobuf 要求的 function_call 变体键。id/type 原样
// 保留；function.name/arguments 移入 function_call 子对象；arguments 已是
// 字符串（OpenAI SDK 约定），无需再序列化。
// 最近修改时间：2026-09-03；改动原因：P0-④——上行历史工具调用键适配。
func traeToolCallsFromOpenAIHistory(tcs []any) []map[string]any {
	out := make([]map[string]any, 0, len(tcs))
	for _, item := range tcs {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rec := map[string]any{}
		if id, ok := call["id"].(string); ok && id != "" {
			rec["id"] = id
		}
		if typ, ok := call["type"].(string); ok && typ != "" {
			rec["type"] = typ
		}
		fn, ok := call["function"].(map[string]any)
		if !ok {
			continue
		}
		fc := map[string]any{}
		if name, ok := fn["name"].(string); ok && name != "" {
			fc["name"] = name
		}
		switch args := fn["arguments"].(type) {
		case string:
			fc["arguments"] = args
		case map[string]any:
			// 防御：个别客户端把 arguments 发成对象，序列化为字符串。
			if raw, err := json.Marshal(args); err == nil {
				fc["arguments"] = string(raw)
			}
		}
		rec["function_call"] = fc
		out = append(out, rec)
	}
	return out
}

// textParts wraps plain text into the single-part content array the upstream
// LLMRawMessage contract requires.
func textParts(s string) []map[string]any {
	return []map[string]any{{"type": "text", "text": s}}
}
