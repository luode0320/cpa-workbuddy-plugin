// upstream.go implements the Trae SOLO llm_utils_chat call with account-level
// failover. The request format is ported from the verified gateway prototype
// (trae-gateway-go/internal/gateway/upstream.go, itself a port of
// trae-solo-local-api/src/trae-client.js llmUtilsChat), and failure
// classification follows workbuddy-provider (accountFailover.go).
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	llmUtilsChatPath = "/api/agent/v3/llm_utils_chat"
	// soloWorkLite is the SOLO-aligned queue pool used by real Trae traffic.
	soloWorkLite = "solo_work_lite"
	inlineChat   = "inline_chat"
	maxBodyBytes = 4 << 20 // 4 MiB non-streamed body cap
	// streamDefaultMaxTokens 是流式请求缺省 max_tokens。客户端不传时上游
	// Trae 会给极小上限，导致 solo 长任务（如 qwen3.8-max 分析项目）刚开口
	// 就 done。20000 与 config models 样例一致。
	streamDefaultMaxTokens = 20000
)

// UpstreamError carries the upstream HTTP status and truncated body so the
// executor can render a client-appropriate error.
type UpstreamError struct {
	Status int
	Body   string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream HTTP %d: %s", e.Status, e.Body)
}

// buildTraeAuthHeaders builds the request headers for one account. The set
// mirrors trae-solo-local-api buildCommonHeaders/buildStreamHeaders: token,
// device identity, app id, tracing, IDE version metadata.
func buildTraeAuthHeaders(a *traeAuth) http.Header {
	cfg := loadedConfig()
	h := http.Header{}
	token := ""
	if a != nil {
		token = a.Token
	}
	traceID := randomHex(16) // 32 hex chars
	requestID := randomUUID()
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "text/event-stream")
	// CRITICAL: the upstream WAF rate-limits clients whose User-Agent is not a
	// recognized client (Go-http-client/1.1 or empty -> HTTP 200 with
	// event:error code 4011 "exceeded the rate limit"). Verified A/B: only
	// UA "node" passes. Keep the reference UA unless the upstream relaxes it.
	h.Set("User-Agent", "node")
	h.Set("Authorization", "Cloud-IDE-JWT "+token)
	h.Set("X-Cloudide-Token", token)
	h.Set("x-app-id", cfg.AppID)
	h.Set("x-app-version", "default")
	h.Set("x-ide-version", ideVersion())
	h.Set("x-ide-version-code", ideVersionCode())
	h.Set("x-app-version-code", ideVersionCode())
	h.Set("x-custom-trace-id", traceID)
	h.Set("x-flow-traceparent", "04-"+traceID+"-"+randomHex(8)+"-01")
	h.Set("x-device-brand", cfg.DeviceModel)
	h.Set("x-device-cpu", "Intel")
	if a != nil && a.DeviceID != "" {
		h.Set("x-device-id", a.DeviceID)
	}
	if a != nil && a.MachineID != "" {
		h.Set("x-machine-id", a.MachineID)
	}
	h.Set("x-os-version", cfg.OSVersion)
	h.Set("x-device-type", cfg.OSName)
	h.Set("x-ide-version-type", "stable")
	h.Set("request-traffic-type", "prod")
	if a != nil && a.UserID != "" {
		h.Set("x-uid", a.UserID)
	}
	h.Set("X-Request-ID", requestID)
	h.Set("X-Trae-Request-ID", requestID)
	return h
}

// ideVersionCode returns the configured code or today's YYYYMMDD (the SOLO
// client convention, e.g. 20260827).
func ideVersionCode() string {
	cfg := loadedConfig()
	if cfg.IDEVersionCode != "" {
		return cfg.IDEVersionCode
	}
	return time.Now().Format("20060102")
}

// ideVersion returns the configured IDE version, or the version auto-detected
// from the installed client's manifest.json (config override wins).
func ideVersion() string {
	cfg := loadedConfig()
	if cfg.IDEVersion != "" {
		return cfg.IDEVersion
	}
	if v := DetectIDEVersion(); v != "" {
		return v
	}
	return "0.1.58" // last known default; keep in sync with Trae releases
}

// buildTraePayload converts a normalized chat request into the Trae
// llm_utils_chat body. Key rule (verified): when config_name is set, do NOT
// add agent_type/device_id/ide_version — the server returns 4023 "model is
// unknown".
func buildTraePayload(messages []map[string]any, model string, stream bool, maxTokens int, temperature, topP *float64) map[string]any {
	fn, configName := resolveModelOptions(model)
	payload := map[string]any{
		"messages": messages,
		"function": fn,
		"stream":   stream,
	}
	if configName != "" {
		payload["config_name"] = configName
		payload["model"] = configName
	}
	// 流式路径缺省 max_tokens：客户端不传时上游 Trae 会给极小上限，导致
	// solo 长任务刚开口就 done；补默认值仅作用于流式，非流式保持原样。
	if stream && maxTokens <= 0 {
		maxTokens = streamDefaultMaxTokens
	}
	if maxTokens > 0 {
		payload["max_tokens"] = maxTokens
	}
	if temperature != nil {
		payload["temperature"] = *temperature
	}
	if topP != nil {
		payload["top_p"] = *topP
	}
	return payload
}

