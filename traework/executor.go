// executor.go implements the chat-completions executor for TraeWork:
// handleExecExecute (non-streaming) and handleExecStream (streaming). Both
// paths normalize the OpenAI request into the Trae llm_utils_chat payload,
// run the per-request account-failover loop on account-level 4xx, and fold
// the upstream SSE back into chat.completion responses.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// executorStreamRequest wraps the host's executor.execute_stream RPC: the
// ExecutorRequest plus the async stream id the host uses to receive chunks.
type executorStreamRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// traeSyncStreamDeps 汇总同步流式账号重试使用的既有依赖，便于用脚本化上游锁定同请求恢复行为。
type traeSyncStreamDeps struct {
	CallLLM      func(a *traeAuth, payload map[string]any, authID string) (*hostHTTPResponse, error)
	PickNextAuth func(currentAuthID string) (nextAuthID string, nextSA *traeAuth, ok bool)
	// PickNextAuthForModel 是带模型维度的候选选择；为 nil 时回退到
	// PickNextAuth（测试注入保持旧签名即可继续工作）。
	PickNextAuthForModel func(currentAuthID, model string) (nextAuthID string, nextSA *traeAuth, ok bool)
}

// pickCandidate 按模型维度选择下一账号；未注入模型维度实现时回退旧签名。
// [参数] currentAuthID：当前失败账号；model：已归一化的上游模型名，可为空。
// [返回] 下一个可用账号标识、凭据与是否找到。
// 最近修改时间：2026-10-19 01:20:00；改动原因：新增，兼容测试旧注入签名。
func (d traeSyncStreamDeps) pickCandidate(currentAuthID, model string) (string, *traeAuth, bool) {
	if d.PickNextAuthForModel != nil {
		return d.PickNextAuthForModel(currentAuthID, model)
	}
	return d.PickNextAuth(currentAuthID)
}

var defaultTraeSyncStreamDeps = traeSyncStreamDeps{
	CallLLM:              callLLM,
	PickNextAuth:         pickNextAuth,
	PickNextAuthForModel: pickNextAuthForModel,
}

// traeSyncStreamContext 保存同步流式重试共享的模型、账号和用量上下文。
type traeSyncStreamContext struct {
	Model         string
	UpstreamModel string
	AuthID        string
	AuthUID       string
	Started       time.Time
	InputChars    int
	Budget        int
	// SessionKey 是执行器入口提取的会话亲和键，随每条 publishUsage 写入
	// usage feed 的 session_key 列；跨换号尝试冻结（req 不随 attempt 变化）。
	SessionKey string
}

// traeAsyncUpstream 统一生产宿主流与测试内存流的读取、关闭契约。
type traeAsyncUpstream struct {
	Reader io.Reader
	Close  func()
}

// traeAsyncStreamDeps 汇总异步逻辑请求协调器的账号、上游和客户端流依赖。
type traeAsyncStreamDeps struct {
	Open         func(a *traeAuth, payload map[string]any, authID, hostCallbackID string) (traeAsyncUpstream, int, error)
	PickNextAuth func(currentAuthID string) (nextAuthID string, nextSA *traeAuth, ok bool)
	// PickNextAuthForModel 是带模型维度的候选选择；为 nil 时回退到
	// PickNextAuth（测试注入保持旧签名即可继续工作）。
	PickNextAuthForModel func(currentAuthID, model string) (nextAuthID string, nextSA *traeAuth, ok bool)
	Emit                 func(streamID string, payload []byte) error
	// EmitError 以宿主 stream.emit 信封的 "error" 字段发送终态错误（映射为
	// 执行器流 chunk.Err），让宿主 conductor 能感知失败并跨平台轮换凭据。
	EmitError func(streamID, message string) error
	Close     func(streamID string)
}

// pickCandidate 按模型维度选择下一账号；未注入模型维度实现时回退旧签名。
// [参数] currentAuthID：当前失败账号；model：已归一化的上游模型名，可为空。
// [返回] 下一个可用账号标识、凭据与是否找到。
// 最近修改时间：2026-10-19 01:20:00；改动原因：新增，兼容测试旧注入签名。
func (d traeAsyncStreamDeps) pickCandidate(currentAuthID, model string) (string, *traeAuth, bool) {
	if d.PickNextAuthForModel != nil {
		return d.PickNextAuthForModel(currentAuthID, model)
	}
	return d.PickNextAuth(currentAuthID)
}

var defaultTraeAsyncStreamDeps = traeAsyncStreamDeps{
	Open: func(a *traeAuth, payload map[string]any, authID, hostCallbackID string) (traeAsyncUpstream, int, error) {
		stream, statusCode, err := callLLMStream(a, payload, authID, hostCallbackID)
		if err != nil {
			return traeAsyncUpstream{}, statusCode, err
		}
		return traeAsyncUpstream{Reader: newHostStreamReader(stream), Close: stream.Close}, statusCode, nil
	},
	PickNextAuth:         pickNextAuth,
	PickNextAuthForModel: pickNextAuthForModel,
	Emit:                 streamEmit,
	EmitError:            emitStreamErrorEnvelope,
	Close:                streamClose,
}

// traeAsyncStreamContext 保存整个异步逻辑请求跨账号共享的输入与终结上下文。
type traeAsyncStreamContext struct {
	StreamID       string
	HostCallbackID string
	Model          string
	UpstreamModel  string
	Payload        map[string]any
	Started        time.Time
	InputChars     int
	Budget         int
	// SessionKey 是执行器入口提取的会话亲和键，随每条 publishUsage 写入
	// usage feed 的 session_key 列；跨换号尝试冻结（req 不随 attempt 变化）。
	SessionKey string
}

