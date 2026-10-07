package cursorauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Service_StartLogin_exposes_challenge_without_verifier(t *testing.T) {
	// Given
	service := NewService(http.DefaultClient, DefaultEndpoints())

	// When
	start, err := service.StartLogin(context.Background())

	// Then
	require.NoError(t, err)
	parsedURL, err := url.Parse(start.URL)
	require.NoError(t, err)
	service.mu.Lock()
	state, ok := service.loginStates[start.State]
	service.mu.Unlock()
	require.True(t, ok)
	require.LessOrEqual(t, len(start.State), 128)
	require.NotEmpty(t, parsedURL.Query().Get("challenge"))
	require.Equal(t, state.UUID, parsedURL.Query().Get("uuid"))
	require.NotContains(t, start.URL, state.Verifier)
}

func Test_Service_Refresh_preserves_refresh_token_when_response_omits_it(t *testing.T) {
	// Given
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Empty(t, request.Header.Get("authorization"))
		rawBody, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		var payload map[string]string
		require.NoError(t, json.Unmarshal(rawBody, &payload))
		require.Equal(t, "refresh_token", payload["grant_type"])
		require.Equal(t, cursorOAuthClientID, payload["client_id"])
		require.Equal(t, "old-refresh", payload["refresh_token"])
		body, err := json.Marshal(oauthTokenResponse{AccessToken: "new-access"})
		require.NoError(t, err)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	service := NewService(&http.Client{Transport: transport}, DefaultEndpoints())

	// When
	credentials, err := service.Refresh(context.Background(), Credentials{
		RefreshToken:   "old-refresh",
		Type:           "cursor-provider",
		DisabledModels: []string{"gpt-5"},
	})

	// Then
	require.NoError(t, err)
	require.Equal(t, "new-access", credentials.AccessToken)
	require.Equal(t, "old-refresh", credentials.RefreshToken)
	require.Equal(t, []string{"gpt-5"}, credentials.DisabledModels)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (transport roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func responseWithStatus(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

func Test_Service_PollLogin_returns_pending_on_not_found(t *testing.T) {
	// Given
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return responseWithStatus(http.StatusNotFound), nil
	})
	service := NewService(&http.Client{Transport: transport}, DefaultEndpoints())
	start, err := service.StartLogin(context.Background())
	require.NoError(t, err)

	// When
	result, err := service.PollLogin(context.Background(), start.State)

	// Then
	require.NoError(t, err)
	require.Equal(t, LoginPending, result.Status)
}
