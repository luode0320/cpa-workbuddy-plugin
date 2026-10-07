package plugin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecutorABI_decodes_v72139_fields_and_legacy_omissions(t *testing.T) {
	raw := []byte(`{
		"AuthID":"auth-fixture","AuthProvider":"cursor-provider","Model":"cursor/auto",
		"Format":"chat-completions","Stream":true,"Alt":"sse",
		"Headers":{"X-Test":["present"]},"Query":{"mode":["fast"]},
		"OriginalRequest":"e30=","SourceFormat":"chat-completions",
		"ResponseFormat":"chat-completions","Payload":"e30=",
		"Metadata":{"execution_session_id":"session-fixture","derived_session_id":"derived-fixture","request_path":"/v1/chat/completions"},
		"StorageJSON":"e30=","AuthMetadata":{"label":"fixture"},
		"AuthAttributes":{"tier":"test"},"stream_id":"stream-fixture"
	}`)

	var got executorRequest
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "auth-fixture", got.AuthID)
	require.Equal(t, "cursor-provider", got.AuthProvider)
	require.Equal(t, "chat-completions", got.SourceFormat)
	require.Equal(t, "chat-completions", got.ResponseFormat)
	require.Equal(t, "present", got.Headers.Get("X-Test"))
	require.Equal(t, []string{"fast"}, got.Query["mode"])
	require.Equal(t, "session-fixture", got.Metadata.ExecutionSessionID.String())
	require.Equal(t, "derived-fixture", got.Metadata.DerivedSessionID.String())
	require.JSONEq(t, `"/v1/chat/completions"`, string(got.Metadata.Fields["request_path"]))
	require.JSONEq(t, `{"label":"fixture"}`, string(got.AuthMetadata))
	require.Equal(t, "test", got.AuthAttributes["tier"])

	var legacy executorRequest
	require.NoError(t, json.Unmarshal([]byte(`{"Model":"cursor/auto","Payload":"e30=","StorageJSON":"e30="}`), &legacy))
	require.Empty(t, legacy.AuthID)
	require.Empty(t, legacy.Headers)
	require.Empty(t, legacy.SourceFormat)
	require.Empty(t, legacy.ResponseFormat)
	require.False(t, legacy.Metadata.ExecutionSessionID.Valid())
}

func TestSessionIdentity_uses_exact_stable_precedence(t *testing.T) {
	tests := []struct {
		name    string
		request executorRequest
		want    string
		ok      bool
	}{
		{
			name: "execution metadata wins every conflict",
			request: requestWithIdentitySources(identitySources{
				execution: "execution-value", sessionHeader: "header-value", underscoreHeader: "session-header-value",
				clientHeader: "client-value", body: `{"conversation_id":"conversation-value","session_id":"body-value"}`, derived: "derived-value",
			}),
			want: "execution-value", ok: true,
		},
		{
			name: "x session header wins lower sources",
			request: requestWithIdentitySources(identitySources{
				sessionHeader: "header-value", underscoreHeader: "session-header-value", clientHeader: "client-value",
				body: `{"conversation_id":"conversation-value","session_id":"body-value"}`, derived: "derived-value",
			}),
			want: "header-value", ok: true,
		},
		{
			name: "session underscore header wins client header",
			request: requestWithIdentitySources(identitySources{
				underscoreHeader: "session-header-value", clientHeader: "client-value",
				body: `{"conversation_id":"conversation-value","session_id":"body-value"}`, derived: "derived-value",
			}),
			want: "session-header-value", ok: true,
		},
		{
			name: "client request header wins body",
			request: requestWithIdentitySources(identitySources{
				clientHeader: "client-value", body: `{"conversation_id":"conversation-value","session_id":"body-value"}`, derived: "derived-value",
			}),
			want: "client-value", ok: true,
		},
		{
			name: "conversation body wins session body",
			request: requestWithIdentitySources(identitySources{
				body: `{"conversation_id":"conversation-value","session_id":"body-value"}`, derived: "derived-value",
			}),
			want: "conversation-value", ok: true,
		},
		{
			name:    "session body wins derived metadata",
			request: requestWithIdentitySources(identitySources{body: `{"session_id":"body-value"}`, derived: "derived-value"}),
			want:    "body-value", ok: true,
		},
		{
			name:    "derived metadata is final source",
			request: requestWithIdentitySources(identitySources{body: `{}`, derived: "derived-value"}),
			want:    "derived-value", ok: true,
		},
		{
			name:    "missing identity disables reuse",
			request: requestWithIdentitySources(identitySources{body: `{}`}),
			ok:      false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identity, ok := test.request.stableSessionIdentity()
			require.Equal(t, test.ok, ok)
			require.Equal(t, test.want, identity.String())
		})
	}
}

func TestSessionIdentity_ignores_malformed_or_blank_sources(t *testing.T) {
	var request executorRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"Headers":{"X-Session-ID":["   "]},
		"OriginalRequest":"eyJjb252ZXJzYXRpb25faWQiOjEyM30=",
		"Metadata":{"execution_session_id":123,"derived_session_id":"  derived-value  "}
	}`), &request))

	identity, ok := request.stableSessionIdentity()
	require.True(t, ok)
	require.Equal(t, "derived-value", identity.String())
}

type identitySources struct {
	execution        string
	sessionHeader    string
	underscoreHeader string
	clientHeader     string
	body             string
	derived          string
}

func requestWithIdentitySources(source identitySources) executorRequest {
	return executorRequest{
		Headers: map[string][]string{
			"X-Session-ID":        {source.sessionHeader},
			"Session_id":          {source.underscoreHeader},
			"X-Client-Request-Id": {source.clientHeader},
		},
		OriginalRequest: []byte(source.body),
		Metadata: executorMetadata{
			ExecutionSessionID: optionalIdentity(source.execution),
			DerivedSessionID:   optionalIdentity(source.derived),
		},
	}
}
