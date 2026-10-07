package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorapi"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func Test_Handler_models_use_discovered_default_context_and_refresh(t *testing.T) {
	// Given
	var capacity atomic.Int64
	capacity.Store(256000)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent.v1.AgentService/GetUsableModels":
			var response []byte
			for _, id := range []string{"family-fast", "legacy", "alias", "unknown", "disabled"} {
				model := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), id)
				response = protowire.AppendBytes(protowire.AppendTag(response, 1, protowire.BytesType), model)
			}
			_, err := w.Write(response)
			require.NoError(t, err)
		case "/aiserver.v1.AiService/AvailableModels":
			var request struct {
				UseModelParameters    bool
				UseReactModelPicker   bool
				ExcludeMaxNamedModels bool
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			require.True(t, request.UseModelParameters)
			require.True(t, request.UseReactModelPicker)
			require.True(t, request.ExcludeMaxNamedModels)
			require.Equal(t, "Bearer access", r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{
				"name": "family", "contextTokenLimit": capacity.Load(), "contextTokenLimitForMaxMode": 1000000,
				"legacySlugs": []string{"legacy"}, "idAliases": []string{"alias"},
				"variants": []map[string]any{
					{"legacySlug": "family-fast", "isMaxMode": false},
					{"legacySlug": "family-fast", "isMaxMode": true},
				},
			}}}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := cursorapi.NewClient(cursorapi.Config{BaseURL: server.URL, HTTPClient: server.Client()})
	require.NoError(t, err)
	handler := NewHandler(Dependencies{Cursor: client})
	request, err := json.Marshal(authModelRequest{StorageJSON: []byte(`{"type":"cursor-provider","access_token":"access","refresh_token":"refresh","disabled_models":["disabled"]}`)})
	require.NoError(t, err)
	for _, expected := range []struct{ native, effective int64 }{{256000, 256000}, {384000, 384000}, {2000000, 1000000}} {
		capacity.Store(expected.native)
		// When
		raw, ok := handler.CallWithStatus(context.Background(), "model.for_auth", request)
		// Then
		require.True(t, ok, string(raw))
		var response struct {
			Result struct {
				Models []struct {
					ID                  string
					ContextLength       *int64
					NativeContextLength *int64
					ClientContextLimit  int64
					MaxCompletionTokens *int64
				}
			}
		}
		require.NoError(t, json.Unmarshal(raw, &response))
		require.Len(t, response.Result.Models, 4)
		for _, model := range response.Result.Models {
			require.Nil(t, model.MaxCompletionTokens)
			require.EqualValues(t, 1000000, model.ClientContextLimit)
			if model.ID == "cursor/unknown" {
				require.Nil(t, model.ContextLength)
				require.Nil(t, model.NativeContextLength)
				continue
			}
			require.NotNil(t, model.ContextLength)
			require.Equal(t, expected.effective, *model.ContextLength, model.ID)
			require.NotNil(t, model.NativeContextLength)
			require.Equal(t, expected.native, *model.NativeContextLength, model.ID)
		}
	}
}
