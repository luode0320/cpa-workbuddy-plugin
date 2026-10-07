package cursorproto

import "google.golang.org/protobuf/reflect/protoreflect"

func replyMCPState(execMessage protoreflect.Message, tools []ToolDefinition) ([]byte, error) {
	result, err := newExecResult(execMessage, "mcp_state_exec_result")
	if err != nil {
		return nil, err
	}
	success, err := nestedMessage(result, "success")
	if err != nil {
		return nil, err
	}
	args := execMessage.Get(field(execMessage, "mcp_state_exec_args")).Message()
	identifiers := args.Get(field(args, "server_identifiers")).List()
	requested := identifiers.Len() == 0
	for index := range identifiers.Len() {
		requested = requested || identifiers.Get(index).String() == toolProvider
	}
	if requested && len(tools) > 0 {
		run, err := newMessage("AgentRunRequest")
		if err != nil {
			return nil, err
		}
		if err := addTools(run, tools); err != nil {
			return nil, err
		}
		server, err := nestedMessage(success, "servers")
		if err != nil {
			return nil, err
		}
		if err := setString(server, "server_name", toolProvider); err != nil {
			return nil, err
		}
		if err := setString(server, "server_identifier", toolProvider); err != nil {
			return nil, err
		}
		catalog := run.Get(field(run, "mcp_tools")).Message()
		definitions := catalog.Get(field(catalog, "mcp_tools")).List()
		for index := range definitions.Len() {
			if err := appendMessage(server, "tools", definitions.Get(index).Message()); err != nil {
				return nil, err
			}
		}
		if err := appendMessage(success, "servers", server); err != nil {
			return nil, err
		}
	}
	if err := setMessage(result, "success", success); err != nil {
		return nil, err
	}
	return encodeNativeExecReply(execMessage, "mcp_state_exec_result", result)
}
