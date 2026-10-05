package auth

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestParseStorageAcceptsGeminiCLIStorage(t *testing.T) {
	raw := []byte(`{
		"type":"gemini",
		"email":"user@example.com",
		"project_id":"primary-project",
		"project_ids":["secondary-project","primary-project","secondary-project"],
		"token":{"access_token":"access-token","refresh_token":"refresh-token","token_type":"Bearer"}
	}`)

	storage, errParse := ParseStorage(raw)
	if errParse != nil {
		t.Fatalf("ParseStorage returned error: %v", errParse)
	}
	if storage == nil {
		t.Fatal("ParseStorage returned nil storage")
	}
	if storage.Type != ProviderKey {
		t.Fatalf("storage type = %q, want %q", storage.Type, ProviderKey)
	}
	if storage.AccessToken != "access-token" {
		t.Fatalf("access token = %q, want access-token", storage.AccessToken)
	}
	if storage.RefreshToken != "refresh-token" {
		t.Fatalf("refresh token = %q, want refresh-token", storage.RefreshToken)
	}
	if len(storage.ProjectIDs) != 2 {
		t.Fatalf("project ids length = %d, want 2", len(storage.ProjectIDs))
	}
}

func TestBuildAuthsExpandsProjectIDsAsVirtualAuths(t *testing.T) {
	storage := Storage{
		Type:         "gemini",
		Email:        "user@example.com",
		ProjectID:    "primary-project",
		ProjectIDs:   []string{"secondary-project", "primary-project"},
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
	}

	auths, errBuild := BuildAuths("account.json", storage)
	if errBuild != nil {
		t.Fatalf("BuildAuths returned error: %v", errBuild)
	}
	if len(auths) != 2 {
		t.Fatalf("auth count = %d, want 2", len(auths))
	}
	primary := auths[0]
	if primary.Provider != ProviderKey || primary.ID != "account.json" {
		t.Fatalf("primary auth = %#v", primary)
	}
	if primary.Attributes["project_id"] != "primary-project" {
		t.Fatalf("primary project attribute = %q, want primary-project", primary.Attributes["project_id"])
	}
	if primary.Attributes["runtime_only"] != "" {
		t.Fatalf("primary runtime_only attribute = %q, want empty", primary.Attributes["runtime_only"])
	}
	virtual := auths[1]
	if virtual.Metadata["virtual"] != true {
		t.Fatalf("virtual metadata = %#v, want virtual=true", virtual.Metadata)
	}
	if virtual.Metadata["parent_auth_id"] != "account.json" {
		t.Fatalf("parent auth id = %#v, want account.json", virtual.Metadata["parent_auth_id"])
	}
	if virtual.Attributes["project_id"] != "secondary-project" {
		t.Fatalf("virtual project attribute = %q, want secondary-project", virtual.Attributes["project_id"])
	}
	if virtual.Attributes["runtime_only"] != "true" {
		t.Fatalf("virtual runtime_only attribute = %q, want true", virtual.Attributes["runtime_only"])
	}
	var stored map[string]any
	if errDecode := json.Unmarshal(virtual.StorageJSON, &stored); errDecode != nil {
		t.Fatalf("decode virtual storage: %v", errDecode)
	}
	if stored["project_id"] != "secondary-project" {
		t.Fatalf("virtual storage project_id = %#v, want secondary-project", stored["project_id"])
	}
}

func TestBuildPersistentAuthsKeepsOnlyPrimaryWithProjectIDs(t *testing.T) {
	auths, errBuild := buildPersistentAuths("", Storage{
		Type:         "gemini",
		Email:        "user@example.com",
		ProjectID:    "primary-project",
		ProjectIDs:   []string{"primary-project", "secondary-project", "third-project"},
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
	})
	if errBuild != nil {
		t.Fatalf("buildPersistentAuths returned error: %v", errBuild)
	}
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1", len(auths))
	}
	auth := auths[0]
	if auth.Metadata["virtual"] == true {
		t.Fatalf("persistent auth metadata = %#v, want physical auth", auth.Metadata)
	}
	if auth.Attributes["runtime_only"] != "" {
		t.Fatalf("runtime_only attribute = %q, want empty", auth.Attributes["runtime_only"])
	}
	var stored map[string]any
	if errDecode := json.Unmarshal(auth.StorageJSON, &stored); errDecode != nil {
		t.Fatalf("decode storage: %v", errDecode)
	}
	projects, okProjects := stored["project_ids"].([]any)
	if !okProjects || len(projects) != 3 {
		t.Fatalf("stored project_ids = %#v, want 3 project ids", stored["project_ids"])
	}
}

