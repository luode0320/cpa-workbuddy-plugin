package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/tidwall/gjson"
)

func streamHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	return h
}

// streamEmit 向宿主 stream 发送一个 chunk。
func streamEmit(streamID string, payload []byte) error {
	if streamID == "" {
		return fmt.Errorf("no stream id")
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID, "payload": payload})
	_, err := hostCall(pluginabi.MethodHostStreamEmit, body)
	return err
}

// streamEmitError 向宿主发送流式错误终止信号。
// 上游错误文本可能回显鉴权头或 JWT，出口前必须经 redactSecrets 脱敏，
// 否则会原样落到宿主日志与客户端可见的错误帧里。
func streamEmitError(streamID string, message string) {
	if streamID == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID, "error": redactSecrets(message)})
	_, _ = hostCall(pluginabi.MethodHostStreamEmit, body)
}

// streamClose 关闭宿主流。
func streamClose(streamID string) {
	if streamID == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID})
	_, _ = hostCall(pluginabi.MethodHostStreamClose, body)
}

// streamEmitFn / streamCloseFn / streamEmitErrorFn 是流出口的测试注入点。
// pumpUpstreamStream 只能通过它们发数据：单测要验证「终止后是否真的停止读取」，
// 若直接调用真实 hostCall，shim 下必然失败并让 pump 提前 return，
// 测出来的「已停止」其实是出口报错导致的假阳性。
var (
	streamEmitFn      = streamEmit
	streamCloseFn     = streamClose
	streamEmitErrorFn = streamEmitError
)

// sseErrorFrame 从一帧 SSE 数据里提取上游业务错误文本。
//
// 上游有两类「失败」：HTTP 状态码非 2xx，以及 200 响应体内承载的
// `{"error": {...}}`。后者如果只按状态码判断就会被当成功，进而
// resetAccountFailover 把刚记下的失败状态清掉，故障账号继续被路由。
//
// [参数] content：已去掉 `data:` 前缀的单帧 JSON 文本。
// [返回] 错误文本；非错误帧返回空串。
// 最近修改时间：2026-10-25；改动原因：新增，识别 HTTP 200 承载的业务错误帧。
func sseErrorFrame(content string) string {
	var obj map[string]any
	if json.Unmarshal([]byte(content), &obj) != nil {
		return ""
	}
	v, present := obj["error"]
	if !present || v == nil {
		return ""
	}
	switch t := v.(type) {
	case map[string]any:
		msg, _ := t["message"].(string)
		if msg == "" {
			return ""
		}
		if code, _ := t["code"].(string); code != "" {
			return code + ": " + msg
		}
		return msg
	case string:
		return t
	}
	return ""
}

// isStreamDoneLine 判定一行 SSE 文本是否为上游的终止标记。
//
// Anthropic 协议用 `data: [DONE]`，部分网关用 `event: done`；两种都要认，
// 否则会继续等在不会再有数据的长连接上。判定只看去掉空白后的行内容，
// 不解析 JSON，因此对畸形帧也安全。
// [参数] line：已 TrimSpace 的单行 SSE 文本。
// [返回] true 表示这是终止标记，调用方应立即停止读取。
// 最近修改时间：2026-10-25；改动原因：新增，修复 [DONE] 后不终止导致死等空闲连接。
func isStreamDoneLine(line string) bool {
	switch line {
	case "data: [DONE]", "data:[DONE]", "event: done", "event:done":
		return true
	}
	return false
}

