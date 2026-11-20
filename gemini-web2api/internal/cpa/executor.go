package cpa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/app"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/balancer"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginabi"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginapi"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/tokencount"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcExecutorHTTPRequest struct {
	pluginapi.ExecutorHTTPRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcExecutorStreamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

func (p *Plugin) handleExecutorIdentifier() (any, error) {
	return rpcIdentifierResponse{
		Identifier:          ProviderID,
		Identifiers:         []string{"gemini-web2api", "gemini-web", "geminiweb2api"},
		CredentialProviders: []string{"gemini-web2api", "gemini-web", "geminiweb2api"},
	}, nil
}

func (p *Plugin) prepareExecutionContext(
	baseCtx context.Context,
	storageJSON []byte,
	authMetadata map[string]any,
	authAttributes map[string]string,
) (context.Context, *app.PluginRuntime, error) {
	rt, err := p.Runtime()
	if err != nil {
		return nil, nil, err
	}

	accountID := extractAccountIDFromAuth(storageJSON, authMetadata, authAttributes)
	if accountID != "" {
		if actualID, state, enabled, exists := rt.Pool.LookupAccount(accountID); exists && state != balancer.StateAuthRequired {
			if state == balancer.StateInitializing {
				rt.Accounts.WaitFirstInit(baseCtx, actualID)
				_, _, enabled, _ = rt.Pool.LookupAccount(actualID)
			}
			if enabled {
				return app.ContextWithPinnedAccountID(baseCtx, actualID), rt, nil
			}
			return baseCtx, rt, nil
		}
	}

	if len(storageJSON) > 0 {
		var payload AuthFilePayload
		_ = json.Unmarshal(storageJSON, &payload)
		if state, ok := extractStorageStateFromPayload(storageJSON, payload); ok {
			if id, err := p.upsertAccountInRuntime(rt, accountID, payload, state); err == nil && id != "" {
				accountID = strings.ToLower(strings.TrimSpace(id))
			}
		}
	}

	reqCtx := baseCtx
	if accountID != "" {
		if actualID, _, enabled, exists := rt.Pool.LookupAccount(accountID); exists && enabled {
			reqCtx = app.ContextWithPinnedAccountID(reqCtx, actualID)
		}
	}
	return reqCtx, rt, nil
}

