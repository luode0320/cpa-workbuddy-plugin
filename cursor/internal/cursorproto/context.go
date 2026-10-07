package cursorproto

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"
)

const toolProvider = "opencodex-responses"

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type ImageAttachment struct {
	Name     string
	MIMEType string
	Data     []byte
}

type FileAttachment struct {
	Name    string
	Content string
}

func addTools(run protoreflect.Message, tools []ToolDefinition) error {
	if len(tools) == 0 {
		return nil
	}
	catalog, err := nestedMessage(run, "mcp_tools")
	if err != nil {
		return err
	}
	for _, tool := range tools {
		if tool.Name == "" {
			return fmt.Errorf("Cursor tool name is required")
		}
		definition, err := nestedMessage(catalog, "mcp_tools")
		if err != nil {
			return err
		}
		for fieldName, value := range map[protoreflect.Name]string{
			"name": tool.Name, "tool_name": tool.Name, "provider_identifier": toolProvider, "description": tool.Description,
		} {
			if err := setString(definition, fieldName, value); err != nil {
				return err
			}
		}
		schema, err := encodeSchema(tool.Parameters)
		if err != nil {
			return fmt.Errorf("encode Cursor tool %q schema: %w", tool.Name, err)
		}
		if err := setBytes(definition, "input_schema", schema); err != nil {
			return err
		}
		if err := appendMessage(catalog, "mcp_tools", definition); err != nil {
			return err
		}
	}
	return setMessage(run, "mcp_tools", catalog)
}

func encodeSchema(raw json.RawMessage) ([]byte, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	schema, err := structpb.NewValue(value)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(schema)
}

func addSelectedContext(userMessage protoreflect.Message, images []ImageAttachment, attachments []FileAttachment) error {
	if len(images) == 0 && len(attachments) == 0 {
		return nil
	}
	selected, err := nestedMessage(userMessage, "selected_context")
	if err != nil {
		return err
	}
	if err := addSelectedImages(selected, images); err != nil {
		return err
	}
	if err := addSelectedFiles(selected, attachments); err != nil {
		return err
	}
	return setMessage(userMessage, "selected_context", selected)
}

func addSelectedImages(selected protoreflect.Message, images []ImageAttachment) error {
	for index, image := range images {
		selectedImage, err := nestedMessage(selected, "selected_images")
		if err != nil {
			return err
		}
		name := image.Name
		if name == "" {
			name = fmt.Sprintf("image-%d", index+1)
		}
		for fieldName, value := range map[protoreflect.Name]string{
			"uuid": fmt.Sprintf("%s-%d", name, index+1), "path": name, "mime_type": image.MIMEType,
		} {
			if err := setString(selectedImage, fieldName, value); err != nil {
				return err
			}
		}
		if err := setBytes(selectedImage, "data", image.Data); err != nil {
			return err
		}
		if err := appendMessage(selected, "selected_images", selectedImage); err != nil {
			return err
		}
	}
	return nil
}

func addSelectedFiles(selected protoreflect.Message, attachments []FileAttachment) error {
	for _, attachment := range attachments {
		selectedFile, err := nestedMessage(selected, "files")
		if err != nil {
			return err
		}
		for fieldName, value := range map[protoreflect.Name]string{
			"content": attachment.Content, "path": attachment.Name, "relative_path": attachment.Name,
		} {
			if err := setString(selectedFile, fieldName, value); err != nil {
				return err
			}
		}
		if err := appendMessage(selected, "files", selectedFile); err != nil {
			return err
		}
	}
	return nil
}
