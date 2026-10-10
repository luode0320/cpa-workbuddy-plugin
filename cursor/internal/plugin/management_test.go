package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorapi"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/openai"

	"github.com/stretchr/testify/require"
)

func Test_Handler_ManagementStatus_reports_models_disabled_rules_and_honest_quota_state(t *testing.T) {
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken:    "secret-access",
		RefreshToken:   "secret-refresh",
		Type:           "cursor-provider",
		Email:          "owner@example.test",
		DisabledModels: []string{"gpt-5"},
	})
	require.NoError(t, err)
	host := &fakeHostCaller{credentialJSON: credentials}
	handler := NewHandler(Dependencies{
		Cursor: fakeModelCursorClient{models: []string{"auto", "gpt-5"}},
		Host:   host,
	})

	response, err := handler.managementStatus(context.Background())

	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	require.Contains(t, string(response.Body), `"subscription_quota":{"status":"unavailable"`)
	require.Contains(t, string(response.Body), `"reason":"Cursor does not publish a subscription remaining-quota API"`)
	require.Contains(t, string(response.Body), `"id":"gpt-5","disabled":true`)
	require.NotContains(t, string(response.Body), "secret-access")
	require.NotContains(t, string(response.Body), "secret-refresh")
}

func Test_Handler_ManagementStatus_ignores_runtime_projection_of_physical_cursor_auth(t *testing.T) {
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken:  "secret-access",
		RefreshToken: "secret-refresh",
		AccountID:    "account-1",
		Type:         "cursor-provider",
	})
	require.NoError(t, err)
	host := &fakeHostCaller{
		credentialJSON: credentials,
		listJSON: json.RawMessage(`{"files":[
			{"auth_index":"cursor-auth","name":"cursor-auth.json","type":"cursor-provider","provider":"cursor-provider","status":"active"},
			{"auth_index":"cursor-auth-runtime","name":"cursor-auth.json","type":"cursor-provider","provider":"cursor-provider","status":"active","runtime_only":true}
		]}`),
	}
	handler := NewHandler(Dependencies{Cursor: fakeModelCursorClient{models: []string{"auto"}}, Host: host})

	response, err := handler.managementStatus(context.Background())

	require.NoError(t, err)
	var status cursorManagementStatus
	require.NoError(t, json.Unmarshal(response.Body, &status))
	require.Len(t, status.Accounts, 1)
	require.Equal(t, "cursor-auth", status.Accounts[0].AuthIndex)
}

func Test_Handler_ManagementStatus_deduplicates_rows_for_same_cursor_identity(t *testing.T) {
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken:  "secret-access",
		RefreshToken: "secret-refresh",
		AccountID:    "account-1",
		Type:         "cursor-provider",
	})
	require.NoError(t, err)
	host := &fakeHostCaller{
		credentialJSON: credentials,
		listJSON: json.RawMessage(`{"files":[
			{"auth_index":"cursor-auth","name":"cursor-auth.json","type":"cursor-provider","provider":"cursor-provider","status":"active"},
			{"auth_index":"cursor-auth-stale","name":"legacy-cursor-auth.json","type":"cursor-provider","provider":"cursor-provider","status":"active"}
		]}`),
	}
	handler := NewHandler(Dependencies{Cursor: fakeModelCursorClient{models: []string{"auto"}}, Host: host})

	response, err := handler.managementStatus(context.Background())

	require.NoError(t, err)
	var status cursorManagementStatus
	require.NoError(t, json.Unmarshal(response.Body, &status))
	require.Len(t, status.Accounts, 1)
}

