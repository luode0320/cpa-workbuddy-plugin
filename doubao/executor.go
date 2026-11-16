package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本文件实现 CPA executor 接入：OpenAI 请求进，OpenAI 响应出。
//
// 宿主的 RPC 是「请求/响应」形态而非长连接：流式请求带 stream_id，插件通过
// host.stream.emit 逐帧回推，最后 host.stream.close 收尾。因此流式路径在
// 后台 goroutine 里读上游，RPC 立即返回响应头。

// streamResponse 是 executor.execute_stream 的响应信封。
type streamResponse struct {
	// Headers 是宿主需要先回给客户端的响应头。
	Headers http.Header `json:"headers,omitempty"`
	// Chunks 是同步收集模式下的帧列表（无 stream_id 时使用）。
	Chunks []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

// executorStreamRequest 是宿主下发的流式执行请求。
type executorStreamRequest struct {
	pluginapi.ExecutorRequest
	// StreamID 是宿主用于接收帧的流标识。
	StreamID string `json:"stream_id,omitempty"`
}

// chatRequest 是本插件读取的 OpenAI 请求字段。
type chatRequest struct {
	// Model 是客户端请求的模型名。
	Model string `json:"model"`
	// Messages 是消息数组原文。
	Messages json.RawMessage `json:"messages"`
	// Stream 表示客户端是否要求流式返回。
	Stream bool `json:"stream"`
	// ConversationID 是续接点之一：上一轮响应的 conversation_id。
	//
	// 视频是两段式的，次轮必须指回同一条会话，否则参数确认永远等不到「开始生成」。
	// 三个续接字段都是可选扩展，不认识的客户端不传，行为等同新会话。
	ConversationID string `json:"conversation_id,omitempty"`
	// SectionID 是续接点之二：上一轮响应的 section_id。
	SectionID string `json:"section_id,omitempty"`
	// LastMessageIndex 是续接点之三：上一轮响应的 message_index。
	LastMessageIndex int64 `json:"last_message_index,omitempty"`
}

// resumePointOf 把客户端带的三个续接字段收敛成续接点。
// [参数] 无。
// [返回] 续接点；三字段全空时为零值，表示新会话。
// 最近修改时间：2026-11-17 01:36:00；改动原因：新增，客户端续接点解析。
func (r *chatRequest) resumePointOf() resumePoint {
	if r == nil {
		return resumePoint{}
	}
	return resumePoint{
		ConversationID: strings.TrimSpace(r.ConversationID),
		SectionID:      strings.TrimSpace(r.SectionID),
		MessageIndex:   r.LastMessageIndex,
	}
}

// decodeExecutorRequest 拆分宿主请求中的账号与 OpenAI 请求体。
//
// 账号优先取 StorageJSON（宿主随请求下发的凭据快照），缺失时按 AuthID 回落到
// 本地账号目录。凭据必须通过字段齐备性校验，否则直接报错而不是发无效请求。
// [参数] raw: 宿主请求 JSON。
// [返回] 宿主请求、账号、OpenAI 请求与错误。
// 最近修改时间：2026-10-17；改动原因：新增，执行入口解析。
func decodeExecutorRequest(raw []byte) (*pluginapi.ExecutorRequest, *StoredAuth, *chatRequest, error) {
	var req pluginapi.ExecutorRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, nil, nil, fmt.Errorf("解析执行请求失败：%w", errUnmarshal)
		}
	}

	sa, errAuth := resolveExecutionAuth(&req)
	if errAuth != nil {
		return nil, nil, nil, errAuth
	}

	payload := req.Payload
	if len(payload) == 0 {
		payload = req.OriginalRequest
	}
	chat := &chatRequest{}
	if len(payload) > 0 {
		if errUnmarshal := json.Unmarshal(payload, chat); errUnmarshal != nil {
			return nil, nil, nil, fmt.Errorf("解析请求体失败：%w", errUnmarshal)
		}
	}
	if chat.Model == "" {
		chat.Model = req.Model
	}
	if len(chat.Messages) == 0 {
		return nil, nil, nil, fmt.Errorf("请求中没有 messages")
	}
	return &req, sa, chat, nil
}

