// Package main implements the qwenwork CLIProxyAPI dynamic plugin.
//
// qwenwork wraps the QwenWork CN (qwenwork.cn) gateway as a cliproxy provider:
// it runs the Device Flow + PKCE login, mints short-lived MaaS access keys via
// /api/desktop-sessions, and exposes the standard OpenAI chat-completions
// interface backed by {maas_base}/v1/chat/completions.
//
// Built with -buildmode=c-shared and exports the cliproxy C ABI entry points.
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
	providerName  = "qwenwork-provider"
	authFileName  = "qwenwork.json"
	pluginLogoURL = "https://raw.githubusercontent.com/luode0320/cpa-workbuddy-plugin/main/assets/icons/QwenWork.png"
	// QwenWork: 单一 gateway 域名承载认证、额度与 MaaS 签发。
	// 推理走 MaaS 网关（base_url 由签发接口下发），不需要 COSY 签名。
	upstreamBaseCN = "https://gateway.qwenwork.cn"
	clientUA       = "qoderwork/1.3.1"

	// 额度与账号信息（宿主 Bearer）。
	endpointUserInfo    = upstreamBaseCN + "/api/v1/adapter/user/account-context?include=user,plan,quota,page,data_sharing"
	endpointUserWallets = upstreamBaseCN + "/api/v1/adapter/user/wallets"

	// MaaS 会话签发（宿主 Bearer → sk-mm-v1 key）。
	endpointMintDesktopSession = upstreamBaseCN + "/api/desktop-sessions"

	// 模型目录（宿主 Bearer，返回 schema_version=2 的 catalog）。
	endpointChatModes = upstreamBaseCN + "/api/desktop/chat-modes"

	// loginTTL bounds one device-authorization flow. Users may need to log in
	// to qwenwork.cn first (Aliyun SSO) before authorizing — give them room.
	loginTTL = 10 * time.Minute
)

