package cursorproto

import (
	_ "embed"
	"fmt"
	"sync"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

//go:embed agent.desc
var agentDescriptor []byte

//go:embed mcp_state.textproto
var mcpStateDescriptor []byte

var loadAgentFile = sync.OnceValues(func() (protoreflect.FileDescriptor, error) {
	var fileProto descriptorpb.FileDescriptorProto
	if err := proto.Unmarshal(agentDescriptor, &fileProto); err != nil {
		return nil, fmt.Errorf("decode Cursor descriptor: %w", err)
	}
	var additions descriptorpb.FileDescriptorProto
	if err := prototext.Unmarshal(mcpStateDescriptor, &additions); err != nil {
		return nil, fmt.Errorf("decode Cursor MCP state contract: %w", err)
	}
	for _, addition := range additions.MessageType {
		merged := false
		for _, existing := range fileProto.MessageType {
			if existing.GetName() == addition.GetName() {
				existing.Field = append(existing.Field, addition.Field...)
				merged = true
				break
			}
		}
		if !merged {
			fileProto.MessageType = append(fileProto.MessageType, addition)
		}
	}
	file, err := protodesc.NewFile(&fileProto, new(protoregistry.Files))
	if err != nil {
		return nil, fmt.Errorf("load Cursor descriptor: %w", err)
	}
	return file, nil
})

func newMessage(name protoreflect.Name) (*dynamicpb.Message, error) {
	file, err := loadAgentFile()
	if err != nil {
		return nil, err
	}
	descriptor := file.Messages().ByName(name)
	if descriptor == nil {
		return nil, fmt.Errorf("Cursor message %q is unavailable", name)
	}
	return dynamicpb.NewMessage(descriptor), nil
}

func field(message protoreflect.Message, name protoreflect.Name) protoreflect.FieldDescriptor {
	return message.Descriptor().Fields().ByName(name)
}

func requireField(message protoreflect.Message, name protoreflect.Name) (protoreflect.FieldDescriptor, error) {
	descriptor := field(message, name)
	if descriptor == nil {
		return nil, fmt.Errorf("Cursor field %q is unavailable on %s", name, message.Descriptor().FullName())
	}
	return descriptor, nil
}

func setString(message protoreflect.Message, name protoreflect.Name, value string) error {
	descriptor, err := requireField(message, name)
	if err != nil {
		return err
	}
	message.Set(descriptor, protoreflect.ValueOfString(value))
	return nil
}

func setUint32(message protoreflect.Message, name protoreflect.Name, value uint32) error {
	descriptor, err := requireField(message, name)
	if err != nil {
		return err
	}
	message.Set(descriptor, protoreflect.ValueOfUint32(value))
	return nil
}

func setBytes(message protoreflect.Message, name protoreflect.Name, value []byte) error {
	descriptor, err := requireField(message, name)
	if err != nil {
		return err
	}
	message.Set(descriptor, protoreflect.ValueOfBytes(append([]byte(nil), value...)))
	return nil
}

func setMessage(message protoreflect.Message, name protoreflect.Name, value protoreflect.Message) error {
	descriptor, err := requireField(message, name)
	if err != nil {
		return err
	}
	message.Set(descriptor, protoreflect.ValueOfMessage(value))
	return nil
}

func appendMessage(message protoreflect.Message, name protoreflect.Name, value protoreflect.Message) error {
	descriptor, err := requireField(message, name)
	if err != nil {
		return err
	}
	if !descriptor.IsList() || descriptor.Message() == nil {
		return fmt.Errorf("Cursor field %q is not a message list", name)
	}
	message.Mutable(descriptor).List().Append(protoreflect.ValueOfMessage(value))
	return nil
}

func nestedMessage(message protoreflect.Message, name protoreflect.Name) (*dynamicpb.Message, error) {
	descriptor, err := requireField(message, name)
	if err != nil {
		return nil, err
	}
	if descriptor.Message() == nil {
		return nil, fmt.Errorf("Cursor field %q is not a message", name)
	}
	return dynamicpb.NewMessage(descriptor.Message()), nil
}
