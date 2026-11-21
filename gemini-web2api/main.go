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

static int invoke_host_call(const cliproxy_host_api* host, const char* method, const uint8_t* req, size_t req_len, cliproxy_buffer* resp) {
	if (host == NULL || host->call == NULL) {
		return -1;
	}
	return host->call(host->host_ctx, method, req, req_len, resp);
}

static void invoke_host_free(const cliproxy_host_api* host, void* ptr, size_t len) {
	if (host == NULL || host->free_buffer == NULL || ptr == NULL) {
		return;
	}
	host->free_buffer(ptr, len);
}

static void fill_plugin_api(cliproxy_plugin_api* out) {
	if (out == NULL) {
		return;
	}
	out->abi_version = 1;
	out->call = cliproxyPluginCall;
	out->free_buffer = cliproxyPluginFree;
	out->shutdown = cliproxyPluginShutdown;
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/app"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/cpa"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginabi"
)

var (
	// version 当前插件版本号，由 CI 的 -X main.version 注入，本地构建回落到 VERSION 文件同值。
	version    = "1.0.15"
	hostAPI    atomic.Pointer[C.cliproxy_host_api]
	pluginMu   sync.Mutex
	pluginInst *cpa.Plugin
)

func getPlugin() *cpa.Plugin {
	pluginMu.Lock()
	defer pluginMu.Unlock()
	if pluginInst == nil {
		app.SetVersion(version)
		pluginInst = cpa.NewPlugin(callHostABI)
	}
	return pluginInst
}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, out *C.cliproxy_plugin_api) C.int {
	return cliproxyPluginInit(host, out)
}

//export cliproxyPluginInit
func cliproxyPluginInit(host *C.cliproxy_host_api, out *C.cliproxy_plugin_api) C.int {
	if host == nil || out == nil || uint32(host.abi_version) != pluginabi.ABIVersion {
		return -1
	}
	hostAPI.Store(host)
	getPlugin().SetHostCall(callHostABI)
	C.fill_plugin_api(out)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, reqPtr *C.uint8_t, reqLen C.size_t, resp *C.cliproxy_buffer) C.int {
	if method == nil || resp == nil {
		return -1
	}
	methodName := C.GoString(method)
	var raw []byte
	if reqPtr != nil && reqLen > 0 {
		raw = C.GoBytes(unsafe.Pointer(reqPtr), C.int(reqLen))
	}

	result, err := safeHandleMethod(methodName, raw)
	if err != nil {
		if pluginErr, ok := err.(*pluginabi.Error); ok && pluginErr != nil {
			rawErr, _ := pluginabi.NewErrorEnvelope(pluginErr.Code, pluginErr.Message, pluginErr.HTTPStatus)
			writeResponse(resp, rawErr)
			return 0
		}
		rawErr, _ := pluginabi.NewErrorEnvelope("plugin_error", err.Error())
		writeResponse(resp, rawErr)
		return 0
	}

	resBytes, err := json.Marshal(result)
	if err != nil {
		rawErr, _ := pluginabi.NewErrorEnvelope("marshal_error", err.Error())
		writeResponse(resp, rawErr)
		return 0
	}
	payload, err := json.Marshal(pluginabi.Envelope{OK: true, Result: json.RawMessage(resBytes)})
	if err != nil {
		rawErr, _ := pluginabi.NewErrorEnvelope("marshal_error", err.Error())
		writeResponse(resp, rawErr)
		return 0
	}
	writeResponse(resp, payload)
	return 0
}

func safeHandleMethod(method string, raw []byte) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = pluginabi.NewError("plugin_panic", fmt.Sprintf("panic in %s: %v", method, r), 500)
		}
	}()
	return getPlugin().HandleMethod(method, raw)
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	pluginMu.Lock()
	inst := pluginInst
	pluginInst = nil
	pluginMu.Unlock()
	if inst != nil {
		_, _ = inst.HandleMethod(pluginabi.MethodPluginShutdown, nil)
	}
	hostAPI.Store(nil)
}

func writeResponse(resp *C.cliproxy_buffer, raw []byte) {
	if resp == nil || len(raw) == 0 {
		if resp != nil {
			resp.ptr = nil
			resp.len = 0
		}
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		resp.ptr = nil
		resp.len = 0
		return
	}
	resp.ptr = ptr
	resp.len = C.size_t(len(raw))
}

func callHostABI(method string, payload []byte) ([]byte, error) {
	host := hostAPI.Load()
	if host == nil {
		return nil, fmt.Errorf("host API is not initialized")
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var reqPtr *C.uint8_t
	var reqLen C.size_t
	if len(payload) > 0 {
		buf := C.CBytes(payload)
		defer C.free(buf)
		reqPtr = (*C.uint8_t)(buf)
		reqLen = C.size_t(len(payload))
	}

	var response C.cliproxy_buffer
	rc := C.invoke_host_call(host, cMethod, reqPtr, reqLen, &response)
	if rc != 0 {
		if response.ptr != nil {
			C.invoke_host_free(host, response.ptr, response.len)
		}
		return nil, fmt.Errorf("host call %s returned code %d", method, int(rc))
	}
	if response.ptr == nil || response.len == 0 {
		return json.Marshal(pluginabi.Envelope{OK: true})
	}
	defer C.invoke_host_free(host, response.ptr, response.len)
	return C.GoBytes(response.ptr, C.int(response.len)), nil
}

func main() {}