// authIDFor returns the account identity used for failover/credit bookkeeping
// under the host's auth-ID namespace: prefer the host-provided ID (scheduler
// key), fall back to the credential's userId.
func authIDFor(a *traeAuth, fallback string) string {
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	if a != nil && strings.TrimSpace(a.UserID) != "" {
		return strings.TrimSpace(a.UserID)
	}
	return ""
}

// authLogHash 返回账号标识的不可逆短哈希，仅用于跨 attempt 关联脱敏日志。
func authLogHash(authID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(authID)))
	return fmt.Sprintf("%x", sum[:4])
}

// circuitModel 把客户端模型名归一为熔断聚合键使用的上游模型名。
// auto 与未配置别名时回退到原值，保证键始终非空、同一模型不会因别名拆成多域。
// [参数] model：客户端或已剥离前缀的模型名。
// [返回] 用于模型级熔断聚合的模型名。
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增模型级风控熔断接入。
func circuitModel(model string) string {
	if _, configName := resolveModelOptions(model); configName != "" {
		return configName
	}
	return model
}

// handleExecExecute performs one non-streaming chat completion.
func handleExecExecute(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	a, err := parseTraeAuth(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	upstreamModel := stripProviderPrefix(req.Model)
	circuitKeyModel := circuitModel(upstreamModel)
	started := time.Now()
	authUID := strings.TrimSpace(a.UserID)
	// 会话亲和键：与 scheduler.pick 同一优先链（extractSessionKeyFromSources），
	// 写入 usage feed 供 dashboard「会话」列区分会话归属；跨换号尝试冻结。
	sessionKey := extractSessionKeyFromSources(req.Headers, req.Metadata)

	// 模型级拦截不再有独立的全局闸门：被拦截的账号×模型已写入冷却，
	// 路由层会跳过它们；本请求若仍落到这里说明上游刚恢复或该组合尚未被
	// 标记，按正常流程发起即可。

	oa := &openAIRequest{}
	if err := json.Unmarshal(req.Payload, oa); err != nil && len(req.Payload) > 0 {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "payload parse: "+err.Error(), "", 0, authUID, sessionKey)
		return nil, fmt.Errorf("payload parse: %w", err)
	}
	messages := toTraeMessages(oa.Messages)
	if len(messages) == 0 {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "empty messages", "", 0, authUID, sessionKey)
		return nil, fmt.Errorf("empty messages after normalization")
	}
	tools := traeToolsFromOpenAI(oa.Tools)
	toolChoice, suppressTools := normalizeTraeToolChoice(oa.ToolChoice)
	if suppressTools {
		// tool_choice=none 或未知形态：上游实测带非空 tools 仍会输出 tool_calls，
		// 唯一可靠抑制是整组删除。
		tools = nil
		toolChoice = ""
	}
	payload := buildTraePayload(messages, upstreamModel, false, oa.MaxTokens, oa.Temperature, oa.TopP, tools, toolChoice)

	budget := loadedRetryOn4xx()
	curSA := a
	usedAuthID := authIDFor(curSA, req.AuthID)

	// 前置冷却拦截：若初始账号处于冷却中，直接换号到健康候选。
	// 判定带模型维度：账号×模型级冷却只挡同一模型，不能把整个账号摘掉。
	if isAuthModelCoolingDown(usedAuthID, circuitKeyModel) || isAuthModelCoolingDown(authUID, circuitKeyModel) {
		if nextID, nextSA, hasNext := pickNextAuthForModel(usedAuthID, circuitKeyModel); hasNext && nextSA != nil {
			curSA = nextSA
			usedAuthID = nextID
			authUID = strings.TrimSpace(curSA.UserID)
		}
	}

	for attempt := 0; attempt <= budget; attempt++ {
		resp, callErr := callLLM(curSA, payload, usedAuthID)
		if callErr == nil {
			completion, aggDetail, aggErr := aggregateTraeCompletion(bytes.NewReader(resp.Body), req.Model, resp.StatusCode)
			if aggErr == nil {
				resetAccountFailover(usedAuthID)
				// 一次真实成功即证明该模型已恢复，清掉该模型上残留的冷却。
				resetModelBlockedState(circuitKeyModel)
				// 轮换打点：只有真正由该账号服务完成的成功请求才推进「连续承流」计时。
				noteRotationUsage(usedAuthID)
				publishUsage(req.Model, upstreamModel, authUID, started, usageDetailForCompletion(aggDetail, completion), false, 0, "", "", 0, authUID, sessionKey)
				return okEnvelope(pluginapi.ExecutorResponse{Payload: completion})
			}
			// SSE-layer business errors: the Trae llm_utils_chat upstream returns
			// HTTP 200 + event:error (4011 rate-limit, 14018 quota exhausted,
			// ...), so callLLM succeeded and the account-level failure only
			// surfaces here. When the error classifies as account-level, rotate
			// to the next candidate on the SAME request (mirrors the HTTP 4xx
			// branch below); otherwise surface it immediately.
			// 11128（含 200 承载的 event:error 帧）：双写记账（模型冷却 + 账号禁用
			// 写盘）后直接返回，恢复由 10 分钟禁用探活裁决，不再把剩余账号逐个赔进往返。
			if isModelBlockedError(resp.StatusCode, aggErr.Error()) {
				noteModelBlockedHit(usedAuthID, circuitKeyModel)
				reconcileAfterExecutorErrorForModel(usedAuthID, circuitKeyModel, resp.StatusCode, aggErr.Error())
				publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, resp.StatusCode, aggErr.Error(), "", 0, authUID, sessionKey)
				return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, truncateRedacted(aggErr.Error(), 200))
			}
			if !isAccountFailure(resp.StatusCode, aggErr.Error()) || attempt >= budget || curSA == nil {
				reconcileAfterExecutorErrorForModel(usedAuthID, circuitKeyModel, resp.StatusCode, aggErr.Error())
				publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, resp.StatusCode, aggErr.Error(), "", 0, authUID, sessionKey)
				return nil, aggErr
			}
			reconcileAfterExecutorErrorForModel(usedAuthID, circuitKeyModel, resp.StatusCode, aggErr.Error())
			nextAuthID, nextSA, hasNext := pickNextAuthForModel(usedAuthID, circuitKeyModel)
			if !hasNext || nextSA == nil {
				return nil, aggErr
			}
			curSA = nextSA
			usedAuthID = nextAuthID
			continue
		}

		statusCode := parseUpstreamStatusFromErr(callErr)
		// 11128：双写记账（模型冷却 + 账号禁用写盘）后直接返回，恢复由禁用探活裁决。
		if isModelBlockedError(statusCode, callErr.Error()) {
			noteModelBlockedHit(usedAuthID, circuitKeyModel)
			reconcileAfterExecutorErrorForModel(usedAuthID, circuitKeyModel, statusCode, callErr.Error())
			publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, statusCode, callErr.Error(), "", 0, authUID, sessionKey)
			return nil, fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(callErr.Error(), 200))
		}
		// 换号判定用 isAccountFailure：open 阶段 transport 级错误（TTFB 超时等
		// status=0，生产实证 stream 4553：上游对单账号挂起 60s 不回响应头）与
		// 5xx / 429 / 账号级 4xx 同责换号（与流式路径对齐），400 业务错仍直通。
		// 核算由 callLLM（upstream.go）统一记账，换号路径不重复计数；终局保留
		// 既有核算入口。换号前驱逐绑定防会话亲和钉死死号。
		if !isAccountFailure(statusCode, callErr.Error()) || attempt >= budget || curSA == nil {
			reconcileAfterExecutorErrorForModel(usedAuthID, circuitKeyModel, statusCode, callErr.Error())
			publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, statusCode, callErr.Error(), "", 0, authUID, sessionKey)
			return nil, callErr
		}
		evictSessionBindingsForAuth(usedAuthID)
		nextAuthID, nextSA, hasNext := pickNextAuthForModel(usedAuthID, circuitKeyModel)
		if !hasNext || nextSA == nil {
			break
		}
		curSA = nextSA
		usedAuthID = nextAuthID
	}
	errFinal := fmt.Errorf("upstream account pool exhausted after %d attempt(s)", budget+1)
	reconcileAfterExecutorErrorForModel(usedAuthID, circuitKeyModel, 0, errFinal.Error())
	publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, errFinal.Error(), "", 0, authUID, sessionKey)
	return nil, errFinal
}