// loginCtx holds one in-flight device-authorization login flow. QwenWork's
// desktop clients use a PKCE device flow: the plugin generates
// verifier/challenge + nonce/machine_id, the user authorizes in a browser,
// and PollLogin exchanges the grant at /api/v1/deviceToken/poll.
type loginCtx struct {
	verifier  string // PKCE code_verifier — required by the poll endpoint
	nonce     string // device-flow nonce — paired with the auth URL
	expires   time.Time
	startedAt int64 // unix nano, set when StartLogin creates the state
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
	// Credits-preserve watchdog (保号池): periodic refresh + preserve flag
	// flips on the physical auth files. Started in init() but waits for the
	// host bridge + auth discovery before its first tick (see watchdog.go).
	// 同步自 workbuddy-provider。
	go preserveWatchdogLoop()
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
		// Upstream QwenWork has no dedicated count_tokens API. Return
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
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
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
var version = "0.2.12"

func wbRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             providerName,
			Version:          version,
			Author:           "Sliverkiss (based on qwenwork by lovingfish)",
			GitHubRepository: "https://github.com/luode0320/cpa-workbuddy-plugin",
			Logo:             pluginLogoURL,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "lifecycle_auto", Type: pluginapi.ConfigFieldTypeBoolean, Description: "额度生命周期自动化（默认关闭）。开启后：额度耗尽（含上游 quota.exceeded）的账号自动停用并打 exhausted_disable 标记，充值恢复后自动重新启用；手动禁用的账号永不被自动改动。"},
				{Name: "token_keepalive", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用每4小时 access-token 自动刷新，防止 Keycloak 离线会话过期（默认开启）。"},
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "可选模型列表。每个条目可包含 id、name、alias、context、max_tokens、enabled、reasoning 字段；配置后优先于自动获取的模型列表。"},
				{Name: "scheduler_mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{schedulerModeSession, schedulerModeCredits, schedulerModeOff}, Description: "多账号选择策略：session（按会话轮询，同一会话 1 小时内固定同一账号；默认）／credits（面板指定的固定账号）／off（交给内置逻辑）。额度耗尽的账号始终被排除（QwenWork 无免费模型），冷却中或低于安全红线的账号同样排除。"},
				{Name: "usage_report_url", Type: pluginapi.ConfigFieldTypeString, Description: "可选：覆盖 CPAMP 用量上报地址（默认 http://cpa-manager-plus:18317/v0/management/usage/import；也可用环境变量 USAGE_REPORT_URL）。"},
				{Name: "usage_report_key", Type: pluginapi.ConfigFieldTypeString, Description: "可选：覆盖 CPAMP 管理密钥。优先从环境变量 CPAMP_ADMIN_KEY / USAGE_REPORT_KEY 或密钥文件 /run/secrets/cpamp_admin_key 自动探测。"},
				{Name: "cpa_management_url", Type: pluginapi.ConfigFieldTypeString, Description: "可选：CPA 宿主管理 API 地址，默认 http://127.0.0.1:8317。仅当宿主管理端口非默认时才需要填。"},
				{Name: "cpa_management_key", Type: pluginapi.ConfigFieldTypeString, Description: "可选：CPA 宿主管理密钥。填写后，额度耗尽自动禁用会改为调用宿主 auth-files/status 接口写入路由真相源。留空则沿用旧的直写物理文件路径。也可用环境变量 WB_CPA_MANAGEMENT_KEY。"},
				{Name: "usage_feed_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "将每次请求的 token 用量追加写入共享 NDJSON 数据流，供 token-usage-tracker 插件消费（默认开启）。"},
				{Name: "usage_feed_path", Type: pluginapi.ConfigFieldTypeString, Description: "可选：共享用量数据流路径（默认 <CLIProxyAPI 根目录>/data/token-usage-feed.ndjson）。必须与 token-usage-tracker 的 usage_feed_path 保持一致。"},
				{Name: "slow_account_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用慢速账号冷却（默认开启）：某账号在某个模型上的首字延迟中位数显著高于同池同模型水平时，在该模型上进入冷却并让出流量。慢是性能问题不是故障，只走内存冷却，到期自动恢复，不写盘禁用。"},
				{Name: "slow_ttft_ratio_x10", Type: pluginapi.ConfigFieldTypeInteger, Description: "慢速判定倍数阈值 ×10（默认 25 即 2.5 倍）：账号首字延迟中位数达到同池基准的该倍数即判慢。必须大于 10。"},
				{Name: "slow_window_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "首字延迟样本统计窗口（默认 15 分钟）：只统计窗口内的成功请求样本。"},
				{Name: "slow_min_samples", Type: pluginapi.ConfigFieldTypeInteger, Description: "参与慢速判定所需的最小样本数（默认 5）：样本不足时不判定，避免单条毛刺误伤。"},
				{Name: "slow_cooldown_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "慢速冷却时长（默认 10 分钟）：到期自动恢复，无需探活。"},
				{Name: "slow_min_ttft_ms", Type: pluginapi.ConfigFieldTypeInteger, Description: "慢速判定的绝对下限毫秒（默认 3000）：低于该值一律不判慢，避免全池都快时相对放大误伤；填 0 表示不设下限。"},
				{Name: "slow_min_pool_size", Type: pluginapi.ConfigFieldTypeInteger, Description: "账号池准入下限（默认 20）：账号总数（启用 + 停用）须严格大于该值才触发慢速判定。池子太小时下四分位基线退化，两个账号里任一次抖动就会让其中一个被判慢、互相顶替。"},
			},
		},
		Capabilities: registrationCapability{
			ModelProvider:        true,
			AuthProvider:         true,
			FrontendAuthProvider: false,
			Executor:             true,
			// 必须是 both 而不是 oauth：宿主只对 static/both 作用域在注册期调用
			// StaticModels（internal/pluginhost/adapters.go:60 executorScopeAllowsStaticModels），
			// 而 oauth-model-alias 的宿主级缓存正是从该回调的 HostConfigSummary 里
			// 填充的（见 models.go cacheModelAliases）。声明成 oauth 会让缓存恒空，
			// 执行器拿不到别名表，别名请求被原样透传后遭上游 400。
			ExecutorModelScope:    pluginapi.ExecutorModelScopeBoth,
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

// dynamicModelsFailCacheTTL 是模型发现「全账号扫描失败」后的负缓存窗口：
// 在窗口内 fetchDynamicModels / fetchDynamicModelsFromStorage 直接返回 nil，
// 不再逐账号重扫。上游失败若不设负缓存，会被宿主每次 auth 重写触发的
// model.for_auth 放大成 N+1 请求风暴（生产 OOM 主因）。
const dynamicModelsFailCacheTTL = 5 * time.Minute

var dynamicModelsCache struct {
	sync.RWMutex
	models   []pluginapi.ModelInfo
	fetched  time.Time
	failedAt time.Time // 最近一次全量扫描失败的时间；零值表示无失败记录
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

func hostAuthListFiles() ([]pluginapi.HostAuthFileEntry, error) {
	raw, err := hostCall(pluginabi.MethodHostAuthList, nil)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return nil, fmt.Errorf("host.auth.list failed")
	}
	var resp rpcHostAuthListResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return nil, err
	}
	return resp.Files, nil
}

// hostAuthGetByIndex fetches the raw JSON for one auth index.
func hostAuthGetByIndex(authIndex string) ([]byte, error) {
	body, _ := json.Marshal(map[string]string{"auth_index": authIndex})
	raw, err := hostCall(pluginabi.MethodHostAuthGet, body)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return nil, fmt.Errorf("host.auth.get failed")
	}
	var resp rpcHostAuthGetResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return nil, err
	}
	return resp.JSON, nil
}

