package main

/*
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

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
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	providerName      = "glm-provider"
	pluginDisplayName = "GLM Web Provider"
	pluginLogoURL     = "https://raw.githubusercontent.com/luode0320/cpa-workbuddy-plugin/main/assets/icons/ZCode.png"
	// schema 6 保留管理接口原始 JSON，面板自行安全显示文本。
	pluginSchemaVersion uint32 = 6
	// refreshEndpoint 是 refresh_token 换 access_token 的上游端点。
	refreshEndpoint = "https://chatglm.cn/chatglm/user-api/user/refresh"
	// streamEndpoint 是流式对话的上游端点。
	streamEndpoint = "https://chatglm.cn/chatglm/backend-api/assistant/stream"
	// userInfoEndpoint 用 access_token 查询账号信息，识别访客账号。
	userInfoEndpoint = "https://chatglm.cn/chatglm/user-api/user/info"
	// assistantListEndpoint 列出用户自建智能体，动态模型目录由此获取。
	assistantListEndpoint = "https://chatglm.cn/chatglm/backend-api/assistant/list"
	// conversationListEndpoint 读取历史会话，从中发现上游实际用过的 chat_mode。
	conversationListEndpoint = "https://chatglm.cn/chatglm/backend-api/assistant/conversation/list"
	// conversationDeleteEndpoint 流结束后清理会话，避免污染用户对话列表。
	conversationDeleteEndpoint = "https://chatglm.cn/chatglm/backend-api/assistant/conversation/delete"
	// defaultAssistantID 是 GLM4 原版智能体 ID。
	defaultAssistantID = "65940acff94777010aa6b796"
	// defaultSignSecret 是网页签名密钥，配置可覆盖。
	defaultSignSecret = "8a1317a7468aa3ad86e997d08f3f31cb"
	// accessTokenLifetimeSec 是 access_token 有效期。
	accessTokenLifetimeSec = 3600
)

var (
	// version 当前插件版本号。
	version      = "0.3.0"
	hostAPI      *C.cliproxy_host_api
	hostAPIMu    sync.RWMutex
	activeConfig atomic.Value
)

// init 初始化插件默认配置。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件入口。
func init() {
	activeConfig.Store(defaultConfig())
}

func main() {}

// Config 是插件运行配置。
type Config struct {
	// SignSecret 网页签名密钥，留空用内置默认值。
	SignSecret string `yaml:"sign_secret" json:"sign_secret"`
	// RefreshEndpoint 覆盖刷新端点，便于本地 mock 与协议漂移修正。
	RefreshEndpoint string `yaml:"refresh_endpoint" json:"refresh_endpoint"`
	// StreamEndpoint 覆盖对话端点，便于本地 mock 与协议漂移修正。
	StreamEndpoint string `yaml:"stream_endpoint" json:"stream_endpoint"`
	// UserInfoEndpoint 覆盖用户信息端点，积分与额度快照由此拉取。
	UserInfoEndpoint string `yaml:"user_info_endpoint" json:"user_info_endpoint"`
	// AssistantListEndpoint 覆盖智能体列表端点，动态模型目录由此拉取。
	AssistantListEndpoint string `yaml:"assistant_list_endpoint" json:"assistant_list_endpoint"`
	// ConversationListEndpoint 覆盖会话列表端点，档位发现由此拉取。
	ConversationListEndpoint string `yaml:"conversation_list_endpoint" json:"conversation_list_endpoint"`
	// TimeoutSeconds 元数据短请求整体超时（秒）。
	TimeoutSeconds int `yaml:"timeout_seconds" json:"timeout_seconds"`
	// StreamTimeoutSeconds 流式请求响应头等待上限（秒）。
	StreamTimeoutSeconds int `yaml:"stream_timeout_seconds" json:"stream_timeout_seconds"`
	// AccountMaxInflight 同一账号最大在途请求数。
	AccountMaxInflight int `yaml:"account_max_inflight" json:"account_max_inflight"`
	// AccountMinIntervalMs 同一账号两次发起的最小间隔（毫秒）。
	AccountMinIntervalMs int `yaml:"account_min_interval_ms" json:"account_min_interval_ms"`
}

// defaultConfig 返回插件默认配置。
// [参数] 无。
// [返回] 默认配置。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件入口。
func defaultConfig() Config {
	return Config{
		SignSecret:               defaultSignSecret,
		RefreshEndpoint:          refreshEndpoint,
		StreamEndpoint:           streamEndpoint,
		UserInfoEndpoint:         userInfoEndpoint,
		AssistantListEndpoint:    assistantListEndpoint,
		ConversationListEndpoint: conversationListEndpoint,
		TimeoutSeconds:           30,
		StreamTimeoutSeconds:     120,
		AccountMaxInflight:       1,
		AccountMinIntervalMs:     2000,
	}
}

// currentConfig 读取当前生效配置。
// [参数] 无。
// [返回] 当前配置。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件入口。
func currentConfig() Config {
	if cfg, ok := activeConfig.Load().(Config); ok {
		return cfg
	}
	return defaultConfig()
}

// cliproxy_plugin_init 注册插件 ABI 入口。
// [参数] host: 宿主 API；plugin: 插件 API 表。
// [返回] 0 表示成功。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件入口。
//
//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	hostAPIMu.Lock()
	hostAPI = host
	hostAPIMu.Unlock()

	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

// cliproxyPluginCall 分发宿主方法调用。
// [参数] method: 方法名；request: 请求字节；requestLen: 长度；response: 响应缓冲。
// [返回] 0 成功，非 0 失败。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件入口。
//
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

	raw, err := handleMethod(C.GoString(method), requestBytes)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

// cliproxyPluginFree 释放宿主分配的响应缓冲。
// [参数] ptr: 缓冲指针；length: 长度。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件入口。
//
//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

// cliproxyPluginShutdown 关闭插件并清理宿主 API 引用。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件入口。
//
//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	hostAPIMu.Lock()
	hostAPI = nil
	hostAPIMu.Unlock()
}

// hostCall 调用宿主 RPC 方法。
// [参数] method: 方法名；request: 请求字节。
// [返回] 响应字节、错误。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件入口。
func hostCall(method string, request []byte) ([]byte, error) {
	hostAPIMu.RLock()
	api := hostAPI
	hostAPIMu.RUnlock()

	if api == nil || api.call == nil {
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
	rc := C.wb_call_host(api, cMethod, (*C.uint8_t)(cReq), reqLen, &resp)
	var out []byte
	if resp.ptr != nil && resp.len > 0 {
		out = C.GoBytes(resp.ptr, C.int(resp.len))
	}
	if resp.ptr != nil && api.free_buffer != nil {
		C.wb_free_host_buffer(api, resp.ptr, resp.len)
	}
	if rc != 0 {
		return out, fmt.Errorf("host call %s returned %d", method, int(rc))
	}
	return out, nil
}

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

type registrationCapability struct {
	ModelProvider         bool                         `json:"model_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats,omitempty"`
	Scheduler             bool                         `json:"scheduler"`
	ManagementAPI         bool                         `json:"management_api"`
	UsagePlugin           bool                         `json:"usage_plugin"`
}

// pluginRegistration 返回插件注册元信息。
// [参数] 无。
// [返回] 注册信息。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件入口。
func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginSchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             providerName,
			Version:          version,
			Author:           "luode0320 (cpa-workbuddy-plugin)",
			GitHubRepository: "https://github.com/luode0320/cpa-workbuddy-plugin",
			Logo:             pluginLogoURL,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "sign_secret", Type: pluginapi.ConfigFieldTypeString, Description: "网页签名密钥，留空使用内置默认值。"},
				{Name: "refresh_endpoint", Type: pluginapi.ConfigFieldTypeString, Description: "刷新令牌端点，留空使用 chatglm.cn 默认地址。"},
				{Name: "stream_endpoint", Type: pluginapi.ConfigFieldTypeString, Description: "流式对话端点，留空使用 chatglm.cn 默认地址。"},
				{Name: "timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "元数据请求超时（秒）。"},
				{Name: "stream_timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "流式请求响应头等待上限（秒）。"},
				{Name: "account_max_inflight", Type: pluginapi.ConfigFieldTypeInteger, Description: "同一账号最大在途请求数（默认 1）。"},
				{Name: "account_min_interval_ms", Type: pluginapi.ConfigFieldTypeInteger, Description: "同一账号两次发起最小间隔（毫秒）。"},
			},
		},
		Capabilities: registrationCapability{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  []string{"openai"},
			ExecutorOutputFormats: []string{"openai"},
			Scheduler:             true,
			ManagementAPI:         true,
			UsagePlugin:           true,
		},
	}
}

// handleMethod 按方法名分发宿主请求。
// [参数] method: 方法名；request: 请求字节。
// [返回] 响应信封、错误。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件入口。
func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configurePlugin(request); err != nil {
			return nil, err
		}
		return okEnvelope(pluginRegistration())

	case pluginabi.MethodModelRegister:
		return okEnvelope(pluginapi.ModelRegistrationResponse{
			Provider: providerName,
			Models:   allModelInfos(),
		})

	case pluginabi.MethodModelStatic, pluginabi.MethodModelForAuth:
		return okEnvelope(pluginapi.ModelResponse{
			Provider: providerName,
			Models:   allModelInfos(),
		})

	case pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerName})

	case pluginabi.MethodAuthParse:
		return handleParseAuth(request)

	case pluginabi.MethodAuthLoginStart:
		return handleStartLogin(request)

	case pluginabi.MethodAuthLoginPoll:
		return handlePollLogin(request)

	case pluginabi.MethodAuthRefresh:
		return handleRefreshAuth(request)

	case pluginabi.MethodExecutorExecute:
		return handleExecExecute(request)

	case pluginabi.MethodExecutorExecuteStream:
		return handleExecStream(request)

	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration())

	case pluginabi.MethodManagementHandle:
		return handleManagement(request)

	case pluginabi.MethodSchedulerPick:
		return handleSchedulerPick(request)

	case pluginabi.MethodUsageHandle:
		return okEnvelope(map[string]any{"forwarded": false})

	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// configurePlugin 解析并应用插件配置。
// [参数] raw: 配置请求字节。
// [返回] 配置错误。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件入口。
func configurePlugin(raw []byte) error {
	var req struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}

	cfg := defaultConfig()
	if len(req.ConfigYAML) > 0 {
		var decoded Config
		if err := yaml.Unmarshal(req.ConfigYAML, &decoded); err != nil {
			return err
		}
		if strings.TrimSpace(decoded.SignSecret) != "" {
			cfg.SignSecret = strings.TrimSpace(decoded.SignSecret)
		}
		if strings.TrimSpace(decoded.RefreshEndpoint) != "" {
			cfg.RefreshEndpoint = strings.TrimRight(strings.TrimSpace(decoded.RefreshEndpoint), "/")
		}
		if strings.TrimSpace(decoded.StreamEndpoint) != "" {
			cfg.StreamEndpoint = strings.TrimRight(strings.TrimSpace(decoded.StreamEndpoint), "/")
		}
		if strings.TrimSpace(decoded.UserInfoEndpoint) != "" {
			cfg.UserInfoEndpoint = strings.TrimRight(strings.TrimSpace(decoded.UserInfoEndpoint), "/")
		}
		if strings.TrimSpace(decoded.AssistantListEndpoint) != "" {
			cfg.AssistantListEndpoint = strings.TrimRight(strings.TrimSpace(decoded.AssistantListEndpoint), "/")
		}
		if strings.TrimSpace(decoded.ConversationListEndpoint) != "" {
			cfg.ConversationListEndpoint = strings.TrimRight(strings.TrimSpace(decoded.ConversationListEndpoint), "/")
		}
		if decoded.TimeoutSeconds > 0 {
			cfg.TimeoutSeconds = decoded.TimeoutSeconds
		}
		if decoded.StreamTimeoutSeconds > 0 {
			cfg.StreamTimeoutSeconds = decoded.StreamTimeoutSeconds
		}
		if decoded.AccountMaxInflight > 0 {
			cfg.AccountMaxInflight = decoded.AccountMaxInflight
		}
		if decoded.AccountMinIntervalMs >= 0 {
			cfg.AccountMinIntervalMs = decoded.AccountMinIntervalMs
		}
	}
	activeConfig.Store(cfg)
	return nil
}

// okEnvelope 包装成功响应信封。
// [参数] v: 结果对象。
// [返回] 信封字节、错误。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件入口。
func okEnvelope(v any) ([]byte, error) {
	result, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: result})
}

// errorEnvelope 包装错误响应信封。
// [参数] code: 错误码；message: 错误文本。
// [返回] 信封字节。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件入口。
func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

// writeResponse 把字节写入 C 响应缓冲。
// [参数] response: 响应缓冲；raw: 字节内容。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件入口。
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
