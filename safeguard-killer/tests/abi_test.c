#include <dlfcn.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	void *ptr;
	size_t len;
} cliproxy_buffer;

typedef struct {
	uint32_t abi_version;
	void *host_ctx;
	void *call;
	void *free_buffer;
} cliproxy_host_api;

typedef int (*plugin_call_fn)(const char *, const uint8_t *, size_t,
							  cliproxy_buffer *);
typedef void (*plugin_free_fn)(void *, size_t);

typedef struct {
	uint32_t abi_version;
	plugin_call_fn call;
	plugin_free_fn free_buffer;
	void (*shutdown)(void);
} cliproxy_plugin_api;

typedef int (*plugin_init_fn)(const cliproxy_host_api *, cliproxy_plugin_api *);

static void require_contains(const cliproxy_plugin_api *plugin,
							 const char *method, const char *payload,
							 const char *expected) {
	cliproxy_buffer response = {0};
	const uint8_t *req_ptr = payload ? (const uint8_t *)payload : NULL;
	size_t req_len = payload ? strlen(payload) : 0;
	if (plugin->call(method, req_ptr, req_len, &response) != 0 ||
		response.ptr == NULL) {
		fprintf(stderr, "%s returned no response\n", method);
		exit(1);
	}
	char *text = calloc(response.len + 1, 1);
	if (text == NULL) {
		exit(1);
	}
	memcpy(text, response.ptr, response.len);
	plugin->free_buffer(response.ptr, response.len);
	if (strstr(text, expected) == NULL) {
		fprintf(stderr, "%s response is missing %s: %s\n", method, expected,
				text);
		free(text);
		exit(1);
	}
	free(text);
}

int main(int argc, char **argv) {
	if (argc != 2) {
		fprintf(stderr, "usage: %s PLUGIN\n", argv[0]);
		return 2;
	}
	void *library = dlopen(argv[1], RTLD_NOW | RTLD_LOCAL);
	if (library == NULL) {
		fprintf(stderr, "dlopen: %s\n", dlerror());
		return 1;
	}
	void *symbol = dlsym(library, "cliproxy_plugin_init");
	if (symbol == NULL) {
		fprintf(stderr, "missing cliproxy_plugin_init\n");
		return 1;
	}
	plugin_init_fn init = NULL;
	memcpy(&init, &symbol, sizeof(init));
	cliproxy_plugin_api plugin = {0};
	if (init(NULL, &plugin) != 0 || plugin.abi_version != 1 ||
		plugin.call == NULL || plugin.free_buffer == NULL) {
		fprintf(stderr, "invalid plugin API\n");
		return 1;
	}
	require_contains(&plugin, "plugin.register", NULL, "\"scheduler\":true");
	require_contains(&plugin, "plugin.register", NULL, "\"executor\":true");
	require_contains(&plugin, "executor.identifier", NULL, "model-groups");
	require_contains(&plugin, "scheduler.pick",
					 "{\"Options\":{\"Metadata\":{\"requested_model\":\"m\"}},"
					 "\"Candidates\":[{\"ID\":\"a\",\"Provider\":\"claude\","
					 "\"Metadata\":{\"quota_signals\":null}}]}",
					 "\"Handled\":false");
	plugin.shutdown();
	return 0;
}
