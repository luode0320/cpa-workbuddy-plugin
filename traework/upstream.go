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

// callLLM performs the raw llm_utils_chat call through the host HTTP bridge.
// On success the caller owns resp. Failures are recorded via
// noteAccountFailure by status.
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
	resetAccountFailover(authID)
	return resp, nil
}

// ---------- SSE scanning (ported from the prototype) ----------

// sseEvent is one parsed SSE event (event name + data payload).
type sseEvent struct {
	Event string
	Data  string
}

// scanSSE reads an SSE stream line-by-line, invoking fn for every data line
// (the event name is carried from the preceding event: line, defaulting to
// "message"). A trailing [DONE] marker yields an event with Data=="[DONE]".
func scanSSE(r io.Reader, fn func(ev sseEvent) error) error {
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 16*1024)
	event := ""
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
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
					// comment / retry / id lines: ignore
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
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
