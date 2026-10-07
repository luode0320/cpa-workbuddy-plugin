package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"

	"github.com/stretchr/testify/require"
)

func Test_AuthParse_defers_physical_identity_to_host(t *testing.T) {
	for _, fileName := range []string{"cursor-d86f70b3c693a77a.json", "renamed-cursor.json", "nested/cursor.json"} {
		t.Run(fileName, func(t *testing.T) {
			// Given
			handler := NewHandler(Dependencies{})
			credentials := cursorauth.Credentials{
				Type: "cursor-provider", AccountID: "account-test", AccessToken: "test-access", RefreshToken: "test-refresh",
				DisabledModels: []string{"disabled-model"}, ToolLoopGuardTools: []string{"test-tool"},
			}
			storage, err := cursorauth.MarshalCredentials(credentials)
			require.NoError(t, err)
			request, err := json.Marshal(authParseRequest{FileName: fileName, RawJSON: storage})
			require.NoError(t, err)

			// When
			raw := handler.Call(context.Background(), "auth.parse", request)

			// Then
			var response envelope
			require.NoError(t, json.Unmarshal(raw, &response))
			require.True(t, response.OK)
			var result struct {
				Handled bool
				Auth    authData
			}
			require.NoError(t, json.Unmarshal(response.Result, &result))
			require.True(t, result.Handled)
			require.Empty(t, result.Auth.ID)
			require.Equal(t, fileName, result.Auth.FileName)
			require.JSONEq(t, string(storage), string(result.Auth.StorageJSON))
		})
	}
}

func Test_AuthPoll_uses_same_ID_as_saved_file(t *testing.T) {
	// Given
	token := "test." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"account-test","exp":4102444800}`)) + ".test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		err := json.NewEncoder(w).Encode(struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		}{AccessToken: token, RefreshToken: "test-refresh"})
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	service := cursorauth.NewService(server.Client(), cursorauth.Endpoints{LoginURL: server.URL, PollURL: server.URL})
	start, err := service.StartLogin(context.Background())
	require.NoError(t, err)
	request, err := json.Marshal(authPollRequest{State: start.State})
	require.NoError(t, err)
	handler := NewHandler(Dependencies{Auth: service})

	// When
	raw := handler.Call(context.Background(), "auth.login.poll", request)

	// Then
	var response envelope
	require.NoError(t, json.Unmarshal(raw, &response))
	require.True(t, response.OK)
	var result struct {
		Status string
		Auth   authData
	}
	require.NoError(t, json.Unmarshal(response.Result, &result))
	require.Equal(t, "success", result.Status)
	require.Equal(t, "cursor-d86f70b3c693a77a.json", result.Auth.ID)
	require.Equal(t, "cursor-d86f70b3c693a77a.json", result.Auth.FileName)
}

func Test_AuthRefresh_inherits_existing_identity_from_host(t *testing.T) {
	// Given
	token := "test." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"account-test","exp":4102444800}`)) + ".test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		err := json.NewEncoder(w).Encode(struct {
			AccessToken string `json:"access_token"`
		}{AccessToken: token})
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	storage, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		Type: "cursor-provider", AccountID: "account-test", AccessToken: "test-access", RefreshToken: "test-refresh",
	})
	require.NoError(t, err)
	request, err := json.Marshal(struct {
		AuthID      string
		StorageJSON []byte
	}{AuthID: "renamed-cursor.json", StorageJSON: storage})
	require.NoError(t, err)
	service := cursorauth.NewService(server.Client(), cursorauth.Endpoints{RefreshURL: server.URL})
	handler := NewHandler(Dependencies{Auth: service})

	// When
	raw := handler.Call(context.Background(), "auth.refresh", request)

	// Then
	var response envelope
	require.NoError(t, json.Unmarshal(raw, &response))
	require.True(t, response.OK)
	var result struct{ Auth authData }
	require.NoError(t, json.Unmarshal(response.Result, &result))
	require.Empty(t, result.Auth.ID)
	require.Empty(t, result.Auth.FileName)
	refreshed, err := cursorauth.ParseCredentials(result.Auth.StorageJSON)
	require.NoError(t, err)
	require.Equal(t, token, refreshed.AccessToken)
	require.Equal(t, "test-refresh", refreshed.RefreshToken)
}