func (p *Plugin) buildInternalHTTPRequest(
	ctx context.Context,
	req pluginapi.ExecutorRequest,
	stream bool,
) (*http.Request, error) {
	cleanModel := StripModelPrefix(req.Model, p.Config().ModelPrefix)
	cleanModel = strings.TrimPrefix(cleanModel, "models/")
	if cleanModel == "" && len(req.Payload) > 0 {
		if m := strings.TrimSpace(gjson.GetBytes(req.Payload, "model").String()); m != "" {
			cleanModel = StripModelPrefix(m, p.Config().ModelPrefix)
			cleanModel = strings.TrimPrefix(cleanModel, "models/")
		}
	}

	body := bytes.Clone(req.Payload)
	if len(body) > 0 && cleanModel != "" && gjson.GetBytes(body, "model").Exists() {
		if updated, err := sjson.SetBytes(body, "model", cleanModel); err == nil {
			body = updated
		}
	}

	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = strings.ToLower(strings.TrimSpace(req.SourceFormat))
	}

	targetPath := "/v1/chat/completions"
	targetQuery := url.Values{}
	for k, v := range req.Query {
		targetQuery[k] = append([]string(nil), v...)
	}

	switch format {
	case "gemini":
		if cleanModel == "" {
			cleanModel = "gemini-3.8-flash"
		}
		lowerModel := strings.ToLower(cleanModel)
		switch {
		case gjson.GetBytes(body, "instances").Exists():
			if strings.Contains(lowerModel, "veo") || strings.Contains(lowerModel, "video") {
				targetPath = "/v1beta/models/" + cleanModel + ":predictLongRunning"
			} else {
				targetPath = "/v1beta/models/" + cleanModel + ":predict"
			}
		case stream:
			targetPath = "/v1beta/models/" + cleanModel + ":streamGenerateContent"
			if targetQuery.Get("alt") == "" {
				targetQuery.Set("alt", "sse")
			}
		default:
			targetPath = "/v1beta/models/" + cleanModel + ":generateContent"
		}

	case "claude", "anthropic":
		targetPath = "/v1/messages"
		if len(body) > 0 {
			if cleanModel != "" {
				if updated, err := sjson.SetBytes(body, "model", cleanModel); err == nil {
					body = updated
				}
			}
			if updated, err := sjson.SetBytes(body, "stream", stream); err == nil {
				body = updated
			}
		}

	case "openai-response", "openai_response", "responses", "openai-responses", "openai_responses":
		targetPath = "/v1/responses"
		if len(body) > 0 {
			if cleanModel != "" {
				if updated, err := sjson.SetBytes(body, "model", cleanModel); err == nil {
					body = updated
				}
			}
			if updated, err := sjson.SetBytes(body, "stream", stream); err == nil {
				body = updated
			}
		}

	default:
		lowerModel := strings.ToLower(cleanModel)
		switch {
		case gjson.GetBytes(body, "messages").Exists():
			targetPath = "/v1/chat/completions"
			if cleanModel != "" {
				if updated, err := sjson.SetBytes(body, "model", cleanModel); err == nil {
					body = updated
				}
			}
			if updated, err := sjson.SetBytes(body, "stream", stream); err == nil {
				body = updated
			}
		case gjson.GetBytes(body, "input").Exists() && gjson.GetBytes(body, "voice").Exists():
			targetPath = "/v1/audio/speech"
		case gjson.GetBytes(body, "prompt").Exists() && (strings.Contains(lowerModel, "imagen") || strings.Contains(lowerModel, "image") || strings.Contains(lowerModel, "banana")):
			targetPath = "/v1/images/generations"
		case gjson.GetBytes(body, "prompt").Exists() && (strings.Contains(lowerModel, "veo") || strings.Contains(lowerModel, "video")):
			targetPath = "/v1/videos"
		default:
			targetPath = "/v1/chat/completions"
			if len(body) > 0 {
				if cleanModel != "" {
					if updated, err := sjson.SetBytes(body, "model", cleanModel); err == nil {
						body = updated
					}
				}
				if updated, err := sjson.SetBytes(body, "stream", stream); err == nil {
					body = updated
				}
			}
		}
	}

	fullURL := "http://gemini-web2api.local" + targetPath
	if encodedQuery := targetQuery.Encode(); encodedQuery != "" {
		fullURL += "?" + encodedQuery
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.RemoteAddr = "127.0.0.1:0"
	for k, vals := range req.Headers {
		for _, v := range vals {
			httpReq.Header.Add(k, v)
		}
	}
	httpReq.Header.Del("Origin")
	if httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	return httpReq, nil
}

func (p *Plugin) handleExecutorExecute(raw []byte) (any, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode executor.execute request: %w", err)
	}

	execCtx, cancelExec := context.WithCancel(p.ctx)
	defer cancelExec()
	reqCtx, rt, err := p.prepareExecutionContext(execCtx, req.StorageJSON, req.AuthMetadata, req.AuthAttributes)
	if err != nil {
		return nil, err
	}

	httpReq, err := p.buildInternalHTTPRequest(reqCtx, req.ExecutorRequest, false)
	if err != nil {
		return nil, err
	}

	rec := httptest.NewRecorder()
	rt.Handler.ServeHTTP(rec, httpReq)
	resp := rec.Result()
	respBody := rec.Body.Bytes()

	if resp.StatusCode >= 400 {
		return nil, extractHTTPError(resp.StatusCode, resp.Header, respBody)
	}

	return pluginapi.ExecutorResponse{
		Payload: bytes.Clone(respBody),
		Headers: cloneHTTPHeader(resp.Header),
	}, nil
}

