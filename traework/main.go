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
var version = "0.5.5"

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
				{Name: "scheduler_mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{schedulerModeSession, schedulerModeCredits, schedulerModeOff}, Description: "多账号选择策略：session（按会话轮询，同一会话 1 小时内固定同一账号；默认）／credits（插件按面板活跃账号选择健康账号）／off（交给内置逻辑）。"},
				{Name: "checkin_auto", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用自动签到（每4小时一次，本地时间 00:00/04:00/08:00/12:00/16:00/20:00，默认开启）。"},
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "可选模型列表。每个条目可为模型 id 字符串或 {id, name, ...} 对象；未配置时使用内置默认列表。"},
				{Name: "retry_on_4xx", Type: pluginapi.ConfigFieldTypeString, Description: "账号级 4xx 时每次请求的换号重试预算（0-10，默认 10）。"},
				{Name: "management_key", Type: pluginapi.ConfigFieldTypeString, Description: "可选：管理类接口的 Bearer 密钥（纵深防御；留空则信任宿主中间件）。"},
				{Name: "usage_report_url", Type: pluginapi.ConfigFieldTypeString, Description: "可选：CPAMP 用量上报地址（NDJSON）。"},
				{Name: "usage_report_key", Type: pluginapi.ConfigFieldTypeString, Description: "可选：用量上报使用的 CPAMP 管理密钥。"},
				{Name: "cpa_management_url", Type: pluginapi.ConfigFieldTypeString, Description: "可选：CPA 宿主管理 API 地址，默认 http://127.0.0.1:8317。仅当宿主管理端口非默认时才需要填。"},
				{Name: "cpa_management_key", Type: pluginapi.ConfigFieldTypeString, Description: "可选：CPA 宿主管理密钥。填写后，失败禁用／低额禁用／额度耗尽禁用会改为调用宿主 auth-files/status 接口写入路由真相源。留空则沿用旧的直写物理文件路径。也可用环境变量 WB_CPA_MANAGEMENT_KEY。"},
				{Name: "usage_feed_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "将每次请求的 token 用量追加写入共享 NDJSON 数据流，供 token-usage-tracker 插件消费（默认开启）。"},
				{Name: "usage_feed_path", Type: pluginapi.ConfigFieldTypeString, Description: "可选：共享用量数据流路径（默认 <CLIProxyAPI 根目录>/data/token-usage-feed.ndjson）。必须与 token-usage-tracker 的 usage_feed_path 保持一致。"},
				{Name: "token_keepalive", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用每4小时 token 保号刷新（默认开启）：access token 临近过期时通过 ExchangeToken 自动续期；刷新令牌失效的账号仅记录过期备注不停用（停用需面板手动操作），路由层靠失败冷却自动避开。"},
				{Name: "lifecycle_auto", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用积分生命周期巡检（默认关闭）：账号积分耗尽（remain<=0）自动停用并标记 exhausted_disable；每 4 小时签到刷新积分，恢复>0 自动重新启用；手动停用（manual_disable）永不自动覆盖。"},
				{Name: "auth_flag_guard", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用禁用标记守护（默认开启）：每 5 分钟核对已停用账号的物理文件，若宿主自动刷新重建时抹掉了 disabled 标记则自动写回，防止停用账号悄悄回到路由池。"},
				{Name: "device_model", Type: pluginapi.ConfigFieldTypeString, Description: "设备指纹-机型（x-device-brand）。一般无需修改；仅在账号频繁触发设备风控时才考虑调整。"},
				{Name: "os_version", Type: pluginapi.ConfigFieldTypeString, Description: "设备指纹-系统版本（x-os-version）。一般无需修改。"},
				{Name: "os_name", Type: pluginapi.ConfigFieldTypeString, Description: "设备指纹-系统类型（x-device-type）。一般无需修改。"},
				{Name: "ide_version", Type: pluginapi.ConfigFieldTypeString, Description: "客户端版本（x-ide-version）。一般无需修改；留空时自动读取已安装版本。"},
				{Name: "ide_version_code", Type: pluginapi.ConfigFieldTypeString, Description: "客户端版本号（YYYYMMDD 形态）。一般无需修改；留空时自动生成当日值。"},
				{Name: "min_routable_credits", Type: pluginapi.ConfigFieldTypeInteger, Description: "保活线（默认 20 点）：剩余积分低于该值但高于禁用线的账号打「保活」标，仍可路由但排在正常号之后。填 0 关闭保活降级。"},
				{Name: "low_credit_disable_below", Type: pluginapi.ConfigFieldTypeInteger, Description: "硬禁用线（默认 0 = 关闭）：刷新积分时发现剩余低于该值就写盘停用该账号；积分回升到线上自动解禁。填 0 关闭该机制，行为与 0.3.0 完全一致。"},
				{Name: "rotation_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用连续使用轮换（默认开启）：单个账号连续承流达到 rotation_serve_minutes 后主动让位，进入 rotation_cooldown_minutes 冷却，后续请求改由其它账号承接。已在会话粘性绑定中的会话同样生效；关闭填 false。"},
				{Name: "rotation_serve_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "单账号连续承流多少分钟后让位（默认 60）。仅在 rotation_enabled 开启时生效；填 0 等同关闭轮换。"},
				{Name: "rotation_cooldown_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "轮换让位后停留在冷却的时长（默认 60）。仅在 rotation_enabled 开启时生效；填 0 等同关闭轮换。"},
				{Name: "rotation_idle_reset_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "连续使用的中断阈值（默认 10）：账号距上次使用超过该时长即视为新一轮，重新计时，避免零散流量被误判为持续负载。"},
				{Name: "slow_account_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用慢速账号冷却（默认开启）：某账号在某个模型上的首字延迟中位数显著高于同池同模型水平时，在该模型上进入冷却并让出流量。慢是性能问题不是故障，只走内存冷却，到期自动恢复，不写盘禁用。"},
				{Name: "slow_ttft_ratio_x10", Type: pluginapi.ConfigFieldTypeInteger, Description: "慢速判定倍数阈值 ×10（默认 25 即 2.5 倍）：账号首字延迟中位数达到同模型全池基准的该倍数即判慢。必须大于 10。"},
				{Name: "slow_window_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "首字延迟样本统计窗口（默认 15 分钟）：只统计窗口内的成功请求样本。"},
				{Name: "slow_min_samples", Type: pluginapi.ConfigFieldTypeInteger, Description: "参与慢速判定所需的最小样本数（默认 5）：样本不足时不判定，避免单条毛刺误伤。"},
				{Name: "slow_cooldown_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "慢速冷却时长（默认 10 分钟）：到期自动恢复，无需探活。"},
				{Name: "slow_min_ttft_ms", Type: pluginapi.ConfigFieldTypeInteger, Description: "慢速判定的绝对下限毫秒（默认 3000）：低于该值一律不判慢，避免全池都快时相对放大误伤；填 0 表示不设下限。"},
				{Name: "slow_min_pool_size", Type: pluginapi.ConfigFieldTypeInteger, Description: "账号池准入下限（默认 20）：账号总数（启用 + 停用）须严格大于该值才触发慢速判定。池子太小时下四分位基线退化，两个账号里任一次抖动就会让其中一个被判慢、互相顶替。"},
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
	ad := toAuthDataOpts(a, parseDisabledFromAuthJSON(req.RawJSON))
	ad.ID = ""
	if fn := strings.TrimSpace(req.FileName); fn != "" {
		ad.FileName = fn
	}
	// 盘上本机制的顶层键必须随这次回传一起交回宿主：宿主吸收文件后，会用我们
	// 给出的 StorageJSON + Metadata 反向重建物理文档，不回传就抹掉标记。
	carryFailoverKeysIntoMetadata(ad.Metadata, req.RawJSON)
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
