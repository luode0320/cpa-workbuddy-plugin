package plugin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Handler_Register_declares_cursor_auth_models_and_executor(t *testing.T) {
	// Given
	handler := NewHandler(Dependencies{})

	// When
	raw := handler.Call(context.Background(), "plugin.register", []byte(`{"schema_version":3}`))

	// Then
	var response envelope
	require.NoError(t, json.Unmarshal(raw, &response))
	require.True(t, response.OK)
	require.Contains(t, string(response.Result), `"auth_provider":true`)
	require.Contains(t, string(response.Result), `"model_provider":true`)
	require.Contains(t, string(response.Result), `"executor":true`)
	require.Contains(t, string(response.Result), `"management_api":true`)
	require.Contains(t, string(response.Result), `"usage_plugin":false`)
	require.Contains(t, string(response.Result), `"request_interceptor":true`)
	require.Contains(t, string(response.Result), `"request_lifecycle_plugin":true`)
	require.Contains(t, string(response.Result), `"Version":"0.1.0"`)
	require.Contains(t, string(response.Result), `"GitHubRepository":"https://github.com/luode0320/cpa-workbuddy-plugin"`)
	require.Contains(t, string(response.Result), `"Logo":"https://raw.githubusercontent.com/luode0320/cpa-workbuddy-plugin/main/assets/icons/Cursor.png"`)
}

func Test_Handler_ManagementRegister_exposes_cursor_management_resource_and_authenticated_routes(t *testing.T) {
	handler := NewHandler(Dependencies{})

	raw := handler.Call(context.Background(), "management.register", nil)

	var response envelope
	require.NoError(t, json.Unmarshal(raw, &response))
	require.True(t, response.OK)
	require.Contains(t, string(response.Result), `"Path":"/panel"`)
	require.Contains(t, string(response.Result), `"Path":"/status"`)
	require.Contains(t, string(response.Result), `"Menu":"Cursor"`)
	require.Contains(t, string(response.Result), `Cursor dashboard: status, usage, token import and model controls`)
	require.Contains(t, string(response.Result), `"Path":"/plugins/cursor-provider/status"`)
	require.Contains(t, string(response.Result), `"Path":"/plugins/cursor-provider/accounts"`)
	require.Contains(t, string(response.Result), `"Path":"/plugins/cursor-provider/models"`)
	require.Contains(t, string(response.Result), `"Path":"/plugins/cursor-provider/test-active"`)
	require.Contains(t, string(response.Result), `"Path":"/plugins/cursor-provider/disabled-models"`)
}