func Test_Handler_ManagementStatus_deduplicates_same_email_with_different_account_ids(t *testing.T) {
	first, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access-1", RefreshToken: "refresh-1", AccountID: "account-1", Email: "owner@example.test", Type: "cursor-provider",
	})
	require.NoError(t, err)
	second, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access-2", RefreshToken: "refresh-2", AccountID: "account-2", Email: "OWNER@example.test", Type: "cursor-provider",
	})
	require.NoError(t, err)
	host := &fakeHostCaller{
		listJSON: json.RawMessage(`{"files":[
			{"auth_index":"cursor-auth-1","name":"cursor-auth-1.json","type":"cursor-provider","provider":"cursor-provider","status":"active","success":2,"failed":3},
			{"auth_index":"cursor-auth-2","name":"cursor-auth-2.json","type":"cursor-provider","provider":"cursor-provider","status":"active","success":5,"failed":7}
		]}`),
		credentialJSONByIndex: map[string]json.RawMessage{"cursor-auth-1": first, "cursor-auth-2": second},
	}
	handler := NewHandler(Dependencies{Cursor: fakeModelCursorClient{models: []string{"auto"}}, Host: host})
	handler.usage.recordTokens("cursor-auth-1", openai.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8})
	handler.usage.recordTokens("cursor-auth-2", openai.Usage{PromptTokens: 7, CompletionTokens: 11, TotalTokens: 18})
	observeRequestAuth(t, handler, "request-1", "cursor-auth-1")
	completeCursorRequest(t, handler, "request-1", "succeeded")
	observeRequestAuth(t, handler, "request-2", "cursor-auth-2")
	completeCursorRequest(t, handler, "request-2", "failed")
	handler.usage.checkpoints.recordLookup("cursor-auth-1", true)
	handler.usage.checkpoints.recordLookup("cursor-auth-2", false)

	response, err := handler.managementStatus(context.Background())

	require.NoError(t, err)
	var status cursorManagementStatus
	require.NoError(t, json.Unmarshal(response.Body, &status))
	require.Len(t, status.Accounts, 1)
	account := status.Accounts[0]
	require.EqualValues(t, 2, account.LocalUsage.ExecutorRuns)
	require.EqualValues(t, 2, account.LocalUsage.Requests)
	require.EqualValues(t, 1, account.LocalUsage.Succeeded)
	require.EqualValues(t, 1, account.LocalUsage.Failed)
	require.EqualValues(t, 10, account.LocalUsage.InputTokens)
	require.EqualValues(t, 16, account.LocalUsage.OutputTokens)
	require.EqualValues(t, 26, account.LocalUsage.TotalTokens)
	require.Equal(t, "failed", account.LocalUsage.LastOutcome)
	require.EqualValues(t, 7, account.HostRuntime.SuccessAttempts)
	require.EqualValues(t, 10, account.HostRuntime.FailedAttempts)
	require.EqualValues(t, 1, account.CheckpointMetrics.Hits)
	require.EqualValues(t, 1, account.CheckpointMetrics.Misses)
	require.Equal(t, "error", account.Status)
}

func Test_Handler_ManagementStatus_merges_transitively_bridged_cursor_identities(t *testing.T) {
	first, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access-1", RefreshToken: "refresh-1", AccountID: "account-a", Email: "first@example.test", Type: "cursor-provider",
	})
	require.NoError(t, err)
	second, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access-2", RefreshToken: "refresh-2", AccountID: "account-b", Email: "second@example.test", Type: "cursor-provider",
	})
	require.NoError(t, err)
	bridge, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access-3", RefreshToken: "refresh-3", AccountID: "account-a", Email: "second@example.test", Type: "cursor-provider",
	})
	require.NoError(t, err)
	host := &fakeHostCaller{
		listJSON: json.RawMessage(`{"files":[
			{"auth_index":"cursor-auth-1","name":"cursor-auth-1.json","type":"cursor-provider","provider":"cursor-provider","status":"active","success":1},
			{"auth_index":"cursor-auth-2","name":"cursor-auth-2.json","type":"cursor-provider","provider":"cursor-provider","status":"active","success":2},
			{"auth_index":"cursor-auth-bridge","name":"cursor-auth-bridge.json","type":"cursor-provider","provider":"cursor-provider","status":"active","success":3}
		]}`),
		credentialJSONByIndex: map[string]json.RawMessage{
			"cursor-auth-1": first, "cursor-auth-2": second, "cursor-auth-bridge": bridge,
		},
	}
	handler := NewHandler(Dependencies{Cursor: fakeModelCursorClient{models: []string{"auto"}}, Host: host})

	response, err := handler.managementStatus(context.Background())

	require.NoError(t, err)
	var status cursorManagementStatus
	require.NoError(t, json.Unmarshal(response.Body, &status))
	require.Len(t, status.Accounts, 1)
	require.Equal(t, "cursor-auth-1", status.Accounts[0].AuthIndex)
	require.EqualValues(t, 6, status.Accounts[0].HostRuntime.SuccessAttempts)
}