// pumpUpstreamStream 后台读取上游 SSE 响应并将数据逐块推送给宿主。
func pumpUpstreamStream(
	ctx context.Context,
	cancel context.CancelFunc,
	streamID string,
	resp *http.Response,
	authID, accountLabel, model, upstreamModel string,
	startTime time.Time,
) {
	defer func() {
		cancel()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		streamCloseFn(streamID)
	}()

	var inTokens, outTokens, cacheReadTokens, cacheCreationTokens int64
	var ttftNS uint64
	firstByteSeen := false
	reader := bufio.NewReader(resp.Body)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if !firstByteSeen {
				firstByteSeen = true
				if d := time.Since(startTime); d > 0 {
					ttftNS = uint64(d.Nanoseconds())
				}
			}

			lineStr := strings.TrimSpace(string(line))

			// 上游发出终止标记后必须立即停止读取：Keep-Alive 空闲连接不会
			// 主动 EOF，继续 ReadBytes 会让 goroutine 挂在流上直到超时，
			// 宿主侧则表现为「明明已经答完却迟迟不结束」。终止帧本身也不
			// 转发——宿主会自行追加自己的流终止符。
			if isStreamDoneLine(lineStr) {
				break
			}

			if strings.HasPrefix(lineStr, "data:") {
				dataPayload := strings.TrimSpace(strings.TrimPrefix(lineStr, "data:"))
				// 200 响应体内的业务错误帧必须按失败收尾：继续当正常帧转发
				// 会让下游看到「成功」，本函数的成功分支还会把刚记的失败状态
				// 清掉，故障账号因此继续被路由。
				if msg := sseErrorFrame(dataPayload); msg != "" {
					streamEmitErrorFn(streamID, truncateRedacted(msg, 200))
					publishUsage(authID, accountLabel, model, upstreamModel, startTime, usageTokensDetail{
						InputTokens:     inTokens,
						OutputTokens:    outTokens,
						CacheReadTokens: cacheReadTokens,
						TotalTokens:     inTokens + outTokens,
					}, time.Since(startTime).Milliseconds(), resp.StatusCode, true, msg, ttftNS)
					// 状态码是 200，不能直接交给分类器（会判成功），
					// 与断流同样按 status 0 记账。
					noteAccountFailureForModel(authID, upstreamModel, 0, msg)
					return
				}
				if dataPayload != "" {
					// 提取 token 用量（Anthropic 协议）
					if it := gjson.Get(dataPayload, "message.usage.input_tokens").Int(); it > 0 {
						inTokens = it
					}
					if ot := gjson.Get(dataPayload, "usage.output_tokens").Int(); ot > 0 {
						outTokens = ot
					}
					if crit := gjson.Get(dataPayload, "message.usage.cache_read_input_tokens").Int(); crit > 0 {
						cacheReadTokens = crit
					}
					if ccit := gjson.Get(dataPayload, "message.usage.cache_creation_input_tokens").Int(); ccit > 0 {
						cacheCreationTokens = ccit
					}
				}
			}

			if errEmit := streamEmitFn(streamID, line); errEmit != nil {
				// 客户端可能已断开
				return
			}
		}

		if err != nil {
			durationMS := time.Since(startTime).Milliseconds()
			detail := usageTokensDetail{
				InputTokens:         inTokens,
				OutputTokens:        outTokens,
				CacheReadTokens:     cacheReadTokens,
				CacheCreationTokens: cacheCreationTokens,
				TotalTokens:         inTokens + outTokens,
			}
			if err != io.EOF {
				streamEmitErrorFn(streamID, err.Error())
				publishUsage(authID, accountLabel, model, upstreamModel, startTime, detail, durationMS, resp.StatusCode, true, err.Error(), ttftNS)
				// 读到此处说明响应头已成功返回（StatusCode 恒为 2xx），失败发生在
				// 流读取阶段，属传输层故障。必须按 status 0 记账：传 200 会被
				// classifyUpstreamFailure 判为成功响应而不计入冷却，导致真实断流漏记。
				noteAccountFailureForModel(authID, upstreamModel, 0, err.Error())
				return
			}
			break
		}
	}

	durationMS := time.Since(startTime).Milliseconds()
	detail := usageTokensDetail{
		InputTokens:         inTokens,
		OutputTokens:        outTokens,
		CacheReadTokens:     cacheReadTokens,
		CacheCreationTokens: cacheCreationTokens,
		TotalTokens:         inTokens + outTokens,
	}
	publishUsage(authID, accountLabel, model, upstreamModel, startTime, detail, durationMS, resp.StatusCode, false, "", ttftNS)
	resetAccountFailover(authID)
	// 一次真实成功即证明该模型已恢复，清掉该模型上残留的冷却。
	resetModelBlockedState(upstreamModel)
	// 慢速观测：流式是主要成功路径，漏接会让绝大多数流量不参与慢速判定。
	noteSlowObservation(authID, upstreamModel, ttftNS)
}
