package cursorproto

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func Test_ImageWriteExecutor_writes_generated_image_and_replies_success(t *testing.T) {
	projectFolder := filepath.Join(t.TempDir(), "project")
	target := filepath.Join(projectFolder, "assets", "generated.png")
	image := []byte("\x89PNG\r\n\x1a\nimage")
	executor := NewImageWriteExecutor(projectFolder)

	reply, handled, err := executor.HandleServerMessage(encodeImageWriteRequest(t, 9, "exec-image-write", target, image))

	require.NoError(t, err)
	require.True(t, handled)
	written, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, image, written)
	client, err := newMessage("AgentClientMessage")
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(reply, client))
	execReply := client.Get(field(client, "exec_client_message")).Message()
	require.EqualValues(t, 9, execReply.Get(field(execReply, "id")).Uint())
	require.Equal(t, "exec-image-write", execReply.Get(field(execReply, "exec_id")).String())
	result := execReply.Get(field(execReply, "write_result")).Message()
	success := result.Get(field(result, "success")).Message()
	require.Equal(t, target, success.Get(field(success, "path")).String())
	require.Zero(t, success.Get(field(success, "lines_created")).Int())
	require.EqualValues(t, len(image), success.Get(field(success, "file_size")).Int())

	require.NoError(t, executor.Cleanup())
	_, err = os.Stat(target)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func Test_ImageWriteExecutor_rejects_path_outside_project_assets(t *testing.T) {
	projectFolder := filepath.Join(t.TempDir(), "project")
	target := filepath.Join(projectFolder, "escape.png")
	executor := NewImageWriteExecutor(projectFolder)

	reply, handled, err := executor.HandleServerMessage(encodeImageWriteRequest(
		t, 9, "exec-image-write", target, []byte("\x89PNG\r\n\x1a\nimage"),
	))

	require.ErrorContains(t, err, "project assets directory")
	require.True(t, handled)
	require.Nil(t, reply)
	_, statErr := os.Stat(target)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func encodeImageWriteRequest(t *testing.T, id uint32, execID, path string, image []byte) []byte {
	t.Helper()
	server, err := newMessage("AgentServerMessage")
	require.NoError(t, err)
	execMessage, err := nestedMessage(server, "exec_server_message")
	require.NoError(t, err)
	require.NoError(t, setUint32(execMessage, "id", id))
	require.NoError(t, setString(execMessage, "exec_id", execID))
	args, err := nestedMessage(execMessage, "write_args")
	require.NoError(t, err)
	require.NoError(t, setString(args, "path", path))
	require.NoError(t, setString(args, "tool_call_id", "tool-image-1"))
	require.NoError(t, setBytes(args, "file_bytes", image))
	require.NoError(t, setMessage(execMessage, "write_args", args))
	require.NoError(t, setMessage(server, "exec_server_message", execMessage))
	raw, err := proto.Marshal(server)
	require.NoError(t, err)
	return raw
}
