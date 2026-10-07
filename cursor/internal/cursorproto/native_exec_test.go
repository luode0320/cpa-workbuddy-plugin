package cursorproto

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func Test_ReplyNativeReadOnlyExec_returns_typed_policy_result_for_grep_and_read(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation protoreflect.Name
		result    protoreflect.Name
		path      string
	}{
		{name: "grep", operation: "grep_args", result: "grep_result", path: "/downstream/workspace"},
		{name: "read", operation: "read_args", result: "read_result", path: "/downstream/workspace/config.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given
			raw := encodeNativeReadOnlyExecRequest(t, 41, "exec-41", test.operation, test.path)

			// When
			reply, handled, err := ReplyNativeReadOnlyExec(raw, []ToolDefinition{{Name: test.name}})

			// Then
			require.NoError(t, err)
			require.True(t, handled, "native %s request must receive a reply instead of stalling", test.name)
			require.NotEmpty(t, reply)
			client, err := newMessage("AgentClientMessage")
			require.NoError(t, err)
			require.NoError(t, proto.Unmarshal(reply, client))
			execReply := client.Get(field(client, "exec_client_message")).Message()
			require.EqualValues(t, 41, execReply.Get(field(execReply, "id")).Uint())
			require.Equal(t, "exec-41", execReply.Get(field(execReply, "exec_id")).String())
			result := execReply.Get(field(execReply, test.result)).Message()
			errorResult := result.Get(field(result, "error")).Message()
			require.Contains(t, errorResult.Get(field(errorResult, "error")).String(), "`"+test.name+"`")
			if test.name == "read" {
				require.Equal(t, test.path, errorResult.Get(field(errorResult, "path")).String())
			}
		})
	}
}

func Test_ReplyNativeShellExec_returns_failure_and_closes_stream(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation protoreflect.Name
		replies   int
	}{
		{name: "shell", operation: "shell_args", replies: 1},
		{name: "shell stream", operation: "shell_stream_args", replies: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given
			raw := encodeNativeShellExecRequest(t, 51, "exec-51", test.operation, "pwd", "/downstream/workspace")

			// When
			replies, handled, err := ReplyNativeShellExec(raw, []ToolDefinition{{Name: "bash"}})

			// Then
			require.NoError(t, err)
			require.True(t, handled, "native %s request must receive replies instead of stalling", test.name)
			require.Len(t, replies, test.replies)
			failureIndex := 0
			if test.operation == "shell_stream_args" {
				failureIndex = 3
				requireShellStreamSequence(t, replies, 51, "exec-51")
			}
			requireShellFailureReply(t, replies[failureIndex], 51, "exec-51")
		})
	}
}

func encodeNativeReadOnlyExecRequest(t *testing.T, id uint32, execID string, operation protoreflect.Name, path string) []byte {
	t.Helper()
	server, err := newMessage("AgentServerMessage")
	require.NoError(t, err)
	execMessage, err := nestedMessage(server, "exec_server_message")
	require.NoError(t, err)
	require.NoError(t, setUint32(execMessage, "id", id))
	require.NoError(t, setString(execMessage, "exec_id", execID))
	args, err := nestedMessage(execMessage, operation)
	require.NoError(t, err)
	if path != "" {
		require.NoError(t, setString(args, "path", path))
	}
	if operation == "grep_args" {
		require.NoError(t, setString(args, "pattern", "needle"))
	}
	require.NoError(t, setMessage(execMessage, operation, args))
	require.NoError(t, setMessage(server, "exec_server_message", execMessage))
	raw, err := proto.Marshal(server)
	require.NoError(t, err)
	return raw
}

func encodeNativeShellExecRequest(t *testing.T, id uint32, execID string, operation protoreflect.Name, command, workingDirectory string) []byte {
	t.Helper()
	server, err := newMessage("AgentServerMessage")
	require.NoError(t, err)
	execMessage, err := nestedMessage(server, "exec_server_message")
	require.NoError(t, err)
	require.NoError(t, setUint32(execMessage, "id", id))
	require.NoError(t, setString(execMessage, "exec_id", execID))
	args, err := nestedMessage(execMessage, operation)
	require.NoError(t, err)
	require.NoError(t, setString(args, "command", command))
	require.NoError(t, setString(args, "working_directory", workingDirectory))
	require.NoError(t, setMessage(execMessage, operation, args))
	require.NoError(t, setMessage(server, "exec_server_message", execMessage))
	raw, err := proto.Marshal(server)
	require.NoError(t, err)
	return raw
}

func requireShellFailureReply(t *testing.T, raw []byte, id uint32, execID string) {
	t.Helper()
	client, err := newMessage("AgentClientMessage")
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(raw, client))
	execReply := client.Get(field(client, "exec_client_message")).Message()
	require.EqualValues(t, id, execReply.Get(field(execReply, "id")).Uint())
	require.Equal(t, execID, execReply.Get(field(execReply, "exec_id")).String())
	result := execReply.Get(field(execReply, "shell_result")).Message()
	failure := result.Get(field(result, "failure")).Message()
	require.Equal(t, "pwd", failure.Get(field(failure, "command")).String())
	require.Equal(t, "/downstream/workspace", failure.Get(field(failure, "working_directory")).String())
	require.EqualValues(t, 1, failure.Get(field(failure, "exit_code")).Int())
	require.Contains(t, failure.Get(field(failure, "stderr")).String(), "`bash`")
	require.True(t, failure.Get(field(failure, "aborted")).Bool())
}

func requireShellStreamSequence(t *testing.T, replies [][]byte, id uint32, execID string) {
	t.Helper()
	for index, eventName := range []protoreflect.Name{"start", "stderr", "exit"} {
		client, err := newMessage("AgentClientMessage")
		require.NoError(t, err)
		require.NoError(t, proto.Unmarshal(replies[index], client))
		execReply := client.Get(field(client, "exec_client_message")).Message()
		require.EqualValues(t, id, execReply.Get(field(execReply, "id")).Uint())
		require.Equal(t, execID, execReply.Get(field(execReply, "exec_id")).String())
		stream := execReply.Get(field(execReply, "shell_stream")).Message()
		require.Equal(t, eventName, stream.WhichOneof(stream.Descriptor().Oneofs().ByName("event")).Name())
		event := stream.Get(field(stream, eventName)).Message()
		switch eventName {
		case "stderr":
			require.Contains(t, event.Get(field(event, "data")).String(), "`bash`")
		case "exit":
			require.EqualValues(t, 1, event.Get(field(event, "code")).Uint())
			require.Equal(t, "/downstream/workspace", event.Get(field(event, "cwd")).String())
			require.True(t, event.Get(field(event, "aborted")).Bool())
		}
	}
	client, err := newMessage("AgentClientMessage")
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(replies[4], client))
	control := client.Get(field(client, "exec_client_control_message")).Message()
	closeMessage := control.Get(field(control, "stream_close")).Message()
	require.EqualValues(t, id, closeMessage.Get(field(closeMessage, "id")).Uint())
}
