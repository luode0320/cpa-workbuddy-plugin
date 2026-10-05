package executor

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestBuildRequestInputGenerateUsesAuthProjectAndHeaders(t *testing.T) {
	storageJSON := []byte(`{"type":"gemini-cli","project_id":"storage-project","access_token":"access-token"}`)
	payload := []byte(`{"project":"old-project","model":"old-model","request":{"contents":[]}}`)

	built, errBuild := BuildRequestInput(storageJSON, map[string]any{
		"project_id": "metadata-project",
		"headers": map[string]string{
			"X-Metadata": "from-metadata",
		},
	}, map[string]string{
		"project_id":       "attribute-project",
		"header:X-Project": "from-attribute",
	}, "gemini-2.5-pro", payload, "generateContent", "")
	if errBuild != nil {
		t.Fatalf("BuildRequestInput returned error: %v", errBuild)
	}
	if built.URL != "https://cloudcode-pa.googleapis.com/v1internal:generateContent" {
		t.Fatalf("url = %q", built.URL)
	}
	if got := built.Headers.Get("Authorization"); got != "Bearer access-token" {
		t.Fatalf("authorization = %q, want Bearer access-token", got)
	}
	if got := built.Headers.Get("X-Project"); got != "from-attribute" {
		t.Fatalf("custom attribute header = %q, want from-attribute", got)
	}
	if got := built.Headers.Get("X-Metadata"); got != "from-metadata" {
		t.Fatalf("custom metadata header = %q, want from-metadata", got)
	}
	if got := gjson.GetBytes(built.Body, "project").String(); got != "attribute-project" {
		t.Fatalf("body project = %q, want attribute-project", got)
	}
	if got := gjson.GetBytes(built.Body, "model").String(); got != "gemini-2.5-pro" {
		t.Fatalf("body model = %q, want gemini-2.5-pro", got)
	}
}

func TestBuildRequestInputWrapsGeminiPayload(t *testing.T) {
	built, errBuild := BuildRequestInput(
		[]byte(`{"type":"gemini-cli","access_token":"access-token","project_id":"project-id"}`),
		nil,
		nil,
		"gemini-2.5-pro",
		[]byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`),
		"generateContent",
		"",
	)
	if errBuild != nil {
		t.Fatalf("BuildRequestInput returned error: %v", errBuild)
	}
	if got := gjson.GetBytes(built.Body, "model").String(); got != "gemini-2.5-pro" {
		t.Fatalf("body model = %q, want gemini-2.5-pro", got)
	}
	if got := gjson.GetBytes(built.Body, "request.contents.0.parts.0.text").String(); got != "hello" {
		t.Fatalf("request text = %q, want hello: %s", got, built.Body)
	}
	if gjson.GetBytes(built.Body, "request.model").Exists() {
		t.Fatalf("wrapped request still contains request.model: %s", built.Body)
	}
}

func TestBuildRequestInputNormalizesWrappedFunctionResponses(t *testing.T) {
	payload := []byte(`{
		"project":"old-project",
		"model":"old-model",
		"request":{
			"contents":[
				{"role":"model","parts":[{"functionCall":{"name":"exec_command","id":"call_1","args":{"cmd":"date"}},"thoughtSignature":"skip_thought_signature_validator"}]},
				{"role":"function","parts":[{"functionResponse":{"name":"exec_command","id":"call_1","response":{"result":"ok"}}}]}
			]
		}
	}`)

	built, errBuild := BuildRequestInput(
		[]byte(`{"type":"gemini-cli","access_token":"access-token","project_id":"project-id"}`),
		nil,
		nil,
		"gemini-2.5-pro",
		payload,
		"streamGenerateContent",
		"",
	)
	if errBuild != nil {
		t.Fatalf("BuildRequestInput returned error: %v", errBuild)
	}
	if got := gjson.GetBytes(built.Body, "request.contents.1.role").String(); got != "user" {
		t.Fatalf("functionResponse role = %q, want user: %s", got, built.Body)
	}
	if !gjson.GetBytes(built.Body, "request.contents.1.parts.0.functionResponse").Exists() {
		t.Fatalf("functionResponse missing: %s", built.Body)
	}
}

func TestBuildRequestInputConvertsGemini25ThinkingLevelToBudget(t *testing.T) {
	built, errBuild := BuildRequestInput(
		[]byte(`{"type":"gemini-cli","access_token":"access-token","project_id":"project-id"}`),
		nil,
		nil,
		"gemini-2.5-flash-lite",
		[]byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinking_level":"xhigh","include_thoughts":true}}}}`),
		"generateContent",
		"",
	)
	if errBuild != nil {
		t.Fatalf("BuildRequestInput returned error: %v", errBuild)
	}
	if gjson.GetBytes(built.Body, "request.generationConfig.thinkingConfig.thinkingLevel").Exists() {
		t.Fatalf("thinkingLevel was not removed: %s", built.Body)
	}
	if gjson.GetBytes(built.Body, "request.generationConfig.thinkingConfig.thinking_level").Exists() {
		t.Fatalf("thinking_level was not removed: %s", built.Body)
	}
	if got := gjson.GetBytes(built.Body, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 24576 {
		t.Fatalf("thinkingBudget = %d, want 24576", got)
	}
	if !gjson.GetBytes(built.Body, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts was not preserved: %s", built.Body)
	}
	if gjson.GetBytes(built.Body, "request.generationConfig.thinkingConfig.include_thoughts").Exists() {
		t.Fatalf("include_thoughts was not removed: %s", built.Body)
	}
}

