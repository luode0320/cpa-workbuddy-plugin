package cursorproto

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	maxBlobEntries    = 4096
	maxBlobEntryBytes = 16 << 20
	maxBlobTotalBytes = 64 << 20
)

var ErrBlobCapacity = errors.New("Cursor blob capacity is exhausted")

type BlobStore struct {
	blobs      map[string][]byte
	totalBytes int
}

func NewBlobStore() *BlobStore {
	return &BlobStore{blobs: make(map[string][]byte)}
}

func (store *BlobStore) HandleServerMessage(raw []byte) ([]byte, bool, error) {
	server, err := newMessage("AgentServerMessage")
	if err != nil {
		return nil, false, err
	}
	if err := proto.Unmarshal(raw, server); err != nil {
		return nil, false, fmt.Errorf("decode Cursor server message for KV: %w", err)
	}
	active := server.WhichOneof(server.Descriptor().Oneofs().ByName("message"))
	if active == nil || active.Name() != "kv_server_message" {
		return nil, false, nil
	}
	kvServer := server.Get(active).Message()
	reply, err := store.kvReply(kvServer)
	if err != nil {
		return nil, true, err
	}
	return reply, true, nil
}

func (store *BlobStore) kvReply(kvServer protoreflect.Message) ([]byte, error) {
	idField, err := requireField(kvServer, "id")
	if err != nil {
		return nil, err
	}
	client, err := newMessage("AgentClientMessage")
	if err != nil {
		return nil, err
	}
	kvClient, err := nestedMessage(client, "kv_client_message")
	if err != nil {
		return nil, err
	}
	if err := setUint32(kvClient, "id", uint32(kvServer.Get(idField).Uint())); err != nil {
		return nil, err
	}
	operation := kvServer.WhichOneof(kvServer.Descriptor().Oneofs().ByName("message"))
	if operation != nil {
		if err := store.applyOperation(kvServer, kvClient, operation); err != nil {
			return nil, err
		}
	}
	if err := setMessage(client, "kv_client_message", kvClient); err != nil {
		return nil, err
	}
	raw, err := proto.Marshal(client)
	if err != nil {
		return nil, fmt.Errorf("encode Cursor KV reply: %w", err)
	}
	return raw, nil
}

func (store *BlobStore) applyOperation(
	kvServer protoreflect.Message,
	kvClient protoreflect.Message,
	operation protoreflect.FieldDescriptor,
) error {
	switch operation.Name() {
	case "get_blob_args":
		args := kvServer.Get(operation).Message()
		blobID := args.Get(field(args, "blob_id")).Bytes()
		result, err := nestedMessage(kvClient, "get_blob_result")
		if err != nil {
			return err
		}
		if data, ok := store.blobs[string(blobID)]; ok {
			if err := setBytes(result, "blob_data", data); err != nil {
				return err
			}
		}
		return setMessage(kvClient, "get_blob_result", result)
	case "set_blob_args":
		args := kvServer.Get(operation).Message()
		blobID := args.Get(field(args, "blob_id")).Bytes()
		blobData := args.Get(field(args, "blob_data")).Bytes()
		if err := store.set(blobID, blobData); err != nil {
			return err
		}
		result, err := nestedMessage(kvClient, "set_blob_result")
		if err != nil {
			return err
		}
		return setMessage(kvClient, "set_blob_result", result)
	default:
		return nil
	}
}

func (store *BlobStore) set(blobID, blobData []byte) error {
	if len(blobID) > maxBlobEntryBytes || len(blobData) > maxBlobEntryBytes-len(blobID) {
		return ErrBlobCapacity
	}
	key := string(blobID)
	previous, exists := store.blobs[key]
	projectedBytes := store.totalBytes - len(previous) + len(blobData)
	projectedEntries := len(store.blobs)
	if !exists {
		projectedEntries++
		projectedBytes += len(key)
	}
	if projectedBytes > maxBlobTotalBytes || projectedEntries > maxBlobEntries {
		return ErrBlobCapacity
	}
	store.blobs[key] = append([]byte(nil), blobData...)
	store.totalBytes = projectedBytes
	return nil
}
