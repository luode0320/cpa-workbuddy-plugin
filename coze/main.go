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
	"bytes"
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
	providerName  = "coze-provider"
	pluginLogoURL = "https://raw.githubusercontent.com/luode0320/cpa-workbuddy-plugin/main/assets/icons/Coze.png"
)

var (
	// version 当前插件版本号。
	version      = "0.7.0"
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
	// RenewIntervalSeconds 会话续期巡检间隔（秒）。
	RenewIntervalSeconds int `yaml:"renew_interval_seconds" json:"renew_interval_seconds"`
	// MinRoutableCredits 是积分路由红线，低于该值的账号不再承载新请求。
	//
	// 用指针区分「未配置」与「显式配 0」：0 是关闭闸门的 kill switch，
	// 若用值类型，缺省键会把红线一起归零，闸门会静默失效。
	MinRoutableCredits *int64 `yaml:"min_routable_credits,omitempty" json:"min_routable_credits,omitempty"`
	// CreditRefreshSeconds 是后台积分刷新间隔（秒）。
	CreditRefreshSeconds int `yaml:"credit_refresh_interval_seconds" json:"credit_refresh_interval_seconds"`
	// CPAManagementURL / CPAManagementKey 用于调用宿主管理 API，把禁用状态
	// 写进宿主路由真相源（MySQL 存储下直写物理文件会被 persist 覆盖）。
	CPAManagementURL string `yaml:"cpa_management_url,omitempty" json:"cpa_management_url,omitempty"`
	CPAManagementKey string `yaml:"cpa_management_key,omitempty" json:"cpa_management_key,omitempty"`
}

// defaultConfig 返回插件默认配置。
// [参数] 无。
// [返回] 带默认值的配置结构。
// 最近修改时间：2026-10-20；改动原因：新增积分路由红线与刷新间隔默认值。
func defaultConfig() Config {
	redline := defaultMinRoutableCredits
	return Config{
		TimeoutSeconds:       30,
		RenewIntervalSeconds: 4 * 3600,
		MinRoutableCredits:   &redline,
		CreditRefreshSeconds: creditRefreshDefaultSeconds,
	}
}

// currentConfig 读取当前生效配置，未初始化时回退默认值。
// [参数] 无。
// [返回] 当前配置。
// 最近修改时间：2026-10-18；改动原因：新增，配置读取入口。
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

	// 续期定时器必须在宿主装配完成后启动：init() 早于 cliproxy_plugin_init，
	// 此刻 hostAPI 仍为 nil，循环会先等宿主就绪再跑首轮（见 renew_loop.go）。
	startRenewLoop()
	// 积分巡检同理：执行入口的积分闸门只读缓存，
	// 没有后台巡检时缓存会一直为空，闸门等于不存在。
	startCreditSweep()
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
		writeExecutionError(response, err)
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
	stopRenewLoop()
	stopCreditSweep()
	hostAPIMu.Lock()
	hostAPI = nil
	hostAPIMu.Unlock()
}

// hostCall 通过宿主导出的 C 函数指针表发起一次 RPC 调用。
// [参数] method: 宿主方法名；request: JSON 请求体，可为空。
// [返回] 宿主返回的原始字节与错误。
// 最近修改时间：2026-10-18；改动原因：新增，宿主 RPC 桥入口。
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
	// HTTPStatus 是宿主判定账号故障与请求故障的唯一依据。
	//
	// 宿主的 decodeEnvelopeResult 直接把它塞进 rpcError.statusCode，
	// statusCodeFromError 再据此查白名单；缺失时取到的是 0，不是 5xx，
	// 结果既不会冷却账号也不会进入换号重试轮（见 faults.go 头部说明）。
	// Retryable 只是提示位，宿主不据此改变路由行为。
	Retryable  bool `json:"retryable,omitempty"`
	HTTPStatus int  `json:"http_status,omitempty"`
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