func TestBuildAuthsUsesFirstProjectIDForPrimaryWhenMissing(t *testing.T) {
	storage := Storage{
		Type:         "gemini",
		Email:        "user@example.com",
		ProjectIDs:   []string{"z-project", "a-project"},
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
	}

	auths, errBuild := BuildAuths("account.json", storage)
	if errBuild != nil {
		t.Fatalf("BuildAuths returned error: %v", errBuild)
	}
	if len(auths) != 2 {
		t.Fatalf("auth count = %d, want 2", len(auths))
	}
	primary := auths[0]
	if primary.Attributes["project_id"] != "z-project" {
		t.Fatalf("primary project attribute = %q, want z-project", primary.Attributes["project_id"])
	}
	if primary.Metadata["project_id"] != "z-project" {
		t.Fatalf("primary project metadata = %#v, want z-project", primary.Metadata["project_id"])
	}
	var stored map[string]any
	if errDecode := json.Unmarshal(primary.StorageJSON, &stored); errDecode != nil {
		t.Fatalf("decode primary storage: %v", errDecode)
	}
	if stored["project_id"] != "z-project" {
		t.Fatalf("primary storage project_id = %#v, want z-project", stored["project_id"])
	}
	if auths[1].Attributes["project_id"] != "a-project" {
		t.Fatalf("virtual project attribute = %q, want a-project", auths[1].Attributes["project_id"])
	}
	if auths[1].Attributes["runtime_only"] != "true" {
		t.Fatalf("virtual runtime_only attribute = %q, want true", auths[1].Attributes["runtime_only"])
	}
}

func TestBuildAuthsSchedulesExpiredTokenForImmediateRefresh(t *testing.T) {
	expired := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	auths, errBuild := BuildAuths("account.json", Storage{
		Type:         "gemini-cli",
		Email:        "user@example.com",
		ProjectID:    "project-id",
		AccessToken:  "old-access",
		RefreshToken: "refresh-token",
		Expiry:       expired,
	})
	if errBuild != nil {
		t.Fatalf("BuildAuths returned error: %v", errBuild)
	}
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1", len(auths))
	}
	if auths[0].NextRefreshAfter.IsZero() {
		t.Fatal("NextRefreshAfter is zero, want immediate refresh time")
	}
	if auths[0].NextRefreshAfter.After(time.Now().Add(time.Second)) {
		t.Fatalf("NextRefreshAfter = %s, want immediate refresh", auths[0].NextRefreshAfter)
	}
}

func TestBuildAuthsSchedulesRefreshBeforeExpiry(t *testing.T) {
	expiry := time.Now().Add(time.Hour).UTC()
	auths, errBuild := BuildAuths("account.json", Storage{
		Type:         "gemini-cli",
		Email:        "user@example.com",
		ProjectID:    "project-id",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		Expiry:       expiry.Format(time.RFC3339),
	})
	if errBuild != nil {
		t.Fatalf("BuildAuths returned error: %v", errBuild)
	}
	want := expiry.Add(-authRefreshLead)
	got := auths[0].NextRefreshAfter
	if got.Before(want.Add(-time.Second)) || got.After(want.Add(time.Second)) {
		t.Fatalf("NextRefreshAfter = %s, want around %s", got, want)
	}
}

func TestProviderParseAuthReturnsPrimaryAndVirtualAuths(t *testing.T) {
	raw := []byte(`{
		"type":"gemini-cli",
		"email":"user@example.com",
		"project_id":"primary-project",
		"project_ids":["primary-project","secondary-project"],
		"access_token":"access-token",
		"refresh_token":"refresh-token"
	}`)

	resp, errParse := NewProvider().ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		FileName: "account.json",
		RawJSON:  raw,
	})
	if errParse != nil {
		t.Fatalf("ParseAuth returned error: %v", errParse)
	}
	if !resp.Handled {
		t.Fatal("ParseAuth did not handle gemini-cli storage")
	}
	if len(resp.Auths) != 2 {
		t.Fatalf("auth count = %d, want 2", len(resp.Auths))
	}
	if resp.Auth.ID != resp.Auths[0].ID {
		t.Fatalf("primary auth id = %q, first auth id = %q", resp.Auth.ID, resp.Auths[0].ID)
	}
}