func (p *Plugin) handleExecutorExecuteStream(raw []byte) (any, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode executor.execute_stream request: %w", err)
	}

	reqCtx, rt, err := p.prepareExecutionContext(p.ctx, req.StorageJSON, req.AuthMetadata, req.AuthAttributes)
	if err != nil {
		return nil, err
	}

	streamCtx, cancelStream := context.WithCancel(reqCtx)
	httpReq, err := p.buildInternalHTTPRequest(streamCtx, req.ExecutorRequest, true)
	if err != nil {
		cancelStream()
		return nil, err
	}

	// Only buffer inline when no StreamID or host callback is available; when streaming to the host,
	// adapter.SetResponseModes(..., FakeStream) handles content buffering while liveStreamResponseWriter
	// returns headers first so host.stream.emit cannot deadlock on the host's bounded stream queue.
	if strings.TrimSpace(req.StreamID) == "" || p.hostCall == nil {
		defer cancelStream()
		rec := httptest.NewRecorder()
		rt.Handler.ServeHTTP(rec, httpReq)
		resp := rec.Result()
		respBody := rec.Body.Bytes()
		if resp.StatusCode >= 400 {
			return nil, extractHTTPError(resp.StatusCode, resp.Header, respBody)
		}
		return rpcExecutorStreamResponse{
			Headers: cloneHTTPHeader(resp.Header),
			Chunks:  splitSSEChunks(respBody),
		}, nil
	}

	sw := newLiveStreamResponseWriter(p, streamCtx, req.StreamID, cancelStream)
	defer close(sw.rpcReturned)
	go func() {
		var panicErr *pluginabi.Error
		defer func() {
			if r := recover(); r != nil {
				msg := fmt.Sprintf("panic in stream handler: %v", r)
				panicErr = pluginabi.NewError("plugin_panic", msg, http.StatusInternalServerError)
				sw.WriteHeader(http.StatusInternalServerError)
				if sw.errBuf.Len() == 0 {
					_, _ = sw.errBuf.WriteString(msg)
				}
			}
			sw.finishHandler(panicErr)
			cancelStream()
		}()
		rt.Handler.ServeHTTP(sw, httpReq)
	}()

	const streamHeaderTimeout = 10 * time.Minute
	headerTimer := time.NewTimer(streamHeaderTimeout)
	defer headerTimer.Stop()
	select {
	case <-sw.headerReady:
	case <-streamCtx.Done():
		<-sw.done
		return nil, pluginabi.NewError("request_canceled", "stream canceled before response headers", 499)
	case <-p.ctx.Done():
		cancelStream()
		<-sw.done
		return nil, pluginabi.NewError("service_shutting_down", "plugin shutting down", http.StatusServiceUnavailable)
	case <-headerTimer.C:
		cancelStream()
		<-sw.done
		return nil, pluginabi.NewError("upstream_timeout", "timed out waiting for upstream stream headers", http.StatusGatewayTimeout)
	}
	if sw.statusCode >= 400 {
		<-sw.done
		return nil, extractHTTPError(sw.statusCode, sw.header, sw.errBuf.Bytes())
	}

	return rpcExecutorStreamResponse{
		Headers: sw.clonedHeaders(),
	}, nil
}

type liveStreamResponseWriter struct {
	plugin      *Plugin
	ctx         context.Context
	streamID    string
	cancel      context.CancelFunc
	header      http.Header
	statusCode  int
	headerOnce  sync.Once
	headerReady chan struct{}
	rpcReturned chan struct{}
	done        chan struct{}
	errBuf      bytes.Buffer
}

func newLiveStreamResponseWriter(plugin *Plugin, ctx context.Context, streamID string, cancel context.CancelFunc) *liveStreamResponseWriter {
	return &liveStreamResponseWriter{
		plugin:      plugin,
		ctx:         ctx,
		streamID:    streamID,
		cancel:      cancel,
		header:      make(http.Header),
		statusCode:  http.StatusOK,
		headerReady: make(chan struct{}),
		rpcReturned: make(chan struct{}),
		done:        make(chan struct{}),
	}
}

func (w *liveStreamResponseWriter) Header() http.Header {
	return w.header
}

func (w *liveStreamResponseWriter) WriteHeader(statusCode int) {
	w.headerOnce.Do(func() {
		w.statusCode = statusCode
		close(w.headerReady)
	})
}

