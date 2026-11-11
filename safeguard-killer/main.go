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

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	stored_host->free_buffer(ptr, len);
}
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unsafe"
)

// version 由 CI 在链接期用 -ldflags -X main.version 注入；本地构建回退到下面的默认值。
var version = "0.1.5"

// pluginVersion 在包初始化阶段继承 version，保证 -X 注入值对注册元数据生效。
var pluginVersion = version

const (
	abiVersion uint32 = 1
	// schemaVersion 是插件声明的宿主契约版本，必须 <= 宿主 sdk/pluginabi.SchemaVersion（生产宿主为 6）。
	// 宿主在 registerRPCPlugin 中对更高版本直接拒绝注册，写超版本会让插件整体不可用。
	schemaVersion uint32 = 6

	pluginName   = "Safeguard Killer"
	pluginAuthor = "luode0320"
	pluginRepo   = "https://github.com/luode0320/cpa-workbuddy-plugin"
	pluginLogo   = "https://raw.githubusercontent.com/luode0320/cpa-workbuddy-plugin/main/assets/icons/SafeguardKiller.png"

	methodPluginRegister     = "plugin.register"
	methodPluginReconfigure  = "plugin.reconfigure"
	methodPluginQuiesce      = "plugin.quiesce"
	methodPluginShutdown     = "plugin.shutdown"
	methodSchedulerPick      = "scheduler.pick"
	methodModelRegister      = "model.register"
	methodModelRoute         = "model.route"
	methodManagementRegister = "management.register"
	methodManagementHandle   = "management.handle"
	methodExecutorIdentifier = "executor.identifier"
	methodExecutorExecute    = "executor.execute"
	methodExecutorStream     = "executor.execute_stream"
	methodExecutorCount      = "executor.count_tokens"
	methodExecutorHTTP       = "executor.http_request"
	methodRequestBefore      = "request.intercept_before"
	methodRequestAfter       = "request.intercept_after"
	methodStreamIntercept    = "response.intercept_stream_chunk"
	methodRequestComplete    = "request.complete"
)

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

// statusError is an error with the HTTP status the host should surface to the client.
type statusError struct {
	Code       string
	Message    string
	HTTPStatus int
}

func (e *statusError) Error() string {
	return e.Message
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(abiVersion)
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
		var errStatus *statusError
		if errors.As(errHandle, &errStatus) {
			rawErr, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: errStatus.Code, Message: errStatus.Message, HTTPStatus: errStatus.HTTPStatus}})
			writeResponse(response, rawErr)
			return 1
		}
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
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case methodPluginRegister, methodPluginReconfigure:
		if errConfig := configurePlugin(request); errConfig != nil {
			return nil, errConfig
		}
		return okEnvelope(pluginRegistration())
	case methodPluginQuiesce, methodPluginShutdown:
		return okEnvelope(struct{}{})
	case methodSchedulerPick:
		var req schedulerPickRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		resp, errPick := pick(req, currentConfig(), time.Now())
		if errPick != nil {
			return nil, errPick
		}
		return okEnvelope(resp)
	case methodModelRegister:
		return okEnvelope(groupModelRegistration(currentConfig()))
	case methodModelRoute:
		return routeGroup(request, currentConfig())
	case methodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": groupProvider})
	case methodExecutorExecute:
		return executeGroup(request, currentConfig())
	case methodExecutorStream:
		return executeGroupStream(request, currentConfig())
	case methodExecutorCount:
		return countGroupTokens(request, currentConfig())
	case methodExecutorHTTP:
		return nil, &statusError{Code: "not_supported", Message: method + " is not supported for model groups", HTTPStatus: 501}
	case methodRequestBefore, methodRequestAfter:
		return handleRequestIntercept(request)
	case methodStreamIntercept:
		return handleStreamIntercept(request)
	case methodRequestComplete:
		return handleStreamCompletion(request)
	case methodManagementRegister:
		return okEnvelope(managementRegistrationPayload())
	case methodManagementHandle:
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback payload %s: %w", method, errMarshal)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	cPayload := C.CBytes(rawPayload)
	defer C.free(cPayload)

	var response C.cliproxy_buffer
	callCode := C.call_host_api(cMethod, (*C.uint8_t)(cPayload), C.size_t(len(rawPayload)), &response)
	rawResponse := C.GoBytes(response.ptr, C.int(response.len))
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}

	var env envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &env); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host callback envelope %s (code=%d): %w", method, int(callCode), errUnmarshal)
	}
	if !env.OK {
		return nil, &statusError{Code: env.Error.Code, Message: fmt.Sprintf("host callback %s: %s", method, env.Error.Message), HTTPStatus: env.Error.HTTPStatus}
	}
	return env.Result, nil
}

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
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
