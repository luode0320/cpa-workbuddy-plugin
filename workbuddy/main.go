// Package main implements the workbuddy CLIProxyAPI dynamic plugin.
//
// workbuddy wraps Tencent CodeBuddy (copilot.tencent.com) as a cliproxy
// provider: it performs the CodeBuddy web login flow, refreshes access
// tokens, and forwards OpenAI-compatible chat completion requests to the
// upstream /v2/chat/completions endpoint.
//
// This file is a clean-room reimplementation reconstructed from the public
// workbuddy.so binary (symbol table, string constants and RPC shape) published
// by Sliverkiss. Original credit for the workbuddy plugin goes to Sliverkiss;
// see https://github.com/Sliverkiss/cpa-plugin. Built with -buildmode=c-shared
// and exports the cliproxy C ABI entry points.
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

// Wrappers so Go can invoke the host function-pointer table via cgo. The host
// API captured at init is used to push streaming chunks back asynchronously.
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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	providerName  = "workbuddy-provider"
	authFileName  = "workbuddy.json"
	pluginLogoURL = "https://raw.githubusercontent.com/DGZSbot/ai-icon/refs/heads/main/WorkBuddy.png"
	// CN chat/auth gateway (iss = codebuddy.cn realm).
	upstreamBaseCN = "https://copilot.tencent.com"
	// Global chat/auth gateway (iss = workbuddy.ai realm). APISIX on
	// copilot.tencent.com rejects Global JWTs with 401; must use workbuddy.ai.
	upstreamBaseGlobal  = "https://www.workbuddy.ai"
	originReferer       = "https://www.codebuddy.cn"
	originRefererGlobal = "https://www.workbuddy.ai"

	// ideNameHeaderValue 是 X-IDE-Name 的取值，同时也是 User-Agent 的品牌前缀
	// （上游形态为 `${X-IDE-Name}/${版本}`，见 client_version.go）。
	ideNameHeaderValue = "WorkBuddy"

	// CN endpoint aliases (login / chat / models). upstreamBaseCN is the only
	// CN base; Global has its own upstreamBaseGlobal. No "upstreamBase" legacy
	// alias — removed in v0.6.31 dead-code sweep.
	endpointAuthState    = upstreamBaseCN + "/v2/plugin/auth/state?platform=CLI"
	endpointLoginAcct    = upstreamBaseCN + "/v2/plugin/login/account?state="
	endpointAuthToken    = upstreamBaseCN + "/v2/plugin/auth/token?state="
	endpointTokenRefresh = upstreamBaseCN + "/v2/plugin/auth/token/refresh"
	endpointChat         = upstreamBaseCN + "/v2/chat/completions"
	endpointModels       = upstreamBaseCN + "/console/enterprises/personal/models"

	loginTTL = 5 * time.Minute
)

// loginCtx holds the cookie-affined HTTP client for one in-flight login flow.
// CodeBuddy associates the browser login with the state issued at auth/state,
// so we must reuse the same cookie jar across the state request and the polls.
type loginCtx struct {
	client  *http.Client
	expires time.Time
}

var (
	hostAPI        *C.cliproxy_host_api // captured at init, used for async host calls
	loginStates    sync.Map             // state(string) -> *loginCtx
	httpClientOnce sync.Once
	sharedClient   *http.Client
)

// loginStatesPruneInterval bounds how often the janitor sweeps abandoned
// login states (user started a login but never finished).
const loginStatesPruneInterval = time.Minute

func init() {
	// Account watchdog: every interval (default 10m) it refreshes the credits
	// snapshot for every workbuddy account and fires the scheduled active ping
	// (the 「hi」 reasoning request) that keeps accounts warm and writes/clears
	// the 「测试」tag. Started here so the first tick fires immediately on
	// plugin init instead of waiting a full interval. The loop is started here
	// (not in a goroutine-closing shutdown path) to avoid the SIGSEGV risk
	// documented in checkin.go:31 from closing stop channels during plugin
	// teardown.
	go watchdogLoop()
	// 积分扫描：每小时串行刷新全部账号（不区分启用/禁用），写 sidecar 后
	// 逐个跑三档分级检查。与 watchdog 分工：watchdog 管探活，本循环管积分。
	// 首轮带抖动，避免六个插件同刻对齐形成整点尖峰。
	go creditSweepLoop()
	// 禁用探活：每 10 分钟串行遍历被本机制禁用的账号，回放失败模型，成功即解除禁用。
	// 冷却本身不再探测（cooldown_probe.go 已移除），见 failover_disable.go。
	go failoverProbeLoop()
	// 账号池规模刷新：慢速冷却的准入闸门需要「启用 + 停用」的账号总数，
	// 而账号枚举是宿主 RPC（跨进程）。放到独立循环里周期探测，判定入口
	// 只读内存缓存，避免每次成功请求都付一次 RPC 往返。
	go slowPoolSizeLoop()
	go func() {
		ticker := time.NewTicker(loginStatesPruneInterval)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			loginStates.Range(func(key, value any) bool {
				if lc, ok := value.(*loginCtx); ok && now.After(lc.expires) {
					loginStates.Delete(key)
				}
				return true
			})
		}
	}()
}

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
	// Intentionally a no-op. The host calls this on its own exit path (after
	// the host Go runtime has started tearing down) and dlclose()es this
	// library immediately afterwards. Touching any Go runtime state here —
	// mutexes, channel close, goroutine synchronization — risks a SIGSEGV in
	// cgo (observed on every docker restart: SIGSEGV in
	// _Cfunc_cliproxy_shutdown_plugin, PC near a freed runtime pointer).
	// The scheduler goroutine and janitor ticker hold no resources that
	// outlive the process; the OS reclaims them on exit.
}