// storedAuth is the on-disk shape of a qwenwork credential.
type storedAuth struct {
	Auth    storedTokens  `json:"auth"`
	Account storedAccount `json:"account"`
}

// storedTokens holds one credential family at a time in Access/RefreshToken,
// plus the long-lived PAT fallback. Two families coexist in one file:
//
//	PAT family:   accessToken=jt- (24h),  refreshToken=jrt- (48h)
//	OAuth family: accessToken=dt- (~30d), refreshToken=drt- (~1y, rotating)
//
// personalToken (pt-) is family-independent and never overwritten by refreshes
// — it re-exchanges a fresh jobToken pair when both live tokens die.
type storedTokens struct {
	AccessToken   string `json:"accessToken"`   // jt- (24h) or dt- (~30d)
	RefreshToken  string `json:"refreshToken"`  // jrt- (48h) or drt- (~1y)
	PersonalToken string `json:"personalToken"` // pt-..., long-lived fallback
	ExpiresAt     int64  `json:"expiresAt"`     // active-token expiry (unix seconds)
	Domain        string `json:"domain"`        // 账号归属域：qwenwork.cn
}

type storedAccount struct {
	UID          string `json:"uid"`
	EnterpriseID string `json:"enterpriseId"`
	Nickname     string `json:"nickname"`
}

// apiEnvelope is the generic {code,msg,data} wrapper used by every QwenWork API.
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// jobTokenResponse is defined in oauth.go; keepalive and handleRefreshAuth
// both use it. The old tokenData struct (camelCase tags) was wrong and has
// been removed — QwenWork returns snake_case JSON.

func parseStored(raw []byte) (*storedAuth, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty auth storage")
	}
	// Accept both shapes seen in the wild:
	//   nested: {"auth":{"accessToken":...},"account":{"uid":...}} (plugin/oauth output)
	//   flat:   {"accessToken":...,"uid":...,"nickname":...} (CPA-Manager-Plus auths/qwenwork.json)
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
			AccessToken   string `json:"accessToken"`
			RefreshToken  string `json:"refreshToken"`
			PersonalToken string `json:"personalToken"` // PAT fallback — must survive the flat shape too
			ExpiresAt     int64  `json:"expiresAt"`
			Domain        string `json:"domain"`
			UID           string `json:"uid"`
			EnterpriseID  string `json:"enterpriseId"`
			Nickname      string `json:"nickname"`
		}
		if err := json.Unmarshal(raw, &flat); err != nil {
			return nil, fmt.Errorf("storage_parse_error: %w", err)
		}
		sa.Auth = storedTokens{AccessToken: flat.AccessToken, RefreshToken: flat.RefreshToken, PersonalToken: flat.PersonalToken, ExpiresAt: flat.ExpiresAt, Domain: flat.Domain}
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

func commonHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", clientUA)
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
	// back to polling every plugin — first Handled=true wins. To prevent
	// claiming foreign providers' legacy files (e.g. workbuddy's type-less
	// auths, which parseStored would otherwise accept because the nested
	// {auth,account} shape is identical), only claim files whose declared
	// type matches us — or whose filename carries our prefix.
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
		// No type declared: only claim when the host already routed this to us
		// (req.Provider == qwenwork) or the filename carries our prefix.
		routed := strings.EqualFold(strings.TrimSpace(req.Provider), providerName)
		prefixed := strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.FileName)), providerName+"-")
		if !routed && !prefixed {
			return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
		}
	}
	sa, err := parseStored(req.RawJSON)
	if err != nil {
		// Not a qwenwork credential; let the host try other providers.
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
	ad := toAuthDataOpts(sa, nil, false)
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
			fileName = "qwenwork-" + uid + ".json"
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
	// 剥离 provider 前缀后反解 oauth-model-alias：宿主只按别名做路由匹配，
	// 传给执行器的仍是客户端原始模型名，而上游只认真实 ID——直接透传别名
	// 会被上游判为 invalid_request（400，param=None）。
	upstreamModel := upstreamModelFor(req.Model, req.AuthAttributes)
	started := time.Now()
	authUID := ""
	if sa.Account.UID != "" {
		authUID = sa.Account.UID
	}
	// Qoderwork-internal account identifier forwarded to the tracker
	// dashboard's "来源" (source) column so users can see which account served
	// the request. Nickname reads best; fall back to UID when the account has
	// no nickname.
	accountLabel := strings.TrimSpace(sa.Account.Nickname)
	if accountLabel == "" {
		accountLabel = authUID
	}
	// Stable per-conversation key forwarded to the tracker dashboard's
	// "会话" (session) column. Extracted from the same priority chain
	// scheduler.pick uses (extractSessionKeyFromSources here,
	// extractSessionKey on the scheduler side). qwenwork does not rewrite
	// the client's reasoning_effort; the value is reported verbatim to the feed.
	sessionKey := extractSessionKeyFromSources(req.Headers, req.Metadata)
	// Build the QwenWork agent_chat_generation body from the OpenAI request,
	// then QoderEncoding-encode it. The template embeds a 10657-token system
	// prompt that the server requires for normal behaviour (KNOWLEDGE §5.2).
	bodyRaw := req.Payload
	if len(bodyRaw) == 0 {
		bodyRaw = req.OriginalRequest
	}
	qwReq := &openAIRequest{}
	if err := json.Unmarshal(bodyRaw, qwReq); err != nil && len(bodyRaw) > 0 {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "payload parse: "+err.Error(), "", 0, accountLabel, sessionKey)
		return nil, fmt.Errorf("payload parse: %w", err)
	}
	// 客户端请求的推理强度：写入 usage feed 供 dashboard 展示。该值不进上游
	// body（QwenWork 模板无此字段位点），仅用于对齐 workbuddy 的用量维度。
	reasoningEffort := strings.TrimSpace(qwReq.ReasoningEffort)
	// QwenWork 透传客户端请求体，只强制 stream 并按官方规格自适应 max_tokens。
	body := prepareUpstreamBody(bodyRaw, upstreamModel)

	// Per-request account-failover loop. We try the prepared body against
	// the configured account first; on an account-level 4xx
	// (401/403/404/405) or 429 soft rate limit (v0.9.1) we rebuild
	// against the next candidate up to retry_on_4xx times.
	// publishUsage / resetAccountFailover / etc. must run against the
	// credential that actually served the request, so we keep (usedSA,
	// usedAuthID) in lockstep with the chosen account. 5xx/0/402 keep the
	// cooldown-only path (cross-request rotation via recordAccountFailure)
	// and skip this loop via isAccountLevel4xx returning false.
	budget := loadedRetryOn4xx()
	curSA := sa
	var (
		completion    []byte
		completionErr error
		// 账号身份一律用宿主 auth.ID（req.AuthID，形如 qwenwork-<uid>.json）。
		// 它是调度候选、冷却表、会话绑定、积分缓存共用的唯一 key；绝不能
		// 用 accessToken 代替——宿主 ID 与凭据无关，混用会让冷却写入落到
		// 永远查不到的键上，故障账号因此永不退出轮转。
		usedAuthID = strings.TrimSpace(req.AuthID)
	)

	// 前置冷却拦截：若初始账号处于冷却中，直接换号到健康候选
	initID := usedAuthID
	if initID == "" && curSA != nil {
		initID = accountIdentity(curSA)
	}
	if initID != "" && isAccountCoolingDown(initID) {
		if nextID, nextSA, hasNext := pickNextAuth(initID); hasNext && nextSA != nil {
			curSA = nextSA
			usedAuthID = nextID
			authUID = curSA.Account.UID
			accountLabel = strings.TrimSpace(curSA.Account.Nickname)
			if accountLabel == "" {
				accountLabel = authUID
			}
		}
	}

	// nonStreamTTFTNS 由 doExecuteOnceQwenWork 带出：非流式路径的首字延迟，
	// 成功分支需要它上报慢速冷却样本。
	nonStreamTTFTNS := uint64(0)
	for attempt := 0; attempt <= budget; attempt++ {
		var attemptTTFT uint64
		completion, attemptTTFT, completionErr = doExecuteOnceQwenWork(body, curSA, req.Model, sessionKey)
		nonStreamTTFTNS = attemptTTFT
		if completionErr == nil {
			// The account that actually served the request may differ from the
			// initial one after failover; keep counter/feed bookkeeping in
			// lockstep with it.
			authUID = curSA.Account.UID
			accountLabel = strings.TrimSpace(curSA.Account.Nickname)
			if accountLabel == "" {
				accountLabel = authUID
			}
			break
		}
		// Decide whether to retry on the next account. We re-classify
		// the surfaced upstream N (doExecuteOnceQoder encoded it via the
		// standard "upstream N:" prefix). Business 400 is request-shaped
		// and would fail identically on every account; 5xx/0/402 are
		// surfaced immediately (cooldown handles long-term failure).
		// 200 is also eligible: doExecuteOnceQoder folds an in-stream
		// OpenAI error frame into "upstream 200: ..." and
		// shouldRotateOnUpstreamErr classifies the body via the account
		// failure markers. （同步自 workbuddy 200 错误帧换号）
		statusCode := parseUpstreamStatusFromErr(completionErr)
		currentID := ""
		if curSA != nil {
			currentID = accountIdentity(curSA)
		}
		if isDailyCountExceeded(statusCode, completionErr.Error()) && currentID != "" {
			logDailyCooldownPersistFailure(currentID, triggerDailyCooldown("", currentID))
		}
		if !shouldRotateOnUpstreamErr(statusCode, completionErr.Error()) || attempt >= budget || curSA == nil {
			break
		}
		evictSessionBindingsForAuth(currentID)
		nextID, nextSA, hasNext := pickNextAuth(currentID)
		if !hasNext || nextSA == nil {
			break
		}
		curSA = nextSA
		// Keep the failover/credit bookkeeping ID in lockstep with the
		// account that will actually serve the next attempt.
		usedAuthID = nextID
	}
	if completionErr != nil {
		statusCode := parseUpstreamStatusFromErr(completionErr)
		if isDailyCountExceeded(statusCode, completionErr.Error()) {
			logDailyCooldownPersistFailure(usedAuthID, triggerDailyCooldown("", usedAuthID))
		}
		// After exhausting retries (or a non-account-level error), mirror
		// the historical accounting path: note the failure on the LAST
		// account actually contacted and propagate the error.
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, statusCode, completionErr.Error(), reasoningEffort, 0, accountLabel, sessionKey)
		reconcileAfterExecutorError(usedAuthID, statusCode, completionErr.Error())
		if statusCode == 0 {
			noteAccountFailure(usedAuthID, 0, completionErr.Error())
		} else if statusCode == 200 {
			// SSE error frame on HTTP 200: remap to 403 so isAccountFailure
			// correctly triggers the laddered failover cooldown.
			noteAccountFailure(usedAuthID, http.StatusForbidden, completionErr.Error())
		}
		return nil, completionErr
	}
	// Success path: credit invalidation + failover counter reset use
	// the actual account that served the request.
	publishUsage(req.Model, upstreamModel, authUID, started, usageDetailFromCompletion(completion), false, 0, "", reasoningEffort, nonStreamTTFTNS, accountLabel, sessionKey)
	invalidateAccountCredits(usedAuthID, authUID)
	resetAccountFailover(usedAuthID)
	// 慢速打点：真实成功才记样本，失败换号不计（错误耗时不是首字延迟）。
	noteSlowObservation(usedAuthID, upstreamModel, nonStreamTTFTNS)
	return okEnvelope(pluginapi.ExecutorResponse{Payload: completion})
}

