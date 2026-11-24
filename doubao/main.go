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
	version      = "0.1.39"
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
	// VideoPollTimeoutSeconds 确认轮等待视频产物的总时长（秒）。
	//
	// 只作用于视频回灌，与上面两个对话超时互不影响：视频是异步产物，出片要
	// 1~5 分钟，用对话超时去卡它只会把正常的片子卡死。
	VideoPollTimeoutSeconds int `yaml:"video_poll_timeout_seconds" json:"video_poll_timeout_seconds"`
	// VideoCreationWaitSeconds 等待上游落下第一个创作块的上限（秒）。
	//
	// 同样是视频专属。上游拒答时照样返回 conversation_id，形态与正常首轮一样，
	// 只能靠「消息链里到底有没有 2074 块」来区分「在渲染」和「根本没开始」。
	VideoCreationWaitSeconds int `yaml:"video_creation_wait_seconds" json:"video_creation_wait_seconds"`
	// KeepaliveEnabled 是否在空闲期定时刷新 msToken。
	//
	// 默认开启：msToken 只有「上游响应头」这一个来源，不主动发请求就永远拿不到
	// 新值。保活只是一次只读偏好查询，不消耗对话额度，代价远低于「账号闲置到
	// 令牌状态不明」。确实不想产生任何额外流量的实例可以显式关掉。
	KeepaliveEnabled bool `yaml:"keepalive_enabled" json:"keepalive_enabled"`
	// KeepaliveIntervalSeconds 保活间隔（秒）。
	//
	// 只在 KeepaliveEnabled 为真时生效；<=0 时回落到默认 30 分钟。
	KeepaliveIntervalSeconds int `yaml:"keepalive_interval_seconds" json:"keepalive_interval_seconds"`
	// AccountMaxInflight 同一账号的最大在途上游请求数。
	//
	// 上游按「同一账号 + 同一设备」判访问频率，并发 2 以上会互相干扰。
	// <=0 时回落到默认 1。
	AccountMaxInflight int `yaml:"account_max_inflight" json:"account_max_inflight"`
	// AccountMinIntervalMs 同一账号两次上游发起的最小间隔（毫秒）。
	//
	// 与在途上限配合：上限管「同时几个」，本项管「串行时也不能挨太近」。
	// <0 时按 0 处理（不限制间隔），未配置时回落默认 2000。
	AccountMinIntervalMs int `yaml:"account_min_interval_ms" json:"account_min_interval_ms"`
	// RateLimitBackoffSeconds 收到「当前服务访问频繁」后该账号的软退避时长（秒）。
	//
	// 独立于 failover 的阶梯冷却：频率判定是可自愈的短时现象，用固定窗口比
	// 60s×次数封顶 10 分钟的阶梯更准。<=0 时回落到默认 60。
	RateLimitBackoffSeconds int `yaml:"rate_limit_backoff_seconds" json:"rate_limit_backoff_seconds"`
}

