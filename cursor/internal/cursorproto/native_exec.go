package cursorproto

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func ReplyNativeReadOnlyExec(raw []byte, tools []ToolDefinition) ([]byte, bool, error) {
	server, err := newMessage("AgentServerMessage")
	if err != nil {
		return nil, false, err
	}
	if err := proto.Unmarshal(raw, server); err != nil {
		return nil, false, fmt.Errorf("decode Cursor server message for native read-only exec: %w", err)
	}
	active := server.WhichOneof(server.Descriptor().Oneofs().ByName("message"))
	if active == nil || active.Name() != "exec_server_message" {
		return nil, false, nil
	}
	execMessage := server.Get(active).Message()
	operation := execMessage.WhichOneof(execMessage.Descriptor().Oneofs().ByName("message"))
	if operation == nil {
		return nil, false, nil
	}

	var resultName protoreflect.Name
	switch operation.Name() {
	case "grep_args":
		resultName = "grep_result"
	case "read_args":
		resultName = "read_result"
	default:
		return nil, false, nil
	}

	args := execMessage.Get(operation).Message()
	client, err := newMessage("AgentClientMessage")
	if err != nil {
		return nil, true, err
	}
	execReply, err := nestedMessage(client, "exec_client_message")
	if err != nil {
		return nil, true, err
	}
	if err := setUint32(execReply, "id", uint32(execMessage.Get(field(execMessage, "id")).Uint())); err != nil {
		return nil, true, err
	}
	if execID := execMessage.Get(field(execMessage, "exec_id")).String(); execID != "" {
		if err := setString(execReply, "exec_id", execID); err != nil {
			return nil, true, err
		}
	}
	result, err := nestedMessage(execReply, resultName)
	if err != nil {
		return nil, true, err
	}
	failure, err := nestedMessage(result, "error")
	if err != nil {
		return nil, true, err
	}
	toolName := string(operation.Name()[:len(operation.Name())-len("_args")])
	if resultName == "read_result" {
		if err := setString(failure, "path", args.Get(field(args, "path")).String()); err != nil {
			return nil, true, err
		}
	}
	if err := setString(failure, "error", nativeExecPolicy(toolName, tools)); err != nil {
		return nil, true, err
	}
	if err := setMessage(result, "error", failure); err != nil {
		return nil, true, err
	}
	if err := setMessage(execReply, resultName, result); err != nil {
		return nil, true, err
	}
	if err := setMessage(client, "exec_client_message", execReply); err != nil {
		return nil, true, err
	}
	reply, err := proto.Marshal(client)
	if err != nil {
		return nil, true, fmt.Errorf("encode Cursor native %s policy reply: %w", toolName, err)
	}
	return reply, true, nil
}

func ReplyNativeShellExec(raw []byte, tools []ToolDefinition) ([][]byte, bool, error) {
	server, err := newMessage("AgentServerMessage")
	if err != nil {
		return nil, false, err
	}
	if err := proto.Unmarshal(raw, server); err != nil {
		return nil, false, fmt.Errorf("decode Cursor server message for native shell exec: %w", err)
	}
	active := server.WhichOneof(server.Descriptor().Oneofs().ByName("message"))
	if active == nil || active.Name() != "exec_server_message" {
		return nil, false, nil
	}
	execMessage := server.Get(active).Message()
	operation := execMessage.WhichOneof(execMessage.Descriptor().Oneofs().ByName("message"))
	if operation == nil || operation.Name() != "shell_args" && operation.Name() != "shell_stream_args" {
		return nil, false, nil
	}
	args := execMessage.Get(operation).Message()
	policy := nativeExecPolicy("shell", tools)
	failure, err := encodeNativeShellFailure(execMessage, args, policy)
	if err != nil {
		return nil, true, err
	}
	if operation.Name() == "shell_args" {
		return [][]byte{failure}, true, nil
	}

	start, err := encodeNativeShellStreamEvent(execMessage, "start", func(protoreflect.Message) error { return nil })
	if err != nil {
		return nil, true, err
	}
	stderr, err := encodeNativeShellStreamEvent(execMessage, "stderr", func(event protoreflect.Message) error {
		return setString(event, "data", policy)
	})
	if err != nil {
		return nil, true, err
	}
	exit, err := encodeNativeShellStreamEvent(execMessage, "exit", func(event protoreflect.Message) error {
		if err := setUint32(event, "code", 1); err != nil {
			return err
		}
		if err := setString(event, "cwd", args.Get(field(args, "working_directory")).String()); err != nil {
			return err
		}
		return setBool(event, "aborted", true)
	})
	if err != nil {
		return nil, true, err
	}
	closeReply, err := encodeNativeExecStreamClose(execMessage)
	if err != nil {
		return nil, true, err
	}
	return [][]byte{start, stderr, exit, failure, closeReply}, true, nil
}

