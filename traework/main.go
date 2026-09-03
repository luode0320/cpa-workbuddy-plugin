// Package main implements the traework CLIProxyAPI dynamic plugin.
//
// traework wraps the Trae Work SOLO API as a cliproxy provider: it decrypts
// pasted Trae credentials (tc-header AES), routes chat-completions traffic to
// llm_utils_chat with account-level failover, and exposes management APIs for
// account credits / check-in / points / enable-disable / failover status.
//
// Built with -buildmode=c-shared; exports the cliproxy C ABI entry points.
// Upstream protocol details (llm_utils_chat body, UA=node WAF rule, header
// set) were verified against the prototype gateway in F:\trae-proto.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

// Wrappers so Go can invoke the host function-pointer table via cgo.
static int wb_call_host(cliproxy_host_api* api, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	return api->call(api->host_ctx, method, request, request_len, response);
}
static void wb_free_host_buffer(cliproxy_host_api* api, void* ptr, size_t len) {
	api->free_buffer(ptr, len);
}

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	providerName  = "traework-provider"
	authFileName  = "traework.json"
	pluginLogoURL = "https://raw.githubusercontent.com/luode0320/cpa-workbuddy-plugin/main/assets/icons/TraeWork.png"

	// checkinRefreshInterval bounds how often the dashboard lazily refreshes
	// credits for accounts without a cached snapshot.
	checkinRefreshInterval = 5 * time.Minute
)

var (
	hostAPI        *C.cliproxy_host_api // captured at init, used for async host calls
	httpClientOnce sync.Once
	sharedClient   *http.Client

	// streamHTTPClientOnce / streamClient 是流式直连专用客户端（无整体超时），
	// 与 sharedClient（120s 整体超时）分离：桥降级直连长推理时不被整体超时掐断。
	streamHTTPClientOnce sync.Once
	streamClient         *http.Client
)

func main() {}

// -----------------------------------------------------------------------------
// C ABI exports
// -----------------------------------------------------------------------------

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	hostAPI = host
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	// Intentionally a no-op (same rationale as qoderwork/workbuddy: touching
	// Go runtime state during host teardown risks a cgo SIGSEGV; the OS
	// reclaims goroutines and tickers on exit).
}

// -----------------------------------------------------------------------------
// Host calls
// -----------------------------------------------------------------------------

