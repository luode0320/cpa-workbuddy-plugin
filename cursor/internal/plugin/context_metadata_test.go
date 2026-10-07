package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type contextMetadataClient struct {
	fakeModelCursorClient
	contexts map[string]int64
	err      error
}

func (client contextMetadataClient) ModelContextLengths(context.Context, string) (map[string]int64, error) {
	return client.contexts, client.err
}

type publishedContext struct {
	ID                  string
	NativeContextLength *int64
	ContextLength       *int64
	ClientContextLimit  int64
}

func Test_Handler_publishes_native_capacity_separately_from_client_cap(t *testing.T) {
	client := contextMetadataClient{
		fakeModelCursorClient: fakeModelCursorClient{models: []string{"large", "small", "unknown"}},
		contexts:              map[string]int64{"large": 2_000_000, "small": 256_000},
	}
	handler := NewHandler(Dependencies{Cursor: client})
	request, err := json.Marshal(authModelRequest{StorageJSON: []byte(`{"type":"cursor-provider","access_token":"synthetic","refresh_token":"synthetic"}`)})
	require.NoError(t, err)
	response, ok := handler.CallWithStatus(context.Background(), "model.for_auth", request)
	require.True(t, ok, string(response))
	var result struct {
		Result struct{ Models []publishedContext }
	}
	require.NoError(t, json.Unmarshal(response, &result))
	require.Len(t, result.Result.Models, 3)
	for _, model := range result.Result.Models {
		require.EqualValues(t, 1_000_000, model.ClientContextLimit)
		switch model.ID {
		case "cursor/large":
			require.NotNil(t, model.NativeContextLength)
			require.EqualValues(t, 2_000_000, *model.NativeContextLength)
			require.NotNil(t, model.ContextLength)
			require.EqualValues(t, 1_000_000, *model.ContextLength)
		case "cursor/small":
			require.NotNil(t, model.NativeContextLength)
			require.EqualValues(t, 256_000, *model.NativeContextLength)
			require.NotNil(t, model.ContextLength)
			require.EqualValues(t, 256_000, *model.ContextLength)
		case "cursor/unknown":
			require.Nil(t, model.NativeContextLength)
			require.Nil(t, model.ContextLength)
		default:
			t.Fatalf("unexpected model: %s", model.ID)
		}
	}
}

func Test_Handler_management_exposes_context_policy_and_metadata_failure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "available", true: "unavailable"}[failed], func(t *testing.T) {
			client := contextMetadataClient{fakeModelCursorClient: fakeModelCursorClient{models: []string{"large", "unknown"}}, contexts: map[string]int64{"large": 2_000_000}}
			if failed {
				client.err = errors.New("synthetic upstream metadata failure")
			}
			handler := NewHandler(Dependencies{Cursor: client, Host: &fakeHostCaller{credentialJSON: json.RawMessage(`{"type":"cursor-provider","access_token":"synthetic","refresh_token":"synthetic"}`)}})
			response, err := handler.managementStatus(context.Background())
			require.NoError(t, err)
			require.Equal(t, 200, response.StatusCode)
			var result struct {
				ContextPolicy struct {
					Limit  int64  `json:"client_context_limit"`
					Method string `json:"count_method"`
				} `json:"context_policy"`
				Accounts []struct {
					MetadataStatus string `json:"model_context_status"`
					Models         []struct {
						ID        string `json:"id"`
						Native    *int64 `json:"native_context_length"`
						Effective *int64 `json:"context_length"`
						Limit     int64  `json:"client_context_limit"`
					} `json:"models"`
				} `json:"accounts"`
			}
			require.NoError(t, json.Unmarshal(response.Body, &result))
			require.EqualValues(t, 1_000_000, result.ContextPolicy.Limit)
			require.Equal(t, "conservative_utf8_bytes_v1", result.ContextPolicy.Method)
			require.Len(t, result.Accounts, 1)
			require.Equal(t, map[bool]string{false: "available", true: "unavailable"}[failed], result.Accounts[0].MetadataStatus)
			require.Len(t, result.Accounts[0].Models, 2)
			for _, model := range result.Accounts[0].Models {
				require.EqualValues(t, 1_000_000, model.Limit)
				if failed || model.ID == "unknown" {
					require.Nil(t, model.Native)
					require.Nil(t, model.Effective)
				} else {
					require.NotNil(t, model.Native)
					require.EqualValues(t, 2_000_000, *model.Native)
					require.NotNil(t, model.Effective)
					require.EqualValues(t, 1_000_000, *model.Effective)
				}
			}
		})
	}
}