func TestBuildRequestInputStreamDefaultsToSSE(t *testing.T) {
	built, errBuild := BuildRequestInput([]byte(`{"type":"gemini-cli","access_token":"access-token","project_id":"project-id"}`), nil, nil, "gemini-2.5-pro", []byte(`{"request":{}}`), "streamGenerateContent", "")
	if errBuild != nil {
		t.Fatalf("BuildRequestInput returned error: %v", errBuild)
	}
	if built.URL != "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse" {
		t.Fatalf("url = %q", built.URL)
	}
	if got := built.Headers.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("accept = %q, want text/event-stream", got)
	}
	if got := built.Headers.Get("User-Agent"); !strings.Contains(got, "GeminiCLI/0.34.0/gemini-2.5-pro") || !strings.Contains(got, "; terminal)") {
		t.Fatalf("user-agent = %q, want GeminiCLI/0.34.0 with terminal suffix", got)
	}
	if got := built.Headers.Get("X-Goog-Api-Client"); got != "google-genai-sdk/1.41.0 gl-node/v22.19.0" {
		t.Fatalf("x-goog-api-client = %q, want old Gemini CLI fingerprint", got)
	}
}

func TestBuildRequestInputStreamRequiresProjectID(t *testing.T) {
	_, errBuild := BuildRequestInput([]byte(`{"type":"gemini-cli","access_token":"access-token"}`), nil, nil, "gemini-2.5-pro", []byte(`{"request":{}}`), "streamGenerateContent", "")
	if errBuild == nil {
		t.Fatal("BuildRequestInput returned nil error")
	}
	if !strings.Contains(errBuild.Error(), "project_id is required") {
		t.Fatalf("error = %q, want project_id is required", errBuild.Error())
	}
}

func TestBuildRequestInputCountTokensRemovesProjectAndModel(t *testing.T) {
	built, errBuild := BuildRequestInput([]byte(`{"type":"gemini-cli","access_token":"access-token"}`), nil, nil, "gemini-2.5-pro", []byte(`{"project":"old","model":"old","request":{}}`), "countTokens", "")
	if errBuild != nil {
		t.Fatalf("BuildRequestInput returned error: %v", errBuild)
	}
	if built.URL != "https://cloudcode-pa.googleapis.com/v1internal:countTokens" {
		t.Fatalf("url = %q", built.URL)
	}
	if gjson.GetBytes(built.Body, "project").Exists() || gjson.GetBytes(built.Body, "model").Exists() {
		t.Fatalf("countTokens body still contains project/model: %s", built.Body)
	}
}

func TestHttpRequestPreservesProviderAuthHeaders(t *testing.T) {
	client := &captureHTTPClient{}
	_, errRequest := NewExecutor().HttpRequest(context.Background(), pluginapi.ExecutorHTTPRequest{
		Method:      "POST",
		URL:         "https://cloudcode-pa.googleapis.com/v1internal:generateContent",
		Headers:     http.Header{"Authorization": []string{"Bearer caller-token"}, "X-Caller": []string{"yes"}},
		StorageJSON: []byte(`{"type":"gemini-cli","access_token":"provider-token"}`),
		HTTPClient:  client,
	})
	if errRequest != nil {
		t.Fatalf("HttpRequest returned error: %v", errRequest)
	}
	if got := client.request.Headers.Get("Authorization"); got != "Bearer provider-token" {
		t.Fatalf("authorization = %q, want Bearer provider-token", got)
	}
	if got := client.request.Headers.Get("X-Caller"); got != "yes" {
		t.Fatalf("caller header = %q, want yes", got)
	}
}

func TestExecuteStreamNon2xxUsesErrorMessageAndStatus(t *testing.T) {
	chunks := make(chan pluginapi.HTTPStreamChunk, 1)
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(`{"error":"permission denied"}`)}
	close(chunks)

	_, errStream := NewExecutor().ExecuteStream(context.Background(), pluginapi.ExecutorRequest{
		Model:       "gemini-2.5-pro",
		Payload:     []byte(`{"request":{}}`),
		StorageJSON: []byte(`{"type":"gemini-cli","access_token":"access-token","project_id":"project-id"}`),
		HTTPClient: streamResponseClient{
			response: pluginapi.HTTPStreamResponse{
				StatusCode: http.StatusForbidden,
				Chunks:     chunks,
			},
		},
	})
	if errStream == nil {
		t.Fatal("ExecuteStream returned nil error")
	}
	if got := errStream.Error(); got != "permission denied" {
		t.Fatalf("error = %q, want permission denied", got)
	}
	statusProvider, ok := errStream.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error %T does not expose StatusCode", errStream)
	}
	if got := statusProvider.StatusCode(); got != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", got, http.StatusForbidden)
	}
}

