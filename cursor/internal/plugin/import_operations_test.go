package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
	"github.com/stretchr/testify/require"
)

func testSessionJWT(t *testing.T, subject string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"sub": subject, "exp": 4102444800})
	require.NoError(t, err)
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func Test_ParseCursorTokenImport_accepts_supported_shapes(t *testing.T) {
	jwt := testSessionJWT(t, "auth0|user_01M2A8KR0XZ5H8T3GHVY7NADPR")
	cases := map[string]string{
		"plain":       "user_01M2A8KR0XZ5H8T3GHVY7NADPR::" + jwt,
		"url_encoded": "user_01M2A8KR0XZ5H8T3GHVY7NADPR%3A%3A" + jwt,
		"cookie":      "WorkosCursorSessionToken=user_01M2A8KR0XZ5H8T3GHVY7NADPR::" + jwt + "; Path=/",
		"bare_jwt":    jwt,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			credentials, err := parseCursorTokenImport(input)
			require.NoError(t, err)
			require.Equal(t, "auth0|user_01M2A8KR0XZ5H8T3GHVY7NADPR", credentials.AccountID)
			require.Equal(t, jwt, credentials.RefreshToken)
			require.Equal(t, cursorauth.ProviderType, credentials.Type)
			require.Empty(t, credentials.AccessToken)
		})
	}
}

func Test_ParseCursorTokenImport_rejects_invalid_tokens(t *testing.T) {
	for _, input := range []string{"", "   ", "not-a-jwt", "a.b"} {
		_, err := parseCursorTokenImport(input)
		require.Error(t, err)
	}
}

type importHost struct {
	files      []hostAuthFile
	getByIndex map[string]hostAuthGetResponse
	savedName  string
	savedJSON  json.RawMessage
}

func (host *importHost) Call(_ context.Context, method string, request any) (json.RawMessage, error) {
	switch method {
	case "host.auth.list":
		return json.Marshal(hostAuthListResponse{Files: host.files})
	case "host.auth.get":
		raw, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		var payload struct {
			AuthIndex string `json:"auth_index"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		if response, ok := host.getByIndex[payload.AuthIndex]; ok {
			return json.Marshal(response)
		}
		return json.Marshal(hostAuthGetResponse{})
	case "host.auth.save":
		raw, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		var payload struct {
			Name string          `json:"name"`
			JSON json.RawMessage `json:"json"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		host.savedName = payload.Name
		host.savedJSON = append(json.RawMessage(nil), payload.JSON...)
		return json.Marshal(map[string]any{"name": payload.Name, "path": "/auth/" + payload.Name})
	}
	return nil, nil
}

func newExchangeServer(t *testing.T, accessToken string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodPost, request.Method)
		_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": accessToken})
	}))
	t.Cleanup(server.Close)
	return server
}

func Test_ImportCredential_exchanges_token_and_saves_auth_file(t *testing.T) {
	jwt := testSessionJWT(t, "auth0|user_01M2A8KR0XZ5H8T3GHVY7NADPR")
	server := newExchangeServer(t, "fresh-access-token")
	host := &importHost{}
	handler := NewHandler(Dependencies{
		Auth: cursorauth.NewService(server.Client(), cursorauth.Endpoints{RefreshURL: server.URL}),
		Host: host,
	})
	body, err := json.Marshal(map[string]any{"content": "user_01M2A8KR0XZ5H8T3GHVY7NADPR::" + jwt})
	require.NoError(t, err)

	response, err := handler.importCredential(context.Background(), body)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.True(t, strings.HasPrefix(host.savedName, cursorAuthFilePrefix))
	var saved map[string]any
	require.NoError(t, json.Unmarshal(host.savedJSON, &saved))
	require.Equal(t, providerName, saved["type"])
	require.Equal(t, providerName, saved["provider"])
	require.Equal(t, "fresh-access-token", saved["access_token"])
	require.Equal(t, jwt, saved["refresh_token"])
	require.Equal(t, "auth0|user_01M2A8KR0XZ5H8T3GHVY7NADPR", saved["account_id"])
	// 落盘 JSON 必须能被 ParseCredentials 重新解析（面板/宿主共用同一契约）。
	parsed, err := cursorauth.ParseCredentials(host.savedJSON)
	require.NoError(t, err)
	require.Equal(t, "fresh-access-token", parsed.AccessToken)
	require.Equal(t, jwt, parsed.RefreshToken)
}

