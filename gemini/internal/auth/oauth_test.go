package auth

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRegisterCommandLineDeclaresLoginFlags(t *testing.T) {
	resp, errRegister := NewProvider().RegisterCommandLine(context.Background(), pluginapi.CommandLineRegistrationRequest{})
	if errRegister != nil {
		t.Fatalf("RegisterCommandLine returned error: %v", errRegister)
	}
	flags := make(map[string]pluginapi.CommandLineFlag, len(resp.Flags))
	for _, flag := range resp.Flags {
		flags[flag.Name] = flag
	}
	for _, name := range []string{"geminicli-login", "geminicli-project-id"} {
		if _, ok := flags[name]; !ok {
			t.Fatalf("missing command line flag %q in %#v", name, flags)
		}
	}
	if _, ok := flags["geminicli-no-browser"]; ok {
		t.Fatalf("unexpected command line flag %q in %#v", "geminicli-no-browser", flags)
	}
}

func TestFlagBoolValueReadsNativeHostFlag(t *testing.T) {
	flags := map[string]pluginapi.CommandLineFlagValue{
		"no-browser": {
			Name:  "no-browser",
			Value: "true",
			Set:   false,
		},
	}
	if !flagBoolValue(flags, "no-browser") {
		t.Fatal("flagBoolValue did not read host no-browser value")
	}
	if flagBool(flags, "no-browser") {
		t.Fatal("flagBool treated unset host no-browser as a triggered plugin flag")
	}
}

func TestParseManualCallbackPayload(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantOK    bool
		wantCode  string
		wantState string
		wantError bool
	}{
		{
			name:      "full callback URL",
			input:     "http://127.0.0.1:12345/oauth2callback?code=auth-code&state=state-token",
			wantOK:    true,
			wantCode:  "auth-code",
			wantState: "state-token",
		},
		{
			name:      "query string",
			input:     "code=auth-code&state=state-token",
			wantOK:    true,
			wantCode:  "auth-code",
			wantState: "state-token",
		},
		{
			name:      "leading question mark",
			input:     "?code=auth-code&state=state-token",
			wantOK:    true,
			wantCode:  "auth-code",
			wantState: "state-token",
		},
		{
			name:      "fragment callback",
			input:     "http://localhost/oauth2callback#code=auth-code&state=state-token",
			wantOK:    true,
			wantCode:  "auth-code",
			wantState: "state-token",
		},
		{
			name:   "empty input keeps waiting",
			input:  "",
			wantOK: false,
		},
		{
			name:      "missing code",
			input:     "http://localhost/oauth2callback?state=state-token",
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, okPayload, errParse := parseManualCallbackPayload(tt.input)
			if tt.wantError {
				if errParse == nil {
					t.Fatal("parseManualCallbackPayload returned nil error")
				}
				return
			}
			if errParse != nil {
				t.Fatalf("parseManualCallbackPayload returned error: %v", errParse)
			}
			if okPayload != tt.wantOK {
				t.Fatalf("ok = %v, want %v", okPayload, tt.wantOK)
			}
			if payload.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", payload.Code, tt.wantCode)
			}
			if payload.State != tt.wantState {
				t.Fatalf("state = %q, want %q", payload.State, tt.wantState)
			}
		})
	}
}

func TestFallbackHTTPClientUsesHostProxySemantics(t *testing.T) {
	proxyClient := fallbackHTTPClient("http://proxy.example.com:8080")
	proxyTransport, ok := proxyClient.Transport.(*http.Transport)
	if !ok || proxyTransport == nil {
		t.Fatalf("proxy client transport = %T, want *http.Transport", proxyClient.Transport)
	}
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "example.com"}}
	proxyURL, errProxy := proxyTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("proxy func returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://proxy.example.com:8080", proxyURL)
	}

	directClient := fallbackHTTPClient("direct")
	directTransport, ok := directClient.Transport.(*http.Transport)
	if !ok || directTransport == nil {
		t.Fatalf("direct client transport = %T, want *http.Transport", directClient.Transport)
	}
	if directTransport.Proxy != nil {
		t.Fatal("direct proxy function is not nil")
	}

	socksClient := fallbackHTTPClient("socks5://proxy.example.com:1080")
	socksTransport, ok := socksClient.Transport.(*http.Transport)
	if !ok || socksTransport == nil {
		t.Fatalf("socks client transport = %T, want *http.Transport", socksClient.Transport)
	}
	if socksTransport.Proxy != nil {
		t.Fatal("socks proxy function is not nil")
	}
	if socksTransport.DialContext == nil {
		t.Fatal("socks DialContext is nil")
	}
}