// -----------------------------------------------------------------------------
// Host calls (async streaming + auth callbacks)
// -----------------------------------------------------------------------------

// hostCall invokes a host RPC method via the function-pointer table captured
// at init. Used to push stream chunks back asynchronously (host.stream.emit /
// host.stream.close) and to read the host's auth store (host.auth.list/get).
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
		return handleStartLogin(request)
	case pluginabi.MethodAuthLoginPoll:
		return handlePollLogin(request)
	case pluginabi.MethodAuthRefresh:
		return handleRefreshAuth(request)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerName})
	case pluginabi.MethodExecutorExecute:
		return handleExecExecute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return handleExecStream(request)
	case pluginabi.MethodExecutorCountTokens:
		// Upstream CodeBuddy has no dedicated count_tokens API. Return
		// unhandled-style zero estimate so clients fall back / skip.
		return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":0}`)})
	case pluginabi.MethodManagementRegister:
		// Cache host-injected BasePath so handleManagement doesn't hardcode
		// /v0/management (v0.6.31: tolerate future host path changes).
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
// Registration & models
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
var version = "0.21.4"

func wbRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             providerName,
			Version:          version,
			Author:           "Sliverkiss (based on workbuddy by lovingfish)",
			GitHubRepository: "https://github.com/luode0320/cpa-workbuddy-plugin",
			Logo:             pluginLogoURL,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "checkin_auto", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用自动签到（每4小时一次，本地时间 00:00/04:00/08:00/12:00/16:00/20:00，CN 账号，默认开启）。"},
				{Name: "growth_auto", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用成长中心自动执行（随 4 小时 tick 运行：Buddy 领养、旅行、任务接单与领奖、补登、兑换、抽奖、盲盒；CN 账号，默认开启）。关闭后仅保留面板手动执行。"},
				{Name: "lifecycle_auto", Type: pluginapi.ConfigFieldTypeBoolean, Description: "额度耗尽时自动禁用账号（CN 与 Global 一致，只写盘禁用并保留凭据）；额度恢复后自动重新启用（默认关闭）。"},
				{Name: "token_keepalive", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用每4小时 access-token 自动刷新，防止 Keycloak 离线会话过期（默认开启）。"},
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "可选模型列表。每个条目可包含 id、name、alias、context、max_tokens、enabled、reasoning 字段；配置后优先于自动获取的模型列表。"},
				{Name: "scheduler_mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{schedulerModeSession, schedulerModeCredits, schedulerModeOff}, Description: "多账号选择策略：session（按会话轮询，同一会话 1 小时内固定同一账号；默认）／credits（面板指定的固定账号）／off（交给内置逻辑）。注意：off 且 lifecycle_auto=false 时，额度耗尽的账号仍可能被路由——请启用 lifecycle_auto 或保持 session/credits。"},
				{Name: "min_routable_credits", Type: pluginapi.ConfigFieldTypeInteger, Description: "保活线，默认 400 点：剩余积分低于该值但高于禁用线的账号打「保活」标，仍可路由但排在正常号之后。填 0 关闭保活降级。"},
				{Name: "low_credit_disable_below", Type: pluginapi.ConfigFieldTypeInteger, Description: "硬禁用线，默认 100 点：每小时扫描或手动刷新发现剩余积分低于该值时，直接写盘停用该账号；积分回到线上且探活成功才自动解禁。填 0 关闭该机制。"},
				{Name: "rotation_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用连续使用轮换（默认开启）：单个账号连续承流达到 rotation_serve_minutes 后主动让位，进入 rotation_cooldown_minutes 冷却，后续请求改由其它账号承接。与 priority 不同，该机制在请求入口换号，已绑定会话同样生效；关闭填 false。"},
				{Name: "rotation_serve_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "单账号连续承流多少分钟后让位（默认 60）。仅在 rotation_enabled 开启时生效；填 0 等同关闭轮换。"},
				{Name: "rotation_cooldown_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "轮换让位后停留在冷却的时长（默认 60）。仅在 rotation_enabled 开启时生效；填 0 等同关闭轮换。"},
				{Name: "rotation_idle_reset_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "连续使用的中断阈值（默认 10）：账号距上次使用超过该时长即视为新一轮，重新计时，避免零散流量被误判为持续负载。"},
				{Name: "slow_account_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用慢速账号冷却（默认开启）：某账号在某个模型上的首字延迟中位数显著高于同池同模型水平时，在该模型上进入冷却并让出流量。慢是性能问题不是故障，只走内存冷却，到期自动恢复，不写盘禁用。"},
				{Name: "slow_ttft_ratio_x10", Type: pluginapi.ConfigFieldTypeInteger, Description: "慢速判定倍数阈值 ×10（默认 25 即 2.5 倍）：账号首字延迟中位数达到同模型全池中位数的该倍数即判慢。必须大于 10。"},
				{Name: "slow_window_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "首字延迟样本统计窗口（默认 15 分钟）：只统计窗口内的成功请求样本。"},
				{Name: "slow_min_samples", Type: pluginapi.ConfigFieldTypeInteger, Description: "参与慢速判定所需的最小样本数（默认 5）：样本不足时不判定，避免单条毛刺误伤。"},
				{Name: "slow_cooldown_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "慢速冷却时长（默认 10 分钟）：到期自动恢复，无需探活。"},
				{Name: "slow_min_ttft_ms", Type: pluginapi.ConfigFieldTypeInteger, Description: "慢速判定的绝对下限毫秒（默认 3000）：低于该值一律不判慢，避免全池都很快时被相对倍数放大误判。填 0 表示不设下限。"},
				{Name: "slow_min_pool_size", Type: pluginapi.ConfigFieldTypeInteger, Description: "账号池准入下限（默认 20）：账号总数（启用 + 停用）须严格大于该值才触发慢速判定。池子太小时下四分位基线退化，两个账号里任一次抖动就会让其中一个被判慢、互相顶替。"},
				{Name: "client_version", Type: pluginapi.ConfigFieldTypeString, Description: "可选：钉死 User-Agent 里上报给上游的产品版本号。留空时每天自动从官方发布源拉取最新版本；填如 2.174.0 则固定使用该值、不再自动更新。注意 X-IDE-Version 头固定为 0.0.0，不受此项影响。"},
				{Name: "usage_report_url", Type: pluginapi.ConfigFieldTypeString, Description: "可选：覆盖 CPAMP 用量上报地址（默认 http://cpa-manager-plus:18317/v0/management/usage/import；也可用环境变量 USAGE_REPORT_URL）。"},
				{Name: "usage_report_key", Type: pluginapi.ConfigFieldTypeString, Description: "可选：覆盖 CPAMP 管理密钥。优先从环境变量 CPAMP_ADMIN_KEY / USAGE_REPORT_KEY 或密钥文件 /run/secrets/cpamp_admin_key 自动探测。"},
				{Name: "usage_feed_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "将每次请求的 token 用量追加写入共享 NDJSON 数据流，供 token-usage-tracker 插件消费（默认开启）。"},
				{Name: "usage_feed_path", Type: pluginapi.ConfigFieldTypeString, Description: "可选：共享用量数据流路径（默认 <CLIProxyAPI 根目录>/data/token-usage-feed.ndjson）。必须与 token-usage-tracker 的 usage_feed_path 保持一致。"},
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

// dynamicModelsCacheTTL bounds how long a fetched model list is reused.
// model.static / model.for_auth are re-invoked by CPA on every config reload
// and on each models query; without caching, every reload fans out to one
// upstream call per account.
const dynamicModelsCacheTTL = 5 * time.Minute

// dynamicModelsFailCacheTTL 是模型发现「全部账号尝试失败」后的负缓存窗口：
// 窗口内动态发现入口直接返回 nil，不再逐账号重扫。上游失败若不设负缓存，
// 会被宿主每次 auth 重写触发的 model.for_auth 放大成 N+1 请求风暴（生产 OOM 主因）。
const dynamicModelsFailCacheTTL = 5 * time.Minute

var dynamicModelsCache struct {
	sync.RWMutex
	models   []pluginapi.ModelInfo
	fetched  time.Time
	failedAt time.Time // 最近一次全量尝试失败的时间；零值表示无失败记录
}

//
// CPA applies oauth-model-alias to the models this plugin registers, so the
// gateway may route a request whose model ID is an alias (e.g.
// "point/deepseek-v4-flash") to this executor. The upstream only knows the
// real model IDs, so the plugin must map the alias back before forwarding.
//
// ExecutorRequest carries no host config, so the alias table is cached from
// the AuthModelRequest.Host summary every time the host asks for models
// (model.static / model.for_auth are re-queried by CPA on config reload,
// keeping this cache in sync with oauth-model-alias changes). Auth-level
// attribute overrides ("model_alias"/"model-alias"/"oauth-model-alias")
// are parsed per request and take precedence over the global table.

var modelAliasCache struct {
	sync.RWMutex
	byAlias map[string]string
}

// ------------------------------------------------------------------------------
// Usage reporting (request monitoring)
// ------------------------------------------------------------------------------
//
// CPA built-in executors publish via host usage.DefaultManager → redisqueue.
// Plugin executors cannot: c-shared .so has its own Go runtime, so
// usage.PublishRecord would hit a separate empty DefaultManager (no sink).
//
// Only effective path: POST NDJSON to CPA-Manager-Plus
// /v0/management/usage/import. Key/URL resolved automatically from
// config → env → docker secret files (see resolveUsageReport).
// usage.Detail is still used as a pure token-counter struct.

// storedAuth is the on-disk shape of a workbuddy credential.
type storedAuth struct {
	Auth    storedTokens  `json:"auth"`
	Account storedAccount `json:"account"`
}

type storedTokens struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"`
	Domain       string `json:"domain"`
}

type storedAccount struct {
	UID          string `json:"uid"`
	EnterpriseID string `json:"enterpriseId"`
	Nickname     string `json:"nickname"`
}

// apiEnvelope is the generic {code,msg,data} wrapper used by every CodeBuddy API.
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type tokenData struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	ExpiresIn        int64  `json:"expiresIn"`
	RefreshExpiresIn int64  `json:"refreshExpiresIn"`
	Domain           string `json:"domain"`
}

type accountData struct {
	UID          string `json:"uid"`
	EnterpriseID string `json:"enterpriseId"`
	Nickname     string `json:"nickname"`
}

type authStateData struct {
	State   string `json:"state"`
	AuthURL string `json:"authUrl"`
}

func parseStored(raw []byte) (*storedAuth, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty auth storage")
	}
	// Accept both shapes seen in the wild:
	//   nested: {"auth":{"accessToken":...},"account":{"uid":...}} (plugin/oauth output)
	//   flat:   {"accessToken":...,"uid":...,"nickname":...} (CPA-Manager-Plus auths/workbuddy.json)
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("storage_parse_error: %w", err)
	}
	var sa storedAuth
	if _, nested := probe["auth"]; nested {
		if err := json.Unmarshal(raw, &sa); err != nil {
			return nil, fmt.Errorf("storage_parse_error: %w", err)
		}
	} else {
		var flat struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    int64  `json:"expiresAt"`
			Domain       string `json:"domain"`
			UID          string `json:"uid"`
			EnterpriseID string `json:"enterpriseId"`
			Nickname     string `json:"nickname"`
		}
		if err := json.Unmarshal(raw, &flat); err != nil {
			return nil, fmt.Errorf("storage_parse_error: %w", err)
		}
		sa.Auth = storedTokens{AccessToken: flat.AccessToken, RefreshToken: flat.RefreshToken, ExpiresAt: flat.ExpiresAt, Domain: flat.Domain}
		sa.Account = storedAccount{UID: flat.UID, EnterpriseID: flat.EnterpriseID, Nickname: flat.Nickname}
	}
	if sa.Auth.AccessToken == "" {
		return nil, fmt.Errorf("parse_error: missing accessToken")
	}
	return &sa, nil
}

// -------------------------------------------------------------------------------
// HTTP plumbing
// -------------------------------------------------------------------------------

// commonHeaders 填充所有上游请求的通用头。
//
// User-Agent 使用每日更新的产品版本号；X-IDE-Version 是另一套语义，固定发
// 0.0.0（独立客户端没有宿主 IDE 版本），详见 client_version.go。
//
// [参数] req：待填充的 HTTP 请求。
// [返回] 无。
// 最近修改时间：2026-10-24；改动原因：UA 改为上游真实形态 `WorkBuddy/<版本>`，并补 X-IDE-Version。
func commonHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", originReferer)
	req.Header.Set("Referer", originReferer+"/")
	req.Header.Set("User-Agent", currentUserAgent())
	req.Header.Set("X-IDE-Version", currentIDEVersionHeader())
}

// originRefererFor returns the Origin/Referer base URL appropriate for the
// account's domain. Global accounts use https://www.workbuddy.ai; CN (and
// legacy auth files with empty domain) use the default https://www.codebuddy.cn.
func originRefererFor(sa *storedAuth) string {
	if sa != nil && isGlobalDomain(sa.Auth.Domain) {
		return originRefererGlobal
	}
	return originReferer
}

// upstreamBaseFor returns the chat/auth API host for the account realm.
// Global JWT iss is workbuddy.ai — those tokens only work on www.workbuddy.ai.
// CN tokens work on copilot.tencent.com. Mixing them yields APISIX 401.
func upstreamBaseFor(sa *storedAuth) string {
	if sa != nil && isGlobalDomain(sa.Auth.Domain) {
		return upstreamBaseGlobal
	}
	return upstreamBaseCN
}

func endpointChatFor(sa *storedAuth) string {
	return upstreamBaseFor(sa) + "/v2/chat/completions"
}

func endpointTokenRefreshFor(sa *storedAuth) string {
	return upstreamBaseFor(sa) + "/v2/plugin/auth/token/refresh"
}

func endpointModelsFor(sa *storedAuth) string {
	return upstreamBaseFor(sa) + "/console/enterprises/personal/models"
}

// backendHeaders 为聊天补全请求注入鉴权、使用端标识与来源头，空字段按 CodeBuddy
// 的 X-No-* 约定标记。
// [参数] req: 待填充的 HTTP 请求; sa: 账号凭据对象。
// [返回] 无。
// 最近修改时间：2026-10-11 21:05:00 补使用端标识头，使上游记录 client 字段为 WorkBuddy
func backendHeaders(req *http.Request, sa *storedAuth) {
	commonHeaders(req)
	if sa.Auth.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+sa.Auth.AccessToken)
	} else {
		req.Header.Set("X-No-Authorization", "1")
	}
	if sa.Account.UID != "" {
		req.Header.Set("X-User-Id", sa.Account.UID)
	} else {
		req.Header.Set("X-No-User-Id", "1")
	}
	if sa.Account.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", sa.Account.EnterpriseID)
	} else {
		req.Header.Set("X-No-Enterprise-Id", "1")
	}
	// SECURITY: do NOT send X-Refresh-Token on chat completions. refresh_token is
	// a long-lived credential that can mint new access_tokens; it only belongs on
	// the refresh endpoint (handleRefreshAuth). Sending it to chat upstream leaks
	// the credential into upstream request logs on every chat call.
	if sa.Auth.Domain != "" {
		req.Header.Set("X-Domain", sa.Auth.Domain)
	} else {
		req.Header.Set("X-No-Department-Info", "1")
	}
	req.Header.Set("X-Product", "SaaS")
	// 使用端标识：声明请求来自 WorkBuddy 客户端，供上游记录 client / agentPurpose 字段。
	// X-IDE-Version 已在 commonHeaders 中填充（固定 0.0.0，与 UA 版本号语义不同）。
	req.Header.Set("X-IDE-Type", ideNameHeaderValue)
	req.Header.Set("X-IDE-Name", ideNameHeaderValue)
	req.Header.Set("X-Agent-Purpose", "conversation")
	// Override Origin/Referer for Global accounts so the upstream doesn't
	// reject the request as cross-origin.
	origin := originRefererFor(sa)
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
}