// defaultConfig 返回插件默认配置。
// [参数] 无。
// [返回] 带默认值的配置结构。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增账号闸门三项，默认 1 / 2000ms / 60s。
func defaultConfig() Config {
	return Config{
		TimeoutSeconds:           120,
		StreamTimeoutSeconds:     600,
		ModelCacheSecs:           300,
		VideoPollTimeoutSeconds:  360,
		VideoCreationWaitSeconds: 120,
		KeepaliveEnabled:         true,
		KeepaliveIntervalSeconds: defaultKeepaliveIntervalSeconds,
		AccountMaxInflight:       defaultAccountMaxInflight,
		AccountMinIntervalMs:     defaultAccountMinIntervalMs,
		RateLimitBackoffSeconds:  defaultRateLimitBackoffSeconds,
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
	// 1. 先停保活协程：它每轮都会读宿主 API 与账号目录，宿主已经在卸载插件，
	//    让它在释放后继续跑只会拿到「宿主 API 不可用」并刷日志。
	stopKeepalive()

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
// 最近修改时间：2026-11-22 00:20:00；改动原因：补齐视频等待与空闲保活四项配置声明，此前只在插件内生效、面板看不到。
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
				{Name: "video_poll_timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "确认轮等待视频产物的总时长（秒），只作用于视频回灌。"},
				{Name: "video_creation_wait_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "等待上游落下第一个创作块的上限（秒），超时即判定未开始渲染。"},
				{Name: "keepalive_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "是否在空闲期定时刷新 msToken。"},
				{Name: "keepalive_interval_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "空闲保活间隔（秒），默认 1800。"},
				{Name: "account_max_inflight", Type: pluginapi.ConfigFieldTypeInteger, Description: "同一账号的最大在途上游请求数，默认 1。"},
				{Name: "account_min_interval_ms", Type: pluginapi.ConfigFieldTypeInteger, Description: "同一账号两次上游发起的最小间隔（毫秒），默认 2000。"},
				{Name: "rate_limit_backoff_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "被判访问频繁后该账号的软退避时长（秒），默认 60。"},
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

// configOverride 是宿主下发的 YAML 解码中间结构。
//
// 为什么不直接用 Config 解码：keepalive_enabled 的默认值是 true，而 Go 的
// bool 零值是 false，直接用 Config 会把「配置里没写这个键」误判成「用户显式
// 关掉」，一 reconfigure 就把默认值冲掉。指针才能区分「没写」与「写了 false」。
// 数值项仍用值类型：它们的零值本来就代表「没写」，由默认值兜底。
type configOverride struct {
	TimeoutSeconds           int   `yaml:"timeout_seconds"`
	StreamTimeoutSeconds     int   `yaml:"stream_timeout_seconds"`
	ModelCacheSecs           int   `yaml:"model_cache_seconds"`
	VideoPollTimeoutSeconds  int   `yaml:"video_poll_timeout_seconds"`
	VideoCreationWaitSeconds int   `yaml:"video_creation_wait_seconds"`
	KeepaliveEnabled         *bool `yaml:"keepalive_enabled"`
	KeepaliveIntervalSeconds int   `yaml:"keepalive_interval_seconds"`
	AccountMaxInflight       int   `yaml:"account_max_inflight"`
	AccountMinIntervalMs     int   `yaml:"account_min_interval_ms"`
	RateLimitBackoffSeconds  int   `yaml:"rate_limit_backoff_seconds"`
}

// configurePlugin 解析宿主下发的 YAML 配置并更新运行期配置。
//
// 数值项按「>0 才覆盖」处理，保证缺省键保留默认值，无关项热更新不会把超时
// 与缓存窗口冲成 0。未知键（如早期版本的 work_* 系列）由 yaml 宽松忽略。
// 布尔项走指针判空：只有配置里真的写了这个键才覆盖默认值。
// [参数] raw: 宿主 reconfigure 载荷，config_yaml 为 base64 解码后的 YAML 字节。
// [返回] 解析错误。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增账号闸门三项的覆盖逻辑。
func configurePlugin(raw []byte) error {
	var req struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}

	cfg := defaultConfig()
	if len(req.ConfigYAML) > 0 {
		var decoded configOverride
		if err := yaml.Unmarshal(req.ConfigYAML, &decoded); err != nil {
			return err
		}
		// 1. 只有给了才覆盖，缺省键保留默认值。
		if decoded.TimeoutSeconds > 0 {
			cfg.TimeoutSeconds = decoded.TimeoutSeconds
		}
		if decoded.StreamTimeoutSeconds > 0 {
			cfg.StreamTimeoutSeconds = decoded.StreamTimeoutSeconds
		}
		if decoded.ModelCacheSecs > 0 {
			cfg.ModelCacheSecs = decoded.ModelCacheSecs
		}
		if decoded.VideoPollTimeoutSeconds > 0 {
			cfg.VideoPollTimeoutSeconds = decoded.VideoPollTimeoutSeconds
		}
		if decoded.VideoCreationWaitSeconds > 0 {
			cfg.VideoCreationWaitSeconds = decoded.VideoCreationWaitSeconds
		}
		if decoded.KeepaliveIntervalSeconds > 0 {
			cfg.KeepaliveIntervalSeconds = decoded.KeepaliveIntervalSeconds
		}
		if decoded.KeepaliveEnabled != nil {
			cfg.KeepaliveEnabled = *decoded.KeepaliveEnabled
		}
		if decoded.AccountMaxInflight > 0 {
			cfg.AccountMaxInflight = decoded.AccountMaxInflight
		}
		if decoded.AccountMinIntervalMs > 0 {
			cfg.AccountMinIntervalMs = decoded.AccountMinIntervalMs
		}
		if decoded.RateLimitBackoffSeconds > 0 {
			cfg.RateLimitBackoffSeconds = decoded.RateLimitBackoffSeconds
		}
	}

	// 2. 整体替换运行期配置，热更新立即对后续请求生效。
	activeConfig.Store(cfg)

	// 3. 配置落地之后再重建定时器：先启后配会让新协程读到旧间隔。
	restartKeepalive(cfg)
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