func Test_ImportCredential_skips_duplicate_identity(t *testing.T) {
	jwt := testSessionJWT(t, "auth0|user_01M2A8KR0XZ5H8T3GHVY7NADPR")
	existing, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "existing-access", RefreshToken: "existing-refresh",
		AccountID: "auth0|user_01M2A8KR0XZ5H8T3GHVY7NADPR", Type: providerName,
	})
	require.NoError(t, err)
	server := newExchangeServer(t, "fresh-access-token")
	host := &importHost{
		files: []hostAuthFile{{AuthIndex: "cursor-auth", Name: "cursor-auth.json", Source: "file", Type: providerName, Provider: providerName}},
		getByIndex: map[string]hostAuthGetResponse{
			"cursor-auth": {AuthIndex: "cursor-auth", Name: "cursor-auth.json", JSON: existing},
		},
	}
	handler := NewHandler(Dependencies{
		Auth: cursorauth.NewService(server.Client(), cursorauth.Endpoints{RefreshURL: server.URL}),
		Host: host,
	})
	body, err := json.Marshal(map[string]any{"content": "user_01M2A8KR0XZ5H8T3GHVY7NADPR::" + jwt})
	require.NoError(t, err)

	response, err := handler.importCredential(context.Background(), body)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Empty(t, host.savedName, "duplicate import must not write a new auth file")
	require.Contains(t, string(response.Body), `"duplicate":true`)
	require.Contains(t, string(response.Body), "cursor-auth")
}

func Test_ExportCredentials_includes_physical_credential(t *testing.T) {
	storage, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access", RefreshToken: "refresh", AccountID: "account-a", Type: providerName,
	})
	require.NoError(t, err)
	host := &importHost{
		files: []hostAuthFile{{AuthIndex: "cursor-auth", Name: "cursor-auth.json", Source: "file", Type: providerName, Provider: providerName}},
		getByIndex: map[string]hostAuthGetResponse{
			"cursor-auth": {AuthIndex: "cursor-auth", Name: "cursor-auth.json", JSON: storage},
		},
	}
	handler := NewHandler(Dependencies{Host: host})

	response, err := handler.exportCredentials(context.Background())

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Contains(t, string(response.Body), `"plugin":"cursor-provider"`)
	require.Contains(t, string(response.Body), `"access_token":"access"`)
	require.Contains(t, string(response.Body), `"count":1`)
}

func Test_DeleteCredential_removes_physical_auth_file(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor-abc123.json")
	storage, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access", RefreshToken: "refresh", AccountID: "account-a", Type: providerName,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, storage, 0o600))
	host := &importHost{
		files: []hostAuthFile{{AuthIndex: "cursor-auth", Name: "cursor-abc123.json", Path: path, Source: "file", Type: providerName, Provider: providerName}},
		getByIndex: map[string]hostAuthGetResponse{
			"cursor-auth": {AuthIndex: "cursor-auth", Name: "cursor-abc123.json", Path: path, JSON: storage},
		},
	}
	handler := NewHandler(Dependencies{Host: host})
	body, err := json.Marshal(map[string]any{"auth_index": "cursor-auth"})
	require.NoError(t, err)

	response, err := handler.deleteCredential(context.Background(), body)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr), "physical auth file must be removed")
}

func Test_DeleteCredential_refuses_foreign_file_name(t *testing.T) {
	host := &importHost{
		files: []hostAuthFile{{AuthIndex: "cursor-auth", Name: "other.json", Source: "file", Type: providerName, Provider: providerName}},
	}
	handler := NewHandler(Dependencies{Host: host})
	body, err := json.Marshal(map[string]any{"auth_index": "cursor-auth"})
	require.NoError(t, err)

	response, err := handler.deleteCredential(context.Background(), body)

	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
}

func Test_ToggleCredential_writes_disabled_flag_directly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor-abc123.json")
	storage, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access", RefreshToken: "refresh", AccountID: "account-a", Type: providerName,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, storage, 0o600))
	host := &importHost{
		files: []hostAuthFile{{AuthIndex: "cursor-auth", Name: "cursor-abc123.json", Path: path, Source: "file", Type: providerName, Provider: providerName}},
		getByIndex: map[string]hostAuthGetResponse{
			"cursor-auth": {AuthIndex: "cursor-auth", Name: "cursor-abc123.json", Path: path, JSON: storage},
		},
	}
	handler := NewHandler(Dependencies{Host: host})
	body, err := json.Marshal(map[string]any{"auth_index": "cursor-auth"})
	require.NoError(t, err)

	response, err := handler.toggleCredential(context.Background(), body, true)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var stored map[string]any
	require.NoError(t, json.Unmarshal(raw, &stored))
	require.Equal(t, true, stored["disabled"])
	// 直写不得丢失凭据字段。
	require.Equal(t, "access", stored["access_token"])
}

func Test_Handler_ImportCredential_restores_from_backup_json(t *testing.T) {
	host := &importHost{}
	handler := NewHandler(Dependencies{Host: host})

	backupContent := `{
		"version": 1,
		"plugin": "cursor-provider",
		"accounts": [
			{
				"name": "cursor-backup1.json",
				"credential": {
					"type": "cursor-provider",
					"access_token": "backup-access",
					"refresh_token": "backup-refresh",
					"email": "backup@example.test"
				}
			}
		]
	}`

	body, err := json.Marshal(map[string]any{
		"filename": "backup.json",
		"content":  backupContent,
	})
	require.NoError(t, err)

	resp, err := handler.importCredential(context.Background(), body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(resp.Body), `"ok":true`)
	require.Contains(t, string(resp.Body), `"restored":1`)
	require.Equal(t, "cursor-backup1.json", host.savedName)
	require.Contains(t, string(host.savedJSON), "backup-access")
}