// -----------------------------------------------------------------------------
// Auth handlers
// -----------------------------------------------------------------------------

func handleParseAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	// Ownership check (CPA native contract): the host routes by the file's
	// top-level "type" field (synthesizer/file.go). Files without a type fall
	// back to polling every plugin — first Handled=true wins. Only claim files
	// whose declared type matches us — or, for type-less legacy files, when the
	// host already routed this to us or the filename carries our prefix.
	// Symmetric with the qoderwork plugin's guard (commit 7b776a9).
	var probeType struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(req.RawJSON, &probeType)
	declared := strings.ToLower(strings.TrimSpace(probeType.Type))
	if declared != "" && declared != providerName {
		// Explicitly another provider's file — never claim it.
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	if declared == "" {
		routed := strings.EqualFold(strings.TrimSpace(req.Provider), providerName)
		prefixed := strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.FileName)), providerName+"-")
		if !routed && !prefixed {
			return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
		}
	}
	sa, err := parseStored(req.RawJSON)
	if err != nil {
		// Not a workbuddy credential; let the host try other providers.
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	// CRITICAL: echo back the host-provided FileName AND leave ID empty.
	//
	// CPA uses ID for auth record identity (upsert key). If we set ID=uid
	// while the host's watcher initially registered with ID=filename,
	// upsertAuthRecord can't find the existing record → creates a NEW one
	// → duplicate auth entries (same file, different IDs).
	//
	// By leaving ID empty, CPA falls back to authIDForPath(path) which
	// derives ID from the file path → always matches the watcher's key.
	// FileName is also echoed back to avoid rename-based duplicates.
	ad := toAuthDataOpts(sa, nil, parseDisabledFromAuthJSON(req.RawJSON))
	// 盘上本机制的顶层键必须随这次回传一起交回宿主：宿主吸收文件后，会用我们
	// 给出的 StorageJSON + Metadata 反向重建物理文档，不回传就抹掉标记。
	carryFailoverKeysIntoMetadata(ad.Metadata, req.RawJSON)
	ad.ID = "" // let host compute from path (prevents ID mismatch dupes)
	if fn := strings.TrimSpace(req.FileName); fn != "" {
		ad.FileName = fn
	}
	return okEnvelope(pluginapi.AuthParseResponse{
		Handled: true,
		Auth:    ad,
	})
}

