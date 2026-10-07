package cursorproto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"
)

func Test_EncodeRunRequest_places_model_and_prompt_in_wire_message(t *testing.T) {
	// Given
	request := RunRequest{
		ConversationID: "cursor_conversation",
		MessageID:      "message-1",
		Model:          "auto",
		System:         "Answer briefly.",
		Prompt:         "Reply with cursor-plugin-ok",
		TimeZone:       "Asia/Shanghai",
		WorkspacePaths: []string{"/workspace"},
		ProjectFolder:  "/root/.cursor/projects/workspace",
	}

	// When
	raw, err := EncodeRunRequest(request)

	// Then
	require.NoError(t, err)
	message, err := newMessage("AgentClientMessage")
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(raw, message))
	run := message.Get(field(message, "run_request")).Message()
	require.Equal(t, "cursor_conversation", run.Get(field(run, "conversation_id")).String())
	require.False(t, run.Has(field(run, "custom_system_prompt")))
	model := run.Get(field(run, "model_details")).Message()
	require.Equal(t, "auto", model.Get(field(model, "model_id")).String())
	action := run.Get(field(run, "action")).Message()
	userAction := action.Get(field(action, "user_message_action")).Message()
	userMessage := userAction.Get(field(userAction, "user_message")).Message()
	require.Equal(t, "Answer briefly.\n\nReply with cursor-plugin-ok", userMessage.Get(field(userMessage, "text")).String())
	requestContext := userAction.Get(field(userAction, "request_context")).Message()
	environment := requestContext.Get(field(requestContext, "env")).Message()
	require.Equal(t, "Asia/Shanghai", environment.Get(field(environment, "time_zone")).String())
	workspacePaths := environment.Get(field(environment, "workspace_paths")).List()
	require.Equal(t, 1, workspacePaths.Len())
	require.Equal(t, "/workspace", workspacePaths.Get(0).String())
	require.Equal(t, "/root/.cursor/projects/workspace", environment.Get(field(environment, "project_folder")).String())
}