func encodeNativeShellFailure(execMessage, args protoreflect.Message, policy string) ([]byte, error) {
	result, err := newExecResult(execMessage, "shell_result")
	if err != nil {
		return nil, err
	}
	failure, err := nestedMessage(result, "failure")
	if err != nil {
		return nil, err
	}
	if err := setString(failure, "command", args.Get(field(args, "command")).String()); err != nil {
		return nil, err
	}
	if err := setString(failure, "working_directory", args.Get(field(args, "working_directory")).String()); err != nil {
		return nil, err
	}
	if err := setInt32(failure, "exit_code", 1); err != nil {
		return nil, err
	}
	if err := setString(failure, "stderr", policy); err != nil {
		return nil, err
	}
	if err := setBool(failure, "aborted", true); err != nil {
		return nil, err
	}
	if err := setMessage(result, "failure", failure); err != nil {
		return nil, err
	}
	return encodeNativeExecReply(execMessage, "shell_result", result)
}

func encodeNativeShellStreamEvent(
	execMessage protoreflect.Message,
	eventName protoreflect.Name,
	populate func(protoreflect.Message) error,
) ([]byte, error) {
	stream, err := newExecResult(execMessage, "shell_stream")
	if err != nil {
		return nil, err
	}
	event, err := nestedMessage(stream, eventName)
	if err != nil {
		return nil, err
	}
	if err := populate(event); err != nil {
		return nil, err
	}
	if err := setMessage(stream, eventName, event); err != nil {
		return nil, err
	}
	return encodeNativeExecReply(execMessage, "shell_stream", stream)
}

func newExecResult(execMessage protoreflect.Message, name protoreflect.Name) (protoreflect.Message, error) {
	client, err := newMessage("AgentClientMessage")
	if err != nil {
		return nil, err
	}
	execReply, err := nestedMessage(client, "exec_client_message")
	if err != nil {
		return nil, err
	}
	return nestedMessage(execReply, name)
}

func encodeNativeExecReply(execMessage protoreflect.Message, name protoreflect.Name, result protoreflect.Message) ([]byte, error) {
	client, err := newMessage("AgentClientMessage")
	if err != nil {
		return nil, err
	}
	execReply, err := nestedMessage(client, "exec_client_message")
	if err != nil {
		return nil, err
	}
	if err := setUint32(execReply, "id", uint32(execMessage.Get(field(execMessage, "id")).Uint())); err != nil {
		return nil, err
	}
	if execID := execMessage.Get(field(execMessage, "exec_id")).String(); execID != "" {
		if err := setString(execReply, "exec_id", execID); err != nil {
			return nil, err
		}
	}
	if err := setMessage(execReply, name, result); err != nil {
		return nil, err
	}
	if err := setMessage(client, "exec_client_message", execReply); err != nil {
		return nil, err
	}
	reply, err := proto.Marshal(client)
	if err != nil {
		return nil, fmt.Errorf("encode Cursor native %s reply: %w", name, err)
	}
	return reply, nil
}

func encodeNativeExecStreamClose(execMessage protoreflect.Message) ([]byte, error) {
	client, err := newMessage("AgentClientMessage")
	if err != nil {
		return nil, err
	}
	control, err := nestedMessage(client, "exec_client_control_message")
	if err != nil {
		return nil, err
	}
	closeMessage, err := nestedMessage(control, "stream_close")
	if err != nil {
		return nil, err
	}
	if err := setUint32(closeMessage, "id", uint32(execMessage.Get(field(execMessage, "id")).Uint())); err != nil {
		return nil, err
	}
	if err := setMessage(control, "stream_close", closeMessage); err != nil {
		return nil, err
	}
	if err := setMessage(client, "exec_client_control_message", control); err != nil {
		return nil, err
	}
	reply, err := proto.Marshal(client)
	if err != nil {
		return nil, fmt.Errorf("encode Cursor native exec stream close: %w", err)
	}
	return reply, nil
}

func setBool(message protoreflect.Message, name protoreflect.Name, value bool) error {
	descriptor, err := requireField(message, name)
	if err != nil {
		return err
	}
	if descriptor.Kind() != protoreflect.BoolKind {
		return fmt.Errorf("Cursor field %q is not bool", name)
	}
	message.Set(descriptor, protoreflect.ValueOfBool(value))
	return nil
}
