package cursorproto

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func Test_ReplyRequestContext_returns_runtime_environment_for_exec_request(t *testing.T) {
	server, err := newMessage("AgentServerMessage")
	require.NoError(t, err)
	execMessage, err := nestedMessage(server, "exec_server_message")
	require.NoError(t, err)
	require.NoError(t, setUint32(execMessage, "id", 7))
	require.NoError(t, setString(execMessage, "exec_id", "exec-image-1"))
	args, err := nestedMessage(execMessage, "request_context_args")
	require.NoError(t, err)
	require.NoError(t, setMessage(execMessage, "request_context_args", args))
	require.NoError(t, setMessage(server, "exec_server_message", execMessage))
	raw, err := proto.Marshal(server)
	require.NoError(t, err)
	event, err := DecodeServerEvent(raw)
	require.NoError(t, err)
	require.Equal(t, EventIgnored, event.Kind)
	require.Equal(t, "exec_server_message.request_context_args", event.Type)

	reply, handled, err := ReplyRequestContext(raw, RequestEnvironment{
		TimeZone:       "Asia/Shanghai",
		WorkspacePaths: []string{"/CLIProxyAPI"},
		ProjectFolder:  "/root/.cursor/projects/CLIProxyAPI",
	})

	require.NoError(t, err)
	require.True(t, handled)
	client, err := newMessage("AgentClientMessage")
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(reply, client))
	execReply := client.Get(field(client, "exec_client_message")).Message()
	require.EqualValues(t, 7, execReply.Get(field(execReply, "id")).Uint())
	require.Equal(t, "exec-image-1", execReply.Get(field(execReply, "exec_id")).String())
	result := execReply.Get(field(execReply, "request_context_result")).Message()
	success := result.Get(field(result, "success")).Message()
	requestContext := success.Get(field(success, "request_context")).Message()
	environment := requestContext.Get(field(requestContext, "env")).Message()
	require.Equal(t, "Asia/Shanghai", environment.Get(field(environment, "time_zone")).String())
	workspacePaths := environment.Get(field(environment, "workspace_paths")).List()
	require.Equal(t, 1, workspacePaths.Len())
	require.Equal(t, "/CLIProxyAPI", workspacePaths.Get(0).String())
	require.Equal(t, "/root/.cursor/projects/CLIProxyAPI", environment.Get(field(environment, "project_folder")).String())
}

func Test_ReplyRequestContext_ignores_other_server_messages(t *testing.T) {
	raw, err := encodeTestInteractionUpdate("text_delta", "text", "hello")
	require.NoError(t, err)

	reply, handled, err := ReplyRequestContext(raw, RequestEnvironment{})

	require.NoError(t, err)
	require.False(t, handled)
	require.Nil(t, reply)
}
