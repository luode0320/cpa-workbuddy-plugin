// Package main implements the WorkBuddy AI (International) CLIProxyAPI dynamic plugin.
//
// workbuddy-ai wraps WorkBuddy AI (www.workbuddy.ai) as a cliproxy provider:
// it performs the native WorkBuddy AI web OAuth login flow, refreshes access
// tokens, queries trial packages / credits, and forwards OpenAI-compatible chat
// completion requests to the upstream /v2/chat/completions endpoint.
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
	providerName      = "workbuddy-ai-provider"
	pluginDisplayName = "WorkBuddy AI"
	authFileName      = "workbuddyai.json"
	pluginLogoURL     = "https://raw.githubusercontent.com/DGZSbot/ai-icon/refs/heads/main/WorkBuddy.png"

	// Upstream endpoints strictly pinned to workbuddy.ai
	upstreamBase  = "https://www.workbuddy.ai"
	clientUA      = "CLI/2.63.2 WorkBuddy/2.63.2"
	originReferer = "https://www.workbuddy.ai"

	endpointAuthState    = upstreamBase + "/v2/plugin/auth/state?platform=CLI"
	endpointLoginAcct    = upstreamBase + "/v2/plugin/login/account?state="
	endpointAuthToken    = upstreamBase + "/v2/plugin/auth/token?state="
	endpointTokenRefresh = upstreamBase + "/v2/plugin/auth/token/refresh"
	endpointChat         = upstreamBase + "/v2/chat/completions"
	endpointModels       = upstreamBase + "/console/enterprises/personal/models"

	loginTTL = 5 * time.Minute
)

type loginCtx struct {
	client  *http.Client
	expires time.Time
}

var (
	hostAPI        *C.cliproxy_host_api
	loginStates    sync.Map
	httpClientOnce sync.Once
	sharedClient   *http.Client
)

const loginStatesPruneInterval = time.Minute

func init() {
	go watchdogLoop()
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
func cliproxyPluginShutdown() {}

// -----------------------------------------------------------------------------
// Host calls
// -----------------------------------------------------------------------------

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

var version = "0.1.2"

func wbRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginDisplayName,
			Version:          version,
			Author:           "luode0320 (based on workbuddy by Sliverkiss/lovingfish)",
			GitHubRepository: "https://github.com/luode0320/cpa-workbuddy-plugin",
			Logo:             pluginLogoURL,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "lifecycle_auto", Type: pluginapi.ConfigFieldTypeBoolean, Description: "额度耗尽时自动删除 Global 账号（默认开启）。"},
				{Name: "token_keepalive", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用每4小时 access-token 自动刷新，防止 Keycloak 离线会话过期（默认开启）。"},
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "可选模型列表。每个条目可包含 id、name、alias、context、max_tokens、enabled、reasoning 字段；配置后优先于自动获取的模型列表。"},
				{Name: "scheduler_mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{schedulerModeSession, schedulerModeCredits, schedulerModeOff}, Description: "多账号选择策略：session（按会话轮询，同一会话 1 小时内固定同一账号；默认）／credits（面板指定的固定账号）／off（交给内置逻辑）。"},
				{Name: "usage_report_url", Type: pluginapi.ConfigFieldTypeString, Description: "可选：覆盖 CPAMP 用量上报地址（默认 http://cpa-manager-plus:18317/v0/management/usage/import；也可用环境变量 USAGE_REPORT_URL）。"},
				{Name: "usage_report_key", Type: pluginapi.ConfigFieldTypeString, Description: "可选：覆盖 CPAMP 管理密钥。优先从环境变量 CPAMP_ADMIN_KEY / USAGE_REPORT_KEY 或密钥文件 /run/secrets/cpamp_admin_key 自动探测。"},
				{Name: "usage_feed_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "将每次请求的 token 用量追加写入共享 NDJSON 数据流，供 token-usage-tracker 插件消费（默认开启）。"},
				{Name: "usage_feed_path", Type: pluginapi.ConfigFieldTypeString, Description: "可选：共享用量数据流路径（默认 CLIProxyAPI根目录/data/token-usage-feed.ndjson）。必须与 token-usage-tracker 的 usage_feed_path 保持一致。"},
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

const dynamicModelsCacheTTL = 5 * time.Minute

var dynamicModelsCache struct {
	sync.RWMutex
	models  []pluginapi.ModelInfo
	fetched time.Time
}

var modelAliasCache struct {
	sync.RWMutex
	byAlias map[string]string
}

// storedAuth is the on-disk shape of a workbuddy-ai credential.
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
	if sa.Auth.Domain == "" {
		sa.Auth.Domain = "www.workbuddy.ai"
	}
	return &sa, nil
}

func commonHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", originReferer)
	req.Header.Set("Referer", originReferer+"/")
	req.Header.Set("User-Agent", clientUA)
}

