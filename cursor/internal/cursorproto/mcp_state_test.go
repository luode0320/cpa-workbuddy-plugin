package cursorproto

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func Test_ReplyRequestContext_answers_empty_MCP_catalog_query(t *testing.T) {
	// Given
	exec := protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), 42)
	exec = protowire.AppendBytes(protowire.AppendTag(exec, 36, protowire.BytesType), nil)
	raw := protowire.AppendBytes(protowire.AppendTag(nil, 2, protowire.BytesType), exec)
	// When
	reply, handled, err := ReplyRequestContext(raw, RequestEnvironment{})
	// Then
	require.NoError(t, err)
	require.True(t, handled, "MCP catalog query must be answered instead of timing out")
	client, err := newMessage("AgentClientMessage")
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(reply, client))
	response := client.Get(field(client, "exec_client_message")).Message()
	require.EqualValues(t, 42, response.Get(field(response, "id")).Uint())
	result := response.Get(field(response, "mcp_state_exec_result")).Message()
	require.Equal(t, "success", string(result.WhichOneof(result.Descriptor().Oneofs().ByName("result")).Name()))
	success := result.Get(field(result, "success")).Message()
	require.Zero(t, success.Get(field(success, "servers")).List().Len())
}

func Test_ReplyRequestContext_scopes_MCP_catalog_to_supplied_tools(t *testing.T) {
	for _, tc := range []struct {
		name        string
		identifiers []string
		kick        bool
		servers     int
	}{
		{"all", nil, false, 1},
		{"matching", []string{toolProvider}, false, 1},
		{"foreign", []string{"other-server"}, false, 0},
		{"kick_only", []string{toolProvider}, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			var args []byte
			for _, id := range tc.identifiers {
				args = protowire.AppendString(protowire.AppendTag(args, 1, protowire.BytesType), id)
			}
			if tc.kick {
				args = append(args, 16, 1)
			}
			exec := protowire.AppendBytes(protowire.AppendTag(nil, 36, protowire.BytesType), args)
			raw := protowire.AppendBytes(protowire.AppendTag(nil, 2, protowire.BytesType), exec)
			environment := RequestEnvironment{Tools: []ToolDefinition{
				{Name: "echo", Description: "Returns the value", Parameters: []byte(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`)},
			}}
			// When
			reply, handled, err := ReplyRequestContext(raw, environment)
			// Then
			require.NoError(t, err)
			require.True(t, handled)
			client, err := newMessage("AgentClientMessage")
			require.NoError(t, err)
			require.NoError(t, proto.Unmarshal(reply, client))
			response := client.Get(field(client, "exec_client_message")).Message()
			result := response.Get(field(response, "mcp_state_exec_result")).Message()
			success := result.Get(field(result, "success")).Message()
			servers := success.Get(field(success, "servers")).List()
			require.Equal(t, tc.servers, servers.Len())
			if tc.servers == 0 {
				return
			}
			server := servers.Get(0).Message()
			require.Equal(t, toolProvider, server.Get(field(server, "server_identifier")).String())
			tools := server.Get(field(server, "tools")).List()
			require.Equal(t, 1, tools.Len())
			tool := tools.Get(0).Message()
			require.Equal(t, environment.Tools[0].Name, tool.Get(field(tool, "tool_name")).String())
			require.Equal(t, toolProvider, tool.Get(field(tool, "provider_identifier")).String())
			require.Equal(t, environment.Tools[0].Description, tool.Get(field(tool, "description")).String())
			var schema structpb.Value
			require.NoError(t, proto.Unmarshal(tool.Get(field(tool, "input_schema")).Bytes(), &schema))
			encoded, err := schema.MarshalJSON()
			require.NoError(t, err)
			require.JSONEq(t, string(environment.Tools[0].Parameters), string(encoded))
		})
	}
}