// handleExecStream 执行流式聊天补全；有 StreamID 时异步推送分片，否则同步收集后返回。
// [参数] raw: 宿主传入的流式执行请求 JSON。
// [返回] 响应 envelope JSON；请求解析或上游调用失败时返回错误。
// 最近修改时间：2026-08-30 23:40:18；改动原因：异步路径改用宿主流桥实时读取并透传 callback context，避免长回答被全量缓冲。
func handleExecStream(raw []byte) ([]byte, error) {
	// 1. 解析请求、账号与模型，构造 Trae 流式上游负载。
	var req executorStreamRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	a, err := parseTraeAuth(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	upstreamModel := stripProviderPrefix(req.Model)
	started := time.Now()
	authUID := strings.TrimSpace(a.UserID)
	// 会话亲和键：与 handleExecExecute 同一规则（extractSessionKeyFromSources），
	// 泵入两条流路径的 ctx，让流式 usage 行也带会话身份。
	sessionKey := extractSessionKeyFromSources(req.Headers, req.Metadata)

	bodyRaw := req.Payload
	if len(bodyRaw) == 0 {
		bodyRaw = req.OriginalRequest
	}
	oa := &openAIRequest{}
	if err := json.Unmarshal(bodyRaw, oa); err != nil && len(bodyRaw) > 0 {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "payload parse: "+err.Error(), "", 0, authUID, sessionKey)
		return nil, fmt.Errorf("payload parse: %w", err)
	}
	messages := toTraeMessages(oa.Messages)
	if len(messages) == 0 {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "empty messages", "", 0, authUID, sessionKey)
		return nil, fmt.Errorf("empty messages after normalization")
	}
	inputChars := estimateInputChars(messages)
	tools := traeToolsFromOpenAI(oa.Tools)
	toolChoice, suppressTools := normalizeTraeToolChoice(oa.ToolChoice)
	if suppressTools {
		tools = nil
		toolChoice = ""
	}
	payload := buildTraePayload(messages, upstreamModel, true, oa.MaxTokens, oa.Temperature, oa.TopP, tools, toolChoice)
	headers := streamHeaders()
	sseFramed := clientNeedsSSEFrame(req.Metadata)

	usedAuthID := authIDFor(a, req.AuthID)

	// 2. 无异步流标识时同步收集分片；账号级失败与伪完成都在同一请求内按预算换号。
	if req.StreamID == "" {
		chunks, streamErr := runTraeSyncStream(a, payload, traeSyncStreamContext{
			Model:         req.Model,
			UpstreamModel: upstreamModel,
			AuthID:        usedAuthID,
			AuthUID:       authUID,
			Started:       started,
			InputChars:    inputChars,
			Budget:        loadedRetryOn4xx(),
			SessionKey:    sessionKey,
		}, defaultTraeSyncStreamDeps)
		if streamErr != nil {
			return nil, streamErr
		}
		if sseFramed {
			chunks = sseFrameChunks(chunks)
		}
		return okEnvelope(streamResponse{Headers: headers, Chunks: chunks})
	}

	// 3. 有异步流标识时立即返回响应头；后台协调器在同一 StreamID 内完成上游打开、伪完成换号和唯一收尾。
	go func() {
		deps := defaultTraeAsyncStreamDeps
		closeStream := deps.Close
		var closeOnce sync.Once
		deps.Close = func(streamID string) {
			closeOnce.Do(func() { closeStream(streamID) })
		}
		runTraeAsyncStream(a, usedAuthID, traeAsyncStreamContext{
			StreamID:       req.StreamID,
			HostCallbackID: req.HostCallbackID,
			Model:          req.Model,
			UpstreamModel:  upstreamModel,
			Payload:        payload,
			Started:        started,
			InputChars:     inputChars,
			Budget:         loadedRetryOn4xx(),
			SessionKey:     sessionKey,
		}, deps)
	}()
	log.Printf("[traework] exec stream async scheduled: model=%s auth_hash=%s stream_id=%s", req.Model, authLogHash(usedAuthID), req.StreamID)
	return okEnvelope(streamResponse{Headers: headers})
}