func upstreamBaseFor(sa *storedAuth) string {
	return upstreamBase
}

func endpointChatFor(sa *storedAuth) string {
	return upstreamBase + "/v2/chat/completions"
}

func endpointTokenRefreshFor(sa *storedAuth) string {
	return upstreamBase + "/v2/plugin/auth/token/refresh"
}

func endpointModelsFor(sa *storedAuth) string {
	return upstreamBase + "/console/enterprises/personal/models"
}

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
	req.Header.Set("X-Domain", "www.workbuddy.ai")
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("Origin", originReferer)
	req.Header.Set("Referer", originReferer+"/")
}

func handleParseAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	var probeType struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(req.RawJSON, &probeType)
	declared := strings.ToLower(strings.TrimSpace(probeType.Type))
	if declared != "" && declared != providerName && declared != "workbuddy-ai" {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	if declared == "" {
		routed := strings.EqualFold(strings.TrimSpace(req.Provider), providerName) || strings.EqualFold(strings.TrimSpace(req.Provider), "workbuddy-ai")
		prefixed := strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.FileName)), authFilePrefix)
		if !routed && !prefixed {
			return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
		}
	}
	sa, err := parseStored(req.RawJSON)
	if err != nil {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	ad := toAuthDataOpts(sa, nil, parseDisabledFromAuthJSON(req.RawJSON))
	ad.ID = ""
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

func toAuthDataOpts(sa *storedAuth, cr *creditsSummary, disabled bool) pluginapi.AuthData {
	storage, _ := json.Marshal(sa)
	id := providerName
	fileName := authFileName
	if sa != nil {
		if uid := sanitizeUIDForFileName(sa.Account.UID); uid != "" {
			id = uid
			fileName = authFilePrefix + uid + ".json"
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
		Metadata:    meta,
	}
}

func handleExecExecute(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
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
	accountLabel := strings.TrimSpace(sa.Account.Nickname)
	if accountLabel == "" {
		accountLabel = authUID
	}
	sessionKey := extractSessionKeyFromSources(req.Headers, req.Metadata)
	body := prepareUpstreamBody(req.Payload, req.OriginalRequest, sa, upstreamModel)
	reasoningEffort := reasoningEffortFromBody(body)

	budget := loadedRetryOn4xx()
	curSA := sa
	curBody := body
	var (
		completion    []byte
		completionErr error
		usedAuthID    = req.AuthID
	)

	initID := strings.TrimSpace(req.AuthID)
	if initID == "" && curSA != nil {
		initID = strings.TrimSpace(curSA.Auth.AccessToken)
		if initID == "" {
			initID = strings.TrimSpace(curSA.Account.UID)
		}
	}
	if initID != "" && (isAccountCoolingDown(initID) || (curSA != nil && curSA.Account.UID != "" && isAccountCoolingDown(curSA.Account.UID))) {
		if nextID, nextSA, hasNext := pickNextAuth(initID); hasNext && nextSA != nil {
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
		completion, completionErr = doExecuteOnce(curBody, curSA, req.Model)
		if completionErr == nil {
			authUID = curSA.Account.UID
			accountLabel = strings.TrimSpace(curSA.Account.Nickname)
			if accountLabel == "" {
				accountLabel = authUID
			}
			usedAuthID = strings.TrimSpace(curSA.Auth.AccessToken)
			if usedAuthID == "" {
				usedAuthID = strings.TrimSpace(curSA.Account.UID)
			}
			break
		}
		statusCode := parseUpstreamStatusFromErr(completionErr)
		if !shouldRotateOnUpstreamErr(statusCode, completionErr.Error()) || attempt >= budget || curSA == nil {
			break
		}
		currentID := strings.TrimSpace(curSA.Auth.AccessToken)
		if currentID == "" {
			currentID = strings.TrimSpace(curSA.Account.UID)
		}
		_, nextSA, hasNext := pickNextAuth(currentID)
		if !hasNext || nextSA == nil {
			break
		}
		curSA = nextSA
		curBody = prepareUpstreamBody(req.Payload, req.OriginalRequest, curSA, upstreamModel)
		reasoningEffort = reasoningEffortFromBody(curBody)
	}
	if completionErr != nil {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, parseUpstreamStatusFromErr(completionErr), completionErr.Error(), reasoningEffort, 0, accountLabel, sessionKey)
		reconcileAfterExecutorError(usedAuthID, parseUpstreamStatusFromErr(completionErr), completionErr.Error())
		statusCode := parseUpstreamStatusFromErr(completionErr)
		if statusCode == 0 {
			noteAccountFailure(usedAuthID, 0, completionErr.Error())
		} else if statusCode == 200 {
			noteAccountFailure(usedAuthID, http.StatusForbidden, completionErr.Error())
		}
		return nil, completionErr
	}
	publishUsage(req.Model, upstreamModel, authUID, started, usageDetailFromCompletion(completion), false, 0, "", reasoningEffort, 0, accountLabel, sessionKey)
	invalidateAccountCredits(usedAuthID, authUID)
	resetAccountFailover(usedAuthID)
	return okEnvelope(pluginapi.ExecutorResponse{Payload: completion})
}

type streamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

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
	accountLabel := strings.TrimSpace(sa.Account.Nickname)
	if accountLabel == "" {
		accountLabel = authUID
	}
	sessionKey := extractSessionKeyFromSources(req.Headers, req.Metadata)
	body := req.Payload
	if len(body) == 0 {
		body = req.OriginalRequest
	}
	body = prepareUpstreamBody(body, nil, sa, upstreamModel)
	reasoningEffort := reasoningEffortFromBody(body)

	headers := streamHeaders()
	sseFramed := clientNeedsSSEFrame(req.Metadata)

	if req.StreamID == "" {
		collector := &sseUsageCollector{}
		chunks, statusCode, errCollect := collectUpstreamStream(body, sa, sseFramed, collector)
		if errCollect != nil {
			publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, statusCode, errCollect.Error(), reasoningEffort, collector.ttftNS(started), accountLabel, sessionKey)
			if statusCode == 0 {
				noteAccountFailure(req.AuthID, 0, errCollect.Error())
			} else if statusCode < 400 {
				noteAccountFailure(req.AuthID, http.StatusForbidden, errCollect.Error())
			}
			return nil, errCollect
		}
		publishUsage(req.Model, upstreamModel, authUID, started, collector.detail(), false, 0, "", reasoningEffort, collector.ttftNS(started), accountLabel, sessionKey)
		invalidateAccountCredits(req.AuthID, authUID)
		resetAccountFailover(req.AuthID)
		return okEnvelope(streamResponse{Headers: headers, Chunks: chunks})
	}

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

func writeResponse(resp *C.cliproxy_buffer, data []byte) {
	if resp == nil || len(data) == 0 {
		return
	}
	resp.ptr = C.CBytes(data)
	resp.len = C.size_t(len(data))
}

func okEnvelope(v any) ([]byte, error) {
	result, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: result})
}

func errorEnvelope(code, msg string) []byte {
	b, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: msg}})
	return b
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func labelForAuth(sa *storedAuth) string {
	if sa == nil {
		return ""
	}
	if sa.Account.Nickname != "" {
		return sa.Account.Nickname
	}
	if sa.Account.UID != "" {
		return sa.Account.UID
	}
	return "workbuddy-ai"
}
