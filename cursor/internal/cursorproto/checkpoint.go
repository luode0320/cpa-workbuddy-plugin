package cursorproto

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func decodeCheckpoint(state protoreflect.Message) (ServerEvent, error) {
	checkpoint, err := proto.Marshal(state.Interface())
	if err != nil {
		return ServerEvent{}, fmt.Errorf("encode Cursor conversation checkpoint: %w", err)
	}
	if len(checkpoint) == 0 {
		return ServerEvent{}, fmt.Errorf("decode Cursor conversation checkpoint: empty state")
	}
	return ServerEvent{
		Kind: EventCheckpoint, Type: "conversation_checkpoint_update", Checkpoint: checkpoint,
	}, nil
}