// runTraeAsyncStream 协调一个宿主 StreamID 下的多账号上游尝试，并只在最终结果上收尾一次。
// [参数] initialAuth: 初始账号；initialAuthID: 初始账号标识；ctx: 逻辑请求上下文；deps: 上游与宿主流依赖。
// [返回] 无；最终正文、错误和关闭通过 deps 发送，所有 attempt 的用量分别按实际账号记录。
// 最近修改时间：2026-09-06 23:40:00；改动原因：open 阶段 transport 级错误
// （TTFB 超时等 status=0，生产实证 stream 4553：上游对单账号挂起 60s 不回响应头）
// 原先被 isAccountLevel4xx 判为非账号级直接终局，池中健康候选未被尝试；改用
// isAccountFailure 与 pump 阶段同责换号，400 业务错仍直通。
func runTraeAsyncStream(initialAuth *traeAuth, initialAuthID string, ctx traeAsyncStreamContext, deps traeAsyncStreamDeps) {
	curSA := initialAuth
	curAuthID := initialAuthID
	curAuthUID := strings.TrimSpace(initialAuth.UserID)
	requestID := randomUUID()
	triedAuthIDs := map[string]struct{}{curAuthID: {}}
	attemptsMade := 0
	circuitKeyModel := circuitModel(ctx.UpstreamModel)

	// 模型级拦截不再有独立的全局闸门：被拦截的账号×模型已写入冷却，
	// 路由层会跳过它们，本请求正常发起即可。

	// 前置冷却拦截：若宿主指定的初始账号已处于冷却中，直接切换到健康账号，不发送自杀式网络调用。
	// 判定带模型维度：账号×模型级冷却只挡同一模型，不能把整个账号摘掉。
	if isAuthModelCoolingDown(curAuthID, circuitKeyModel) || isAuthModelCoolingDown(curAuthUID, circuitKeyModel) {
		if nextAuthID, nextSA, hasNext := deps.pickCandidate(curAuthID, circuitKeyModel); hasNext && nextSA != nil {
			curSA = nextSA
			curAuthID = nextAuthID
			curAuthUID = strings.TrimSpace(nextSA.UserID)
			triedAuthIDs[curAuthID] = struct{}{}
		}
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr := fmt.Errorf("Trae stream coordinator panic: %v", recovered)
			emitTraeAsyncError(ctx.StreamID, panicErr.Error(), deps)
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usage.Detail{}, true, 0, panicErr.Error(), "", 0, curAuthUID, ctx.SessionKey)
		}
	}()
	for attempt := 0; attempt <= ctx.Budget; attempt++ {
		attemptsMade = attempt + 1
		// 1. 每次账号尝试复用同一 payload 和 HostCallbackID，但拥有独立、及时关闭的上游句柄。
		//    伪完成是上游对账号的窗口性限流信号；先核算 + 换号，仅当池中已无其它可用
		//    候选（单号池或他号全冷却）时才对当前账号同号退避重试——生产实证 2351：
		//    同号约 60s 后恢复完整长输出，直接判 pool exhausted 会让单号池的请求必败。
		pseudoTries := 0
	retrySameAuth:
		upstream, statusCode, openErr := deps.Open(curSA, ctx.Payload, curAuthID, ctx.HostCallbackID)
		if openErr != nil {
			if statusCode == 0 {
				statusCode = parseUpstreamStatusFromErr(openErr)
			}
			log.Printf("[traework] exec stream async open error: request_id=%s stream_id=%s model=%s auth_hash=%s attempt=%d status=%d err=%s",
				requestID, ctx.StreamID, ctx.Model, authLogHash(curAuthID), attempt+1, statusCode, truncateRedacted(openErr.Error(), 200))
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usage.Detail{}, true, statusCode, openErr.Error(), "", 0, curAuthUID, ctx.SessionKey)
			// 11128：双写记账后停止换号，恢复由 10 分钟禁用探活裁决。
			if isModelBlockedError(statusCode, openErr.Error()) {
				noteModelBlockedHit(curAuthID, circuitKeyModel)
				emitTraeAsyncError(ctx.StreamID, fmt.Sprintf("upstream %d: %s", statusCode, truncateRedacted(openErr.Error(), 200)), deps)
				return
			}
			// 换号判定用 isAccountFailure 而非 isAccountLevel4xx：open 阶段的
			// transport 级错误（TTFB 超时、连接重置等 status=0——生产实证 stream
			// 4553：上游对单账号挂起 60s 不回响应头，池中健康候选未被尝试）与
			// 5xx / 429 / 账号级 4xx 同责换号；400 业务错仍直通。核算已由
			// upstream.go 打开失败路径统一记账（noteAccountFailure），此处仅对
			// 4xx 维持既有二次核算（快速隔离死号），transport/5xx 不重复计数。
			if !isAccountFailure(statusCode, openErr.Error()) || attempt >= ctx.Budget {
				if isAccountLevel4xx(statusCode) {
					reconcileAfterExecutorErrorForModel(curAuthID, circuitKeyModel, statusCode, openErr.Error())
					evictSessionBindingsForAuth(curAuthID)
				}
				emitTraeAsyncError(ctx.StreamID, openErr.Error(), deps)
				return
			}
			// 可换号失败必须驱逐绑定，否则下个请求的会话亲和仍会选回该死号
			//（生产实证：225774 反复 401，host 每次 cache-miss 重绑死号 → pool exhausted）。
			if isAccountLevel4xx(statusCode) {
				reconcileAfterExecutorErrorForModel(curAuthID, circuitKeyModel, statusCode, openErr.Error())
			}
			evictSessionBindingsForAuth(curAuthID)
			nextAuthID, nextSA, hasNext := deps.pickCandidate(curAuthID, circuitKeyModel)
			if !hasNext || nextSA == nil {
				break
			}
			if _, seen := triedAuthIDs[nextAuthID]; seen {
				break
			}
			triedAuthIDs[nextAuthID] = struct{}{}
			curSA = nextSA
			curAuthID = nextAuthID
			curAuthUID = strings.TrimSpace(nextSA.UserID)
			continue
		}

		result := func() traeStreamAttemptResult {
			if upstream.Close != nil {
				defer upstream.Close()
			}
			return pumpTraeStreamAttempt(upstream.Reader, traeStreamPumpContext{
				StreamID:      ctx.StreamID,
				Model:         ctx.Model,
				UpstreamModel: ctx.UpstreamModel,
				StatusCode:    statusCode,
				AuthID:        curAuthID,
				AuthUID:       curAuthUID,
				Started:       ctx.Started,
				InputChars:    ctx.InputChars,
				SessionKey:    ctx.SessionKey,
			}, requestID, func(payload []byte) error {
				return deps.Emit(ctx.StreamID, payload)
			})
		}()

		// 2. 门槛前伪完成不产生任何客户端输出；核算失败后换号，仅当无候选才同号退避重试。
		if result.Pseudo {
			reason := "pseudo completion: upstream returned done with near-empty output"
			log.Printf("[traework] exec stream async pseudo retry: request_id=%s stream_id=%s model=%s auth_hash=%s attempt=%d chunks=%d",
				requestID, ctx.StreamID, ctx.Model, authLogHash(curAuthID), attempt+1, len(result.Chunks))
			noteForcedAccountFailureForModel(curAuthID, circuitKeyModel, reason)
			evictSessionBindingsForAuth(curAuthID)
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usageDetailForAttempt(result.Usage, result.Chunks), true, statusCode, reason, "", ttftNSBetween(ctx.Started, result.FirstOutputAt), curAuthUID, ctx.SessionKey)
			if attempt >= ctx.Budget {
				break
			}
			nextAuthID, nextSA, hasNext := deps.pickCandidate(curAuthID, circuitKeyModel)
			if !hasNext || nextSA == nil {
				// 无其它候选：不立即判池耗尽，先同号退避重试一次（窗口性限流自愈），
				// 仍伪才由外层 attempt 预算收口为 pool exhausted。
				if pseudoTries < pseudoRetryBudget {
					pseudoTries++
					log.Printf("[traework] exec stream async pseudo same-auth retry (pool exhausted candidates): request_id=%s stream_id=%s model=%s auth_hash=%s attempt=%d chunks=%d",
						requestID, ctx.StreamID, ctx.Model, authLogHash(curAuthID), attempt+1, len(result.Chunks))
					goto retrySameAuth
				}
				break
			}
			if _, seen := triedAuthIDs[nextAuthID]; seen {
				break
			}
			triedAuthIDs[nextAuthID] = struct{}{}
			curSA = nextSA
			curAuthID = nextAuthID
			curAuthUID = strings.TrimSpace(nextSA.UserID)
			continue
		}
		if result.Err != nil {
			log.Printf("[traework] exec stream async attempt error: request_id=%s stream_id=%s model=%s auth_hash=%s attempt=%d status=%d emitted=%v err=%s",
				requestID, ctx.StreamID, ctx.Model, authLogHash(curAuthID), attempt+1, statusCode, result.Emitted, truncateRedacted(result.Err.Error(), 200))
			reconcileAfterExecutorErrorForModel(curAuthID, circuitKeyModel, statusCode, result.Err.Error())
			// 每个 attempt 的用量先按实际账号记失败（与 openErr / pseudo 分支同款），
			// 再决定透传错误还是换号续试。
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usageDetailForAttempt(result.Usage, result.Chunks), true, statusCode, result.Err.Error(), "", ttftNSBetween(ctx.Started, result.FirstOutputAt), curAuthUID, ctx.SessionKey)
			// 11128（含 200 承载的 event:error 帧）：双写记账后返回，
			// 恢复由 10 分钟禁用探活裁决。
			if isModelBlockedError(statusCode, result.Err.Error()) {
				noteModelBlockedHit(curAuthID, circuitKeyModel)
				emitTraeAsyncError(ctx.StreamID, fmt.Sprintf("upstream %d: %s", statusCode, truncateRedacted(result.Err.Error(), 200)), deps)
				return
			}
			// HTTP 200 + event:error 是上游的业务失败通道（配额 14018 / 限流 4011
			// 等以 200 SSE 帧下发，生产实证 stream 389/391），账号级分类命中时与
			// HTTP 4xx 同责：未向客户端泄漏任何分片且预算允许时在同一请求内换号。
			// emitted=true 说明客户端已收到部分正文，换号会造成输出重复或拼接错乱，
			// 只能透传错误收尾。
			if result.Emitted || !isAccountFailure(statusCode, result.Err.Error()) || attempt >= ctx.Budget {
				emitTraeAsyncError(ctx.StreamID, result.Err.Error(), deps)
				return
			}
			evictSessionBindingsForAuth(curAuthID)
			nextAuthID, nextSA, hasNext := deps.pickCandidate(curAuthID, circuitKeyModel)
			if !hasNext || nextSA == nil {
				emitTraeAsyncError(ctx.StreamID, result.Err.Error(), deps)
				return
			}
			if _, seen := triedAuthIDs[nextAuthID]; seen {
				break
			}
			triedAuthIDs[nextAuthID] = struct{}{}
			curSA = nextSA
			curAuthID = nextAuthID
			curAuthUID = strings.TrimSpace(nextSA.UserID)
			continue
		}

		// 3. 只有最终可交付 attempt 能发送 finish；随后恰好关闭一次宿主 StreamID。
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
		finishRaw, finishErr := chunkDelta(requestID, ctx.Model, "", "", finish)
		if finishErr == nil && finishRaw != nil {
			finishErr = deps.Emit(ctx.StreamID, finishRaw)
			if finishErr == nil {
				result.Chunks = append(result.Chunks, pluginapi.ExecutorStreamChunk{Payload: finishRaw})
			}
		}
		if finishErr != nil {
			reconcileAfterExecutorErrorForModel(curAuthID, circuitKeyModel, statusCode, finishErr.Error())
			emitTraeAsyncError(ctx.StreamID, finishErr.Error(), deps)
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usageDetailForAttempt(result.Usage, result.Chunks), true, statusCode, finishErr.Error(), "", ttftNSBetween(ctx.Started, result.FirstOutputAt), curAuthUID, ctx.SessionKey)
			return
		}
		deps.Close(ctx.StreamID)
		if failed {
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usageDetailForAttempt(result.Usage, result.Chunks), true, statusCode, failureReason, "", ttftNSBetween(ctx.Started, result.FirstOutputAt), curAuthUID, ctx.SessionKey)
			return
		}
		resetAccountFailover(curAuthID)
		// 一次真实成功即证明该模型已恢复，清掉该模型上残留的冷却。
		resetModelBlockedState(circuitKeyModel)
		// 轮换打点：异步流成功路径。漏接这里会让长时间流式会话永远不触发轮换。
		noteRotationUsage(curAuthID)
		publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usageDetailForAttempt(result.Usage, result.Chunks), false, 0, "", "", ttftNSBetween(ctx.Started, result.FirstOutputAt), curAuthUID, ctx.SessionKey)
		log.Printf("[traework] exec stream async done: request_id=%s stream_id=%s model=%s auth_hash=%s attempt=%d chunks=%d",
			requestID, ctx.StreamID, ctx.Model, authLogHash(curAuthID), attempt+1, len(result.Chunks))
		return
	}

	errFinal := fmt.Errorf("upstream account pool exhausted after %d attempt(s)", attemptsMade)
	log.Printf("[traework] exec stream async pool exhausted: request_id=%s stream_id=%s model=%s auth_hash=%s err=%s",
		requestID, ctx.StreamID, ctx.Model, authLogHash(curAuthID), truncateRedacted(errFinal.Error(), 200))
	emitTraeAsyncError(ctx.StreamID, errFinal.Error(), deps)
}