func toAuthData(sa *storedAuth) pluginapi.AuthData {
	return toAuthDataOpts(sa, nil, false)
}

// toAuthDataOpts builds AuthData with optional credits snapshot and disabled flag.
func toAuthDataOpts(sa *storedAuth, cr *creditsSummary, disabled bool) pluginapi.AuthData {
	storage, _ := json.Marshal(sa)
	id := providerName
	fileName := authFileName
	if sa != nil {
		if uid := sanitizeUIDForFileName(sa.Account.UID); uid != "" {
			id = uid
			fileName = "workbuddy-" + uid + ".json"
		}
	}
	label := labelForAuth(sa)
	meta := enrichAuthMetadata(sa, cr, disabled)
	return pluginapi.AuthData{
		Provider:    providerName,
		ID:          id,
		FileName:    fileName,
		Label:       label,
		Disabled:    disabled,
		StorageJSON: storage,
		// Standardized auth metadata. `type` is required by the host for
		// auth-file classification; `logo`/`note`/`disabled` surface on auth rows.
		Metadata: meta,
	}
}

// -----------------------------------------------------------------------------

func handleExecExecute(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	sa, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	// Resolve oauth-model-alias (e.g. "point/deepseek-v4-flash") back to the
	// real upstream model ID; the upstream rejects unknown alias IDs.
	upstreamModel := resolveUpstreamModel(req.Model, req.AuthAttributes)
	started := time.Now()
	authUID := ""
	if sa.Account.UID != "" {
		authUID = sa.Account.UID
	}
	// Workbuddy-internal account identifier forwarded to the tracker dashboard's
	// "来源" (source) column so users can see which account served the request.
	// Nickname reads best; fall back to UID when the account has no nickname.
	accountLabel := strings.TrimSpace(sa.Account.Nickname)
	if accountLabel == "" {
		accountLabel = authUID
	}
	// Stable per-conversation key forwarded to the tracker dashboard's
	// "会话" (session) column so users can tell at a glance whether two rows
	// came from the same stickiness-bound conversation. Extracted from the
	// same priority chain scheduler.pick uses (extractSessionKeyFromSources
	// here, extractSessionKey on the scheduler side). Frozen across the
	// per-request account-failover loop because the underlying req does not
	// change between attempts.
	sessionKey := extractSessionKeyFromSources(req.Headers, req.Metadata)
	// CodeBuddy rejects non-stream requests (code 11101), so always stream
	// upstream and fold the chunks into a single chat.completion object.
	// prepareUpstreamBody does forceStream + normalizeTools + rewriteSystem +
	// ensureSystemMessage + rewriteModel in ONE unmarshal/marshal pass.
	body := prepareUpstreamBody(req.Payload, req.OriginalRequest, sa, upstreamModel)
	reasoningEffort := reasoningEffortFromBody(body)

	// Per-request account-failover loop. We try the prepared body against
	// the configured account first; on an account-level 4xx
	// (401/403/404/405) or 429 soft rate limit (v0.14.2) we rebuild
	// against the next candidate up to retry_on_4xx times.
	// publishUsage / resetAccountFailover / etc. must run against the
	// credential that actually served the request, so we keep (usedSA,
	// usedBody) in lockstep with the chosen account. 5xx/0/402 keep the
	// cooldown-only path (cross-request rotation via recordAccountFailure)
	// and skip this loop via isAccountLevel4xx returning false.
	budget := loadedRetryOn4xx()
	curSA := sa
	curBody := body
	var (
		completion    []byte
		completionErr error
		usedAuthID    = req.AuthID
		// completionTTFT 是「真正成功的那次尝试」的首字延迟。失败尝试的计时
		// 不写入，避免把上一个账号的失败耗时记到最终承接的账号上。
		completionTTFT uint64
	)

	// 前置冷却拦截：如果初始账号正处于冷却中，不要发送无效的上游请求，立即换号！
	// 轮换冷却同样走这条拦截——这正是它区别于 priority 降级的地方：宿主的会话亲和
	// 命中时不会看 priority，但请求进入插件后由这里主动换号，已绑定会话也能切走。
	initID := strings.TrimSpace(req.AuthID)
	if initID == "" && curSA != nil {
		initID = strings.TrimSpace(curSA.Auth.AccessToken)
		if initID == "" {
			initID = strings.TrimSpace(curSA.Account.UID)
		}
	}
	rotationNow := time.Now()
	if initID != "" && (isAuthModelCoolingDown(initID, upstreamModel) ||
		(curSA != nil && curSA.Account.UID != "" && isAuthModelCoolingDown(curSA.Account.UID, upstreamModel)) ||
		rotationCoolingDown(initID, rotationNow) ||
		(curSA != nil && curSA.Account.UID != "" && rotationCoolingDown(curSA.Account.UID, rotationNow)) ||
		slowCoolingDown(initID, upstreamModel, rotationNow) ||
		(curSA != nil && curSA.Account.UID != "" && slowCoolingDown(curSA.Account.UID, upstreamModel, rotationNow))) {
		if nextID, nextSA, hasNext := pickNextAuthForModel(initID, upstreamModel); hasNext && nextSA != nil {
			curSA = nextSA
			curBody = prepareUpstreamBody(req.Payload, req.OriginalRequest, curSA, upstreamModel)
			reasoningEffort = reasoningEffortFromBody(curBody)
			usedAuthID = nextID
			authUID = curSA.Account.UID
			accountLabel = strings.TrimSpace(curSA.Account.Nickname)
			if accountLabel == "" {
				accountLabel = authUID
			}
		}
	}

	for attempt := 0; attempt <= budget; attempt++ {
		var attemptTTFT uint64
		completion, attemptTTFT, completionErr = doExecuteOnce(curBody, curSA, req.Model, upstreamModel)
		if completionErr == nil {
			completionTTFT = attemptTTFT
			authUID = curSA.Account.UID
			accountLabel = strings.TrimSpace(curSA.Account.Nickname)
			if accountLabel == "" {
				accountLabel = authUID
			}
			// Resolve the scheduler-level ID used by noteAccountFailure / reset
			// bookkeeping. Same heuristic as collectUpstreamStream.
			usedAuthID = strings.TrimSpace(curSA.Auth.AccessToken)
			if usedAuthID == "" {
				usedAuthID = strings.TrimSpace(curSA.Account.UID)
			}
			resetModelBlockedState(upstreamModel)
			break
		}
		// Decide whether to retry on the next account. Account-level 4xx
		// (401/403/404/405) and 429 rotate as before; an in-stream error
		// frame on a 200 body rotates only when isAccountFailure
		// classifies the body as account-level (quota/rate-limit markers).
		// 5xx/0/402 keep the cooldown-only path (cross-request rotation
		// via recordAccountFailure) and skip this loop.
		statusCode := parseUpstreamStatusFromErr(completionErr)
		// 11128：双写记账（模型冷却 + 账号禁用写盘），并立即停止本轮换号。
		// 上游拦截的是这个组合，其余账号大概率同样命中；恢复统一由 60 秒
		// 禁用探活裁决，它会把这个模型排在回放序列最前。
		if isModelBlockedError(statusCode, completionErr.Error()) {
			if curSA != nil {
				currentID := strings.TrimSpace(curSA.Auth.AccessToken)
				if currentID == "" {
					currentID = strings.TrimSpace(curSA.Account.UID)
				}
				noteModelBlockedHit(currentID, upstreamModel)
			}
			break
		}
		if !shouldRotateOnUpstreamErr(statusCode, completionErr.Error()) || attempt >= budget || curSA == nil {
			break
		}
		currentID := strings.TrimSpace(curSA.Auth.AccessToken)
		if currentID == "" {
			currentID = strings.TrimSpace(curSA.Account.UID)
		}
		_, nextSA, hasNext := pickNextAuthForModel(currentID, upstreamModel)
		if !hasNext || nextSA == nil {
			break
		}
		curSA = nextSA
		// Re-prepare against the new account's stored identity so any
		// per-account tool/schema/system rules apply. body itself
		// stays byte-identical w.r.t. the user's intent.
		curBody = prepareUpstreamBody(req.Payload, req.OriginalRequest, curSA, upstreamModel)
		reasoningEffort = reasoningEffortFromBody(curBody)
	}
	if completionErr != nil {
		// After exhausting retries (or a non-account-level error), mirror
		// the historical accounting path: note the failure on the LAST
		// account actually contacted and propagate the error.
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, parseUpstreamStatusFromErr(completionErr), completionErr.Error(), reasoningEffort, 0, accountLabel, sessionKey)
		reconcileAfterExecutorErrorForModel(usedAuthID, upstreamModel, parseUpstreamStatusFromErr(completionErr), completionErr.Error())
		statusCode := parseUpstreamStatusFromErr(completionErr)
		if statusCode == 0 {
			noteAccountFailureForModel(usedAuthID, upstreamModel, 0, completionErr.Error())
		} else if statusCode == 200 {
			// SSE error frame on HTTP 200: remap to 403 so isAccountFailure
			// correctly triggers the laddered failover cooldown.
			noteSSEErrorFailure(usedAuthID, upstreamModel, completionErr.Error())
		}
		return nil, completionErr
	}
	// Success path: credit invalidation + failover counter reset use
	// the actual account that served the request.
	publishUsage(req.Model, upstreamModel, authUID, started, usageDetailFromCompletion(completion), false, 0, "", reasoningEffort, completionTTFT, accountLabel, sessionKey)
	invalidateAccountCredits(usedAuthID, authUID)
	resetAccountFailover(usedAuthID)
	// 连续使用轮换：只在真实成功后推进计时；达到 serve 阈值时让位，
	// 使长时间承流的账号主动退出，把后续请求交给其它账号。
	noteRotationUsage(usedAuthID)
	// 慢速观测：只记真实成功请求的首字延迟，样本达标时判定该账号在该模型上
	// 是否显著慢于同池水平。失败尝试的耗时不计入，避免污染样本。
	noteSlowObservation(usedAuthID, upstreamModel, completionTTFT)
	return okEnvelope(pluginapi.ExecutorResponse{Payload: completion})
}