func Test_Handler_ManagementStatus_ignores_stale_memory_only_cursor_record(t *testing.T) {
	// Given
	persisted, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access-1", RefreshToken: "refresh-1", AccountID: "account-1", Type: "cursor-provider",
	})
	require.NoError(t, err)
	stale, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "access-2", RefreshToken: "refresh-2", AccountID: "account-2", Type: "cursor-provider",
	})
	require.NoError(t, err)
	host := &fakeHostCaller{
		listJSON: json.RawMessage(`{"files":[
			{"auth_index":"cursor-auth","name":"cursor-auth.json","path":"/auth/cursor-auth.json","source":"file","type":"cursor-provider","provider":"cursor-provider","status":"active"},
			{"auth_index":"cursor-auth-stale","name":"deleted-cursor-auth.json","path":"/auth/deleted-cursor-auth.json","source":"memory","type":"cursor-provider","provider":"cursor-provider","status":"active"}
		]}`),
		credentialJSONByIndex: map[string]json.RawMessage{"cursor-auth": persisted, "cursor-auth-stale": stale},
	}
	handler := NewHandler(Dependencies{Cursor: fakeModelCursorClient{models: []string{"auto"}}, Host: host})

	// When
	response, err := handler.managementStatus(context.Background())

	// Then
	require.NoError(t, err)
	var status cursorManagementStatus
	require.NoError(t, json.Unmarshal(response.Body, &status))
	require.Len(t, status.Accounts, 1)
	require.Equal(t, "cursor-auth", status.Accounts[0].AuthIndex)
}

func Test_Handler_UpdateDisabledModels_persists_rules_in_cursor_auth_without_losing_credentials(t *testing.T) {
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken:  "secret-access",
		RefreshToken: "secret-refresh",
		Type:         "cursor-provider",
	})
	require.NoError(t, err)
	host := &fakeHostCaller{credentialJSON: credentials}
	handler := NewHandler(Dependencies{
		Cursor: fakeModelCursorClient{models: []string{"auto", "gpt-5"}},
		Host:   host,
	})
	body := []byte(`{"auth_index":"cursor-auth","disabled_models":["cursor/gpt-5"]}`)

	response, err := handler.updateDisabledModels(context.Background(), body)

	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	var saved cursorauth.Credentials
	require.NoError(t, json.Unmarshal(host.savedJSON, &saved))
	require.Equal(t, "secret-access", saved.AccessToken)
	require.Equal(t, "secret-refresh", saved.RefreshToken)
	require.Equal(t, []string{"gpt-5"}, saved.DisabledModels)
	require.Equal(t, "cursor-auth.json", host.savedName)
}