// resolveModelOptions 把客户端模型名映射到 SOLO 队列池与精确 config_name。
// 未知名称仍按 config_name 透传，空值或 auto 使用通用 inline_chat 池；
// 部分客户端短 ID 保持稳定，由别名表映射到 Trae 提供的品牌化 config_name。
//
// [参数] model: 客户端传入的模型 ID。
// [返回] Trae 功能池与上游精确 config_name。
// 最近修改时间：2026-08-30 02:55:11；改动原因：兼容 Seed 短 ID，同时保持精确模型 ID 和 auto 的既有语义。
func resolveModelOptions(model string) (fn, configName string) {
	if model == "" || model == "auto" {
		return inlineChat, ""
	}
	if upstream, ok := traeConfigNameAliases[model]; ok {
		model = upstream
	}
	return soloWorkLite, model
}

// traeConfigNameAliases 把稳定的客户端短 ID 映射到 get_detail_param 返回的精确 config_name。
// Seed 短 ID 若直接发往上游，会被 SSE 业务错误 4001 以参数无效拒绝。
var traeConfigNameAliases = map[string]string{
	"seed-evolving":  "Doubao-Seed-Evolving",
	"seed-2.1-pro":   "Doubao-Seed-2.1-Pro",
	"seed-2.1-turbo": "Doubao-Seed-2.1-Turbo",
}

// apiHostFor resolves the upstream llm_utils_chat host. credential.Host is
// deliberately ignored — it is an auth-domain host with no /api/agent/v3/*
// routes (verified 404 TLB in the prototype). The config default is
// defaultChatAPIHost (trae-api-cn.mchost.guru); do NOT point it at
// api.trae.cn — that host only serves check-in/points routes and 404s
// (TLB) on llm_utils_chat.
func apiHostFor(a *traeAuth) string {
	_ = a
	return strings.TrimRight(loadedConfig().APIHost, "/")
}

// callLLM 通过宿主普通 HTTP 桥执行需要完整响应体的 llm_utils_chat 请求。
// [参数] a: Trae 账号；payload: 上游请求体；authID: 故障核算使用的账号标识。
// [返回] hostHTTPResponse: 完整缓冲响应；error: 请求构造、传输或 HTTP 状态错误。
// 最近修改时间：2026-08-30 23:40:18；改动原因：同步路径不再凭 HTTP 2xx 提前复位账号，业务成功改由 SSE done 确认。
func callLLM(a *traeAuth, payload map[string]any, authID string) (*hostHTTPResponse, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	url := apiHostFor(a) + llmUtilsChatPath
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header = buildTraeAuthHeaders(a)
	resp, err := hostHTTPDo(req)
	if err != nil {
		// Transport error counts as an account-level failure (status 0).
		noteAccountFailure(authID, 0, err.Error())
		return nil, fmt.Errorf("llm_utils_chat transport: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body := truncateRedacted(string(resp.Body), 300)
		noteAccountFailure(authID, resp.StatusCode, string(resp.Body))
		return nil, &UpstreamError{Status: resp.StatusCode, Body: body}
	}
	// HTTP 2xx 只代表传输成功，SSE 业务终止必须由调用方收到 done 后确认。
	return resp, nil
}

// callLLMStream 通过宿主流桥打开 llm_utils_chat，并保留响应体的实时读取能力。
// [参数] a: Trae 账号；payload: 上游请求体；authID: 故障核算使用的账号标识；hostCallbackID: CPA 异步执行的 callback 标识。
// [返回] hostHTTPStream: 实时响应流；statusCode: HTTP 状态码；error: 请求构造、传输或非 2xx 错误。
// 最近修改时间：2026-08-30 23:40:18；改动原因：异步聊天透传 callback context，并边读边下发，避免长回答期间客户端收不到分片。
func callLLMStream(a *traeAuth, payload map[string]any, authID, hostCallbackID string) (*hostHTTPStream, int, error) {
	// 1. 复用同步路径的请求体和认证头构造，确保两条路径只在响应读取方式上不同。
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal payload: %w", err)
	}
	url := apiHostFor(a) + llmUtilsChatPath
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header = buildTraeAuthHeaders(a)

	// 2. 非 2xx 响应读取有限正文后关闭流，沿用现有账号故障分类与脱敏错误结构。
	stream, statusCode, _, err := hostHTTPDoStream(req, hostCallbackID)
	if err != nil {
		noteAccountFailure(authID, 0, err.Error())
		return nil, 0, fmt.Errorf("llm_utils_chat stream transport: %w", err)
	}
	if statusCode < 200 || statusCode >= 300 {
		defer stream.Close()
		body, readErr := io.ReadAll(io.LimitReader(newHostStreamReader(stream), maxBodyBytes))
		if readErr != nil {
			noteAccountFailure(authID, statusCode, readErr.Error())
			return nil, statusCode, fmt.Errorf("read llm_utils_chat error body: %w", readErr)
		}
		noteAccountFailure(authID, statusCode, string(body))
		return nil, statusCode, &UpstreamError{Status: statusCode, Body: truncateRedacted(string(body), 300)}
	}
	return stream, statusCode, nil
}