// emitTraeAsyncError 以错误 chunk（信封 "error" 字段）发送终态错误并关闭宿主流；
// 调用方发送后必须立即结束逻辑请求。错误必须走 chunk.Err 通道而非 payload 数据帧，
// 否则宿主 conductor 视请求为成功，跨平台失败切换（trae↔workbuddy）不会触发。
func emitTraeAsyncError(streamID, message string, deps traeAsyncStreamDeps) {
	if deps.EmitError != nil {
		_ = deps.EmitError(streamID, message)
	} else {
		_ = emitStreamErrorEnvelope(streamID, message)
	}
	deps.Close(streamID)
}

// runTraeSyncStream 在同步流式请求内执行账号重试；伪完成分片在切号前直接丢弃。
// [参数] initialAuth: 初始账号；payload: 上游负载；ctx: 重试与用量上下文；deps: 上游和候选选择依赖。
// [返回] 最终健康账号分片；账号池耗尽或上游失败时返回错误且不返回伪完成分片。
// 最近修改时间：2026-09-06 23:40:00；改动原因：open 阶段 transport 级错误（status=0）
// 与 5xx 原先被 isAccountLevel4xx 判为非账号级直接终局，改用 isAccountFailure 同责
// 换号（与 async 分支对齐）；换号路径补 evictSessionBindingsForAuth 防会话亲和钉死。
func runTraeSyncStream(initialAuth *traeAuth, payload map[string]any, ctx traeSyncStreamContext, deps traeSyncStreamDeps) ([]pluginapi.ExecutorStreamChunk, error) {
	curSA := initialAuth
	curAuthID := ctx.AuthID
	curAuthUID := ctx.AuthUID
	triedAuthIDs := map[string]struct{}{curAuthID: {}}
	lastFailurePublished := false
	attemptsMade := 0
	circuitKeyModel := circuitModel(ctx.UpstreamModel)

	// 模型级拦截不再有独立的全局闸门：被拦截的账号×模型已写入冷却，
	// 路由层会跳过它们，本请求正常发起即可。

	// 前置冷却拦截：若宿主指定的初始账号已处于冷却中，直接换号到健康候选。
	// 判定带模型维度：账号×模型级冷却只挡同一模型，不能把整个账号摘掉。
	if isAuthModelCoolingDown(curAuthID, circuitKeyModel) || isAuthModelCoolingDown(curAuthUID, circuitKeyModel) {
		if nextAuthID, nextSA, hasNext := deps.pickCandidate(curAuthID, circuitKeyModel); hasNext && nextSA != nil {
			curSA = nextSA
			curAuthID = nextAuthID
			curAuthUID = strings.TrimSpace(nextSA.UserID)
			triedAuthIDs[curAuthID] = struct{}{}
		}
	}

	for attempt := 0; attempt <= ctx.Budget; attempt++ {
		attemptsMade = attempt + 1
		lastFailurePublished = false
		// 1. 每次尝试重新调用上游；普通账号级错误沿用既有同请求换号契约。
		//    伪完成仅在池中已无其它候选时同号退避重试（见下方伪完成分支）。
		pseudoTries := 0
	retrySameAuth:
		resp, callErr := deps.CallLLM(curSA, payload, curAuthID)
		if callErr != nil {
			statusCode := parseUpstreamStatusFromErr(callErr)
			log.Printf("[traework] exec stream upstream error: model=%s auth_hash=%s status=%d err=%s", ctx.Model, authLogHash(curAuthID), statusCode, truncateRedacted(callErr.Error(), 200))
			// 11128：双写记账（模型冷却 + 账号禁用写盘）后直接返回，恢复由禁用探活裁决。
			if isModelBlockedError(statusCode, callErr.Error()) {
				noteModelBlockedHit(curAuthID, circuitKeyModel)
				publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usage.Detail{}, true, statusCode, callErr.Error(), "", 0, curAuthUID, ctx.SessionKey)
				return nil, fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(callErr.Error(), 200))
			}
			// 换号判定用 isAccountFailure：open 阶段 transport 级错误（TTFB 超时等
			// status=0）与 5xx / 429 / 账号级 4xx 同责换号，400 业务错仍直通。核算
			// 由 callLLM（upstream.go）统一记账；此处仅对 4xx 维持既有二次核算，
			// transport/5xx 不重复计数，并驱逐绑定防会话亲和钉死死号。
			if !isAccountFailure(statusCode, callErr.Error()) || attempt >= ctx.Budget || curSA == nil {
				if isAccountLevel4xx(statusCode) {
					reconcileAfterExecutorErrorForModel(curAuthID, circuitKeyModel, statusCode, callErr.Error())
					evictSessionBindingsForAuth(curAuthID)
				}
				publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usage.Detail{}, true, statusCode, callErr.Error(), "", 0, curAuthUID, ctx.SessionKey)
				return nil, callErr
			}
			if isAccountLevel4xx(statusCode) {
				reconcileAfterExecutorErrorForModel(curAuthID, circuitKeyModel, statusCode, callErr.Error())
			}
			evictSessionBindingsForAuth(curAuthID)
			nextAuthID, nextSA, hasNext := deps.pickCandidate(curAuthID, circuitKeyModel)
			if !hasNext || nextSA == nil {
				break
			}
			if _, seen := triedAuthIDs[nextAuthID]; seen {
				break
			}
			triedAuthIDs[nextAuthID] = struct{}{}
			curSA = nextSA
			curAuthID = nextAuthID
			curAuthUID = strings.TrimSpace(nextSA.UserID)
			continue
		}

		chunks, hasToolCalls, firstOutputAt, detail, collectErr := collectTraeStream(bytes.NewReader(resp.Body), ctx.Model, resp.StatusCode)
		if collectErr == nil {
			if isPseudoCompletion(chunks, ctx.InputChars, hasToolCalls) {
				// 2. 伪完成按失败 attempt 核算并驱逐绑定；候选允许时丢弃当前 chunks 后继续。
				reason := "pseudo completion: upstream returned done with near-empty output"
				log.Printf("[traework] exec stream collect pseudo-done: model=%s auth_hash=%s attempt=%d chunks=%d", ctx.Model, authLogHash(curAuthID), attempt+1, len(chunks))
				noteForcedAccountFailureForModel(curAuthID, circuitKeyModel, reason)
				evictSessionBindingsForAuth(curAuthID)
				publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usageDetailForAttempt(detail, chunks), true, resp.StatusCode, reason, "", ttftNSBetween(ctx.Started, firstOutputAt), curAuthUID, ctx.SessionKey)
				lastFailurePublished = true
				if attempt >= ctx.Budget {
					break
				}
				nextAuthID, nextSA, hasNext := deps.pickCandidate(curAuthID, circuitKeyModel)
				if !hasNext || nextSA == nil {
					// 无其它候选：不立即判池耗尽，先同号退避重试一次（窗口性限流自愈），
					// 仍伪才由外层 attempt 预算收口为 pool exhausted。
					if pseudoTries < pseudoRetryBudget {
						pseudoTries++
						log.Printf("[traework] exec stream collect pseudo same-auth retry (pool exhausted candidates): model=%s auth_hash=%s attempt=%d chunks=%d", ctx.Model, authLogHash(curAuthID), attempt+1, len(chunks))
						goto retrySameAuth
					}
					break
				}
				if _, seen := triedAuthIDs[nextAuthID]; seen {
					break
				}
				triedAuthIDs[nextAuthID] = struct{}{}
				curSA = nextSA
				curAuthID = nextAuthID
				curAuthUID = strings.TrimSpace(nextSA.UserID)
				continue
			}
			resetAccountFailover(curAuthID)
			// 一次真实成功即证明该模型已恢复，清掉该模型上残留的冷却。
			resetModelBlockedState(circuitKeyModel)
			// 轮换打点：同步流式 collect 成功路径。与异步路径分开接，两条路径
			// 各有独立的成功返回点，漏一处就会漏掉对应模式的全部流量。
			noteRotationUsage(curAuthID)
			log.Printf("[traework] exec stream collect ok: model=%s auth_hash=%s attempt=%d chunks=%d", ctx.Model, authLogHash(curAuthID), attempt+1, len(chunks))
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usageDetailForAttempt(detail, chunks), false, 0, "", "", ttftNSBetween(ctx.Started, firstOutputAt), curAuthUID, ctx.SessionKey)
			return chunks, nil
		}

		// 3. HTTP 200 内的账号级 SSE 错误可继续换号，其余错误立即返回。
		// 11128（含 200 承载的 event:error 帧）：双写记账后返回，
		// 恢复由 10 分钟禁用探活裁决。
		if isModelBlockedError(resp.StatusCode, collectErr.Error()) {
			noteModelBlockedHit(curAuthID, circuitKeyModel)
			reconcileAfterExecutorErrorForModel(curAuthID, circuitKeyModel, resp.StatusCode, collectErr.Error())
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usage.Detail{}, true, resp.StatusCode, collectErr.Error(), "", ttftNSBetween(ctx.Started, firstOutputAt), curAuthUID, ctx.SessionKey)
			return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, truncateRedacted(collectErr.Error(), 200))
		}
		if !isAccountFailure(resp.StatusCode, collectErr.Error()) || attempt >= ctx.Budget || curSA == nil {
			reconcileAfterExecutorErrorForModel(curAuthID, circuitKeyModel, resp.StatusCode, collectErr.Error())
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usage.Detail{}, true, resp.StatusCode, collectErr.Error(), "", ttftNSBetween(ctx.Started, firstOutputAt), curAuthUID, ctx.SessionKey)
			return nil, collectErr
		}
		reconcileAfterExecutorErrorForModel(curAuthID, circuitKeyModel, resp.StatusCode, collectErr.Error())
		nextAuthID, nextSA, hasNext := deps.pickCandidate(curAuthID, circuitKeyModel)
		if !hasNext || nextSA == nil {
			return nil, collectErr
		}
		if _, seen := triedAuthIDs[nextAuthID]; seen {
			break
		}
		triedAuthIDs[nextAuthID] = struct{}{}
		curSA = nextSA
		curAuthID = nextAuthID
		curAuthUID = strings.TrimSpace(nextSA.UserID)
	}

	errFinal := fmt.Errorf("upstream account pool exhausted after %d attempt(s)", attemptsMade)
	log.Printf("[traework] exec stream auth pool exhausted: model=%s auth_hash=%s err=%s", ctx.Model, authLogHash(curAuthID), truncateRedacted(errFinal.Error(), 200))
	if !lastFailurePublished {
		publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usage.Detail{}, true, 0, errFinal.Error(), "", 0, curAuthUID, ctx.SessionKey)
	}
	return nil, errFinal
}