func Test_Handler_ManagementStatus_includes_plugin_local_estimated_usage(t *testing.T) {
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken:  "secret-access",
		RefreshToken: "secret-refresh",
		Type:         "cursor-provider",
	})
	require.NoError(t, err)
	handler := NewHandler(Dependencies{
		Cursor: &recordingCursorClient{steps: []cursorRunStep{successfulTextStep("answer", "conversation-a", []byte("checkpoint-a"))}},
		Host:   &fakeHostCaller{credentialJSON: credentials},
	})
	request := executorFixture(t, "session-a", "account-a", "cursor-auth", "auto", "", []map[string]any{textMessage("user", "question")})
	observeRequestAuth(t, handler, "request-a", "cursor-auth")
	_, err = handler.execute(context.Background(), request)
	require.NoError(t, err)
	completeCursorRequest(t, handler, "request-a", "succeeded")

	response, err := handler.managementStatus(context.Background())

	require.NoError(t, err)
	require.Contains(t, string(response.Body), `"local_usage":{"scope":"plugin_process"`)
	require.Contains(t, string(response.Body), `"estimated":true`)
	require.Contains(t, string(response.Body), `"executor_runs":1`)
	require.Contains(t, string(response.Body), `"requests":1`)
	require.Contains(t, string(response.Body), `"succeeded":1`)
	require.NotContains(t, string(response.Body), `"cached_tokens"`)
}

func Test_Handler_ManagementStatus_separates_plugin_outcomes_from_host_attempts_and_joins_metrics_by_auth_id(t *testing.T) {
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "secret-access", RefreshToken: "secret-refresh", AccountID: "account-a", Type: "cursor-provider",
	})
	require.NoError(t, err)
	host := &fakeHostCaller{
		credentialJSON: credentials,
		listJSON: json.RawMessage(`{"files":[{
			"id":"cursor-runtime-id",
			"auth_index":"cursor-auth-index",
			"name":"cursor-runtime-id.json",
			"source":"file",
			"type":"cursor-provider",
			"provider":"cursor-provider",
			"status":"error",
			"status_message":"Cursor upstream timed out",
			"success":12,
			"failed":13
		}]}`),
	}
	client := &recordingCursorClient{steps: []cursorRunStep{
		{err: fmt.Errorf("transient Cursor timeout")},
		successfulTextStep("answer", "conversation-a", []byte("checkpoint-a")),
	}}
	handler := NewHandler(Dependencies{Cursor: client, Host: host})
	request := executorFixture(t, "session-a", "account-a", "cursor-runtime-id", "auto", "", []map[string]any{textMessage("user", "question")})

	observeRequestAuth(t, handler, "request-a", "cursor-runtime-id")
	_, err = handler.execute(context.Background(), request)
	require.Error(t, err)
	_, err = handler.execute(context.Background(), request)
	require.NoError(t, err)
	completeCursorRequest(t, handler, "request-a", "succeeded")
	completeCursorRequest(t, handler, "request-a", "failed")
	response, err := handler.managementStatus(context.Background())
	require.NoError(t, err)

	var status struct {
		Accounts []struct {
			Status     string `json:"status"`
			LocalUsage struct {
				ExecutorRuns int64      `json:"executor_runs"`
				Requests     int64      `json:"requests"`
				Succeeded    int64      `json:"succeeded"`
				Failed       int64      `json:"failed"`
				TotalTokens  int64      `json:"total_tokens"`
				LastOutcome  string     `json:"last_outcome"`
				UpdatedAt    *time.Time `json:"updated_at"`
			} `json:"local_usage"`
			HostRuntime struct {
				Scope           string `json:"scope"`
				Status          string `json:"status"`
				StatusMessage   string `json:"status_message"`
				SuccessAttempts int64  `json:"success_attempts"`
				FailedAttempts  int64  `json:"failed_attempts"`
			} `json:"host_runtime"`
			CheckpointMetrics checkpointMetricStatus `json:"checkpoint_metrics"`
		} `json:"accounts"`
	}
	require.NoError(t, json.Unmarshal(response.Body, &status))
	require.Len(t, status.Accounts, 1)
	account := status.Accounts[0]
	require.Equal(t, "active", account.Status)
	require.EqualValues(t, 2, account.LocalUsage.ExecutorRuns)
	require.EqualValues(t, 1, account.LocalUsage.Requests)
	require.EqualValues(t, 1, account.LocalUsage.Succeeded)
	require.Zero(t, account.LocalUsage.Failed)
	require.Positive(t, account.LocalUsage.TotalTokens)
	require.Equal(t, "succeeded", account.LocalUsage.LastOutcome)
	require.NotNil(t, account.LocalUsage.UpdatedAt)
	require.Equal(t, "cli_proxy_process", account.HostRuntime.Scope)
	require.Equal(t, "error", account.HostRuntime.Status)
	require.Equal(t, "Cursor upstream timed out", account.HostRuntime.StatusMessage)
	require.EqualValues(t, 12, account.HostRuntime.SuccessAttempts)
	require.EqualValues(t, 13, account.HostRuntime.FailedAttempts)
	require.Positive(t, account.CheckpointMetrics.FullReplayBytes)
}