// resolveExecutionAuth 解析本次请求应使用的账号。
//
// 优先用宿主下发的凭据快照（那是宿主已经选定的账号）；没有快照时按 AuthID
// 在本地池中定位，仍找不到就交给调度器挑一个健康账号。
// [参数] req: 宿主执行请求。
// [返回] 账号与错误。
// 最近修改时间：2026-10-17；改动原因：新增，账号解析（TASK-008 接入选号与冷却）。
func resolveExecutionAuth(req *pluginapi.ExecutorRequest) (*StoredAuth, error) {
	if len(req.StorageJSON) > 0 {
		var sa StoredAuth
		if errUnmarshal := json.Unmarshal(req.StorageJSON, &sa); errUnmarshal == nil && sa.CookieHeader != "" {
			if sa.AuthID == "" {
				sa.AuthID = strings.TrimSuffix(req.AuthID, ".json")
			}
			return &sa, nil
		}
	}

	authID := strings.TrimSuffix(strings.TrimSpace(req.AuthID), ".json")
	if authID != "" {
		accounts, errList := listAllAuthFiles()
		if errList != nil {
			return nil, fmt.Errorf("读取账号失败：%w", errList)
		}
		for _, account := range accounts {
			if account.Disabled {
				continue
			}
			if account.AuthID == authID {
				return account, nil
			}
		}
		return nil, fmt.Errorf("账号 %s 不存在或已停用", authID)
	}

	return pickAccount("")
}

// runWithFailover 执行一次对话，失败时按冷却策略换号重试。
//
// 实现见 executor_failover.go：本文件只负责把宿主请求翻译成上游调用。

// resolveGenerationMode 把客户端模型名解析成生成模式。
//
// 目录外一律报错而不是回落：插件只做生图与生视频，静默换成别的模型会让调用方
// 把「模型名写错」误判成「账号不可用」。错误文案直接列出可选别名，省一次查文档。
// [参数] model: 客户端传入的模型名。
// [返回] 生成模式与错误。
// 最近修改时间：2026-11-17 00:45:00；改动原因：工作模式模型目录删除，未知名由回落改为报错。
func resolveGenerationMode(model string) (generationMode, error) {
	mode, ok := modeForModel(model)
	if !ok {
		return generationMode{}, fmt.Errorf("不支持的模型 %q：本插件只做生图与生视频，可选 %s",
			model, strings.Join(testableModelIDs(), " / "))
	}
	return mode, nil
}

// executorExecute 处理非流式推理请求。
// [参数] raw: 宿主请求 JSON。
// [返回] 含 OpenAI 响应的信封 JSON 与错误。
// 最近修改时间：2026-10-17；改动原因：新增，非流式执行入口。
func executorExecute(raw []byte) ([]byte, error) {
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

	res, errRun := runWithFailover(sa, time.Duration(currentConfig().TimeoutSeconds)*time.Second, mode, prompt, chat.resumePointOf(), nil)
	if errRun != nil {
		return nil, errRun
	}

	// 视频是异步产物：确认轮只回一句「正在生成」，成品得从会话消息链里拉。
	// 放在这里而不是流里，是因为这条链路的产物从来不会推给本次连接。
	// 续接点要一起传进去：首轮与视频模式同时成立，只有「这一次是确认轮」才该等产物。
	if errVideo := ensureVideoAsset(sa, mode, chat.resumePointOf(), res); errVideo != nil {
		return nil, errVideo
	}

	answer, think := splitReasoning(res.Text)
	if res.Reasoning != "" && think == "" {
		think = res.Reasoning
	}
	// 生成类回复本身没有正文：给一句摘要，避免客户端显示空白消息。
	if strings.TrimSpace(answer) == "" && len(res.Assets) > 0 {
		answer = describeAssets(res.Assets)
	}
	resp := buildChatCompletion(chat.Model, answer, think, res.ConversationID, prompt)
	attachAssets(resp, res.Assets)
	// 把续接点回给客户端：视频要靠它发第二轮，不认识的客户端会忽略这些扩展字段。
	attachResumePoint(resp, res.resumePointOf())

	body, errMarshal := json.Marshal(resp)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: body,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	})
}