// doExecuteOnceQwenWork 对 sa 执行一次同步推理。
//
// 与 qoderwork 的差别：QwenWork 走 MaaS 网关的 OpenAI 兼容端点，用短期
// sk-mm-v1 key 做裸 Bearer，不重建上游 body、不做 COSY 签名、不做自定义
// 编码，直接把客户端请求体转发上去。
//
// [参数] bodyRaw 客户端原始请求体；sa 账号凭据；requestedModel CPA 侧模型名。
// [返回] 折叠后的 chat.completion 负载；上游 4xx/5xx 时返回 "upstream N: ..."。
// 最近修改时间：2026-10-17 23:50:00；改动原因：切换 QwenWork MaaS 推理链路。
func doExecuteOnceQwenWork(bodyRaw []byte, sa *storedAuth, requestedModel, sessionKey string) ([]byte, uint64, error) {
	attemptStarted := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sess, err := ensureMaasSession(sa, sessionKey)
	if err != nil {
		return nil, 0, fmt.Errorf("maas: %w", err)
	}
	url := maasInferenceURL(sess, "/v1/chat/completions")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyRaw))
	if err != nil {
		return nil, 0, err
	}
	applyMaasHeaders(httpReq, sess)
	// Compliance: route via host.http.do_stream so request-log captures the
	// outbound call. Read entire body via the bridge, then fold SSE → completion.
	stream, statusCode, _, err := hostHTTPDoStream(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("http_error: %w", err)
	}
	defer stream.Close()
	reader := newHostStreamReader(stream)
	if statusCode >= 400 {
		payload := readAllUpstreamErr(reader)
		if statusCode == http.StatusUnauthorized {
			invalidateMaasSession(sa)
		}
		if sa != nil && sa.Account.UID != "" {
			go reconcileByUID(sa.Account.UID, statusCode, payload)
		}
		return nil, 0, fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(payload, 200))
	}
	var firstByteAt time.Time
	completion, errAgg := aggregateOpenAISSE(reader, requestedModel, statusCode, &firstByteAt)
	if errAgg != nil {
		return nil, 0, errAgg
	}
	// 非流式路径也要上报 TTFT：慢速冷却的样本池需要覆盖这条路径，
	// 否则纯非流式流量永远达不到样本门槛，机制对它们等同不存在。
	return completion, ttftNSBetween(attemptStarted, firstByteAt), nil
}