func Test_CursorPluginStatus_uses_successful_discovery_as_readiness_before_local_outcome(t *testing.T) {
	require.Equal(t, "active", cursorPluginStatus("error", ""))
	require.Equal(t, "error", cursorPluginStatus("active", "failed"))
	require.Equal(t, "active", cursorPluginStatus("error", "succeeded"))
	require.Equal(t, "disabled", cursorPluginStatus("disabled", "succeeded"))
}

func Test_Handler_ManagementStatus_aggregates_checkpoint_metrics_without_sensitive_state(t *testing.T) {
	// Given
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken: "secret-access", RefreshToken: "secret-refresh", AccountID: "account-a", Type: "cursor-provider",
	})
	require.NoError(t, err)
	client := &recordingCursorClient{steps: []cursorRunStep{
		{events: []cursorproto.ServerEvent{{Kind: cursorproto.EventText, Text: "seed-answer"}, {Kind: cursorproto.EventDone}}, result: cursorapi.RunResult{ConversationID: "conversation-secret", Checkpoint: []byte("checkpoint-secret"), OutputExposed: true, TTFT: 10 * time.Millisecond}},
		{result: cursorapi.RunResult{ConversationID: "conversation-secret", TTFT: 20 * time.Millisecond}, err: fmt.Errorf("checkpoint rejected: %w", cursorapi.ErrInvalidArgument)},
		{events: []cursorproto.ServerEvent{{Kind: cursorproto.EventText, Text: "fallback-answer"}, {Kind: cursorproto.EventDone}}, result: cursorapi.RunResult{ConversationID: "fresh-conversation-secret", Checkpoint: []byte("fresh-checkpoint-secret"), OutputExposed: true, TTFT: 30 * time.Millisecond}},
	}}
	handler := NewHandler(Dependencies{Cursor: client, Host: &fakeHostCaller{credentialJSON: credentials}})
	seed := executorFixture(t, "session-secret", "account-a", "cursor-auth", "auto", "", []map[string]any{textMessage("user", "seed")})
	continuation := executorFixture(t, "session-secret", "account-a", "cursor-auth", "auto", "", []map[string]any{
		textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "next"),
	})

	_, err = handler.execute(context.Background(), seed)
	require.NoError(t, err)
	observeRequestAuth(t, handler, "request-seed", "cursor-auth")
	completeCursorRequest(t, handler, "request-seed", "succeeded")

	// When
	_, err = handler.execute(context.Background(), continuation)
	require.NoError(t, err)
	observeRequestAuth(t, handler, "request-continuation", "cursor-auth")
	completeCursorRequest(t, handler, "request-continuation", "succeeded")
	response, err := handler.managementStatus(context.Background())

	// Then
	require.NoError(t, err)
	var status cursorManagementStatus
	require.NoError(t, json.Unmarshal(response.Body, &status))
	require.Len(t, status.Accounts, 1)
	metrics := status.Accounts[0].CheckpointMetrics
	require.EqualValues(t, 1, metrics.Hits)
	require.EqualValues(t, 2, metrics.Misses)
	require.EqualValues(t, 1, metrics.Invalidations)
	require.EqualValues(t, 1, metrics.Fallbacks)
	require.Positive(t, metrics.FullReplayBytes)
	require.Positive(t, metrics.SuffixBytes)
	require.EqualValues(t, 3, metrics.TTFTSamples)
	require.EqualValues(t, 60, metrics.TTFTTotalMilliseconds)
	var logical struct {
		Accounts []struct {
			LocalUsage struct {
				Requests  int64 `json:"requests"`
				Succeeded int64 `json:"succeeded"`
				Failed    int64 `json:"failed"`
			} `json:"local_usage"`
		} `json:"accounts"`
	}
	require.NoError(t, json.Unmarshal(response.Body, &logical))
	require.EqualValues(t, 2, logical.Accounts[0].LocalUsage.Requests)
	require.EqualValues(t, 2, logical.Accounts[0].LocalUsage.Succeeded)
	require.Zero(t, logical.Accounts[0].LocalUsage.Failed)
	require.NotContains(t, string(response.Body), "session-secret")
	require.NotContains(t, string(response.Body), "conversation-secret")
	require.NotContains(t, string(response.Body), "checkpoint-secret")
	require.NotContains(t, string(response.Body), "secret-access")
}

