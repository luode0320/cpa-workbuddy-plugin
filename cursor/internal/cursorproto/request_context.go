package cursorproto

import (
	"fmt"

	"google.golang.org/protobuf/proto"
)

func ReplyRequestContext(raw []byte, environment RequestEnvironment) ([]byte, bool, error) {
	server, err := newMessage("AgentServerMessage")
	if err != nil {
		return nil, false, err
	}
	if err := proto.Unmarshal(raw, server); err != nil {
		return nil, false, fmt.Errorf("decode Cursor server message for request context: %w", err)
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
	switch operation.Name() {
	case "mcp_state_exec_args":
		reply, err := replyMCPState(execMessage, environment.Tools)
		return reply, true, err
	case "request_context_args":
	default:
		return nil, false, nil
	}

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
	result, err := nestedMessage(execReply, "request_context_result")
	if err != nil {
		return nil, true, err
	}
	success, err := nestedMessage(result, "success")
	if err != nil {
		return nil, true, err
	}
	requestContext, err := nestedMessage(success, "request_context")
	if err != nil {
		return nil, true, err
	}
	if err := populateRequestContext(requestContext, environment); err != nil {
		return nil, true, err
	}
	if err := setMessage(success, "request_context", requestContext); err != nil {
		return nil, true, err
	}
	if err := setMessage(result, "success", success); err != nil {
		return nil, true, err
	}
	if err := setMessage(execReply, "request_context_result", result); err != nil {
		return nil, true, err
	}
	if err := setMessage(client, "exec_client_message", execReply); err != nil {
		return nil, true, err
	}
	reply, err := proto.Marshal(client)
	if err != nil {
		return nil, true, fmt.Errorf("encode Cursor request context reply: %w", err)
	}
	return reply, true, nil
}
