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

	"github.com/luode0320/cpa-workbuddy-plugin/zcode/internal/mimic"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	providerName      = "zcode-provider"
	pluginDisplayName = "ZCode Provider"
	pluginLogoURL     = "https://raw.githubusercontent.com/luode0320/cpa-workbuddy-plugin/main/assets/icons/ZCode.png"
	defaultBaseURL    = "https://open.bigmodel.cn/api/anthropic"
)

var (
	version      = "0.1.0"
	hostAPI      *C.cliproxy_host_api
	hostAPIMu    sync.RWMutex
	activeConfig atomic.Value
)

func init() {
	activeConfig.Store(defaultConfig())
}

func main() {}

type Config struct {
	APIKey         string            `yaml:"api_key" json:"api_key"`
	BaseURL        string            `yaml:"base_url" json:"base_url"`
	ModelMap       map[string]string `yaml:"model_map" json:"model_map"`
	Models         []string          `yaml:"models" json:"models"`
	DynamicModels  *bool             `yaml:"dynamic_models" json:"dynamic_models"`
	ModelCacheSecs int               `yaml:"model_cache_seconds" json:"model_cache_seconds"`
	TimeoutSeconds int               `yaml:"timeout_seconds" json:"timeout_seconds"`
	Mimic          mimic.Config      `yaml:"mimic" json:"mimic"`
}

func defaultConfig() Config {
	return Config{
		BaseURL:        defaultBaseURL,
		Models:         append([]string(nil), defaultModels...),
		ModelCacheSecs: 300,
		TimeoutSeconds: 300,
		Mimic:          mimic.DefaultConfig(),
	}
}

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
}

func zcodeRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             providerName,
			Version:          version,
			Author:           "Sliverkiss (cpa-workbuddy-plugin)",
			GitHubRepository: "https://github.com/luode0320/cpa-workbuddy-plugin",
			Logo:             pluginLogoURL,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "api_key", Type: pluginapi.ConfigFieldTypeString, Description: "Fixed BigModel API key. Leave empty to use managed accounts."},
				{Name: "base_url", Type: pluginapi.ConfigFieldTypeString, Description: "BigModel Anthropic-compatible upstream base URL."},
				{Name: "model_map", Type: pluginapi.ConfigFieldTypeObject, Description: "Requested model to upstream model mapping."},
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Fallback/static model IDs."},
				{Name: "dynamic_models", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Fetch model IDs from GET <base_url>/v1/models."},
				{Name: "model_cache_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Model cache TTL in seconds."},
				{Name: "timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Request timeout in seconds."},
				{Name: "mimic", Type: pluginapi.ConfigFieldTypeObject, Description: "ZCode client fingerprint settings."},
			},
		},
		Capabilities: registrationCapability{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeStatic,
			ExecutorInputFormats:  []string{"anthropic"},
			ExecutorOutputFormats: []string{"anthropic"},
			Scheduler:             true,
			ManagementAPI:         true,
		},
	}
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configurePlugin(request); err != nil {
			return nil, err
		}
		return okEnvelope(zcodeRegistration())

	case pluginabi.MethodModelRegister:
		return okEnvelope(pluginapi.ModelRegistrationResponse{
			Provider: providerName,
			Models:   allConfiguredModels(),
		})

	case pluginabi.MethodModelStatic:
		return okEnvelope(pluginapi.ModelResponse{
			Provider: providerName,
			Models:   allConfiguredModels(),
		})

	case pluginabi.MethodModelForAuth:
		var req pluginapi.AuthModelRequest
		_ = json.Unmarshal(request, &req)
		models := authModelsForIndex(req.AuthID)
		var modelInfos []pluginapi.ModelInfo
		for _, m := range models {
			modelInfos = append(modelInfos, pluginapi.ModelInfo{
				ID:                         m,
				Object:                     "model",
				OwnedBy:                    providerName,
				DisplayName:                m,
				SupportedGenerationMethods: []string{"chat"},
			})
		}
		return okEnvelope(pluginapi.ModelResponse{
			Provider: providerName,
			Models:   modelInfos,
		})

	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerName})

	case pluginabi.MethodAuthParse:
		return handleParseAuth(request)

	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerName})

	case pluginabi.MethodExecutorExecute:
		return handleExecExecute(request)

	case pluginabi.MethodExecutorExecuteStream:
		return handleExecStream(request)

	case pluginabi.MethodManagementRegister:
		var regReq pluginapi.ManagementRegistrationRequest
		if err := json.Unmarshal(request, &regReq); err == nil && regReq.BasePath != "" {
			setManagementBasePath(regReq.BasePath)
		}
		return okEnvelope(managementRegistration())

	case pluginabi.MethodManagementHandle:
		var mgmtReq pluginapi.ManagementRequest
		if err := json.Unmarshal(request, &mgmtReq); err == nil {
			if isPanelPath(mgmtReq.Path) {
				return handlePanelPage(), nil
			}
		}
		return handleManagement(request)

	case pluginabi.MethodSchedulerPick:
		return handleSchedulerPick(request)

	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

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
		if strings.TrimSpace(decoded.APIKey) != "" {
			cfg.APIKey = strings.TrimSpace(decoded.APIKey)
		}
		if strings.TrimSpace(decoded.BaseURL) != "" {
			cfg.BaseURL = strings.TrimRight(strings.TrimSpace(decoded.BaseURL), "/")
		}
		if len(decoded.ModelMap) > 0 {
			cfg.ModelMap = decoded.ModelMap
		}
		if len(decoded.Models) > 0 {
			cfg.Models = decoded.Models
		}
		if decoded.DynamicModels != nil {
			cfg.DynamicModels = decoded.DynamicModels
		}
		if decoded.ModelCacheSecs > 0 {
			cfg.ModelCacheSecs = decoded.ModelCacheSecs
		}
		if decoded.TimeoutSeconds > 0 {
			cfg.TimeoutSeconds = decoded.TimeoutSeconds
		}
		if decoded.Mimic.AppVersion != "" {
			cfg.Mimic.AppVersion = decoded.Mimic.AppVersion
		}
	}
	activeConfig.Store(cfg)
	return nil
}

func handleParseAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	var probe struct {
		Type     string `json:"type"`
		Provider string `json:"provider"`
		APIKey   string `json:"api_key"`
		Label    string `json:"label"`
		Disabled bool   `json:"disabled"`
	}
	_ = json.Unmarshal(req.RawJSON, &probe)

	declared := strings.ToLower(strings.TrimSpace(probe.Type))
	prov := strings.ToLower(strings.TrimSpace(probe.Provider))

	isZCode := declared == providerName || declared == "zcode" ||
		prov == providerName || prov == "zcode" ||
		strings.EqualFold(strings.TrimSpace(req.Provider), providerName) ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.FileName)), zcodeAuthFilePrefix)

	if !isZCode || strings.TrimSpace(probe.APIKey) == "" {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}

	label := strings.TrimSpace(probe.Label)
	if label == "" {
		label = "ZCode " + maskAPIKey(probe.APIKey)
	}

	ad := pluginapi.AuthData{
		Provider:    providerName,
		ID:          "",
		FileName:    strings.TrimSpace(req.FileName),
		Label:       label,
		Disabled:    probe.Disabled,
		StorageJSON: req.RawJSON,
		Metadata: map[string]any{
			"type":     providerName,
			"provider": providerName,
			"logo":     pluginLogoURL,
			"disabled": probe.Disabled,
		},
	}

	return okEnvelope(pluginapi.AuthParseResponse{
		Handled: true,
		Auth:    ad,
	})
}

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
