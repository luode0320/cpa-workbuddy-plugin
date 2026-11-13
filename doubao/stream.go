package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

// 本文件实现宿主流式通道的写入与 OpenAI 帧构造。
//
// 宿主的流式 RPC 是「请求/响应」形态：插件在 executor.execute_stream 里立即返回
// 响应头，随后通过 host.stream.emit 逐帧回推，最后 host.stream.close 收尾。
// 帧内容是 OpenAI chat.completion.chunk JSON，SSE 的 "data: " 分帧由宿主添加。

// streamHeaders 返回流式响应的标准头。
// [参数] 无。
// [返回] 响应头。
// 最近修改时间：2026-10-17；改动原因：新增，流式响应头。
func streamHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	return h
}

// streamEmit 向宿主推送一个流式帧。
// [参数] streamID: 宿主流标识；payload: 帧内容。
// [返回] 推送错误；客户端已断开时由宿主返回错误。
// 最近修改时间：2026-10-17；改动原因：新增，宿主流写入。
func streamEmit(streamID string, payload []byte) error {
	if streamID == "" {
		return fmt.Errorf("缺少 stream id")
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID, "payload": payload})
	_, errCall := hostCall(pluginabi.MethodHostStreamEmit, body)
	return errCall
}

// streamEmitError 向宿主发送流式错误终止信号。
// [参数] streamID: 宿主流标识；message: 错误说明。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，流式错误上报。
func streamEmitError(streamID, message string) {
	if streamID == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID, "error": message})
	_, _ = hostCall(pluginabi.MethodHostStreamEmit, body)
}

// streamClose 关闭宿主流。
//
// 每条流必须且只能关闭一次：未关闭会泄漏宿主侧资源，重复关闭会被宿主忽略但
// 仍属调用方缺陷。
// [参数] streamID: 宿主流标识。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，流式收尾。
func streamClose(streamID string) {
	if streamID == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID})
	_, _ = hostCall(pluginabi.MethodHostStreamClose, body)
}

// streamDelta 构造一个文本增量帧。
//
// 首帧带 role=assistant：OpenAI 客户端据此初始化消息对象，缺失会导致部分 SDK
// 渲染异常；后续帧不再重复该字段。
// [参数] model: 客户端请求的模型名；delta: 文本增量；first: 是否首帧。
// [返回] 帧 JSON；序列化失败时返回 nil。
// 最近修改时间：2026-10-17；改动原因：新增，增量帧构造。
func streamDelta(model, delta string, first bool) []byte {
	body := map[string]any{"content": delta}
	if first {
		body["role"] = "assistant"
	}
	payload, errMarshal := json.Marshal(streamChunk(model, "", body, ""))
	if errMarshal != nil {
		return nil
	}
	return payload
}

// streamStopFrame 构造终止帧。
// [参数] model: 模型名；conversationID: 上游会话 id。
// [返回] 帧 JSON；序列化失败时返回 nil。
// 最近修改时间：2026-10-17；改动原因：新增，终止帧构造。
func streamStopFrame(model, conversationID string) []byte {
	payload, errMarshal := json.Marshal(streamChunk(model, conversationID, map[string]any{}, "stop"))
	if errMarshal != nil {
		return nil
	}
	return payload
}
