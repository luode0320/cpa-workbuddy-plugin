package cursorproto

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var ErrUnsupportedInteraction = errors.New("Cursor interaction is unsupported by this gateway")

const interactionRefusal = "This gateway cannot authorize this native interaction. Use the client-provided tools or return the request to the user; do not repeat this interaction."

func ReplyInteractionQuery(raw []byte) ([]byte, bool, error) {
	server, err := newMessage("AgentServerMessage")
	if err != nil {
		return nil, false, err
	}
	if err := proto.Unmarshal(raw, server); err != nil {
		return nil, false, fmt.Errorf("decode Cursor interaction query: %w", err)
	}
	active := server.WhichOneof(server.Descriptor().Oneofs().ByName("message"))
	if active == nil || active.Name() != "interaction_query" {
		return nil, false, nil
	}
	query := server.Get(active).Message()
	selected := query.WhichOneof(query.Descriptor().Oneofs().ByName("query"))
	if selected == nil {
		return nil, false, nil
	}
	var responsePath []protoreflect.Name
	textField := protoreflect.Name("reason")
	switch selected.Name() {
	case "generate_image_request_query":
		return ApproveGenerateImageRequest(raw)
	case "web_search_request_query":
		responsePath = []protoreflect.Name{"web_search_request_response", "rejected"}
	case "exa_search_request_query":
		responsePath = []protoreflect.Name{"exa_search_request_response", "rejected"}
	case "exa_fetch_request_query":
		responsePath = []protoreflect.Name{"exa_fetch_request_response", "rejected"}
	case "switch_mode_request_query":
		responsePath = []protoreflect.Name{"switch_mode_request_response", "rejected"}
	case "ask_question_interaction_query":
		responsePath = []protoreflect.Name{"ask_question_interaction_response", "result", "rejected"}
	case "create_plan_request_query":
		responsePath = []protoreflect.Name{"create_plan_request_response", "result", "error"}
		textField = "error"
	case "setup_vm_environment_args":
		return nil, true, fmt.Errorf("%w: setup_vm_environment_args", ErrUnsupportedInteraction)
	default:
		return nil, false, nil
	}
	client, err := newMessage("AgentClientMessage")
	if err != nil {
		return nil, true, err
	}
	response, err := nestedMessage(client, "interaction_response")
	if err != nil {
		return nil, true, err
	}
	if err := setUint32(response, "id", uint32(query.Get(field(query, "id")).Uint())); err != nil {
		return nil, true, err
	}
	var leaf protoreflect.Message = response
	for _, name := range responsePath {
		descriptor, err := requireField(leaf, name)
		if err != nil {
			return nil, true, err
		}
		leaf = leaf.Mutable(descriptor).Message()
	}
	if err := setString(leaf, textField, interactionRefusal); err != nil {
		return nil, true, err
	}
	if err := setMessage(client, "interaction_response", response); err != nil {
		return nil, true, err
	}
	reply, err := proto.Marshal(client)
	if err != nil {
		return nil, true, fmt.Errorf("encode Cursor interaction refusal: %w", err)
	}
	return reply, true, nil
}