func TestStatusErrorUsesUpstreamErrorMessage(t *testing.T) {
	errStatus := statusError{
		statusCode: http.StatusForbidden,
		body:       []byte(`{"error":{"message":"license required"}}`),
	}
	if got := errStatus.Error(); got != "license required" {
		t.Fatalf("error = %q, want license required", got)
	}
	if got := errStatus.StatusCode(); got != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", got, http.StatusForbidden)
	}
}

func TestExecuteReturnsGeminiPayload(t *testing.T) {
	resp, errExecute := NewExecutor().Execute(context.Background(), pluginapi.ExecutorRequest{
		Model:       "gemini-2.5-pro",
		Payload:     []byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`),
		StorageJSON: []byte(`{"type":"gemini-cli","access_token":"access-token","project_id":"project-id"}`),
		HTTPClient: responseClient{
			response: pluginapi.HTTPResponse{
				StatusCode: http.StatusOK,
				Body:       []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}}`),
			},
		},
	})
	if errExecute != nil {
		t.Fatalf("Execute returned error: %v", errExecute)
	}
	if got := gjson.GetBytes(resp.Payload, "candidates.0.content.parts.0.text").String(); got != "hi" {
		t.Fatalf("response text = %q, want hi: %s", got, resp.Payload)
	}
}

func TestExecuteRefreshesExpiredStorageBeforeRequest(t *testing.T) {
	expired := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	client := &refreshExecuteHTTPClient{}
	resp, errExecute := NewExecutor().Execute(context.Background(), pluginapi.ExecutorRequest{
		AuthID:      "account.json",
		Model:       "gemini-2.5-pro",
		Payload:     []byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`),
		StorageJSON: []byte(`{"type":"gemini-cli","access_token":"old-access","refresh_token":"refresh-token","expiry":"` + expired + `","project_id":"project-id"}`),
		HTTPClient:  client,
	})
	if errExecute != nil {
		t.Fatalf("Execute returned error: %v", errExecute)
	}
	if got := gjson.GetBytes(resp.Payload, "candidates.0.content.parts.0.text").String(); got != "hi" {
		t.Fatalf("response text = %q, want hi: %s", got, resp.Payload)
	}
	if len(client.requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(client.requests))
	}
	if !strings.Contains(client.requests[0].URL, "oauth2.googleapis.com/token") {
		t.Fatalf("first request URL = %q, want token endpoint", client.requests[0].URL)
	}
	if got := client.requests[1].Headers.Get("Authorization"); got != "Bearer new-access" {
		t.Fatalf("upstream authorization = %q, want Bearer new-access", got)
	}
}

func TestExecuteStreamReturnsGeminiChunks(t *testing.T) {
	chunks := make(chan pluginapi.HTTPStreamChunk, 1)
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}}`)}
	close(chunks)

	resp, errExecute := NewExecutor().ExecuteStream(context.Background(), pluginapi.ExecutorRequest{
		Model:       "gemini-2.5-pro",
		Payload:     []byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`),
		StorageJSON: []byte(`{"type":"gemini-cli","access_token":"access-token","project_id":"project-id"}`),
		HTTPClient: streamResponseClient{
			response: pluginapi.HTTPStreamResponse{
				StatusCode: http.StatusOK,
				Chunks:     chunks,
			},
		},
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream returned error: %v", errExecute)
	}
	chunk := <-resp.Chunks
	if got := gjson.GetBytes(chunk.Payload, "candidates.0.content.parts.0.text").String(); got != "hi" {
		t.Fatalf("chunk text = %q, want hi: %s", got, chunk.Payload)
	}
	if _, ok := <-resp.Chunks; ok {
		t.Fatal("stream chunks channel still open, want closed")
	}
}

type captureHTTPClient struct {
	request pluginapi.HTTPRequest
}

func (c *captureHTTPClient) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	c.request = req
	return pluginapi.HTTPResponse{StatusCode: 200}, nil
}

func (c *captureHTTPClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, nil
}

type streamResponseClient struct {
	response pluginapi.HTTPStreamResponse
}

func (c streamResponseClient) Do(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return pluginapi.HTTPResponse{}, nil
}

func (c streamResponseClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return c.response, nil
}

type responseClient struct {
	response pluginapi.HTTPResponse
}

func (c responseClient) Do(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return c.response, nil
}

func (c responseClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, nil
}

type refreshExecuteHTTPClient struct {
	requests []pluginapi.HTTPRequest
}

func (c *refreshExecuteHTTPClient) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	c.requests = append(c.requests, req)
	if strings.Contains(req.URL, "oauth2.googleapis.com/token") {
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"access_token":"new-access","refresh_token":"refresh-token","token_type":"Bearer","expires_in":3600}`),
		}, nil
	}
	return pluginapi.HTTPResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}}`),
	}, nil
}

func (c *refreshExecuteHTTPClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, nil
}
