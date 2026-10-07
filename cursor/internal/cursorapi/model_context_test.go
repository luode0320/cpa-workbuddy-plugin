package cursorapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_ModelContextLengths_omits_missing_invalid_and_max_only_limits(t *testing.T) {
	// Given
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(`{"models":[
			{"name":"missing","contextTokenLimitForMaxMode":1000000},
			{"name":"negative","contextTokenLimit":-1},
			{"name":"max-only","contextTokenLimit":1000000,"supportsNonMaxMode":false},
			{"name":"base","serverModelName":"server","contextTokenLimit":123456,"variants":[{"legacySlug":"max-variant","isMaxMode":true}]}
		]}`))
		require.NoError(t, err)
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, HTTPClient: server.Client()})
	require.NoError(t, err)

	// When
	limits, err := client.ModelContextLengths(context.Background(), "token")

	// Then
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"base": 123456, "server": 123456}, limits)
}

func Test_ModelContextLengths_rejects_invalid_metadata(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		status int
		body   string
	}{
		{"http_failure", http.StatusServiceUnavailable, `{"error":"private upstream detail"}`},
		{"malformed_json", http.StatusOK, `{"models":`},
		{"invalid_type", http.StatusOK, `{"models":[{"name":"bad","contextTokenLimit":"256k"}]}`},
		{"oversized", http.StatusOK, strings.Repeat(" ", maxModelResponseBytes+1)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			// Given
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(fixture.status)
				_, err := w.Write([]byte(fixture.body))
				require.NoError(t, err)
			}))
			defer server.Close()
			client, err := NewClient(Config{BaseURL: server.URL, HTTPClient: server.Client()})
			require.NoError(t, err)

			// When
			limits, err := client.ModelContextLengths(context.Background(), "token")

			// Then
			require.Error(t, err)
			require.Nil(t, limits)
			require.NotContains(t, err.Error(), "private upstream detail")
		})
	}
}