// cozeRegistration 返回插件注册元数据与能力声明。
//
// 声明 model_provider 以对外暴露扣子模型目录（对话 / 生图 / 生视频等），
// 并声明 executor 以承接推理请求（CYCLE-01 已打通真流式）。
// 模型目录与推理能力是两件事：前者只影响模型选择器，后者才决定请求去向。
// [参数] 无。
// [返回] 注册结构。
// 最近修改时间：2026-10-18；改动原因：CYCLE-01 打通对话链路，开启 executor 能力。
func cozeRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             providerName,
			Version:          version,
			Author:           "Sliverkiss (cpa-workbuddy-plugin)",
			GitHubRepository: "https://github.com/luode0320/cpa-workbuddy-plugin",
			Logo:             pluginLogoURL,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "上游请求超时（秒）。"},
				{Name: "renew_interval_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "会话续期巡检间隔（秒）。"},
				{Name: "credit_refresh_interval_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "积分巡检间隔（秒），默认 3600。巡检同时完成当日签到领取、积分落盘与低额分级。"},
				{Name: "min_routable_credits", Type: pluginapi.ConfigFieldTypeInteger, Description: "积分红线，默认 200 分：剩余积分低于该值的账号不承载新请求，并被自动写盘禁用；积分回到线上后自动解禁。填 0 关闭整个机制。"},
				{Name: "cpa_management_url", Type: pluginapi.ConfigFieldTypeString, Description: "可选：CPA 宿主管理 API 地址，默认 http://127.0.0.1:8317。仅当宿主管理端口非默认时才需要填。"},
				{Name: "cpa_management_key", Type: pluginapi.ConfigFieldTypeString, Description: "可选：CPA 宿主管理密钥。填写后，低额自动禁用/解禁会同时调用宿主 auth-files/status 接口写入路由真相源。留空则只做物理文件写盘（旧行为）。也可用环境变量 WB_CPA_MANAGEMENT_KEY。"},
			},
		},
		Capabilities: registrationCapability{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeBoth,
			ExecutorInputFormats:  []string{"openai", "openai-image"},
			ExecutorOutputFormats: []string{"openai", "openai-image"},
			Scheduler:             false,
			ManagementAPI:         true,
			UsagePlugin:           false,
		},
	}
}

// handleMethod 按宿主方法名分发插件能力。
// [参数] method: 宿主方法名；request: 原始 JSON 请求体。
// [返回] 信封 JSON 与错误。
// 最近修改时间：2026-10-18；改动原因：新增，方法分发表（CYCLE-00 仅认证与管理 API）。
func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configurePlugin(request); err != nil {
			return nil, err
		}
		return okEnvelope(cozeRegistration())

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
		return handleExecExecute(request)

	case pluginabi.MethodExecutorExecuteStream:
		return handleExecStream(request)

	case pluginabi.MethodExecutorCountTokens:
		// 扣子没有独立的 token 计数端点；返回 0 让宿主跳过预估，
		// 而不是伪造一个看似合理的数字误导用量统计。
		return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":0}`)})

	case pluginabi.MethodManagementRegister:
		var regReq pluginapi.ManagementRegistrationRequest
		if err := json.Unmarshal(request, &regReq); err == nil && regReq.BasePath != "" {
			setManagementBasePath(regReq.BasePath)
		}
		return okEnvelope(managementRegistration())

	case pluginabi.MethodManagementHandle:
		return handleManagement(request)

	default:
		return errorEnvelope("unknown_method", "unknown_method: "+method), nil
	}
}

// configurePlugin 解析宿主下发的 YAML 配置并更新运行期配置。
// [参数] raw: 宿主 reconfigure 载荷，config_yaml 为 base64 解码后的 YAML 字节。
// [返回] 解析错误。
// 最近修改时间：2026-10-18；改动原因：新增，配置热更新入口。
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
		if decoded.TimeoutSeconds > 0 {
			cfg.TimeoutSeconds = decoded.TimeoutSeconds
		}
		if decoded.RenewIntervalSeconds > 0 {
			cfg.RenewIntervalSeconds = decoded.RenewIntervalSeconds
		}
		// 只有显式出现该键才覆盖：缺省键保持默认红线，
		// 显式 0 表示关闭积分闸门。
		if decoded.MinRoutableCredits != nil {
			cfg.MinRoutableCredits = decoded.MinRoutableCredits
		}
		if decoded.CreditRefreshSeconds > 0 {
			cfg.CreditRefreshSeconds = decoded.CreditRefreshSeconds
		}
		// cpa_management_* 是字符串配置，空串即「未配置」，直接照抄；
		// 漏抄这两个字段会让管理 API 通道永远拿不到密钥（0.6.3 首版实测）。
		cfg.CPAManagementURL = decoded.CPAManagementURL
		cfg.CPAManagementKey = decoded.CPAManagementKey
	}
	activeConfig.Store(cfg)
	applyCreditConfig(cfg)
	// cpa_management_*: 宿主管理 API 地址与密钥。密钥缺失时通道不启用，
	// 禁用/恢复继续走写盘兜底（行为与旧版一致）。
	prevConfigured := cpaDisableSyncConfigured()
	prevURL, _ := loadedCPAManagement()
	setCPAManagement(cfg.CPAManagementURL, cpaManagementSecretResolver(cfg.CPAManagementKey))
	if nowConfigured := cpaDisableSyncConfigured(); nowConfigured != prevConfigured {
		// 只打印是否启用，绝不打印密钥本身。
		state := "disabled"
		if nowConfigured {
			state = "enabled"
		}
		curURL, _ := loadedCPAManagement()
		hostLogf("info", "cpa management sync "+state, map[string]any{"url": curURL})
	} else if nowConfigured {
		if curURL, _ := loadedCPAManagement(); curURL != prevURL {
			hostLogf("info", "cpa management sync url changed", map[string]any{"url": curURL})
		}
	}
	// 间隔可能在本次 reconfigure 里被改小：让循环立即重排，避免旧 timer
	// 继续按旧间隔睡满一整轮。
	requestRenewTick()
	requestCreditTick()
	return nil
}

