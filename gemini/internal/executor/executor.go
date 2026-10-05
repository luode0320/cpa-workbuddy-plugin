package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	authpkg "github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/auth"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/compat"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/fingerprint"
)

const maxStreamErrorBodyBytes = 1 << 20

type Executor struct{}

func NewExecutor() *Executor { return &Executor{} }

func (e *Executor) Identifier() string { return "gemini-cli" }

func (e *Executor) Execute(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	storageJSON, errStorage := refreshStorageJSONForRequest(ctx, req.StorageJSON, req.AuthID, req.AuthMetadata, req.AuthAttributes, req.HTTPClient)
	if errStorage != nil {
		return pluginapi.ExecutorResponse{}, errStorage
	}
	built, errBuild := BuildRequestInput(storageJSON, req.AuthMetadata, req.AuthAttributes, req.Model, req.Payload, "generateContent", req.Alt)
	if errBuild != nil {
		return pluginapi.ExecutorResponse{}, errBuild
	}
	resp, errDo := requireClient(req.HTTPClient).Do(ctx, pluginapi.HTTPRequest{
		Method:  built.Method,
		URL:     built.URL,
		Headers: built.Headers,
		Body:    built.Body,
	})
	if errDo != nil {
		return pluginapi.ExecutorResponse{}, errDo
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pluginapi.ExecutorResponse{}, statusError{statusCode: resp.StatusCode, body: resp.Body}
	}
	return pluginapi.ExecutorResponse{Payload: compat.UnwrapResponse(resp.Body), Headers: resp.Headers}, nil
}

func (e *Executor) ExecuteStream(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	storageJSON, errStorage := refreshStorageJSONForRequest(ctx, req.StorageJSON, req.AuthID, req.AuthMetadata, req.AuthAttributes, req.HTTPClient)
	if errStorage != nil {
		return pluginapi.ExecutorStreamResponse{}, errStorage
	}
	built, errBuild := BuildRequestInput(storageJSON, req.AuthMetadata, req.AuthAttributes, req.Model, req.Payload, "streamGenerateContent", req.Alt)
	if errBuild != nil {
		return pluginapi.ExecutorStreamResponse{}, errBuild
	}
	resp, errDo := requireClient(req.HTTPClient).DoStream(ctx, pluginapi.HTTPRequest{
		Method:  built.Method,
		URL:     built.URL,
		Headers: built.Headers,
		Body:    built.Body,
	})
	if errDo != nil {
		return pluginapi.ExecutorStreamResponse{}, errDo
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pluginapi.ExecutorStreamResponse{}, statusError{statusCode: resp.StatusCode, body: readStreamErrorBody(ctx, resp.Chunks)}
	}
	return pluginapi.ExecutorStreamResponse{Headers: resp.Headers, Chunks: convertHTTPChunks(ctx, resp.Chunks)}, nil
}

func (e *Executor) CountTokens(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	storageJSON, errStorage := refreshStorageJSONForRequest(ctx, req.StorageJSON, req.AuthID, req.AuthMetadata, req.AuthAttributes, req.HTTPClient)
	if errStorage != nil {
		return pluginapi.ExecutorResponse{}, errStorage
	}
	built, errBuild := BuildRequestInput(storageJSON, req.AuthMetadata, req.AuthAttributes, req.Model, req.Payload, "countTokens", req.Alt)
	if errBuild != nil {
		return pluginapi.ExecutorResponse{}, errBuild
	}
	resp, errDo := requireClient(req.HTTPClient).Do(ctx, pluginapi.HTTPRequest{
		Method:  built.Method,
		URL:     built.URL,
		Headers: built.Headers,
		Body:    built.Body,
	})
	if errDo != nil {
		return pluginapi.ExecutorResponse{}, errDo
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pluginapi.ExecutorResponse{}, statusError{statusCode: resp.StatusCode, body: resp.Body}
	}
	return pluginapi.ExecutorResponse{Payload: compat.UnwrapResponse(resp.Body), Headers: resp.Headers}, nil
}

