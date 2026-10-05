package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestConfigurePreviewChannelCreatesSettingAndBinding(t *testing.T) {
	client := &previewHTTPClient{}
	errConfigure := configurePreviewChannel(context.Background(), client, "access-token", "my-project")
	if errConfigure != nil {
		t.Fatalf("configurePreviewChannel returned error: %v", errConfigure)
	}
	if len(client.requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(client.requests))
	}

	settingReq := client.requests[0]
	if settingReq.Method != http.MethodPost {
		t.Fatalf("setting method = %q, want POST", settingReq.Method)
	}
	if !strings.Contains(settingReq.URL, "cloudaicompanion.googleapis.com/v1/projects/my-project/locations/global/releaseChannelSettings") {
		t.Fatalf("setting URL = %q, want releaseChannelSettings endpoint", settingReq.URL)
	}
	if !strings.Contains(settingReq.URL, "release_channel_setting_id=preview-setting-") {
		t.Fatalf("setting URL = %q, want release_channel_setting_id query", settingReq.URL)
	}
	if got := settingReq.Headers.Get("Authorization"); got != "Bearer access-token" {
		t.Fatalf("authorization = %q, want Bearer access-token", got)
	}
	var settingBody map[string]string
	if errUnmarshal := json.Unmarshal(settingReq.Body, &settingBody); errUnmarshal != nil {
		t.Fatalf("decode setting body: %v", errUnmarshal)
	}
	if settingBody["release_channel"] != releaseChannelValue {
		t.Fatalf("release_channel = %q, want %q", settingBody["release_channel"], releaseChannelValue)
	}

	bindingReq := client.requests[1]
	if bindingReq.Method != http.MethodPost {
		t.Fatalf("binding method = %q, want POST", bindingReq.Method)
	}
	if !strings.Contains(bindingReq.URL, "/releaseChannelSettings/") || !strings.Contains(bindingReq.URL, "/settingBindings") {
		t.Fatalf("binding URL = %q, want settingBindings endpoint", bindingReq.URL)
	}
	var bindingBody map[string]string
	if errUnmarshal := json.Unmarshal(bindingReq.Body, &bindingBody); errUnmarshal != nil {
		t.Fatalf("decode binding body: %v", errUnmarshal)
	}
	if bindingBody["target"] != "projects/my-project" {
		t.Fatalf("binding target = %q, want projects/my-project", bindingBody["target"])
	}
	if bindingBody["product"] != releaseChannelProduct {
		t.Fatalf("binding product = %q, want %q", bindingBody["product"], releaseChannelProduct)
	}
}

func TestConfigurePreviewChannelReusesExistingSettingOnConflict(t *testing.T) {
	client := &previewHTTPClient{conflictOnCreate: true}
	errConfigure := configurePreviewChannel(context.Background(), client, "access-token", "my-project")
	if errConfigure != nil {
		t.Fatalf("configurePreviewChannel returned error: %v", errConfigure)
	}
	if len(client.requests) != 3 {
		t.Fatalf("request count = %d, want 3", len(client.requests))
	}
	if client.requests[1].Method != http.MethodGet {
		t.Fatalf("list method = %q, want GET", client.requests[1].Method)
	}
	if !strings.Contains(client.requests[2].URL, "/releaseChannelSettings/existing-setting/settingBindings") {
		t.Fatalf("binding URL = %q, want existing-setting id", client.requests[2].URL)
	}
}

func TestConfigurePreviewChannelTreatsBindingConflictAsSuccess(t *testing.T) {
	client := &previewHTTPClient{conflictOnBinding: true}
	errConfigure := configurePreviewChannel(context.Background(), client, "access-token", "my-project")
	if errConfigure != nil {
		t.Fatalf("configurePreviewChannel returned error: %v", errConfigure)
	}
}

func TestConfigurePreviewChannelRejectsMissingInputs(t *testing.T) {
	if errConfigure := configurePreviewChannel(context.Background(), &previewHTTPClient{}, "", "project"); errConfigure == nil {
		t.Fatal("expected error for missing access token")
	}
	if errConfigure := configurePreviewChannel(context.Background(), &previewHTTPClient{}, "token", ""); errConfigure == nil {
		t.Fatal("expected error for missing project id")
	}
}