func Test_Handler_ManagementResource_serves_bilingual_shell_without_exposing_auth_data(t *testing.T) {
	// Given
	handler := NewHandler(Dependencies{})
	rawRequest, err := json.Marshal(managementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/cursor-provider/status",
	})
	require.NoError(t, err)

	// When
	result, err := handler.handleManagement(context.Background(), rawRequest)

	// Then
	require.NoError(t, err)
	response := result.(managementResponse)
	require.Equal(t, 200, response.StatusCode)
	require.Equal(t, "text/html; charset=utf-8", response.Headers.Get("content-type"))
	require.Contains(t, string(response.Body), "Cursor 管理")
	require.Contains(t, string(response.Body), "Cursor Management")
	require.Contains(t, string(response.Body), `disableAll: "全部禁用"`)
	require.Contains(t, string(response.Body), `disableAll: "Disable all"`)
	require.Contains(t, string(response.Body), `disableAll.dataset.action = "disable-all"`)
	require.Contains(t, string(response.Body), `saveSettings: "保存设置"`)
	require.Contains(t, string(response.Body), `saveSettings: "Save settings"`)
	require.Contains(t, string(response.Body), `save.dataset.action = "save-settings"`)
	require.Contains(t, string(response.Body), `checkpointMetrics: "检查点指标"`)
	require.Contains(t, string(response.Body), `checkpointMetrics: "Checkpoint metrics"`)
	require.Contains(t, string(response.Body), `logicalRequestsHelp: "每\u2060次`)
	for _, protectedTerm := range []string{
		`检\u2060查\u2060点`,
		`近\u2060期\u2060请\u2060求`,
		`固\u2060定\u2060内\u2060存`,
		`完\u2060成\u2060事\u2060件`,
		`设\u2060计\u2060负\u2060载`,
		`误\u2060判\u2060概\u2060率`,
		`亿\u2060分\u2060之\u2060六`,
		`本\u2060地\u2060终\u2060态\u2060样\u2060本`,
		`最\u2060近\u2060结\u2060果`,
		`实\u2060际\u2060执\u2060行\u2060次\u2060数`,
	} {
		require.Contains(t, string(response.Body), protectedTerm)
	}
	visibleBody := strings.ReplaceAll(string(response.Body), `\u2060`, "")
	require.Contains(t, visibleBody, `近期请求 ID 使用 16 MiB 固定内存去重`)
	require.Contains(t, visibleBody, `100 万个`)
	require.Contains(t, string(response.Body), `logicalRequestsHelp: "At most one terminal outcome`)
	require.Contains(t, string(response.Body), `Recent request IDs use 16 MiB fixed-memory deduplication.`)
	require.Contains(t, string(response.Body), `false-positive probability is below 6e-8.`)
	require.Contains(t, string(response.Body), `A false positive skips one local terminal sample`)
	require.Contains(t, string(response.Body), `hostSchedulerMetrics: "宿主调度指标"`)
	require.Contains(t, string(response.Body), `hostSchedulerMetrics: "Host scheduler metrics"`)
	require.Contains(t, string(response.Body), `account.local_usage?.succeeded || 0`)
	require.Contains(t, string(response.Body), `account.local_usage?.executor_runs || 0`)
	require.Contains(t, string(response.Body), `account.host_runtime?.success_attempts || 0`)
	require.Contains(t, string(response.Body), `unknown: "未知"`)
	require.Contains(t, string(response.Body), `unknown: "Unknown"`)
	require.Contains(t, string(response.Body), `metric(translate("cachedTokens"), translate("unknown"))`)
	require.Contains(t, string(response.Body), `checkpoint.ttft_average_ms == null ? translate("unknown")`)
	// 面板免密直入：密钥输入区默认隐藏；有密钥时启动直接加载状态，无密钥才显示输入区。
	require.Contains(t, string(response.Body), `<div class="credential-grid" id="authBox" style="display:none">`)
	require.Contains(t, string(response.Body), `function showAuth() {`)
	require.Contains(t, string(response.Body), `function saveKey() {`)
	require.Contains(t, string(response.Body), `authBox.style.display = "";`)
	require.Contains(t, string(response.Body), `authBox.style.display = "none";`)
	require.Contains(t, string(response.Body), `loadButton.addEventListener("click", () => saveKey());`)
	require.Contains(t, string(response.Body), `if (event.key === "Enter") saveKey();`)
	require.Contains(t, string(response.Body), `if (window.__cursorThemeSync) window.__cursorThemeSync();`)
	require.Contains(t, string(response.Body), `if (getKey()) {`)
	require.Contains(t, string(response.Body), `keyHelp: "未自动获取到管理密钥`)
	require.Contains(t, string(response.Body), `keyHelp: "No management key was detected`)
	require.NotContains(t, string(response.Body), `quotaTitle`)
	require.NotContains(t, string(response.Body), `订阅额度说明`)
	require.NotContains(t, string(response.Body), `panel warning`)
	require.Contains(t, string(response.Body), `.nowrap { white-space: nowrap; }`)
	require.Contains(t, string(response.Body), `--focus: #1d4ed8;`)
	require.Contains(t, string(response.Body), `.models { max-height: none; overflow: visible; }`)
	require.Contains(t, string(response.Body), `accountNotice.setAttribute("aria-live", "polite")`)
	require.Contains(t, string(response.Body), `setAccountNoticeKey(noticeKey, { count: selected.length }, "success")`)
	require.Contains(t, string(response.Body), `modelLegendMany: "OAuth models to disable ({count} available models)"`)
	require.Contains(t, string(response.Body), `<select id="language"`)
	require.Contains(t, string(response.Body), `<option value="zh-CN">中文</option>`)
	require.Contains(t, string(response.Body), `<option value="en">English</option>`)
	require.Contains(t, string(response.Body), "document.documentElement.lang = currentLanguage")
	require.Contains(t, string(response.Body), `createElement("wbr")`)
	require.NotContains(t, string(response.Body), "overflow-wrap: anywhere")
	require.NotContains(t, string(response.Body), "access_token")
}

