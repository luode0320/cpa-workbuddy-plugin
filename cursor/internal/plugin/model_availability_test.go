package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Model_catalog_survives_optional_metadata_failure(t *testing.T) {
	client := contextMetadataClient{
		fakeModelCursorClient: fakeModelCursorClient{models: []string{"auto", "disabled"}},
		contexts:              map[string]int64{"auto": 2_000_000}, err: errors.New("metadata unavailable"),
	}
	handler := NewHandler(Dependencies{Cursor: client})
	request, err := json.Marshal(authModelRequest{StorageJSON: []byte(`{"type":"cursor-provider","access_token":"synthetic","refresh_token":"synthetic","disabled_models":["disabled"]}`)})
	require.NoError(t, err)

	raw, ok := handler.CallWithStatus(context.Background(), "model.for_auth", request)

	require.True(t, ok, string(raw))
	var result struct {
		Result struct{ Models []publishedContext }
	}
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Len(t, result.Result.Models, 1)
	require.Equal(t, "cursor/auto", result.Result.Models[0].ID)
	require.Nil(t, result.Result.Models[0].NativeContextLength)
	require.Nil(t, result.Result.Models[0].ContextLength)
	require.EqualValues(t, 1_000_000, result.Result.Models[0].ClientContextLimit)
}
