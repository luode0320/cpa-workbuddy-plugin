package cursorproto

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func Test_ReplyInteractionQuery_returns_correlated_refusal_without_user_data(t *testing.T) {
	cases := []struct {
		query protoreflect.Name
		path  []protoreflect.Name
	}{
		{"web_search_request_query", []protoreflect.Name{"web_search_request_response", "rejected", "reason"}},
		{"exa_search_request_query", []protoreflect.Name{"exa_search_request_response", "rejected", "reason"}},
		{"exa_fetch_request_query", []protoreflect.Name{"exa_fetch_request_response", "rejected", "reason"}},
		{"switch_mode_request_query", []protoreflect.Name{"switch_mode_request_response", "rejected", "reason"}},
		{"ask_question_interaction_query", []protoreflect.Name{"ask_question_interaction_response", "result", "rejected", "reason"}},
		{"create_plan_request_query", []protoreflect.Name{"create_plan_request_response", "result", "error", "error"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.query), func(t *testing.T) {
			server, err := newMessage("AgentServerMessage")
			require.NoError(t, err)
			query := server.Mutable(field(server, "interaction_query")).Message()
			require.NoError(t, setUint32(query, "id", 42))
			request := query.Mutable(field(query, tc.query)).Message()
			request.SetUnknown([]byte("\xa2\x06\x0ePRIVATE_CANARY"))
			raw, err := proto.Marshal(server)
			require.NoError(t, err)
			reply, handled, err := ReplyInteractionQuery(raw)
			require.NoError(t, err)
			require.True(t, handled)
			require.NotContains(t, string(reply), "PRIVATE_CANARY")
			client, err := newMessage("AgentClientMessage")
			require.NoError(t, err)
			require.NoError(t, proto.Unmarshal(reply, client))
			response := client.Get(field(client, "interaction_response")).Message()
			require.EqualValues(t, 42, response.Get(field(response, "id")).Uint())
			for _, name := range tc.path[:len(tc.path)-1] {
				descriptor := field(response, name)
				require.NotNil(t, descriptor)
				require.True(t, response.Has(descriptor), "%s missing", name)
				if oneof := descriptor.ContainingOneof(); oneof != nil {
					require.Equal(t, name, response.WhichOneof(oneof).Name())
				}
				response = response.Get(descriptor).Message()
			}
			require.Equal(t, interactionRefusal, response.Get(field(response, tc.path[len(tc.path)-1])).String())
		})
	}
}

func Test_ReplyInteractionQuery_preserves_unknown_and_nonquery_messages(t *testing.T) {
	for _, raw := range [][]byte{{}, {0x3a, 2, 8, 42}, {0x3a, 4, 8, 42, 0x4a, 0}} {
		reply, handled, err := ReplyInteractionQuery(raw)
		require.NoError(t, err)
		require.False(t, handled)
		require.Nil(t, reply)
	}
	reply, handled, err := ReplyInteractionQuery([]byte{0x3a, 4, 8, 42, 0x42, 0})
	require.ErrorIs(t, err, ErrUnsupportedInteraction)
	require.True(t, handled)
	require.Nil(t, reply, "VM setup has no error branch; must not fabricate success")
	_, _, err = ReplyInteractionQuery([]byte{0x3a, 0xff})
	require.Error(t, err)
}

func Test_ReplyInteractionQuery_preserves_existing_image_approval(t *testing.T) {
	raw := encodeGenerateImageQuery(t, 42, "Draw a blue fox", "tool-image-1")
	reply, handled, err := ReplyInteractionQuery(raw)
	require.NoError(t, err)
	require.True(t, handled)
	expected, _, err := ApproveGenerateImageRequest(raw)
	require.NoError(t, err)
	expectedMessage, err := newMessage("AgentClientMessage")
	require.NoError(t, err)
	actualMessage, err := newMessage("AgentClientMessage")
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(expected, expectedMessage))
	require.NoError(t, proto.Unmarshal(reply, actualMessage))
	require.True(t, proto.Equal(expectedMessage, actualMessage))
}