// reconcileAfterExecutorError records the failure (failover cooldown +
// anomaly trip) for an executor error. Kept as a small indirection so the
// executor never has to know the failover/anomaly mechanics.
//
// When status is 200, the error came from an SSE error frame on an HTTP 200
// response (event:error surface). Remap to 403 so isAccountFailure classifies
// it as account-level and the laddered failover cooldown fires correctly.
func reconcileAfterExecutorError(authID string, status int, body string) {
	reconcileAfterExecutorErrorForModel(authID, "", status, body)
}

// reconcileAfterExecutorErrorForModel 带模型维度的执行器错误收尾。
// 除按维度记账外，其余（200→403 重映射、硬额度生命周期）行为与历史一致。
// [参数] authID：账号唯一标识；model：已归一化的上游模型名，可为空；
// status：上游 HTTP 状态码；body：上游响应正文。
// 最近修改时间：2026-10-19 01:20:00；改动原因：新增账号×模型双层冷却。
func reconcileAfterExecutorErrorForModel(authID, model string, status int, body string) {
	if status == 200 {
		status = 403
	}
	noteAccountFailureForModel(authID, model, status, body)
}

// sseFrameChunks wraps raw JSON payloads into SSE-framed text for legacy
// clients that expect "data: {...}\n\n" chunks.
func sseFrameChunks(chunks []pluginapi.ExecutorStreamChunk) []pluginapi.ExecutorStreamChunk {
	out := make([]pluginapi.ExecutorStreamChunk, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, pluginapi.ExecutorStreamChunk{
			Payload: append([]byte("data: "), append(c.Payload, '\n', '\n')...),
		})
	}
	return out
}

