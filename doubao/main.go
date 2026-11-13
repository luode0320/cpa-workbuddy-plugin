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
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	providerName  = "doubao-provider"
	pluginLogoURL = "https://raw.githubusercontent.com/luode0320/cpa-workbuddy-plugin/main/assets/icons/Doubao.png"
)

var (
	// version 当前插件版本号。
	version      = "0.1.7"
	hostAPI      *C.cliproxy_host_api
	hostAPIMu    sync.RWMutex
	activeConfig atomic.Value
)

func init() {
	activeConfig.Store(defaultConfig())
}

func main() {}

// Config 是插件运行配置。
type Config struct {
	// TimeoutSeconds 非流式上游请求超时（秒）。
	TimeoutSeconds int `yaml:"timeout_seconds" json:"timeout_seconds"`
	// StreamTimeoutSeconds 流式上游请求超时（秒）；流式生成耗时较长，单独配置。
	StreamTimeoutSeconds int `yaml:"stream_timeout_seconds" json:"stream_timeout_seconds"`
	// ModelCacheSecs 模型目录缓存时长（秒）。
	ModelCacheSecs int `yaml:"model_cache_seconds" json:"model_cache_seconds"`
	// WorkBotID 工作模式会话机器人标识；空表示用内置默认值。
	WorkBotID string `yaml:"work_bot_id" json:"work_bot_id"`
	// WorkModeID 工作模式编号；3 是上游「工作任务」模式。
	WorkModeID string `yaml:"work_mode_id" json:"work_mode_id"`
	// WorkReasoningEffort 工作模式推理档位（0-4，4 最高）。
	WorkReasoningEffort string `yaml:"work_reasoning_effort" json:"work_reasoning_effort"`
	// WorkMemoryProfile 工作模式记忆档位，必须与 WorkTotalWindow 配套。
	WorkMemoryProfile string `yaml:"work_memory_profile" json:"work_memory_profile"`
	// WorkTotalWindow 工作模式上下文窗口大小。
	WorkTotalWindow string `yaml:"work_total_window" json:"work_total_window"`
	// WorkEnableSandbox 是否启用上游沙箱；headless 环境必须关闭，否则请求挂起。
	WorkEnableSandbox bool `yaml:"work_enable_sandbox" json:"work_enable_sandbox"`
	// WorkWorkspace 工作模式工作目录；空表示不启用本地目录映射。
	WorkWorkspace string `yaml:"work_workspace" json:"work_workspace"`
}

// defaultConfig 返回插件默认配置。
// [参数] 无。
// [返回] 带默认值的配置结构。
// 最近修改时间：2026-11-14 02:12:00；改动原因：新增工作模式七项配置，默认值与上游客户端实测一致。
func defaultConfig() Config {
	return Config{
		TimeoutSeconds:       120,
		StreamTimeoutSeconds: 600,
		ModelCacheSecs:       300,
		WorkBotID:            defaultBotID,
		WorkModeID:           defaultWorkModeID,
		WorkReasoningEffort:  defaultWorkReasoningEffort,
		WorkMemoryProfile:    defaultWorkMemoryProfile,
		WorkTotalWindow:      defaultWorkTotalWindow,
		WorkEnableSandbox:    false,
		WorkWorkspace:        "",
	}
}

// currentConfig 读取当前生效配置，未初始化时回退默认值。
// [参数] 无。
// [返回] 当前配置。
// 最近修改时间：2026-10-17；改动原因：新增，配置读取入口。
func currentConfig() Config {
	if cfg, ok := activeConfig.Load().(Config); ok {
		return cfg
	}
	return defaultConfig()
}

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

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	hostAPIMu.Lock()
	hostAPI = nil
	hostAPIMu.Unlock()
}

// hostCall 通过宿主导出的 C 函数指针表发起一次 RPC 调用。
// [参数] method: 宿主方法名；request: JSON 请求体，可为空。
// [返回] 宿主返回的原始字节与错误。
// 最近修改时间：2026-10-17；改动原因：新增，宿主流式 RPC 桥入口。
func hostCall(method string, request []byte) ([]byte, error) {
	hostAPIMu.RLock()
	api := hostAPI
	hostAPIMu.RUnlock()

	if api == nil || api.call == nil {
		return nil, fmt.Errorf("宿主 API 不可用")
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
		return out, fmt.Errorf("宿主调用 %s 返回 %d", method, int(rc))
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

// doubaoRegistration 返回插件注册元数据与能力声明。
// [参数] 无。
// [返回] 注册结构。
// 最近修改时间：2026-11-14 04:00:00；改动原因：补充工作模式七项配置字段声明。
func doubaoRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             providerName,
			Version:          version,
			Author:           "Sliverkiss (cpa-workbuddy-plugin)",
			GitHubRepository: "https://github.com/luode0320/cpa-workbuddy-plugin",
			Logo:             pluginLogoURL,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "非流式上游请求超时（秒）。"},
				{Name: "stream_timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "流式上游请求超时（秒）。"},
				{Name: "model_cache_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "模型目录缓存时长（秒）。"},
				{Name: "work_bot_id", Type: pluginapi.ConfigFieldTypeString, Description: "工作模式 bot_id；为空时用账号上的 bot_id。"},
				{Name: "work_mode_id", Type: pluginapi.ConfigFieldTypeString, Description: "工作模式 mode_id（工作任务为 3）。"},
				{Name: "work_reasoning_effort", Type: pluginapi.ConfigFieldTypeString, Description: "工作模式默认推理档位（0-4，4 最高）。"},
				{Name: "work_memory_profile", Type: pluginapi.ConfigFieldTypeString, Description: "工作模式默认记忆档位（如 300k_230）。"},
				{Name: "work_total_window", Type: pluginapi.ConfigFieldTypeString, Description: "工作模式默认上下文窗口（如 300000）。"},
				{Name: "work_enable_sandbox", Type: pluginapi.ConfigFieldTypeBoolean, Description: "是否声明本地沙箱；无人值守部署必须为 false，否则请求会挂到超时。"},
				{Name: "work_workspace", Type: pluginapi.ConfigFieldTypeString, Description: "工作模式工作目录；留空表示不声明本地目录。"},
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
			UsagePlugin:           false,
		},
	}
}