func Test_EncodeRunRequest_registers_tools_images_and_file_attachments(t *testing.T) {
	request := RunRequest{
		ConversationID: "cursor_conversation",
		MessageID:      "message-1",
		Model:          "auto",
		Prompt:         "Inspect the attachments",
		Tools: []ToolDefinition{{
			Name:        "read_file",
			Description: "Read a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
		Images:      []ImageAttachment{{Name: "image.png", MIMEType: "image/png", Data: []byte("image")}},
		Attachments: []FileAttachment{{Name: "notes.txt", Content: "notes"}},
	}

	raw, err := EncodeRunRequest(request)

	require.NoError(t, err)
	message, err := newMessage("AgentClientMessage")
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(raw, message))
	run := message.Get(field(message, "run_request")).Message()
	mcpTools := run.Get(field(run, "mcp_tools")).Message().Get(field(run.Get(field(run, "mcp_tools")).Message(), "mcp_tools")).List()
	require.Equal(t, 1, mcpTools.Len())
	tool := mcpTools.Get(0).Message()
	require.Equal(t, "read_file", tool.Get(field(tool, "tool_name")).String())
	require.NotEmpty(t, tool.Get(field(tool, "input_schema")).Bytes())

	action := run.Get(field(run, "action")).Message()
	userAction := action.Get(field(action, "user_message_action")).Message()
	userMessage := userAction.Get(field(userAction, "user_message")).Message()
	selected := userMessage.Get(field(userMessage, "selected_context")).Message()
	require.Equal(t, 1, selected.Get(field(selected, "selected_images")).List().Len())
	image := selected.Get(field(selected, "selected_images")).List().Get(0).Message()
	require.Equal(t, "image/png", image.Get(field(image, "mime_type")).String())
	require.Equal(t, []byte("image"), image.Get(field(image, "data")).Bytes())
	require.Equal(t, 1, selected.Get(field(selected, "files")).List().Len())
	file := selected.Get(field(selected, "files")).List().Get(0).Message()
	require.Equal(t, "notes.txt", file.Get(field(file, "path")).String())
	require.Equal(t, "notes", file.Get(field(file, "content")).String())
}

func Test_DecodeServerEvent_maps_text_and_turn_end(t *testing.T) {
	// Given
	textWire, err := encodeTestInteractionUpdate("text_delta", "text", "hello")
	require.NoError(t, err)
	endWire, err := encodeTestInteractionUpdate("turn_ended", "", "")
	require.NoError(t, err)

	// When
	textEvent, err := DecodeServerEvent(textWire)
	require.NoError(t, err)
	endEvent, err := DecodeServerEvent(endWire)

	// Then
	require.NoError(t, err)
	require.Equal(t, EventText, textEvent.Kind)
	require.Equal(t, "hello", textEvent.Text)
	require.Equal(t, EventDone, endEvent.Kind)
}

func Test_DecodeServerEvent_names_ignored_top_level_message(t *testing.T) {
	// Given
	server, err := newMessage("AgentServerMessage")
	require.NoError(t, err)
	kv, err := nestedMessage(server, "kv_server_message")
	require.NoError(t, err)
	require.NoError(t, setMessage(server, "kv_server_message", kv))
	raw, err := proto.Marshal(server)
	require.NoError(t, err)

	// When
	event, err := DecodeServerEvent(raw)

	// Then
	require.NoError(t, err)
	require.Equal(t, EventIgnored, event.Kind)
	require.Equal(t, "kv_server_message", event.Type)
}

func Test_DecodeServerEvent_names_ignored_interaction_update(t *testing.T) {
	server, err := newMessage("AgentServerMessage")
	require.NoError(t, err)
	interaction, err := nestedMessage(server, "interaction_update")
	require.NoError(t, err)
	started, err := nestedMessage(interaction, "tool_call_started")
	require.NoError(t, err)
	require.NoError(t, setMessage(interaction, "tool_call_started", started))
	require.NoError(t, setMessage(server, "interaction_update", interaction))
	raw, err := proto.Marshal(server)
	require.NoError(t, err)

	event, err := DecodeServerEvent(raw)

	require.NoError(t, err)
	require.Equal(t, EventIgnored, event.Kind)
	require.Equal(t, "interaction_update.tool_call_started", event.Type)
}

func Test_DecodeServerEvent_maps_completed_mcp_tool_call(t *testing.T) {
	server, err := newMessage("AgentServerMessage")
	require.NoError(t, err)
	interaction, err := nestedMessage(server, "interaction_update")
	require.NoError(t, err)
	completed, err := nestedMessage(interaction, "tool_call_completed")
	require.NoError(t, err)
	require.NoError(t, setString(completed, "call_id", "call_1"))
	toolCall, err := nestedMessage(completed, "tool_call")
	require.NoError(t, err)
	mcpCall, err := nestedMessage(toolCall, "mcp_tool_call")
	require.NoError(t, err)
	args, err := nestedMessage(mcpCall, "args")
	require.NoError(t, err)
	require.NoError(t, setString(args, "provider_identifier", "opencodex-responses"))
	require.NoError(t, setString(args, "tool_name", "read_file"))
	require.NoError(t, setString(args, "tool_call_id", "call_1"))
	argsMap := args.Mutable(field(args, "args")).Map()
	argsMap.Set(protoreflect.ValueOfString("path").MapKey(), protoreflect.ValueOfBytes([]byte(`"a.txt"`)))
	require.NoError(t, setMessage(mcpCall, "args", args))
	require.NoError(t, setMessage(toolCall, "mcp_tool_call", mcpCall))
	require.NoError(t, setMessage(completed, "tool_call", toolCall))
	require.NoError(t, setMessage(interaction, "tool_call_completed", completed))
	require.NoError(t, setMessage(server, "interaction_update", interaction))
	raw, err := proto.Marshal(server)
	require.NoError(t, err)

	event, err := DecodeServerEvent(raw)

	require.NoError(t, err)
	require.Equal(t, EventToolCall, event.Kind)
	require.Equal(t, "call_1", event.ID)
	require.Equal(t, "read_file", event.Name)
	require.JSONEq(t, `{"path":"a.txt"}`, event.Arguments)
}

func Test_DecodeServerEvent_maps_exec_mcp_args_to_tool_call(t *testing.T) {
	server, err := newMessage("AgentServerMessage")
	require.NoError(t, err)
	execMessage, err := nestedMessage(server, "exec_server_message")
	require.NoError(t, err)
	execMessage.Set(field(execMessage, "id"), protoreflect.ValueOfUint32(7))
	args, err := nestedMessage(execMessage, "mcp_args")
	require.NoError(t, err)
	require.NoError(t, setString(args, "provider_identifier", "opencodex-responses"))
	require.NoError(t, setString(args, "tool_name", "read_file"))
	require.NoError(t, setString(args, "tool_call_id", "call_exec_1"))
	pathValue, err := proto.Marshal(structpb.NewStringValue("probe.txt"))
	require.NoError(t, err)
	argsMap := args.Mutable(field(args, "args")).Map()
	argsMap.Set(protoreflect.ValueOfString("path").MapKey(), protoreflect.ValueOfBytes(pathValue))
	require.NoError(t, setMessage(execMessage, "mcp_args", args))
	require.NoError(t, setMessage(server, "exec_server_message", execMessage))
	raw, err := proto.Marshal(server)
	require.NoError(t, err)

	event, err := DecodeServerEvent(raw)

	require.NoError(t, err)
	require.Equal(t, EventToolCall, event.Kind)
	require.Equal(t, "call_exec_1", event.ID)
	require.Equal(t, "read_file", event.Name)
	require.JSONEq(t, `{"path":"probe.txt"}`, event.Arguments)
}

func encodeTestInteractionUpdate(updateName, valueName, value string) ([]byte, error) {
	server, err := newMessage("AgentServerMessage")
	if err != nil {
		return nil, err
	}
	interaction, err := nestedMessage(server, "interaction_update")
	if err != nil {
		return nil, err
	}
	updateField := protoreflect.Name(updateName)
	update, err := nestedMessage(interaction, updateField)
	if err != nil {
		return nil, err
	}
	if valueName != "" {
		if err := setString(update, protoreflect.Name(valueName), value); err != nil {
			return nil, err
		}
	}
	if err := setMessage(interaction, updateField, update); err != nil {
		return nil, err
	}
	if err := setMessage(server, "interaction_update", interaction); err != nil {
		return nil, err
	}
	return proto.Marshal(server)
}
