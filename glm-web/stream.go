package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

// streamEmit 向宿主 stream 发送一个 chunk。
// [参数] streamID: 流 ID；payload: 数据块。
// [返回] 发送错误。
// 最近修改时间：2026-11-28 新增, 宿主流出口。
func streamEmit(streamID string, payload []byte) error {
	if streamID == "" {
		return fmt.Errorf("no stream id")
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID, "payload": payload})
	_, err := hostCall(pluginabi.MethodHostStreamEmit, body)
	return err
}

// streamEmitError 向宿主发送流式错误终止信号。
// [参数] streamID: 流 ID；message: 错误文本。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, 宿主流错误出口。
func streamEmitError(streamID string, message string) {
	if streamID == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID, "error": redactSecrets(message)})
	_, _ = hostCall(pluginabi.MethodHostStreamEmit, body)
}

// streamClose 关闭宿主流。
// [参数] streamID: 流 ID。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, 宿主流关闭出口。
func streamClose(streamID string) {
	if streamID == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID})
	_, _ = hostCall(pluginabi.MethodHostStreamClose, body)
}

// streamEmitFn / streamCloseFn / streamEmitErrorFn 是流出口的测试注入点。
var (
	streamEmitFn      = streamEmit
	streamCloseFn     = streamClose
	streamEmitErrorFn = streamEmitError
)

// isStreamDoneLine 判定一行 SSE 文本是否为终止标记。
// [参数] line: 已 TrimSpace 的单行文本。
// [返回] 是否终止。
// 最近修改时间：2026-11-28 新增, SSE 终止契约。
func isStreamDoneLine(line string) bool {
	switch line {
	case "data: [DONE]", "data:[DONE]", "event: done", "event:done", "event: finish", "event:finish":
		return true
	}
	return false
}

// sseErrorFrame 从单帧 SSE 数据里提取业务错误文本。
// [参数] content: 去掉 data: 前缀的单帧 JSON。
// [返回] 错误文本；非错误帧返回空串。
// 最近修改时间：2026-11-28 新增, 200 响应体业务错误帧识别。
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

// redactSecrets 脱敏错误文本中的令牌。
// [参数] message: 原始错误文本。
// [返回] 脱敏后文本。
// 最近修改时间：2026-11-28 新增, 出口脱敏。
func redactSecrets(message string) string {
	parts := strings.SplitN(message, "Bearer ", 2)
	if len(parts) == 2 {
		message = parts[0] + "Bearer <redacted>"
	}
	return message
}

// truncateRedacted 截断脱敏后的错误文本。
// [参数] message: 错误文本；max: 最大字节数。
// [返回] 截断文本。
// 最近修改时间：2026-11-28 新增, 错误帧长度限制。
func truncateRedacted(message string, max int) string {
	if len(message) <= max {
		return message
	}
	return message[:max]
}
