package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本文件实现 executor 的流式路径。
//
// 宿主的 RPC 是「请求/响应」形态而非长连接：流式请求带 stream_id，插件通过
// host.stream.emit 逐帧回推，最后 host.stream.close 收尾。因此流式路径在
// 后台 goroutine 里读上游，RPC 立即返回响应头。

// executorExecuteStream 处理流式推理请求。
//
// 上游是 SSE 流，插件在后台 goroutine 里边读边转成 OpenAI chunk 推给宿主。
// RPC 本身立即返回响应头，否则宿主会一直阻塞等不到首帧。
// [参数] raw: 宿主请求 JSON。
// [返回] 含响应头的信封 JSON 与错误。
// 最近修改时间：2026-10-17；改动原因：新增，流式执行入口。
func executorExecuteStream(raw []byte) ([]byte, error) {
	var streamReq executorStreamRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &streamReq); errUnmarshal != nil {
			return nil, fmt.Errorf("解析流式执行请求失败：%w", errUnmarshal)
		}
	}

	_, sa, chat, errDecode := decodeExecutorRequest(raw)
	if errDecode != nil {
		return nil, errDecode
	}
	prompt, errPrompt := buildPrompt(chat.Messages)
	if errPrompt != nil {
		return nil, errPrompt
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("请求中没有可发送的文本内容")
	}

	mode, errMode := resolveGenerationMode(chat.Model)
	if errMode != nil {
		return nil, errMode
	}

	// 视频参数在这里归一一次，首轮提示词与确认轮 chat_ability 都从这里取值。
	// 与 executorExecute 同一套处理：两条路径若各写一份，迟早会分叉。
	params := normalizeVideoParams(chat.VideoDuration, chat.VideoResolution, chat.VideoRatio)
	prompt = appendVideoParamHint(prompt, mode, params)

	// 同步收集模式：宿主未提供 stream_id 时一次性返回全部帧。
	resume := chat.resumePointOf()
	if streamReq.StreamID == "" {
		return collectStreamResponse(sa, mode, resume, params, chat, prompt)
	}

	go pumpCompletionStream(streamReq.StreamID, sa, mode, resume, params, chat, prompt)
	return okEnvelope(streamResponse{Headers: streamHeaders()})
}

// collectStreamResponse 在无宿主流标识时同步收集全部帧。
// [参数] sa: 账号；mode: 已解析的生成模式；resume: 会话续接点；chat: 客户端请求；
// prompt: 折叠后的提示词。
// [返回] 含帧列表的信封 JSON 与错误。
// 最近修改时间：2026-11-17 01:40:00；改动原因：新增续接点入参，视频确认轮要靠它续接。
func collectStreamResponse(sa *StoredAuth, mode generationMode, resume resumePoint, params videoParams, chat *chatRequest, prompt string) ([]byte, error) {
	var chunks []pluginapi.ExecutorStreamChunk
	first := true
	res, errRun := runWithFailover(sa, time.Duration(currentConfig().StreamTimeoutSeconds)*time.Second, mode, prompt, resume, func(delta string) {
		payload := streamDelta(chat.Model, delta, first)
		first = false
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: payload})
	}, params)
	if errRun != nil {
		return nil, errRun
	}
	// 视频成品只能异步回灌：确认轮的 SSE 只回一段「正在生成」的文本。
	if errVideo := ensureVideoAsset(sa, mode, resume, res); errVideo != nil {
		return nil, errVideo
	}

	// 生成结果无法拆成增量：在流末尾单独发帧。
	for _, payload := range streamAssetFrames(chat.Model, res.Assets) {
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: payload})
	}
	// 终止帧：finish_reason=stop，宿主据此收尾。
	chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: streamStopFrame(chat.Model, res.resumePointOf())})
	return okEnvelope(streamResponse{Headers: streamHeaders(), Chunks: chunks})
}

// pumpCompletionStream 后台读取上游并把每个增量转成 OpenAI chunk 推给宿主。
//
// 退出路径必须保证 stream.close 只调一次：宿主流关闭后继续 emit 会报错，
// 且未关闭的流会让宿主侧资源泄漏。
// [参数] streamID: 宿主流标识；sa: 账号；mode: 已解析的生成模式；resume: 会话续接点；
// chat: 客户端请求；prompt: 提示词。
// [返回] 无。
// 最近修改时间：2026-11-17 01:40:00；改动原因：新增续接点入参，视频确认轮要靠它续接。
func pumpCompletionStream(streamID string, sa *StoredAuth, mode generationMode, resume resumePoint, params videoParams, chat *chatRequest, prompt string) {
	defer streamClose(streamID)

	first := true
	res, errRun := runWithFailover(sa, time.Duration(currentConfig().StreamTimeoutSeconds)*time.Second, mode, prompt, resume, func(delta string) {
		payload := streamDelta(chat.Model, delta, first)
		first = false
		_ = streamEmit(streamID, payload)
	}, params)
	if errRun != nil {
		streamEmitError(streamID, errRun.Error())
		return
	}
	// 视频成品只能异步回灌；拉不到就把失败推给客户端，而不是发一个空结局。
	if errVideo := ensureVideoAsset(sa, mode, resume, res); errVideo != nil {
		streamEmitError(streamID, errVideo.Error())
		return
	}

	// 收尾：先补思考内容（若有），再发生成结果，最后发终止帧。
	if res.Reasoning != "" {
		if payload, errMarshal := json.Marshal(streamChunk(chat.Model, res.ConversationID, map[string]any{"reasoning_content": res.Reasoning}, "")); errMarshal == nil {
			_ = streamEmit(streamID, payload)
		}
	}
	for _, payload := range streamAssetFrames(chat.Model, res.Assets) {
		_ = streamEmit(streamID, payload)
	}
	if payload := streamStopFrame(chat.Model, res.resumePointOf()); payload != nil {
		_ = streamEmit(streamID, payload)
	}
}