// ---------- SSE scanning (ported from the prototype) ----------

// sseEvent is one parsed SSE event (event name + data payload).
type sseEvent struct {
	Event string
	Data  string
}

// scanSSE 逐行扫描 SSE 流，并把 event 字段关联到后续 data 字段。
//
// [参数] r: 上游 SSE 字节流；fn: 每条 data 事件的处理函数；hasPayload: 报告当前是否已累积可交付业务事件的判定函数（可为 nil）。
// [返回] error: 事件处理失败、零内容空响应或无可交付内容时返回错误；已有可交付内容的上游断流按截断正常返回 nil。
// 最近修改时间：2026-08-31 15:20:00；改动原因：读错误型断流（RST/unexpected EOF）若已有可交付内容，应与干净 EOF 同款兜底收尾，不再中断并丢弃已生成内容。
func scanSSE(r io.Reader, fn func(ev sseEvent) error, hasPayload func() bool) error {
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 16*1024)
	event := ""
	for {
		// 1. 累积任意读取分片；EOF 前的残留字节按最后一条完整行处理，避免终止事件因缺少换行被丢弃。
		n, err := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err == io.EOF && len(buf) > 0 {
			buf = append(buf, '\n')
		}

		// 2. 逐行解析 event 与 data；只有 data 行才会触发回调，孤立 event 残片不会伪造业务事件。
		for {
			idx := bytes.IndexByte(buf, '\n')
			if idx < 0 {
				break
			}
			line := strings.TrimRight(string(buf[:idx]), "\r")
			buf = buf[idx+1:]
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			switch {
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "data:"):
				data := strings.TrimSpace(line[len("data:"):])
				ev := sseEvent{Event: event, Data: data}
				if event == "" {
					ev.Event = "message"
				}
				if err := fn(ev); err != nil {
					return err
				}
			default:
				// 注释、retry 和 id 行不参与当前业务事件转换。
			}
		}

		// 3. 完成当前读取批次后再处理终止或读取错误，确保 n > 0 与 EOF 同时返回时不会丢数据。
		if err == io.EOF {
			return nil
		}
		if err != nil {
			// 上游中途断流：读错误（RST / unexpected EOF / 桥接错误）时，只要已经收到可交付
			// 的业务内容，就按截断正常收尾——保留已生成内容，交由 classify 补 length 结束，
			// 而不是把内容连同错误一起丢弃导致 IDE 侧中断且无下文。零内容才让读错误致命。
			if hasPayload != nil && hasPayload() {
				return nil
			}
			return err
		}
	}
}

// normalizeOutput parses an "output" event's JSON into text / reasoning /
// tool_calls fragments. Both legacy ({response}) and 2026-05 formats
// ({type:text, content}) are handled (port of normalizeLlmUtilsChunk).
func normalizeOutput(data string) (text, reasoning string, toolCalls json.RawMessage) {
	var chunk map[string]any
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return "", "", nil
	}
	if s, _ := chunk["response"].(string); s != "" {
		if !strings.HasPrefix(s, "Building prompt:") && !strings.HasPrefix(s, "Completed building prompt") {
			text += s
		}
	}
	if s, _ := chunk["content"].(string); s != "" {
		text += s
	}
	if s, _ := chunk["reasoning_content"].(string); s != "" {
		reasoning += s
	}
	if s, _ := chunk["reasoning"].(string); s != "" {
		reasoning += s
	}
	if tc, ok := chunk["tool_calls"]; ok {
		if raw, err := json.Marshal(tc); err == nil {
			toolCalls = raw
		}
	}
	return text, reasoning, toolCalls
}

// streamErrData extracts an error description from an SSE event:error payload.
// Returns "" when the payload is not an error shape.
func streamErrData(data string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return ""
	}
	if msg, _ := m["message"].(string); msg != "" {
		if code, _ := m["code"].(string); code != "" {
			return code + ": " + msg
		}
		return msg
	}
	return ""
}

// ---------- small helpers ----------

func randomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", 2*n)
	}
	return hex.EncodeToString(b)
}
