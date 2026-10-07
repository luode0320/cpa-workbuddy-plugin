package cursorproto

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
)

func ApproveGenerateImageRequest(raw []byte) ([]byte, bool, error) {
	server, err := newMessage("AgentServerMessage")
	if err != nil {
		return nil, false, err
	}
	if err := proto.Unmarshal(raw, server); err != nil {
		return nil, false, fmt.Errorf("decode Cursor server message for image approval: %w", err)
	}
	active := server.WhichOneof(server.Descriptor().Oneofs().ByName("message"))
	if active == nil || active.Name() != "interaction_query" {
		return nil, false, nil
	}
	interaction := server.Get(active).Message()
	requestField := field(interaction, "generate_image_request_query")
	if requestField == nil || !interaction.Has(requestField) {
		return nil, false, nil
	}
	request := interaction.Get(requestField).Message()
	argsField, err := requireField(request, "args")
	if err != nil {
		return nil, true, err
	}
	if !request.Has(argsField) {
		return nil, true, fmt.Errorf("Cursor image generation request requires arguments")
	}
	descriptionField, err := requireField(request.Get(argsField).Message(), "description")
	if err != nil {
		return nil, true, err
	}
	description := request.Get(argsField).Message().Get(descriptionField).String()
	if strings.TrimSpace(description) == "" {
		return nil, true, fmt.Errorf("Cursor image generation request requires a description")
	}

	client, err := newMessage("AgentClientMessage")
	if err != nil {
		return nil, true, err
	}
	response, err := nestedMessage(client, "interaction_response")
	if err != nil {
		return nil, true, err
	}
	idField, err := requireField(interaction, "id")
	if err != nil {
		return nil, true, err
	}
	if err := setUint32(response, "id", uint32(interaction.Get(idField).Uint())); err != nil {
		return nil, true, err
	}
	imageResponse, err := nestedMessage(response, "generate_image_request_response")
	if err != nil {
		return nil, true, err
	}
	approved, err := nestedMessage(imageResponse, "approved")
	if err != nil {
		return nil, true, err
	}
	if err := setString(approved, "description", description); err != nil {
		return nil, true, err
	}
	if err := setMessage(imageResponse, "approved", approved); err != nil {
		return nil, true, err
	}
	if err := setMessage(response, "generate_image_request_response", imageResponse); err != nil {
		return nil, true, err
	}
	if err := setMessage(client, "interaction_response", response); err != nil {
		return nil, true, err
	}
	reply, err := proto.Marshal(client)
	if err != nil {
		return nil, true, fmt.Errorf("encode Cursor image generation approval: %w", err)
	}
	return reply, true, nil
}