// applyMaasHeaders 给推理请求加 MaaS 鉴权与内容类型头。
//
// [参数] req 待发送请求；sess 已签发的 MaaS 票据。
// [返回] 无（就地修改请求头）。
// 最近修改时间：2026-10-17 23:50:00；改动原因：新建 QwenWork MaaS 鉴权。
func applyMaasHeaders(req *http.Request, sess *maasSession) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+sess.AccessKey)
}

// accountIdentity 返回账号在调度/冷却/会话绑定/积分缓存中的统一 key。
//
// 宿主给执行器的 AuthID 就是 auth.ID（相对 auth 目录的文件名，形如
// qwenwork-<uid>.json），与调度候选 ID 完全同源；缺失时退回按 UID 构造的
// 文件名，保证任何路径都不会把凭据本身当成账号身份。
//
// [参数] sa 账号凭据。
// [返回] 非空时返回宿主 ID 形态的 key；无法推导时返回空串。
// 最近修改时间：2026-10-18 01:40:00；改动原因：修正 accessToken 误作账号身份。
func accountIdentity(sa *storedAuth) string {
	if sa == nil {
		return ""
	}
	if uid := sanitizeUIDForFileName(sa.Account.UID); uid != "" {
		return authFilePrefix + uid + ".json"
	}
	return ""
}

