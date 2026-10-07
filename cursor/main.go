// Package main implements the Cursor CLIProxyAPI dynamic plugin.
//
// cursor wraps Cursor's Connect-RPC agent service as a cliproxy provider: it
// imports Cursor session tokens (WorkosCursorSessionToken, shape
// "user_<id>::<jwt>"), refreshes them through the OAuth token endpoint, routes
// chat-completions traffic to the Cursor agent service with checkpoint/session
// continuity, and exposes Management APIs for account status, usage and
// per-account model controls.
//
// Built with -buildmode=c-shared; exports the cliproxy C ABI entry points.
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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unsafe"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/plugin"
)

const abiVersion uint32 = 1

// version 由构建期通过 -ldflags "-X main.version=..." 注入；默认值仅用于本地。
var version = "0.1.0"

// hostAPI is captured at init and used for host RPC callbacks (stream emit/close
// and host.auth.* used by the Management API).
var hostAPI *C.cliproxy_host_api

var handler = plugin.NewHandler(plugin.Dependencies{Emitter: cStreamEmitter{}, Host: cHostCaller{}})

func init() {
	// 把构建期注入的版本同步给 plugin 包，保证 registration 与二进制一致。
	plugin.SetVersion(version)
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, api *C.cliproxy_plugin_api) C.int {
	if api == nil {
		return 1
	}
	hostAPI = host
	api.abi_version = C.uint32_t(abiVersion)
	api.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	api.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	api.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, []byte("{\"ok\":false,\"error\":{\"code\":\"invalid_method\",\"message\":\"method is required\"}}"))
		return 1
	}
	methodName := C.GoString(method)
	if err := plugin.ValidateExecutorRequestSize(methodName, uint64(requestLen)); err != nil {
		writeResponse(response, []byte("{\"ok\":false,\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"executor envelope exceeds 4 MiB\",\"http_status\":400,\"request_scoped\":true}}"))
		return 1
	}
	var rawRequest []byte
	if request != nil && requestLen > 0 {
		rawRequest = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	rawResponse, ok := handler.CallWithStatus(context.Background(), methodName, rawRequest)
	writeResponse(response, rawResponse)
	if !ok {
		return 1
	}
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(pointer unsafe.Pointer, length C.size_t) {
	if pointer != nil {
		C.free(pointer)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

// cStreamEmitter forwards stream chunks to the host through host.stream.emit/close.
type cStreamEmitter struct{}

// cHostCaller issues host RPC calls for the Management API (host.auth.list/get/save).
type cHostCaller struct{}

func (cHostCaller) Call(ctx context.Context, method string, requestValue any) (json.RawMessage, error) {
	return callHostRaw(ctx, method, requestValue)
}

func (cStreamEmitter) Emit(ctx context.Context, streamID string, payload []byte) error {
	request := struct {
		StreamID string `json:"stream_id"`
		Payload  []byte `json:"payload"`
	}{StreamID: streamID, Payload: payload}
	return callHost(ctx, "host.stream.emit", request)
}

func (cStreamEmitter) Close(streamID string, streamErr error) error {
	request := plugin.NewStreamCloseRequest(streamID, streamErr)
	return callHost(context.Background(), "host.stream.close", request)
}

func callHost(ctx context.Context, method string, requestValue any) error {
	_, err := callHostRaw(ctx, method, requestValue)
	return err
}

func callHostRaw(ctx context.Context, method string, requestValue any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(requestValue)
	if err != nil {
		return nil, fmt.Errorf("encode host callback: %w", err)
	}
	if hostAPI == nil || hostAPI.call == nil {
		return nil, errors.New("host API unavailable")
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var requestPtr *C.uint8_t
	if len(raw) > 0 {
		requestPtr = (*C.uint8_t)(unsafe.Pointer(&raw[0]))
	}
	var response C.cliproxy_buffer
	code := C.wb_call_host(hostAPI, cMethod, requestPtr, C.size_t(len(raw)), &response)
	var responseRaw []byte
	if response.ptr != nil && response.len > 0 {
		responseRaw = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil && hostAPI.free_buffer != nil {
		C.wb_free_host_buffer(hostAPI, response.ptr, response.len)
	}
	if code != 0 {
		return nil, fmt.Errorf("host callback %s returned %d", method, int(code))
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(responseRaw, &envelope); err != nil {
		return nil, fmt.Errorf("decode host callback response: %w", err)
	}
	if !envelope.OK {
		if envelope.Error != nil && envelope.Error.Message != "" {
			return nil, errors.New(envelope.Error.Message)
		}
		return nil, errors.New("host callback failed")
	}
	return append(json.RawMessage(nil), envelope.Result...), nil
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	pointer := C.CBytes(raw)
	if pointer == nil {
		return
	}
	response.ptr = pointer
	response.len = C.size_t(len(raw))
}