// handleMethod 按宿主方法名分发插件能力。
// [参数] method: 宿主方法名；request: 原始 JSON 请求体。
// [返回] 信封 JSON 与错误。
// 最近修改时间：2026-10-17；改动原因：新增，方法分发表（骨架期仅注册与管理 API 落地）。
func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configurePlugin(request); err != nil {
			return nil, err
		}
		return okEnvelope(doubaoRegistration())

	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerName})

	case pluginabi.MethodAuthParse:
		return handleParseAuth(request)

	case pluginabi.MethodAuthLoginStart:
		return handleStartLogin(request)

	case pluginabi.MethodAuthLoginPoll:
		return handlePollLogin(request)

	case pluginabi.MethodAuthRefresh:
		return handleRefreshAuth(request)

	case pluginabi.MethodModelRegister, pluginabi.MethodModelStatic:
		return modelStatic(request)

	case pluginabi.MethodModelForAuth:
		return modelForAuth(request)

	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerName})

	case pluginabi.MethodExecutorExecute:
		return executorExecute(request)

	case pluginabi.MethodExecutorExecuteStream:
		return executorExecuteStream(request)

	case pluginabi.MethodManagementRegister:
		var regReq pluginapi.ManagementRegistrationRequest
		if err := json.Unmarshal(request, &regReq); err == nil && regReq.BasePath != "" {
			setManagementBasePath(regReq.BasePath)
		}
		return okEnvelope(managementRegistration())

	case pluginabi.MethodManagementHandle:
		return handleManagement(request)

	case pluginabi.MethodSchedulerPick:
		return handleSchedulerPick(request)

	default:
		return errorEnvelope("unknown_method", "unknown_method: "+method), nil
	}
}

// configurePlugin 解析宿主下发的 YAML 配置并更新运行期配置。
//
// 数值项按「>0 才覆盖」处理；字符串项按「非空才覆盖」处理，保证缺省键保留
// 默认值，无关项热更新不会把工作模式参数冲成空串。
// [参数] raw: 宿主 reconfigure 载荷，config_yaml 为 base64 解码后的 YAML 字节。
// [返回] 解析错误。
// 最近修改时间：2026-11-14 02:16:00；改动原因：新增工作模式配置项覆盖逻辑。
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
		// 1. 数值项与字符串项一视同仁：给了才覆盖，缺省键保留默认值。
		if decoded.TimeoutSeconds > 0 {
			cfg.TimeoutSeconds = decoded.TimeoutSeconds
		}
		if decoded.StreamTimeoutSeconds > 0 {
			cfg.StreamTimeoutSeconds = decoded.StreamTimeoutSeconds
		}
		if decoded.ModelCacheSecs > 0 {
			cfg.ModelCacheSecs = decoded.ModelCacheSecs
		}
		if decoded.WorkBotID != "" {
			cfg.WorkBotID = decoded.WorkBotID
		}
		if decoded.WorkModeID != "" {
			cfg.WorkModeID = decoded.WorkModeID
		}
		if decoded.WorkReasoningEffort != "" {
			cfg.WorkReasoningEffort = decoded.WorkReasoningEffort
		}
		if decoded.WorkMemoryProfile != "" {
			cfg.WorkMemoryProfile = decoded.WorkMemoryProfile
		}
		if decoded.WorkTotalWindow != "" {
			cfg.WorkTotalWindow = decoded.WorkTotalWindow
		}
		if decoded.WorkWorkspace != "" {
			cfg.WorkWorkspace = decoded.WorkWorkspace
		}
		// 2. 布尔项无法区分「未配置」与「显式 false」，按配置来源整体覆盖。
		cfg.WorkEnableSandbox = decoded.WorkEnableSandbox
	}

	// 3. 整体替换运行期配置，热更新立即对后续请求生效。
	activeConfig.Store(cfg)
	return nil
}

// okEnvelope 包装成功信封。
// [参数] v: 任意可序列化结果。
// [返回] 信封 JSON 与序列化错误。
// 最近修改时间：2026-10-17；改动原因：新增，统一成功响应构造。
func okEnvelope(v any) ([]byte, error) {
	result, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: result})
}

// errorEnvelope 构造错误信封（不返回 error，供分发表直接回写）。
// [参数] code: 错误码；message: 错误说明。
// [返回] 信封 JSON。
// 最近修改时间：2026-10-17；改动原因：新增，统一错误响应构造。
func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

// writeResponse 把响应字节写入宿主提供的缓冲区。
// [参数] response: 宿主缓冲区指针；raw: 响应字节。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，C ABI 回写出口。
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