// hostCall invokes a host RPC method via the function-pointer table captured
// at init. Used to read the host's auth store (host.auth.list/get/save) and
// push stream chunks (host.stream.emit/close).
func hostCall(method string, request []byte) ([]byte, error) {
	if hostAPI == nil || hostAPI.call == nil {
		return nil, fmt.Errorf("host API unavailable")
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var cReq unsafe.Pointer
	var reqLen C.size_t
	if len(request) > 0 {
		cReq = C.CBytes(request)
		defer C.free(cReq)
		reqLen = C.size_t(len(request))
	}
	var resp C.cliproxy_buffer
	rc := C.wb_call_host(hostAPI, cMethod, (*C.uint8_t)(cReq), reqLen, &resp)
	var out []byte
	if resp.ptr != nil && resp.len > 0 {
		out = C.GoBytes(resp.ptr, C.int(resp.len))
	}
	if resp.ptr != nil && hostAPI.free_buffer != nil {
		C.wb_free_host_buffer(hostAPI, resp.ptr, resp.len)
	}
	if rc != 0 {
		return out, fmt.Errorf("host call %s returned %d", method, int(rc))
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Method dispatch
// -----------------------------------------------------------------------------

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		configure(request)
		return okEnvelope(wbRegistration())
	case pluginabi.MethodModelStatic:
		return handleModelStatic(request)
	case pluginabi.MethodModelForAuth:
		return handleModelForAuth(request)
	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerName})
	case pluginabi.MethodAuthParse:
		return handleParseAuth(request)
	case pluginabi.MethodAuthLoginStart:
		// Trae has no plugin-driven OAuth; see login.go for why we must
		// return a valid state (empty state trips the host's
		// "invalid oauth state" guard) plus a data: guide page.
		return handleStartLogin(request)
	case pluginabi.MethodAuthLoginPoll:
		return handlePollLogin(request)
	case pluginabi.MethodAuthRefresh:
		// Token refresh is a P1 follow-up (ExchangeToken currently 404s for
		// fresh tokens); treat as no-op success.
		return okEnvelope(map[string]any{"ok": true, "refreshed": false})
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerName})
	case pluginabi.MethodExecutorExecute:
		return handleExecExecute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return handleExecStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":0}`)})
	case pluginabi.MethodManagementRegister:
		var regReq pluginapi.ManagementRegistrationRequest
		if err := json.Unmarshal(request, &regReq); err == nil {
			if regReq.BasePath != "" {
				setManagementBasePath(regReq.BasePath)
			}
		}
		return okEnvelope(managementRegistration())
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	case pluginabi.MethodSchedulerPick:
		return handleSchedulerPick(request)
	case pluginabi.MethodUsageHandle:
		return handleUsage(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// -----------------------------------------------------------------------------
// Registration
// -----------------------------------------------------------------------------

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type streamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

type registrationCapability struct {
	ModelProvider         bool                         `json:"model_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	FrontendAuthProvider  bool                         `json:"frontend_auth_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats,omitempty"`
	Scheduler             bool                         `json:"scheduler"`
	ManagementAPI         bool                         `json:"management_api"`
	UsagePlugin           bool                         `json:"usage_plugin"`
}

// version is injected at build time via -ldflags "-X main.version=...".
var version = "0.1.34"

func wbRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             providerName,
			Version:          version,
			Author:           "luode (based on qoderwork-provider by Sliverkiss)",
			GitHubRepository: "https://github.com/luode0320/cpa-workbuddy-plugin",
			Logo:             pluginLogoURL,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "api_host", Type: pluginapi.ConfigFieldTypeString, Description: "上游 llm_utils_chat 服务地址（默认 https://trae-api-cn.mchost.guru）。"},
				{Name: "app_id", Type: pluginapi.ConfigFieldTypeString, Description: "x-app-id 请求头取值（默认共享的 Trae 客户端 ID）。"},
				{Name: "scheduler_mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{schedulerModeOff, schedulerModeCredits, schedulerModeSession}, Description: "多账号选择策略：off（交给内置逻辑，默认）、credits（插件按面板活跃账号选择健康账号）或 session（会话级粘性路由，同一会话固定同一账号 1 小时，失败自动换号）。"},
				{Name: "checkin_auto", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用每日自动签到（本地时间 09:00 与 21:00，默认开启）。"},
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "可选模型列表。每个条目可为模型 id 字符串或 {id, name, ...} 对象；未配置时使用内置默认列表。"},
				{Name: "retry_on_4xx", Type: pluginapi.ConfigFieldTypeString, Description: "账号级 4xx 时每次请求的换号重试预算（0-10，默认 10）。"},
				{Name: "anomaly_pool_threshold", Type: pluginapi.ConfigFieldTypeString, Description: "连续失败次数阈值（1-50），达到后账号进入异常池（默认 10）。"},
				{Name: "anomaly_refresh_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用每日 00:00 异常池自动重置（默认开启）。"},
				{Name: "management_key", Type: pluginapi.ConfigFieldTypeString, Description: "可选：管理类接口的 Bearer 密钥（纵深防御；留空则信任宿主中间件）。"},
				{Name: "usage_report_url", Type: pluginapi.ConfigFieldTypeString, Description: "可选：CPAMP 用量上报地址（NDJSON）。"},
				{Name: "usage_report_key", Type: pluginapi.ConfigFieldTypeString, Description: "可选：用量上报使用的 CPAMP 管理密钥。"},
				{Name: "usage_feed_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "将每次请求的 token 用量追加写入共享 NDJSON 数据流，供 token-usage-tracker 插件消费（默认开启）。"},
				{Name: "usage_feed_path", Type: pluginapi.ConfigFieldTypeString, Description: "可选：共享用量数据流路径（默认 <CLIProxyAPI 根目录>/data/token-usage-feed.ndjson）。必须与 token-usage-tracker 的 usage_feed_path 保持一致。"},
				{Name: "preserve_threshold", Type: pluginapi.ConfigFieldTypeString, Description: "保号池积分阈值（1-500，默认 50）：可用积分低于该值的账号自动进入保号池，仅在无其它可用账号时兜底路由。"},
				{Name: "preserve_watchdog_interval", Type: pluginapi.ConfigFieldTypeString, Description: "保号看护（watchdog）检查间隔（分钟，默认 10）：周期性刷新积分快照并更新保号池归属。"},
				{Name: "preserve_watchdog_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用保号看护循环（默认开启）：关闭后保号池不再自动维护，仅保留手动路由。"},
				{Name: "token_keepalive", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用每日 token 保号刷新（本地时间 22:00，默认开启）：access token 临近过期时通过 ExchangeToken 自动续期；刷新令牌失效的账号自动标记禁用待重新导入。"},
				{Name: "lifecycle_auto", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用积分生命周期自动停用（默认开启）：账号积分耗尽（remain<=0）后自动禁用，避免浪费请求；不自动复活，需面板手动启用或重新导入。"},
			},
		},
		Capabilities: registrationCapability{
			ModelProvider:         true,
			AuthProvider:          true,
			FrontendAuthProvider:  false,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  []string{"chat-completions"},
			ExecutorOutputFormats: []string{"chat-completions"},
			ManagementAPI:         true,
			Scheduler:             true,
			UsagePlugin:           true,
		},
	}
}

// -----------------------------------------------------------------------------
// Auth parse (paste credential from storage.json)
// -----------------------------------------------------------------------------

func handleParseAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	// Ownership check (CPA native contract): only claim files whose declared
	// type matches us — or whose filename carries our prefix.
	var probeType struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(req.RawJSON, &probeType)
	declared := strings.ToLower(strings.TrimSpace(probeType.Type))
	if declared != "" && declared != providerName {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	if declared == "" {
		routed := strings.EqualFold(strings.TrimSpace(req.Provider), providerName)
		prefixed := strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.FileName)), providerName+"-")
		if !routed && !prefixed {
			return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
		}
	}
	a, err := parseTraeAuth(req.RawJSON)
	if err != nil {
		// Not a traework credential; let the host try other providers.
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	// CRITICAL: echo back the host-provided FileName AND leave ID empty so
	// CPA derives ID from the file path (prevents duplicate auth records).
	ad := toAuthDataOpts(a, false)
	ad.ID = ""
	if fn := strings.TrimSpace(req.FileName); fn != "" {
		ad.FileName = fn
	}
	return okEnvelope(pluginapi.AuthParseResponse{
		Handled: true,
		Auth:    ad,
	})
}

// toAuthDataOpts builds AuthData for a parsed traework credential.
func toAuthDataOpts(a *traeAuth, disabled bool) pluginapi.AuthData {
	storage, _ := json.Marshal(a)
	id := providerName
	fileName := authFileName
	if a != nil {
		if uid := sanitizeUIDForFileName(a.UserID); uid != "" {
			id = uid
			fileName = "traework-" + uid + ".json"
		}
	}
	label := a.Nickname
	if label == "" {
		label = a.UserID
	}
	if label == "" {
		label = providerName
	}
	meta := map[string]any{
		"type":     providerName,
		"provider": providerName,
		"logo":     pluginLogoURL,
		"disabled": disabled,
	}
	return pluginapi.AuthData{
		Provider:    providerName,
		ID:          id,
		FileName:    fileName,
		Label:       label,
		Disabled:    disabled,
		StorageJSON: storage,
		Metadata:    meta,
	}
}

// -----------------------------------------------------------------------------

func okEnvelope(v any) ([]byte, error) {
	result, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: result})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