func TestProviderParseAuthExpandsLegacyCPACommaSeparatedProjectID(t *testing.T) {
	raw := []byte(`{
		"type":"gemini",
		"auto":true,
		"checked":true,
		"disabled":false,
		"email":"user@example.com",
		"project_id":"plenary-edition-467318-m9,fluent-ego-467318-s8,vaulted-dolphin-464307-i3,gen-lang-client-0714693288",
		"token":{"access_token":"access-token","refresh_token":"refresh-token","token_type":"Bearer"}
	}`)

	resp, errParse := NewProvider().ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		FileName: "gemini-user@example.com-all.json",
		RawJSON:  raw,
	})
	if errParse != nil {
		t.Fatalf("ParseAuth returned error: %v", errParse)
	}
	if !resp.Handled {
		t.Fatal("ParseAuth did not handle legacy CPA Gemini credential")
	}
	if len(resp.Auths) != 4 {
		t.Fatalf("auth count = %d, want 4", len(resp.Auths))
	}
	auth := resp.Auths[0]
	if auth.Provider != ProviderKey {
		t.Fatalf("provider = %q, want %q", auth.Provider, ProviderKey)
	}
	if auth.FileName != "gemini-user@example.com-all.json" {
		t.Fatalf("file name = %q, want legacy CPA file name", auth.FileName)
	}
	if auth.Attributes["project_id"] != "plenary-edition-467318-m9" {
		t.Fatalf("project_id attribute = %q, want plenary-edition-467318-m9", auth.Attributes["project_id"])
	}
	var stored map[string]any
	if errDecode := json.Unmarshal(auth.StorageJSON, &stored); errDecode != nil {
		t.Fatalf("decode storage: %v", errDecode)
	}
	if stored["type"] != ProviderKey {
		t.Fatalf("storage type = %#v, want %q", stored["type"], ProviderKey)
	}
	if stored["project_id"] != "plenary-edition-467318-m9" {
		t.Fatalf("stored project_id = %#v, want plenary-edition-467318-m9", stored["project_id"])
	}
	wantProjects := []string{
		"plenary-edition-467318-m9",
		"fluent-ego-467318-s8",
		"vaulted-dolphin-464307-i3",
		"gen-lang-client-0714693288",
	}
	for idx, wantProject := range wantProjects[1:] {
		virtual := resp.Auths[idx+1]
		if virtual.Metadata["virtual"] != true {
			t.Fatalf("virtual metadata = %#v, want virtual=true", virtual.Metadata)
		}
		if virtual.Metadata["parent_auth_id"] != auth.ID {
			t.Fatalf("parent auth id = %#v, want %s", virtual.Metadata["parent_auth_id"], auth.ID)
		}
		if virtual.Attributes["project_id"] != wantProject {
			t.Fatalf("virtual project attribute = %q, want %s", virtual.Attributes["project_id"], wantProject)
		}
		if virtual.Attributes["runtime_only"] != "true" {
			t.Fatalf("virtual runtime_only attribute = %q, want true", virtual.Attributes["runtime_only"])
		}
	}
}

func TestRefreshAuthPreservesVirtualMetadata(t *testing.T) {
	before := time.Now()
	resp, errRefresh := NewProvider().RefreshAuth(context.Background(), pluginapi.AuthRefreshRequest{
		AuthID:      "virtual-auth.json",
		StorageJSON: []byte(`{"type":"gemini-cli","email":"user@example.com","project_id":"secondary-project","project_ids":["primary-project","secondary-project"],"refresh_token":"refresh-token"}`),
		Metadata: map[string]any{
			"virtual":        true,
			"parent_auth_id": "account.json",
			"project_id":     "secondary-project",
		},
		HTTPClient: refreshHTTPClient{},
	})
	if errRefresh != nil {
		t.Fatalf("RefreshAuth returned error: %v", errRefresh)
	}
	if resp.Auth.Metadata["virtual"] != true {
		t.Fatalf("virtual metadata = %#v, want virtual=true", resp.Auth.Metadata)
	}
	if resp.Auth.Metadata["parent_auth_id"] != "account.json" {
		t.Fatalf("parent auth id = %#v, want account.json", resp.Auth.Metadata["parent_auth_id"])
	}
	if got := resp.Auth.Metadata["project_id"]; got != "secondary-project" {
		t.Fatalf("project id metadata = %#v, want secondary-project", got)
	}
	if resp.Auth.Attributes["runtime_only"] != "true" {
		t.Fatalf("runtime_only attribute = %q, want true", resp.Auth.Attributes["runtime_only"])
	}
	expiryRaw, ok := resp.Auth.Metadata["expiry"].(string)
	if !ok || expiryRaw == "" {
		t.Fatalf("expiry metadata = %#v, want non-empty string", resp.Auth.Metadata["expiry"])
	}
	expiry, errParse := time.Parse(time.RFC3339, expiryRaw)
	if errParse != nil {
		t.Fatalf("parse expiry metadata: %v", errParse)
	}
	if expiry.Before(before.Add(59*time.Minute)) || expiry.After(time.Now().Add(61*time.Minute)) {
		t.Fatalf("expiry = %s, want about one hour from now", expiry)
	}
	if resp.NextRefreshAfter.IsZero() {
		t.Fatal("NextRefreshAfter is zero")
	}
	refreshLead := expiry.Sub(resp.NextRefreshAfter)
	if refreshLead < authRefreshLead-time.Second || refreshLead > authRefreshLead+time.Second {
		t.Fatalf("refresh lead = %s, want around %s", refreshLead, authRefreshLead)
	}
	var stored map[string]any
	if errDecode := json.Unmarshal(resp.Auth.StorageJSON, &stored); errDecode != nil {
		t.Fatalf("decode refreshed storage: %v", errDecode)
	}
	if stored["expiry"] == "" {
		t.Fatalf("stored expiry = %#v, want non-empty", stored["expiry"])
	}
	token, ok := stored["token"].(map[string]any)
	if !ok || token["expiry"] == "" {
		t.Fatalf("stored token = %#v, want nested expiry", stored["token"])
	}
}

type refreshHTTPClient struct{}

func (refreshHTTPClient) Do(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return pluginapi.HTTPResponse{
		StatusCode: 200,
		Body:       []byte(`{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`),
	}, nil
}

func (refreshHTTPClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, nil
}