// stripProviderPrefix removes the leading "qwenwork/" (or any "<provider>/")
// segment from a CPA-facing model name, leaving the bare alias/key.
func stripProviderPrefix(model string) string {
	if i := strings.Index(model, "/"); i > 0 {
		return model[i+1:]
	}
	return model
}

// upstreamModelFor 把执行器收到的客户端模型名解析为上游真实模型 ID。
//
// 三步不可调换：先剥 provider 前缀（"qwenwork/x" → "x"），再反解
// oauth-model-alias（别名 → 真实 ID），最后做模型键归一。宿主只按别名做
// 路由匹配，传给执行器的仍是客户端原始名字，而上游只认真实 ID——漏掉中间
// 这一步会把别名原样透传，被上游判为 invalid_request（400，param=None）。
// 2026-10-18 生产实证：面板「测试」直接指定真实 ID 故成功，经 new-api 走
// 别名则失败，两条路径的差异掩盖了该缺陷。
//
// [参数] model: 执行器请求里的客户端模型名；attrs: 宿主传入的 auth 属性。
// [返回] 可直接发给上游的模型 ID。
// 最近修改时间：2026-10-18 04:30:00；改动原因：补齐遗漏的别名反解。
func upstreamModelFor(model string, attrs map[string]string) string {
	return cpaToUpstreamKey(resolveUpstreamModel(stripProviderPrefix(model), attrs))
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
	// 与同步路径同构：先剥 provider 前缀，再反解 oauth-model-alias。
	upstreamModel := upstreamModelFor(req.Model, req.AuthAttributes)
	started := time.Now()
	authUID := ""
	if sa.Account.UID != "" {
		authUID = sa.Account.UID
	}
	// Qoderwork-internal account identifier for the tracker dashboard's 来源
	// column (same rule as handleExecExecute): nickname preferred, UID
	// fallback. sessionKey is the same per-conversation key as
	// handleExecExecute; pumped downstream into pumpUpstreamStream so SSE rows
	// carry the session identity too.
	accountLabel := strings.TrimSpace(sa.Account.Nickname)
	if accountLabel == "" {
		accountLabel = authUID
	}
	sessionKey := extractSessionKeyFromSources(req.Headers, req.Metadata)

	// Build the QwenWork body (template-based) and QoderEncoding-encode.
	bodyRaw := req.Payload
	if len(bodyRaw) == 0 {
		bodyRaw = req.OriginalRequest
	}
	qwReq := &openAIRequest{}
	if err := json.Unmarshal(bodyRaw, qwReq); err != nil && len(bodyRaw) > 0 {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "payload parse: "+err.Error(), "", 0, accountLabel, sessionKey)
		return nil, fmt.Errorf("payload parse: %w", err)
	}
	// 客户端请求的推理强度：写入 usage feed 供 dashboard 展示（同非流式路径）。
	reasoningEffort := strings.TrimSpace(qwReq.ReasoningEffort)
	// QwenWork 透传客户端请求体，只强制 stream 并按官方规格自适应 max_tokens。
	body := prepareUpstreamBody(bodyRaw, upstreamModel)

	headers := streamHeaders()
	sseFramed := clientNeedsSSEFrame(req.Metadata)

	// No async stream id → fall back to synchronous chunk collection.
	if req.StreamID == "" {
		collector := &sseUsageCollector{}
		chunks, statusCode, errCollect := collectUpstreamStreamOpenAI(body, sa, sessionKey, sseFramed, collector, req.AuthID)
		if errCollect != nil {
			publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, statusCode, errCollect.Error(), reasoningEffort, collector.ttftNS(started), accountLabel, sessionKey)
			// Unlike the workbuddy plugin, collectUpstreamStreamQoder does not
			// run reconcileByUID internally — handle both cases here.
			// statusCode=0 → transport failure, failover note only;
			// statusCode>=400 → reconcileAfterExecutorError (records failover
			// internally and applies the hard-credit lifecycle when relevant).
			// statusCode=200 → SSE error frame, must trigger failover cooldown.
			if statusCode == 0 {
				noteAccountFailure(req.AuthID, 0, errCollect.Error())
			} else if statusCode < 400 {
				noteAccountFailure(req.AuthID, http.StatusForbidden, errCollect.Error())
			} else {
				reconcileAfterExecutorError(req.AuthID, statusCode, errCollect.Error())
			}
			return nil, errCollect
		}
		syncTTFTNS := collector.ttftNS(started)
		publishUsage(req.Model, upstreamModel, authUID, started, collector.detail(), false, 0, "", reasoningEffort, syncTTFTNS, accountLabel, sessionKey)
		invalidateAccountCredits(req.AuthID, authUID)
		resetAccountFailover(req.AuthID)
		// 慢速打点：真实成功才记样本，失败换号不计（错误耗时不是首字延迟）。
		noteSlowObservation(req.AuthID, upstreamModel, syncTTFTNS)
		return okEnvelope(streamResponse{Headers: headers, Chunks: chunks})
	}

	// Async: return immediately with empty chunks. A goroutine pumps the upstream
	// and emits each chunk via host.stream.emit so the client sees true streaming.
	// Use context.Background() (not nil) so the request can be cancelled when the
	// client disconnects — otherwise the pump keeps reading a dead upstream until
	// sharedHTTPClient's 120s timeout, holding a pool slot the whole time.
	ctx, cancel := context.WithCancel(context.Background())
	sess, errSess := ensureMaasSession(sa, sessionKey)
	if errSess != nil {
		cancel()
		streamEmitError(req.StreamID, "maas: "+errSess.Error())
		streamClose(req.StreamID)
		return okEnvelope(streamResponse{Headers: headers})
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, maasInferenceURL(sess, "/v1/chat/completions"), bytes.NewReader(body))
	if err != nil {
		cancel()
		streamEmitError(req.StreamID, err.Error())
		streamClose(req.StreamID)
		return okEnvelope(streamResponse{Headers: headers})
	}
	applyMaasHeaders(httpReq, sess)
	go pumpUpstreamStream(httpReq, cancel, req.StreamID, sseFramed, req.Model, req.Model, authUID, started, req.AuthID, reasoningEffort, accountLabel, sessionKey)
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

// errorEnvelope 构造失败信封。排队帧按 envelopeHTTPStatusForError 带上 503，
// 使宿主 statusCodeFromError 命中跨 provider 轮转白名单；其余错误保持 0
// （宿主按既有语义处理）。
// [参数] code：错误码；message：错误文本。
// [返回] 序列化后的失败信封；序列化失败返回空切片。
// 最近修改时间 2026-10-15（打通宿主跨 provider 轮转的状态码通道）
func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{
		Code:       code,
		Message:    message,
		HTTPStatus: envelopeHTTPStatusForError(message),
	}})
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
