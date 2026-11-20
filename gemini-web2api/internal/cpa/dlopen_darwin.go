//go:build darwin

package cpa

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
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

typedef int (*cliproxy_plugin_init_fn)(const cliproxy_host_api*, cliproxy_plugin_api*);

static int mock_host_call(void* ctx, const char* method, const uint8_t* req, size_t req_len, cliproxy_buffer* resp) {
	const char* ok = "{\"ok\":true}";
	size_t len = strlen(ok);
	char* buf = (char*)malloc(len);
	memcpy(buf, ok, len);
	resp->ptr = buf;
	resp->len = len;
	return 0;
}

static void mock_host_free(void* ptr, size_t len) {
	if (ptr != NULL) {
		free(ptr);
	}
}

static int test_dlopen_plugin(const char* path, const char* method, const char* payload, char** out_json, char** err_msg) {
	void* handle = dlopen(path, RTLD_NOW | RTLD_LOCAL);
	if (handle == NULL) {
		const char* e = dlerror();
		*err_msg = strdup(e ? e : "dlopen failed");
		return -1;
	}
	cliproxy_plugin_init_fn init_fn = (cliproxy_plugin_init_fn)dlsym(handle, "cliproxy_plugin_init");
	if (init_fn == NULL) {
		*err_msg = strdup("cliproxy_plugin_init symbol not found");
		dlclose(handle);
		return -2;
	}

	cliproxy_host_api host = {0};
	host.abi_version = 1;
	host.call = mock_host_call;
	host.free_buffer = mock_host_free;

	cliproxy_plugin_api plugin = {0};
	int rc = init_fn(&host, &plugin);
	if (rc != 0) {
		*err_msg = strdup("cliproxy_plugin_init returned non-zero");
		dlclose(handle);
		return rc;
	}

	cliproxy_buffer resp = {0};
	rc = plugin.call((char*)method, (uint8_t*)payload, strlen(payload), &resp);
	if (rc != 0) {
		*err_msg = strdup("plugin.call returned non-zero");
		if (plugin.shutdown) plugin.shutdown();
		return rc;
	}

	if (resp.ptr != NULL && resp.len > 0) {
		char* copy = (char*)malloc(resp.len + 1);
		memcpy(copy, resp.ptr, resp.len);
		copy[resp.len] = '\0';
		*out_json = copy;
		plugin.free_buffer(resp.ptr, resp.len);
	}
	if (plugin.shutdown) {
		plugin.shutdown();
	}
	return 0;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func CallSharedLibraryABI(dylibPath, method, payload string) (string, error) {
	cPath := C.CString(dylibPath)
	defer C.free(unsafe.Pointer(cPath))
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	cPayload := C.CString(payload)
	defer C.free(unsafe.Pointer(cPayload))

	var outJSON *C.char
	var errMsg *C.char
	rc := C.test_dlopen_plugin(cPath, cMethod, cPayload, &outJSON, &errMsg)
	if errMsg != nil {
		defer C.free(unsafe.Pointer(errMsg))
	}
	if outJSON != nil {
		defer C.free(unsafe.Pointer(outJSON))
	}
	if rc != 0 {
		return "", fmt.Errorf("dlopen ABI call failed (rc=%d): %s", int(rc), C.GoString(errMsg))
	}
	return C.GoString(outJSON), nil
}