// applyCreditConfig 把配置里的积分策略落到运行时状态。
// [参数] cfg: 已解析的配置。
// [返回] 无。
func applyCreditConfig(cfg Config) {
	if cfg.MinRoutableCredits != nil {
		applyMinRoutableCredits(*cfg.MinRoutableCredits)
	}
}

// okEnvelope 包装成功信封。
//
// 不用 json.Marshal：它默认开启 HTML 转义，会把脚本里的 `>`、`'`、`&`
// 变成 `&gt;` / `&#39;` / `&amp;`，面板复制到的脚本将无法执行。
// [参数] v: 任意可序列化结果。
// [返回] 信封 JSON 与序列化错误。
// 最近修改时间：2026-10-18；改动原因：关闭 HTML 转义，避免授权脚本被实体化。
func okEnvelope(v any) ([]byte, error) {
	result, err := marshalNoHTMLEscape(v)
	if err != nil {
		return nil, err
	}
	return marshalNoHTMLEscape(envelope{OK: true, Result: result})
}

// marshalNoHTMLEscape 序列化且不做 HTML 转义。
// [参数] v: 任意可序列化值。
// [返回] JSON 字节与错误。
// 最近修改时间：2026-10-18；改动原因：新增，关闭 json.Marshal 的默认实体转义。
func marshalNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// errorEnvelope 构造错误信封（不返回 error，供分发表直接回写）。
// [参数] code: 错误码；message: 错误说明。
// [返回] 信封 JSON。
// 最近修改时间：2026-10-18；改动原因：新增，统一错误响应构造。
func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

// faultEnvelope 构造带状态码的失败信封。
//
// 宿主只按 http_status 区分「账号故障」与「请求故障」：
// 缺失时 statusCodeFromError 取到 0，既跳过账号冷却也跳过换号重试轮，
// 坏号会被后续请求反复挑中（2026-10-19 生产实证）。
// [参数] status: HTTP 状态码；code: 错误码；message: 错误说明。
// [返回] 信封 JSON。
// 最近修改时间：2026-10-19；改动原因：新增，状态码统一出口。
func faultEnvelope(status int, code, message string) []byte {
	raw, errMarshal := json.Marshal(envelope{OK: false, Error: &envelopeError{
		Code:       code,
		Message:    message,
		HTTPStatus: status,
	}})
	if errMarshal != nil {
		return errorEnvelope(code, message)
	}
	return raw
}

// writeExecutionError 按错误类型回写执行器失败信封。
//
// 每个失败都必须带状态码：请求级故障（4xx）让宿主直接返回用户，
// 账号级故障（402/5xx）让宿主冷却该号并换号重试。
// [参数] response: 宿主缓冲区指针；err: 执行错误。
// [返回] 无。
// 最近修改时间：2026-10-19；改动原因：兜底补状态码，修复坏号不被换下。
func writeExecutionError(response *C.cliproxy_buffer, err error) {
	writeResponse(response, executionFaultEnvelope(err))
}

// writeResponse 把响应字节写入宿主提供的缓冲区。
// [参数] response: 宿主缓冲区指针；raw: 响应字节。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，C ABI 回写出口。
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
