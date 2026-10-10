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
func streamEmitError(streamID, message string) {
	if streamID == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID, "error": message})
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

// pumpUpstreamStream 后台读取上游 SSE 响应并将数据逐块推送给宿主。
func pumpUpstreamStream(
	ctx context.Context,
	cancel context.CancelFunc,
	streamID string,
	resp *http.Response,
	authID, model, upstreamModel string,
	startTime time.Time,
) {
	defer func() {
		cancel()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		streamClose(streamID)
	}()

	var inTokens, outTokens int64
	reader := bufio.NewReader(resp.Body)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineStr := strings.TrimSpace(string(line))
			if strings.HasPrefix(lineStr, "data:") {
				dataPayload := strings.TrimSpace(strings.TrimPrefix(lineStr, "data:"))
				if dataPayload != "" && dataPayload != "[DONE]" {
					// 提取 token 用量
					if it := gjson.Get(dataPayload, "message.usage.input_tokens").Int(); it > 0 {
						inTokens = it
					}
					if ot := gjson.Get(dataPayload, "usage.output_tokens").Int(); ot > 0 {
						outTokens = ot
					}
				}
			}

			if errEmit := streamEmit(streamID, line); errEmit != nil {
				// 客户端可能已断开
				return
			}
		}

		if err != nil {
			if err != io.EOF {
				streamEmitError(streamID, err.Error())
				durationMS := time.Since(startTime).Milliseconds()
				publishUsage(authID, model, upstreamModel, inTokens, outTokens, durationMS, resp.StatusCode, true, err.Error())
				noteAccountFailure(authID, resp.StatusCode, err.Error())
				return
			}
			break
		}
	}

	durationMS := time.Since(startTime).Milliseconds()
	publishUsage(authID, model, upstreamModel, inTokens, outTokens, durationMS, resp.StatusCode, false, "")
	resetAccountFailover(authID)
}
