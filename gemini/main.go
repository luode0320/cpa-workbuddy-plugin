// Package main implements the Gemini Provider CLIProxyAPI dynamic plugin.
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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	geminicli "github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/plugin"
	"github.com/tidwall/gjson"
)

const (
	providerName      = "gemini-provider"
	pluginDisplayName = "Gemini Provider"
	pluginLogoURL     = "https://raw.githubusercontent.com/luode0320/cpa-workbuddy-plugin/main/assets/icons/Gemini.png"
)

var version = "0.1.0"

var abiState = struct {
	sync.RWMutex
	host   *C.cliproxy_host_api
	plugin *geminicli.GeminiCLIPlugin
}{}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil {
		return 1
	}
	abiState.Lock()
	abiState.host = host
	abiState.Unlock()
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
	abiState.Lock()
	abiState.plugin = nil
	abiState.host = nil
	abiState.Unlock()
}

type abiLifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type abiRegistration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  abiCapabilities    `json:"capabilities"`
}

type abiCapabilities struct {
	AuthProvider          bool                         `json:"auth_provider"`
	ModelProvider         bool                         `json:"model_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats,omitempty"`
	RequestTranslator     bool                         `json:"request_translator"`
	ResponseTranslator    bool                         `json:"response_translator"`
	ThinkingApplier       bool                         `json:"thinking_applier"`
	CommandLinePlugin     bool                         `json:"command_line_plugin"`
	UsagePlugin           bool                         `json:"usage_plugin"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type abiAuthLoginStartRequest struct {
	pluginapi.AuthLoginStartRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiAuthLoginPollRequest struct {
	pluginapi.AuthLoginPollRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiAuthRefreshRequest struct {
	pluginapi.AuthRefreshRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiAuthModelRequest struct {
	pluginapi.AuthModelRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiExecutorRequest struct {
	pluginapi.ExecutorRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
	StreamID       string `json:"stream_id,omitempty"`
}

type abiExecutorHTTPRequest struct {
	pluginapi.ExecutorHTTPRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiThinkingApplyRequest struct {
	pluginapi.ThinkingApplyRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiExecutorStreamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

type abiHostHTTPRequest struct {
	pluginapi.HTTPRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiHostHTTPStreamResponse struct {
	StatusCode int                         `json:"status_code"`
	Headers    http.Header                 `json:"headers,omitempty"`
	StreamID   string                      `json:"stream_id,omitempty"`
	Chunks     []pluginapi.HTTPStreamChunk `json:"chunks,omitempty"`
}

type abiHostHTTPStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}

type abiHostHTTPStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

type abiHostHTTPStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
}

type abiHostStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
}

type abiHostStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

type abiEmptyResponse struct{}

func handleMethod(method string, request []byte) ([]byte, error) {
	ctx := context.Background()
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return handleRegister(request)
	case pluginabi.MethodUsageHandle:
		return handleUsage(request)
	}
	p, errPlugin := currentPlugin()
	if errPlugin != nil {
		return nil, errPlugin
	}
	switch method {
	case pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier, pluginabi.MethodThinkingIdentifier:
		return okEnvelope(identifierResponse{Identifier: p.Identifier()})
	case pluginabi.MethodAuthParse:
		var req pluginapi.AuthParseRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.ParseAuth(ctx, req)
		return okEnvelopeWithError(resp, errCall)
	case pluginabi.MethodAuthLoginStart:
		var rpcReq abiAuthLoginStartRequest
		if errDecode := json.Unmarshal(request, &rpcReq); errDecode != nil {
			return nil, errDecode
		}
		req := rpcReq.AuthLoginStartRequest
		req.HTTPClient = abiHostHTTPClient{callbackID: rpcReq.HostCallbackID}
		resp, errCall := p.StartLogin(ctx, req)
		return okEnvelopeWithError(resp, errCall)
	case pluginabi.MethodAuthLoginPoll:
		var rpcReq abiAuthLoginPollRequest
		if errDecode := json.Unmarshal(request, &rpcReq); errDecode != nil {
			return nil, errDecode
		}
		req := rpcReq.AuthLoginPollRequest
		req.HTTPClient = abiHostHTTPClient{callbackID: rpcReq.HostCallbackID}
		resp, errCall := p.PollLogin(ctx, req)
		return okEnvelopeWithError(resp, errCall)
	case pluginabi.MethodAuthRefresh:
		var rpcReq abiAuthRefreshRequest
		if errDecode := json.Unmarshal(request, &rpcReq); errDecode != nil {
			return nil, errDecode
		}
		req := rpcReq.AuthRefreshRequest
		req.HTTPClient = abiHostHTTPClient{callbackID: rpcReq.HostCallbackID}
		resp, errCall := p.RefreshAuth(ctx, req)
		return okEnvelopeWithError(resp, errCall)
	case pluginabi.MethodModelStatic:
		var req pluginapi.StaticModelRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.StaticModels(ctx, req)
		return okEnvelopeWithError(resp, errCall)
	case pluginabi.MethodModelForAuth:
		var rpcReq abiAuthModelRequest
		if errDecode := json.Unmarshal(request, &rpcReq); errDecode != nil {
			return nil, errDecode
		}
		req := rpcReq.AuthModelRequest
		req.HTTPClient = abiHostHTTPClient{callbackID: rpcReq.HostCallbackID}
		resp, errCall := p.ModelsForAuth(ctx, req)
		return okEnvelopeWithError(resp, errCall)
	case pluginabi.MethodRequestTranslate:
		var req pluginapi.RequestTransformRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.TranslateRequest(ctx, req)
		return okEnvelopeWithError(resp, errCall)
	case pluginabi.MethodResponseTranslate:
		var req pluginapi.ResponseTransformRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.TranslateResponse(ctx, req)
		return okEnvelopeWithError(resp, errCall)
	case pluginabi.MethodExecutorExecute:
		return handleExecuteWithUsage(ctx, p, request)
	case pluginabi.MethodExecutorExecuteStream:
		return handleExecuteStreamWithUsage(ctx, p, request)
	case pluginabi.MethodExecutorCountTokens:
		var rpcReq abiExecutorRequest
		if errDecode := json.Unmarshal(request, &rpcReq); errDecode != nil {
			return nil, errDecode
		}
		req := rpcReq.ExecutorRequest
		req.HTTPClient = abiHostHTTPClient{callbackID: rpcReq.HostCallbackID}
		resp, errCall := p.CountTokens(ctx, req)
		return okEnvelopeWithError(resp, errCall)
	case pluginabi.MethodExecutorHTTPRequest:
		var rpcReq abiExecutorHTTPRequest
		if errDecode := json.Unmarshal(request, &rpcReq); errDecode != nil {
			return nil, errDecode
		}
		req := rpcReq.ExecutorHTTPRequest
		req.HTTPClient = abiHostHTTPClient{callbackID: rpcReq.HostCallbackID}
		resp, errCall := p.HttpRequest(ctx, req)
		return okEnvelopeWithError(resp, errCall)
	case pluginabi.MethodThinkingApply:
		var rpcReq abiThinkingApplyRequest
		if errDecode := json.Unmarshal(request, &rpcReq); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.ApplyThinking(ctx, rpcReq.ThinkingApplyRequest)
		return okEnvelopeWithError(resp, errCall)
	case pluginabi.MethodCommandLineRegister:
		var req pluginapi.CommandLineRegistrationRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.RegisterCommandLine(ctx, req)
		return okEnvelopeWithError(resp, errCall)
	case pluginabi.MethodCommandLineExecute:
		var req pluginapi.CommandLineExecutionRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.ExecuteCommandLine(ctx, req)
		return okEnvelopeWithError(resp, errCall)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func handleRegister(request []byte) ([]byte, error) {
	var req abiLifecycleRequest
	if errDecode := json.Unmarshal(request, &req); errDecode != nil {
		return nil, errDecode
	}
	configureUsageFeed(req.ConfigYAML)
	plugin := geminicli.Build(req.ConfigYAML)
	plugin.Metadata.Name = pluginDisplayName
	plugin.Metadata.Version = version
	plugin.Metadata.Author = "router-for-me / luode"
	plugin.Metadata.GitHubRepository = "https://github.com/luode0320/cpa-workbuddy-plugin"
	plugin.Metadata.Logo = pluginLogoURL
	plugin.Metadata.ConfigFields = append(plugin.Metadata.ConfigFields,
		pluginapi.ConfigField{Name: "usage_feed_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用 shared NDJSON 用量记录（默认开启），供 token-usage-tracker 插件消费。"},
		pluginapi.ConfigField{Name: "usage_feed_path", Type: pluginapi.ConfigFieldTypeString, Description: "共享用量 feed 路径（默认 <CLIProxyAPI root>/data/token-usage-feed.ndjson）。"},
	)

	p, ok := plugin.Capabilities.AuthProvider.(*geminicli.GeminiCLIPlugin)
	if !ok || p == nil {
		return nil, fmt.Errorf("gemini-cli plugin registration returned invalid auth provider")
	}
	abiState.Lock()
	abiState.plugin = p
	abiState.Unlock()
	return okEnvelope(abiRegistration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata:      plugin.Metadata,
		Capabilities: abiCapabilities{
			AuthProvider:          plugin.Capabilities.AuthProvider != nil,
			ModelProvider:         plugin.Capabilities.ModelProvider != nil,
			Executor:              plugin.Capabilities.Executor != nil,
			ExecutorModelScope:    plugin.Capabilities.ExecutorModelScope,
			ExecutorInputFormats:  append([]string(nil), plugin.Capabilities.ExecutorInputFormats...),
			ExecutorOutputFormats: append([]string(nil), plugin.Capabilities.ExecutorOutputFormats...),
			RequestTranslator:     plugin.Capabilities.RequestTranslator != nil,
			ResponseTranslator:    plugin.Capabilities.ResponseTranslator != nil,
			ThinkingApplier:       plugin.Capabilities.ThinkingApplier != nil,
			CommandLinePlugin:     plugin.Capabilities.CommandLinePlugin != nil,
			UsagePlugin:           true,
		},
	})
}

func handleExecuteWithUsage(ctx context.Context, p *geminicli.GeminiCLIPlugin, request []byte) ([]byte, error) {
	var rpcReq abiExecutorRequest
	if errDecode := json.Unmarshal(request, &rpcReq); errDecode != nil {
		return nil, errDecode
	}
	req := rpcReq.ExecutorRequest
	req.HTTPClient = abiHostHTTPClient{callbackID: rpcReq.HostCallbackID}
	started := time.Now()
	accountLabel := extractAccountLabel(req.StorageJSON, req.AuthID)

	resp, errCall := p.Execute(ctx, req)
	if errCall != nil {
		statusCode := 500
		if se, ok := errCall.(interface{ StatusCode() int }); ok {
			statusCode = se.StatusCode()
		}
		publishUsage(req.Model, req.Model, req.AuthID, started, extractGeminiUsage(nil), true, statusCode, errCall.Error(), "", 0, accountLabel, "")
		return okEnvelopeWithError(resp, errCall)
	}
	detail := extractGeminiUsage(resp.Payload)
	publishUsage(req.Model, req.Model, req.AuthID, started, detail, false, 200, "", "", 0, accountLabel, "")
	return okEnvelope(resp)
}

func handleExecuteStreamWithUsage(ctx context.Context, p *geminicli.GeminiCLIPlugin, request []byte) ([]byte, error) {
	var rpcReq abiExecutorRequest
	if errDecode := json.Unmarshal(request, &rpcReq); errDecode != nil {
		return nil, errDecode
	}
	req := rpcReq.ExecutorRequest
	req.HTTPClient = abiHostHTTPClient{callbackID: rpcReq.HostCallbackID}
	started := time.Now()
	accountLabel := extractAccountLabel(req.StorageJSON, req.AuthID)

	resp, errCall := p.ExecuteStream(ctx, req)
	if errCall != nil {
		statusCode := 500
		if se, ok := errCall.(interface{ StatusCode() int }); ok {
			statusCode = se.StatusCode()
		}
		publishUsage(req.Model, req.Model, req.AuthID, started, extractGeminiUsage(nil), true, statusCode, errCall.Error(), "", 0, accountLabel, "")
		return nil, errCall
	}

	streamResp, errMarshal := marshalStreamResponseWithUsage(ctx, rpcReq.StreamID, resp, req.Model, req.AuthID, accountLabel, started)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return okEnvelope(streamResp)
}

func marshalStreamResponseWithUsage(ctx context.Context, streamID string, resp pluginapi.ExecutorStreamResponse, model, authID, accountLabel string, started time.Time) (abiExecutorStreamResponse, error) {
	collector := &geminiStreamUsageCollector{}
	if streamID == "" {
		chunks := make([]pluginapi.ExecutorStreamChunk, 0)
		hasErr := false
		var finalErr error
		for chunk := range resp.Chunks {
			if chunk.Err != nil {
				hasErr = true
				finalErr = chunk.Err
			}
			if len(chunk.Payload) > 0 {
				collector.feed(chunk.Payload)
			}
			chunks = append(chunks, chunk)
		}
		ttftNS := collector.ttftNS(started)
		if hasErr {
			publishUsage(model, model, authID, started, collector.detail, true, 500, finalErr.Error(), "", ttftNS, accountLabel, "")
		} else {
			publishUsage(model, model, authID, started, collector.detail, false, 200, "", "", ttftNS, accountLabel, "")
		}
		return abiExecutorStreamResponse{Headers: resp.Headers, Chunks: chunks}, nil
	}
	go pumpStreamWithUsage(ctx, streamID, resp.Chunks, collector, model, authID, accountLabel, started)
	return abiExecutorStreamResponse{Headers: resp.Headers}, nil
}

func pumpStreamWithUsage(ctx context.Context, streamID string, chunks <-chan pluginapi.ExecutorStreamChunk, collector *geminiStreamUsageCollector, model, authID, accountLabel string, started time.Time) {
	errorMessage := ""
	failed := false
	defer func() {
		ttftNS := collector.ttftNS(started)
		status := 200
		if failed {
			status = 500
		}
		publishUsage(model, model, authID, started, collector.detail, failed, status, errorMessage, "", ttftNS, accountLabel, "")
		_, _ = callHost[abiEmptyResponse](pluginabi.MethodHostStreamClose, abiHostStreamCloseRequest{StreamID: streamID, Error: errorMessage})
	}()
	for {
		select {
		case <-ctx.Done():
			if errCtx := ctx.Err(); errCtx != nil {
				errorMessage = errCtx.Error()
				failed = true
			}
			return
		case chunk, ok := <-chunks:
			if !ok {
				return
			}
			if chunk.Err != nil {
				errorMessage = chunk.Err.Error()
				failed = true
				return
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			collector.feed(chunk.Payload)
			_, errCall := callHost[abiEmptyResponse](pluginabi.MethodHostStreamEmit, abiHostStreamEmitRequest{
				StreamID: streamID,
				Payload:  append([]byte(nil), chunk.Payload...),
			})
			if errCall != nil {
				errorMessage = errCall.Error()
				failed = true
				return
			}
		}
	}
}

func extractAccountLabel(storageJSON []byte, authID string) string {
	if len(storageJSON) == 0 {
		return authID
	}
	email := gjson.GetBytes(storageJSON, "email").String()
	proj := gjson.GetBytes(storageJSON, "project_id").String()
	if email != "" && proj != "" {
		return fmt.Sprintf("%s (%s)", email, proj)
	}
	if email != "" {
		return email
	}
	if proj != "" {
		return proj
	}
	return authID
}

func currentPlugin() (*geminicli.GeminiCLIPlugin, error) {
	abiState.RLock()
	defer abiState.RUnlock()
	if abiState.plugin == nil {
		return nil, fmt.Errorf("gemini-cli plugin is not registered")
	}
	return abiState.plugin, nil
}

type abiHostHTTPClient struct {
	callbackID string
}

func (c abiHostHTTPClient) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return callHost[pluginapi.HTTPResponse](pluginabi.MethodHostHTTPDo, abiHostHTTPRequest{
		HTTPRequest:    req,
		HostCallbackID: c.callbackID,
	})
}

func (c abiHostHTTPClient) DoStream(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	resp, errCall := callHost[abiHostHTTPStreamResponse](pluginabi.MethodHostHTTPDoStream, abiHostHTTPRequest{
		HTTPRequest:    req,
		HostCallbackID: c.callbackID,
	})
	if errCall != nil {
		return pluginapi.HTTPStreamResponse{}, errCall
	}
	if resp.StreamID != "" {
		chunks := make(chan pluginapi.HTTPStreamChunk)
		go readHostHTTPStream(ctx, resp.StreamID, chunks)
		return pluginapi.HTTPStreamResponse{StatusCode: resp.StatusCode, Headers: resp.Headers, Chunks: chunks}, nil
	}
	chunks := make(chan pluginapi.HTTPStreamChunk, len(resp.Chunks))
	for _, chunk := range resp.Chunks {
		chunks <- chunk
	}
	close(chunks)
	return pluginapi.HTTPStreamResponse{StatusCode: resp.StatusCode, Headers: resp.Headers, Chunks: chunks}, nil
}

func readHostHTTPStream(ctx context.Context, streamID string, out chan<- pluginapi.HTTPStreamChunk) {
	defer close(out)
	for {
		select {
		case <-ctx.Done():
			closeHostHTTPStream(streamID)
			return
		default:
		}
		resp, errRead := callHost[abiHostHTTPStreamReadResponse](pluginabi.MethodHostHTTPStreamRead, abiHostHTTPStreamReadRequest{StreamID: streamID})
		if errRead != nil {
			closeHostHTTPStream(streamID)
			out <- pluginapi.HTTPStreamChunk{Err: errRead}
			return
		}
		if resp.Error != "" {
			out <- pluginapi.HTTPStreamChunk{Err: fmt.Errorf("%s", resp.Error)}
			return
		}
		if len(resp.Payload) > 0 {
			out <- pluginapi.HTTPStreamChunk{Payload: append([]byte(nil), resp.Payload...)}
		}
		if resp.Done {
			return
		}
	}
}

func closeHostHTTPStream(streamID string) {
	_, _ = callHost[abiEmptyResponse](pluginabi.MethodHostHTTPStreamClose, abiHostHTTPStreamCloseRequest{StreamID: streamID})
}

func callHost[T any](method string, request any) (T, error) {
	var zero T
	abiState.RLock()
	host := abiState.host
	abiState.RUnlock()
	if host == nil || host.call == nil {
		return zero, fmt.Errorf("host callback is unavailable")
	}
	rawRequest, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		return zero, errMarshal
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var requestPtr *C.uint8_t
	if len(rawRequest) > 0 {
		requestPtr = (*C.uint8_t)(unsafe.Pointer(&rawRequest[0]))
	}
	var resp C.cliproxy_buffer
	code := C.wb_call_host(host, cMethod, requestPtr, C.size_t(len(rawRequest)), &resp)
	if resp.ptr != nil {
		defer C.wb_free_host_buffer(host, resp.ptr, resp.len)
	}
	if code != 0 {
		return zero, fmt.Errorf("host callback %s failed with code %d", method, int(code))
	}
	rawResp := C.GoBytes(resp.ptr, C.int(resp.len))
	var envelope pluginabi.Envelope
	if errDecode := json.Unmarshal(rawResp, &envelope); errDecode != nil {
		return zero, errDecode
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return zero, fmt.Errorf("%s", envelope.Error.Message)
		}
		return zero, fmt.Errorf("host callback %s failed", method)
	}
	var out T
	if len(envelope.Result) == 0 {
		return out, nil
	}
	if errDecode := json.Unmarshal(envelope.Result, &out); errDecode != nil {
		return zero, errDecode
	}
	return out, nil
}

func okEnvelope(v any) ([]byte, error) {
	result, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(pluginabi.Envelope{OK: true, Result: result})
}

func okEnvelopeWithError(v any, err error) ([]byte, error) {
	if err != nil {
		return errorEnvelopeFromError("plugin_error", err), nil
	}
	return okEnvelope(v)
}

func errorEnvelopeFromError(code string, err error) []byte {
	if err == nil {
		return errorEnvelope(code, "")
	}
	httpStatus := 0
	if statusProvider, ok := err.(interface{ StatusCode() int }); ok && statusProvider != nil {
		httpStatus = statusProvider.StatusCode()
	}
	return errorEnvelopeWithStatus(code, err.Error(), httpStatus)
}

func errorEnvelope(code string, message string) []byte {
	return errorEnvelopeWithStatus(code, message, 0)
}

func errorEnvelopeWithStatus(code string, message string, httpStatus int) []byte {
	raw, _ := json.Marshal(pluginabi.Envelope{
		OK: false,
		Error: &pluginabi.Error{
			Code:       code,
			Message:    message,
			HTTPStatus: httpStatus,
		},
	})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, data []byte) {
	if response == nil {
		return
	}
	if len(data) == 0 {
		response.ptr = nil
		response.len = 0
		return
	}
	ptr := C.CBytes(data)
	if ptr == nil {
		response.ptr = nil
		response.len = 0
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(data))
}