func (e *Executor) HttpRequest(ctx context.Context, req pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	if req.HTTPClient == nil {
		return pluginapi.ExecutorHTTPResponse{}, fmt.Errorf("host HTTP client is required")
	}
	method := req.Method
	if method == "" {
		method = "POST"
	}
	if req.URL == "" {
		return pluginapi.ExecutorHTTPResponse{}, fmt.Errorf("request URL is required")
	}
	storageJSON, errStorage := refreshStorageJSONForRequest(ctx, req.StorageJSON, req.AuthID, req.Metadata, req.Attributes, req.HTTPClient)
	if errStorage != nil {
		return pluginapi.ExecutorHTTPResponse{}, errStorage
	}
	headers := req.Headers.Clone()
	token, errToken := accessTokenFromStorage(storageJSON)
	if errToken != nil {
		return pluginapi.ExecutorHTTPResponse{}, errToken
	}
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("User-Agent", fingerprint.UserAgent(""))
	headers.Set("X-Goog-Api-Client", fingerprint.APIClientHeader)
	applyCustomHeaders(headers, req.Metadata, req.Attributes)
	resp, errDo := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{
		Method:  method,
		URL:     req.URL,
		Headers: headers,
		Body:    req.Body,
	})
	if errDo != nil {
		return pluginapi.ExecutorHTTPResponse{}, errDo
	}
	return pluginapi.ExecutorHTTPResponse{StatusCode: resp.StatusCode, Headers: resp.Headers, Body: resp.Body}, nil
}

func refreshStorageJSONForRequest(ctx context.Context, storageJSON []byte, authID string, metadata map[string]any, attributes map[string]string, client pluginapi.HostHTTPClient) ([]byte, error) {
	storage, errParse := authpkg.ParseStorage(storageJSON)
	if errParse != nil {
		return nil, errParse
	}
	if storage == nil || !authpkg.ShouldRefreshStorage(*storage, time.Now()) {
		return storageJSON, nil
	}
	resp, errRefresh := authpkg.NewProvider().RefreshAuth(ctx, pluginapi.AuthRefreshRequest{
		AuthID:       authID,
		AuthProvider: authpkg.ProviderKey,
		StorageJSON:  storageJSON,
		Metadata:     metadata,
		Attributes:   attributes,
		HTTPClient:   client,
	})
	if errRefresh != nil {
		return nil, errRefresh
	}
	if len(resp.Auth.StorageJSON) == 0 {
		return storageJSON, nil
	}
	return resp.Auth.StorageJSON, nil
}

func requireClient(client pluginapi.HostHTTPClient) pluginapi.HostHTTPClient {
	if client != nil {
		return client
	}
	return missingClient{}
}

type missingClient struct{}

func (missingClient) Do(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return pluginapi.HTTPResponse{}, fmt.Errorf("host HTTP client is required")
}

func (missingClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, fmt.Errorf("host HTTP client is required")
}

func convertHTTPChunks(ctx context.Context, in <-chan pluginapi.HTTPStreamChunk) <-chan pluginapi.ExecutorStreamChunk {
	out := make(chan pluginapi.ExecutorStreamChunk)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				out <- pluginapi.ExecutorStreamChunk{Err: ctx.Err()}
				return
			case chunk, ok := <-in:
				if !ok {
					return
				}
				out <- pluginapi.ExecutorStreamChunk{Payload: compat.UnwrapResponse(chunk.Payload), Err: chunk.Err}
			}
		}
	}()
	return out
}

func readStreamErrorBody(ctx context.Context, chunks <-chan pluginapi.HTTPStreamChunk) []byte {
	if chunks == nil {
		return nil
	}
	body := make([]byte, 0)
	for len(body) < maxStreamErrorBodyBytes {
		select {
		case <-ctx.Done():
			return body
		case chunk, ok := <-chunks:
			if !ok {
				return body
			}
			if len(chunk.Payload) > 0 {
				remaining := maxStreamErrorBodyBytes - len(body)
				if len(chunk.Payload) > remaining {
					body = append(body, chunk.Payload[:remaining]...)
					return body
				}
				body = append(body, chunk.Payload...)
			}
			if chunk.Err != nil {
				return body
			}
		}
	}
	return body
}