func (w *liveStreamResponseWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	if len(p) == 0 {
		return 0, nil
	}
	if w.statusCode >= 400 {
		return w.errBuf.Write(p)
	}
	select {
	case <-w.rpcReturned:
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	}
	if err := w.plugin.emitStreamChunk(w.streamID, p); err != nil {
		if w.cancel != nil {
			w.cancel()
		}
		return 0, err
	}
	return len(p), nil
}

func (w *liveStreamResponseWriter) Flush() {
	w.WriteHeader(http.StatusOK)
}

func (w *liveStreamResponseWriter) clonedHeaders() http.Header {
	return cloneHTTPHeader(w.header)
}

func (w *liveStreamResponseWriter) finishHandler(streamErr *pluginabi.Error) {
	w.WriteHeader(http.StatusOK)
	defer close(w.done)
	if w.statusCode >= 400 && streamErr == nil {
		return
	}
	select {
	case <-w.rpcReturned:
		_ = w.plugin.closeStream(w.streamID, streamErr)
	default:
		if streamErr != nil {
			return
		}
		select {
		case <-w.rpcReturned:
			_ = w.plugin.closeStream(w.streamID, nil)
		case <-w.ctx.Done():
		case <-w.plugin.ctx.Done():
		}
	}
}

func (p *Plugin) emitStreamChunk(streamID string, payload []byte) error {
	req := map[string]any{
		"stream_id": streamID,
		"payload":   payload,
	}
	_, err := p.callHost(pluginabi.MethodHostStreamEmit, req)
	return err
}

func (p *Plugin) closeStream(streamID string, streamErr *pluginabi.Error) error {
	req := map[string]any{
		"stream_id": streamID,
	}
	if streamErr != nil {
		req["error"] = streamErr.Message
		req["error_detail"] = streamErr
	}
	_, err := p.callHost(pluginabi.MethodHostStreamClose, req)
	return err
}

func (p *Plugin) handleExecutorCountTokens(raw []byte) (any, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode executor.count_tokens request: %w", err)
	}

	reqCtx, rt, err := p.prepareExecutionContext(p.ctx, req.StorageJSON, req.AuthMetadata, req.AuthAttributes)
	if err != nil {
		return nil, err
	}

	cleanModel := StripModelPrefix(req.Model, p.Config().ModelPrefix)
	cleanModel = strings.TrimPrefix(cleanModel, "models/")
	if cleanModel == "" {
		cleanModel = "gemini-3.8-flash"
	}

	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = strings.ToLower(strings.TrimSpace(req.SourceFormat))
	}

	body := bytes.Clone(req.Payload)
	if len(body) > 0 && gjson.GetBytes(body, "model").Exists() {
		if updated, err := sjson.SetBytes(body, "model", cleanModel); err == nil {
			body = updated
		}
	}

	var targetPath string
	switch format {
	case "claude", "anthropic":
		targetPath = "/v1/messages/count_tokens"
	case "openai-response", "openai_response", "responses", "openai-responses", "openai_responses":
		targetPath = "/v1/responses/input_tokens"
	case "gemini":
		targetPath = "/v1beta/models/" + cleanModel + ":countTokens"
	default:
		text := extractMessagesText(body)
		if text == "" {
			text = string(body)
		}
		tokens := tokencount.Text(text)
		respPayload, _ := json.Marshal(map[string]any{
			"totalTokens":  tokens,
			"total_tokens": tokens,
			"input_tokens": tokens,
			"usage": map[string]any{
				"prompt_tokens": tokens,
				"total_tokens":  tokens,
			},
		})
		return pluginapi.ExecutorResponse{
			Payload: respPayload,
			Headers: http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "http://gemini-web2api.local"+targetPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.RemoteAddr = "127.0.0.1:0"
	httpReq.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	rt.Handler.ServeHTTP(rec, httpReq)
	resp := rec.Result()
	respBody := rec.Body.Bytes()
	if resp.StatusCode >= 400 {
		return nil, extractHTTPError(resp.StatusCode, resp.Header, respBody)
	}
	return pluginapi.ExecutorResponse{
		Payload: bytes.Clone(respBody),
		Headers: cloneHTTPHeader(resp.Header),
	}, nil
}

func (p *Plugin) handleExecutorHTTPRequest(raw []byte) (any, error) {
	var req rpcExecutorHTTPRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode executor.http_request: %w", err)
	}

	reqCtx, rt, err := p.prepareExecutionContext(p.ctx, req.StorageJSON, req.Metadata, req.Attributes)
	if err != nil {
		return nil, err
	}

	method := strings.TrimSpace(req.Method)
	if method == "" {
		method = http.MethodGet
	}
	targetURL := strings.TrimSpace(req.URL)
	if targetURL == "" {
		targetURL = "/v1/models"
	}
	if strings.HasPrefix(targetURL, "/") {
		targetURL = "http://gemini-web2api.local" + targetURL
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, method, targetURL, bytes.NewReader(req.Body))
	if err != nil {
		return nil, err
	}
	httpReq.RemoteAddr = "127.0.0.1:0"
	for k, vals := range req.Headers {
		for _, v := range vals {
			httpReq.Header.Add(k, v)
		}
	}
	httpReq.Header.Del("Origin")

	rec := httptest.NewRecorder()
	rt.Handler.ServeHTTP(rec, httpReq)
	resp := rec.Result()

	return pluginapi.ExecutorHTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    cloneHTTPHeader(resp.Header),
		Body:       bytes.Clone(rec.Body.Bytes()),
	}, nil
}