func TestPollLoginConsumesCallbackPayload(t *testing.T) {
	authDir := t.TempDir()
	state := "state-token"
	callbackPath := callbackPayloadPath(authDir, state)
	if errWrite := os.WriteFile(callbackPath, []byte(`{"code":"auth-code","state":"state-token"}`), 0600); errWrite != nil {
		t.Fatalf("write callback payload: %v", errWrite)
	}

	resp, errPoll := NewProvider().PollLogin(context.Background(), pluginapi.AuthLoginPollRequest{
		State: state,
		Host: pluginapi.HostConfigSummary{
			AuthDir: authDir,
		},
		Metadata: map[string]any{
			"redirect_uri": "http://localhost/oauth",
			"project_id":   "project-id",
		},
		HTTPClient: loginPollHTTPClient{},
	})
	if errPoll != nil {
		t.Fatalf("PollLogin returned error: %v", errPoll)
	}
	if resp.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("poll status = %q, want success: %s", resp.Status, resp.Message)
	}
	if _, errStat := os.Stat(callbackPath); !os.IsNotExist(errStat) {
		t.Fatalf("callback file still exists, stat error: %v", errStat)
	}
}

func TestPollLoginDiscoversProjectWithCodeAssistWhenProjectListFails(t *testing.T) {
	authDir := t.TempDir()
	state := "state-token"
	callbackPath := callbackPayloadPath(authDir, state)
	if errWrite := os.WriteFile(callbackPath, []byte(`{"code":"auth-code","state":"state-token"}`), 0600); errWrite != nil {
		t.Fatalf("write callback payload: %v", errWrite)
	}

	resp, errPoll := NewProvider().PollLogin(context.Background(), pluginapi.AuthLoginPollRequest{
		State: state,
		Host: pluginapi.HostConfigSummary{
			AuthDir: authDir,
		},
		Metadata: map[string]any{
			"redirect_uri": "http://localhost/oauth",
		},
		HTTPClient: loginPollHTTPClient{},
	})
	if errPoll != nil {
		t.Fatalf("PollLogin returned error: %v", errPoll)
	}
	if resp.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("poll status = %q, want success: %s", resp.Status, resp.Message)
	}
	if got := resp.Auth.Metadata["project_id"]; got != "auto-project" {
		t.Fatalf("auth project_id = %v, want auto-project", got)
	}
	if _, errStat := os.Stat(callbackPath); !os.IsNotExist(errStat) {
		t.Fatalf("callback file still exists, stat error: %v", errStat)
	}
}

type loginPollHTTPClient struct{}

func (loginPollHTTPClient) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	if req.URL == tokenURL {
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"access_token":"access-token","refresh_token":"refresh-token","token_type":"Bearer"}`),
		}, nil
	}
	if req.URL == codeAssistBaseURL+"/"+codeAssistVersion+":loadCodeAssist" {
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`),
		}, nil
	}
	if req.URL == codeAssistBaseURL+"/"+codeAssistVersion+":onboardUser" {
		projectID := "auto-project"
		if strings.Contains(string(req.Body), `"cloudaicompanionProject":"project-id"`) {
			projectID = "project-id"
		}
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"done":true,"response":{"cloudaicompanionProject":"` + projectID + `"}}`),
		}, nil
	}
	return pluginapi.HTTPResponse{StatusCode: http.StatusNotFound, Body: []byte(`{}`)}, nil
}

func (loginPollHTTPClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, nil
}