// estimateUsageFromCompletion estimates token usage from a chat.completion
// aggregate payload (best-effort: content length / 4).
func estimateUsageFromCompletion(completion []byte) usage.Detail {
	var c struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(completion, &c); err != nil {
		return usage.Detail{}
	}
	var chars int
	for _, ch := range c.Choices {
		chars += len(ch.Message.Content)
	}
	toks := int64(chars / 4)
	return usage.Detail{InputTokens: 0, OutputTokens: toks, TotalTokens: toks}
}

// estimateUsageFromChunks estimates output tokens from collected stream chunks.
func estimateUsageFromChunks(chunks []pluginapi.ExecutorStreamChunk) usage.Detail {
	var chars int
	for _, c := range chunks {
		var ch struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(c.Payload, &ch); err != nil {
			continue
		}
		for _, c2 := range ch.Choices {
			chars += len(c2.Delta.Content)
		}
	}
	toks := int64(chars / 4)
	return usage.Detail{InputTokens: 0, OutputTokens: toks, TotalTokens: toks}
}

// estimateInputChars sums the text characters across normalized request
// messages (toTraeMessages output: content is a text-parts array). Used as the
// pseudo-completion input-length signal: a long prompt that yields far less
// output than warranted is flagged, while a short prompt with a short answer is
// not.
func estimateInputChars(messages []map[string]any) int {
	var chars int
	for _, m := range messages {
		parts, _ := m["content"].([]map[string]any)
		for _, p := range parts {
			if txt, ok := p["text"].(string); ok {
				chars += len(txt)
			}
		}
	}
	return chars
}