func TestConfigurePreviewChannelsConfiguresAllProjects(t *testing.T) {
	client := &previewHTTPClient{}
	errConfigure := configurePreviewChannels(context.Background(), client, "access-token", []string{"project-a", "project-b", "project-a"})
	if errConfigure != nil {
		t.Fatalf("configurePreviewChannels returned error: %v", errConfigure)
	}
	// Two unique projects, two requests each (setting + binding).
	if len(client.requests) != 4 {
		t.Fatalf("request count = %d, want 4", len(client.requests))
	}
	seen := map[string]bool{}
	for _, req := range client.requests {
		if strings.Contains(req.URL, "/projects/project-a/") {
			seen["project-a"] = true
		}
		if strings.Contains(req.URL, "/projects/project-b/") {
			seen["project-b"] = true
		}
	}
	if !seen["project-a"] || !seen["project-b"] {
		t.Fatalf("projects seen = %#v, want project-a and project-b", seen)
	}
}

func TestFinalizeCodeEnablesPreviewChannel(t *testing.T) {
	client := &loginPollWithPreviewHTTPClient{}
	provider := NewProvider()
	storage, errFinalize := provider.finalizeCode(context.Background(), client, "auth-code", map[string]any{
		"redirect_uri": "http://localhost/oauth",
		"project_id":   "project-id",
	})
	if errFinalize != nil {
		t.Fatalf("finalizeCode returned error: %v", errFinalize)
	}
	if storage == nil || storage.ProjectID != "project-id" {
		t.Fatalf("storage = %#v, want project-id", storage)
	}
	if !client.previewCalled {
		t.Fatal("expected preview channel configuration after login")
	}
}

func TestFinalizeCodeSucceedsWhenPreviewChannelFails(t *testing.T) {
	client := &loginPollWithPreviewHTTPClient{failPreview: true}
	provider := NewProvider()
	storage, errFinalize := provider.finalizeCode(context.Background(), client, "auth-code", map[string]any{
		"redirect_uri": "http://localhost/oauth",
		"project_id":   "project-id",
	})
	if errFinalize != nil {
		t.Fatalf("finalizeCode returned error: %v", errFinalize)
	}
	if storage == nil || storage.ProjectID != "project-id" {
		t.Fatalf("storage = %#v, want project-id", storage)
	}
}

type previewHTTPClient struct {
	mu                sync.Mutex
	requests          []pluginapi.HTTPRequest
	conflictOnCreate  bool
	conflictOnBinding bool
}

func (c *previewHTTPClient) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()

	switch {
	case req.Method == http.MethodPost && strings.Contains(req.URL, "/releaseChannelSettings?") && strings.Contains(req.URL, "release_channel_setting_id="):
		if c.conflictOnCreate {
			return pluginapi.HTTPResponse{StatusCode: http.StatusConflict, Body: []byte(`{"error":"already exists"}`)}, nil
		}
		return pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"name":"projects/my-project/locations/global/releaseChannelSettings/new-setting"}`)}, nil
	case req.Method == http.MethodGet && strings.HasSuffix(req.URL, "/releaseChannelSettings"):
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"releaseChannelSettings":[{"name":"projects/my-project/locations/global/releaseChannelSettings/existing-setting"}]}`),
		}, nil
	case req.Method == http.MethodPost && strings.Contains(req.URL, "/settingBindings"):
		if c.conflictOnBinding {
			return pluginapi.HTTPResponse{StatusCode: http.StatusConflict, Body: []byte(`{"error":"already exists"}`)}, nil
		}
		return pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"name":"binding"}`)}, nil
	default:
		return pluginapi.HTTPResponse{StatusCode: http.StatusNotFound, Body: []byte(`{}`)}, nil
	}
}

func (c *previewHTTPClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, nil
}

type loginPollWithPreviewHTTPClient struct {
	previewCalled bool
	failPreview   bool
}

func (c *loginPollWithPreviewHTTPClient) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	if strings.Contains(req.URL, "cloudaicompanion.googleapis.com") {
		c.previewCalled = true
		if c.failPreview {
			return pluginapi.HTTPResponse{StatusCode: http.StatusForbidden, Body: []byte(`{"error":"denied"}`)}, nil
		}
		if req.Method == http.MethodPost && strings.Contains(req.URL, "/releaseChannelSettings?") {
			return pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"name":"setting"}`)}, nil
		}
		if req.Method == http.MethodPost && strings.Contains(req.URL, "/settingBindings") {
			return pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"name":"binding"}`)}, nil
		}
		return pluginapi.HTTPResponse{StatusCode: http.StatusNotFound, Body: []byte(`{}`)}, nil
	}

	// Reuse the same OAuth/Code Assist responses as loginPollHTTPClient.
	base := loginPollHTTPClient{}
	return base.Do(context.Background(), req)
}

func (c *loginPollWithPreviewHTTPClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, nil
}