func extractMessagesText(body []byte) string {
	var parts []string
	messages := gjson.GetBytes(body, "messages")
	if messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			content := msg.Get("content")
			if content.Type == gjson.String {
				parts = append(parts, content.String())
			} else if content.IsArray() {
				content.ForEach(func(_, item gjson.Result) bool {
					if t := item.Get("text").String(); t != "" {
						parts = append(parts, t)
					}
					return true
				})
			}
			return true
		})
	}
	return strings.Join(parts, "\n")
}

func extractHTTPError(statusCode int, headers http.Header, body []byte) *pluginabi.Error {
	code := "upstream_error"
	msg := strings.TrimSpace(string(body))
	if len(body) > 0 {
		if errMsg := strings.TrimSpace(gjson.GetBytes(body, "error.message").String()); errMsg != "" {
			msg = errMsg
		} else if errStr := strings.TrimSpace(gjson.GetBytes(body, "error").String()); errStr != "" && !strings.HasPrefix(errStr, "{") {
			msg = errStr
		}
		if errCode := strings.TrimSpace(gjson.GetBytes(body, "error.code").String()); errCode != "" {
			code = errCode
		} else if errType := strings.TrimSpace(gjson.GetBytes(body, "error.type").String()); errType != "" {
			code = errType
		}
	}
	if msg == "" {
		msg = http.StatusText(statusCode)
	}
	err := pluginabi.NewError(code, msg, statusCode)
	if headers != nil {
		if ra := strings.TrimSpace(headers.Get("Retry-After")); ra != "" {
			if secs, convErr := strconv.Atoi(ra); convErr == nil && secs > 0 {
				err.RetryAfterSeconds = secs
			} else if retryTime, timeErr := http.ParseTime(ra); timeErr == nil {
				if delta := int(time.Until(retryTime).Seconds()); delta > 0 {
					err.RetryAfterSeconds = delta
				}
			}
		}
	}
	return err
}

func splitSSEChunks(raw []byte) []pluginapi.ExecutorStreamChunk {
	if len(raw) == 0 {
		return nil
	}
	lines := bytes.SplitAfter(raw, []byte("\n\n"))
	chunks := make([]pluginapi.ExecutorStreamChunk, 0, len(lines))
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: bytes.Clone(line)})
	}
	return chunks
}

func cloneHTTPHeader(in http.Header) http.Header {
	if len(in) == 0 {
		return nil
	}
	out := make(http.Header, len(in))
	for k, vals := range in {
		out[k] = append([]string(nil), vals...)
	}
	return out
}