type fakeHostCaller struct {
	credentialJSON        json.RawMessage
	credentialJSONByIndex map[string]json.RawMessage
	listJSON              json.RawMessage
	savedJSON             json.RawMessage
	savedName             string
}

func (host *fakeHostCaller) Call(_ context.Context, method string, request any) (json.RawMessage, error) {
	switch method {
	case "host.auth.list":
		if len(host.listJSON) > 0 {
			return host.listJSON, nil
		}
		return json.RawMessage(`{"files":[{"auth_index":"cursor-auth","name":"cursor-auth.json","type":"cursor-provider","provider":"cursor-provider","label":"owner@example.test","status":"active","success":3,"failed":1}]}`), nil
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
		if credential, ok := host.credentialJSONByIndex[payload.AuthIndex]; ok {
			return json.RawMessage(`{"auth_index":"` + payload.AuthIndex + `","name":"` + payload.AuthIndex + `.json","json":` + string(credential) + `}`), nil
		}
		return json.RawMessage(`{"auth_index":"cursor-auth","name":"cursor-auth.json","json":` + string(host.credentialJSON) + `}`), nil
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
		return json.RawMessage(`{"name":"cursor-auth.json","path":"/auth/cursor-auth.json"}`), nil
	default:
		return nil, nil
	}
}

