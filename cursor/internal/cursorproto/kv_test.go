package cursorproto

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func Test_BlobStore_replies_to_set_then_get(t *testing.T) {
	// Given
	store := NewBlobStore()
	setRequest := encodeKVServerMessage(t, 7, "set_blob_args", []byte("blob-id"), []byte("blob-data"))
	getRequest := encodeKVServerMessage(t, 8, "get_blob_args", []byte("blob-id"), nil)

	// When
	setReply, handled, err := store.HandleServerMessage(setRequest)
	require.NoError(t, err)
	require.True(t, handled)
	getReply, handled, err := store.HandleServerMessage(getRequest)

	// Then
	require.NoError(t, err)
	require.True(t, handled)
	requireKVReply(t, setReply, 7, "set_blob_result", nil)
	requireKVReply(t, getReply, 8, "get_blob_result", []byte("blob-data"))
}

func encodeKVServerMessage(t *testing.T, id uint32, operation string, blobID, blobData []byte) []byte {
	t.Helper()
	server, err := newMessage("AgentServerMessage")
	require.NoError(t, err)
	kv, err := nestedMessage(server, "kv_server_message")
	require.NoError(t, err)
	require.NoError(t, setUint32(kv, "id", id))
	args, err := nestedMessage(kv, protoreflectName(operation))
	require.NoError(t, err)
	require.NoError(t, setBytes(args, "blob_id", blobID))
	if operation == "set_blob_args" {
		require.NoError(t, setBytes(args, "blob_data", blobData))
	}
	require.NoError(t, setMessage(kv, protoreflectName(operation), args))
	require.NoError(t, setMessage(server, "kv_server_message", kv))
	raw, err := proto.Marshal(server)
	require.NoError(t, err)
	return raw
}

func requireKVReply(t *testing.T, raw []byte, id uint32, resultName string, blobData []byte) {
	t.Helper()
	client, err := newMessage("AgentClientMessage")
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(raw, client))
	kv := client.Get(field(client, "kv_client_message")).Message()
	require.Equal(t, uint64(id), kv.Get(field(kv, "id")).Uint())
	result := kv.Get(field(kv, protoreflectName(resultName))).Message()
	if blobData != nil {
		require.Equal(t, blobData, result.Get(field(result, "blob_data")).Bytes())
	}
}

func protoreflectName(value string) protoreflect.Name {
	return protoreflect.Name(value)
}