// executorStreamRequest wraps the host's executor.execute_stream RPC: the
// ExecutorRequest plus the async stream id the host uses to receive chunks.
type executorStreamRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func handleExecStream(raw []byte) ([]byte, error) {
	var req executorStreamRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	sa, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	upstreamModel := resolveUpstreamModel(req.Model, req.AuthAttributes)
	started := time.Now()
	authUID := ""
	if sa.Account.UID != "" {
		authUID = sa.Account.UID
	}
	// Workbuddy-internal account identifier for the tracker dashboard's 来源
	// column (same rule as handleExecExecute): nickname preferred, UID fallback.
	accountLabel := strings.TrimSpace(sa.Account.Nickname)
	if accountLabel == "" {
		accountLabel = authUID
	}
	// Same per-conversation key as handleExecExecute; see that function for the
	// rationale. Pumped downstream into pumpUpstreamStream so SSE rows carry
	// the session identity too.
	sessionKey := extractSessionKeyFromSources(req.Headers, req.Metadata)
	body := req.Payload
	if len(body) == 0 {
		body = req.OriginalRequest
	}
	// Single-pass JSON rewrite (see handleExecExecute for the non-stream path).
	body = prepareUpstreamBody(body, nil, sa, upstreamModel)
	reasoningEffort := reasoningEffortFromBody(body)

	headers := streamHeaders()
	sseFramed := clientNeedsSSEFrame(req.Metadata)

	// No async stream id → fall back to synchronous chunk collection.
	if req.StreamID == "" {
		collector := &sseUsageCollector{}
		chunks, statusCode, servedAuthID, errCollect := collectUpstreamStream(body, sa, sseFramed, collector, upstreamModel)
		if errCollect != nil {
			publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, statusCode, errCollect.Error(), reasoningEffort, collector.ttftNS(started), accountLabel, sessionKey)
			// statusCode >= 400 already went through reconcileByUID inside
			// collectUpstreamStream; only transport-level failures and HTTP-200
			// SSE error frames need the failover note here. Override 200 to 403
			// so isAccountFailure classifies it as an account-level failure and
			// the account enters its laddered cooldown.
			if statusCode == 0 {
				noteAccountFailureForModel(req.AuthID, upstreamModel, 0, errCollect.Error())
			} else if statusCode < 400 {
				noteSSEErrorFailure(req.AuthID, upstreamModel, errCollect.Error())
			}
			return nil, errCollect
		}
		// 换号后实际承流的是 servedAuthID：成功打点必须用它，否则慢速样本
		// 会记到被换掉的初始账号头上。
		ttftNS := collector.ttftNS(started)
		publishUsage(req.Model, upstreamModel, authUID, started, collector.detail(), false, 0, "", reasoningEffort, ttftNS, accountLabel, sessionKey)
		invalidateAccountCredits(req.AuthID, authUID)
		resetAccountFailover(req.AuthID)
		noteRotationUsage(req.AuthID)
		if servedAuthID != "" {
			noteSlowObservation(servedAuthID, upstreamModel, ttftNS)
		}
		return okEnvelope(streamResponse{Headers: headers, Chunks: chunks})
	}

	// Async: return immediately with empty chunks. A goroutine pumps the upstream
	// and emits each chunk via host.stream.emit so the client sees true streaming.
	// Use context.Background() (not nil) so the request can be cancelled when the
	// client disconnects — otherwise the pump keeps reading a dead upstream until
	// sharedHTTPClient's 120s timeout, holding a pool slot the whole time.
	ctx, cancel := context.WithCancel(context.Background())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointChatFor(sa), bytes.NewReader(body))
	if err != nil {
		cancel()
		streamEmitError(req.StreamID, err.Error())
		streamClose(req.StreamID)
		return okEnvelope(streamResponse{Headers: headers})
	}
	backendHeaders(httpReq, sa)
	go pumpUpstreamStream(httpReq, cancel, req.StreamID, sseFramed, req.Model, upstreamModel, authUID, started, req.AuthID, reasoningEffort, accountLabel, sessionKey)
	return okEnvelope(streamResponse{Headers: headers})
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