func Test_Handler_Management_serves_panel_resource(t *testing.T) {
	handler := NewHandler(Dependencies{})
	req := managementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/cursor-provider/panel",
	}
	rawReq, err := json.Marshal(req)
	require.NoError(t, err)

	respAny, err := handler.handleManagement(context.Background(), rawReq)
	require.NoError(t, err)
	resp, ok := respAny.(managementResponse)
	require.True(t, ok)
	require.Equal(t, 200, resp.StatusCode)
	require.Contains(t, string(resp.Body), "<!doctype html>")
	require.Contains(t, string(resp.Body), "Cursor")
}

func Test_Handler_Management_accounts_route(t *testing.T) {
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken:  "secret-access",
		RefreshToken: "secret-refresh",
		Type:         "cursor-provider",
		Email:        "user@example.test",
	})
	require.NoError(t, err)
	host := &fakeHostCaller{credentialJSON: credentials}
	handler := NewHandler(Dependencies{
		Cursor: fakeModelCursorClient{models: []string{"auto"}},
		Host:   host,
	})

	req := managementRequest{
		Method: "GET",
		Path:   "/plugins/cursor-provider/accounts",
	}
	rawReq, err := json.Marshal(req)
	require.NoError(t, err)

	respAny, err := handler.handleManagement(context.Background(), rawReq)
	require.NoError(t, err)
	resp, ok := respAny.(managementResponse)
	require.True(t, ok)
	require.Equal(t, 200, resp.StatusCode)
	require.Contains(t, string(resp.Body), "cursor-auth")
}

func Test_Handler_Management_models_and_test_active_routes(t *testing.T) {
	credentials, err := cursorauth.MarshalCredentials(cursorauth.Credentials{
		AccessToken:  "secret-access",
		RefreshToken: "secret-refresh",
		Type:         "cursor-provider",
		Email:        "user@example.test",
	})
	require.NoError(t, err)
	host := &fakeHostCaller{credentialJSON: credentials}
	handler := NewHandler(Dependencies{
		Cursor: fakeModelCursorClient{models: []string{"auto", "cursor-fast"}},
		Host:   host,
	})

	// 1. 测试 /models 接口
	reqModels := managementRequest{
		Method: "GET",
		Path:   "/plugins/cursor-provider/models?auth_index=cursor-auth",
	}
	rawReqModels, err := json.Marshal(reqModels)
	require.NoError(t, err)
	respAny, err := handler.handleManagement(context.Background(), rawReqModels)
	require.NoError(t, err)
	resp, ok := respAny.(managementResponse)
	require.True(t, ok)
	require.Equal(t, 200, resp.StatusCode)
	require.Contains(t, string(resp.Body), "auto")

	// 2. 测试 /test-active 接口
	bodyTestActive, err := json.Marshal(map[string]string{
		"auth_index": "cursor-auth",
		"model":      "auto",
	})
	require.NoError(t, err)
	reqActive := managementRequest{
		Method: "POST",
		Path:   "/plugins/cursor-provider/test-active",
		Body:   bodyTestActive,
	}
	rawReqActive, err := json.Marshal(reqActive)
	require.NoError(t, err)
	respAnyActive, err := handler.handleManagement(context.Background(), rawReqActive)
	require.NoError(t, err)
	respActive, ok := respAnyActive.(managementResponse)
	require.True(t, ok)
	require.Equal(t, 200, respActive.StatusCode)
	require.Contains(t, string(respActive.Body), `"ok":true`)
	require.Contains(t, string(respActive.Body), `"model":"auto"`)
}
