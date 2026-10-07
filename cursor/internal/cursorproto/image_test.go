package cursorproto

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func Test_AgentDescriptor_includes_generate_image_interaction_contract(t *testing.T) {
	file, err := loadAgentFile()

	require.NoError(t, err)
	args := file.Messages().ByName("GenerateImageArgs")
	require.NotNil(t, args)
	require.EqualValues(t, 6, args.Fields().ByName("aspect_ratio").Number())
	query := file.Messages().ByName("InteractionQuery")
	require.NotNil(t, query)
	require.EqualValues(t, 12, query.Fields().ByName("generate_image_request_query").Number())
	response := file.Messages().ByName("InteractionResponse")
	require.NotNil(t, response)
	require.EqualValues(t, 12, response.Fields().ByName("generate_image_request_response").Number())
}

func Test_ApproveGenerateImageRequest_mirrors_query_id_and_description(t *testing.T) {
	raw := encodeGenerateImageQuery(t, 42, "Draw a blue fox", "tool-image-1")

	reply, handled, err := ApproveGenerateImageRequest(raw)

	require.NoError(t, err)
	require.True(t, handled)
	client, err := newMessage("AgentClientMessage")
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(reply, client))
	interaction := client.Get(field(client, "interaction_response")).Message()
	require.EqualValues(t, 42, interaction.Get(field(interaction, "id")).Uint())
	response := interaction.Get(field(interaction, "generate_image_request_response")).Message()
	approved := response.Get(field(response, "approved")).Message()
	require.Equal(t, "Draw a blue fox", approved.Get(field(approved, "description")).String())
}

func Test_DecodeServerEvent_names_generate_image_query(t *testing.T) {
	raw := encodeGenerateImageQuery(t, 42, "Draw a blue fox", "tool-image-1")

	event, err := DecodeServerEvent(raw)

	require.NoError(t, err)
	require.Equal(t, EventIgnored, event.Kind)
	require.Equal(t, "interaction_query.generate_image_request_query", event.Type)
}

func Test_ApproveGenerateImageRequest_ignores_other_server_messages(t *testing.T) {
	raw, err := encodeTestInteractionUpdate("text_delta", "text", "hello")
	require.NoError(t, err)

	reply, handled, err := ApproveGenerateImageRequest(raw)

	require.NoError(t, err)
	require.False(t, handled)
	require.Nil(t, reply)
}

func Test_ApproveGenerateImageRequest_rejects_blank_description(t *testing.T) {
	raw := encodeGenerateImageQuery(t, 42, "   ", "tool-image-1")

	reply, handled, err := ApproveGenerateImageRequest(raw)

	require.ErrorContains(t, err, "requires a description")
	require.True(t, handled)
	require.Nil(t, reply)
}

func Test_DecodeServerEvent_maps_completed_generated_image(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nimage-payload")
	raw := encodeGenerateImageCompletion(t, png, "generated.png", "")

	event, err := DecodeServerEvent(raw)

	require.NoError(t, err)
	require.Equal(t, EventImage, event.Kind)
	require.Equal(t, "tool-image-1", event.ID)
	require.Equal(t, "image/png", event.MIMEType)
	require.Equal(t, png, event.ImageData)
	require.Equal(t, "generated.png", event.Path)
}

func Test_DecodeServerEvent_reports_generate_image_failure(t *testing.T) {
	raw := encodeGenerateImageCompletion(t, nil, "", "image quota exhausted")

	_, err := DecodeServerEvent(raw)

	require.ErrorContains(t, err, "image quota exhausted")
}

func Test_DecodeServerEvent_rejects_invalid_generated_image_data(t *testing.T) {
	for _, test := range []struct {
		name    string
		encoded string
		path    string
		message string
	}{
		{name: "missing", message: "no image data"},
		{name: "invalid base64", encoded: "not-base64", path: "generated.png", message: "decode Cursor generated image"},
		{name: "non-image", encoded: base64.StdEncoding.EncodeToString([]byte("plain text")), path: "generated.bin", message: "non-image data"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := encodeGenerateImageCompletionValue(t, test.encoded, test.path, "")

			_, err := DecodeServerEvent(raw)

			require.ErrorContains(t, err, test.message)
		})
	}
}

func encodeGenerateImageQuery(t *testing.T, id uint32, description, toolCallID string) []byte {
	t.Helper()
	server, err := newMessage("AgentServerMessage")
	require.NoError(t, err)
	interaction, err := nestedMessage(server, "interaction_query")
	require.NoError(t, err)
	require.NoError(t, setUint32(interaction, "id", id))
	query, err := nestedMessage(interaction, "generate_image_request_query")
	require.NoError(t, err)
	args, err := nestedMessage(query, "args")
	require.NoError(t, err)
	require.NoError(t, setString(args, "description", description))
	require.NoError(t, setMessage(query, "args", args))
	require.NoError(t, setString(query, "tool_call_id", toolCallID))
	require.NoError(t, setMessage(interaction, "generate_image_request_query", query))
	require.NoError(t, setMessage(server, "interaction_query", interaction))
	raw, err := proto.Marshal(server)
	require.NoError(t, err)
	return raw
}

func encodeGenerateImageCompletion(t *testing.T, image []byte, path, failure string) []byte {
	t.Helper()
	return encodeGenerateImageCompletionValue(t, base64.StdEncoding.EncodeToString(image), path, failure)
}

func encodeGenerateImageCompletionValue(t *testing.T, encoded, path, failure string) []byte {
	t.Helper()
	server, err := newMessage("AgentServerMessage")
	require.NoError(t, err)
	interaction, err := nestedMessage(server, "interaction_update")
	require.NoError(t, err)
	completed, err := nestedMessage(interaction, "tool_call_completed")
	require.NoError(t, err)
	require.NoError(t, setString(completed, "call_id", "tool-image-1"))
	toolCall, err := nestedMessage(completed, "tool_call")
	require.NoError(t, err)
	generateImage, err := nestedMessage(toolCall, "generate_image_tool_call")
	require.NoError(t, err)
	result, err := nestedMessage(generateImage, "result")
	require.NoError(t, err)
	if failure == "" {
		success, successErr := nestedMessage(result, "success")
		require.NoError(t, successErr)
		require.NoError(t, setString(success, "file_path", path))
		require.NoError(t, setString(success, "image_data", encoded))
		require.NoError(t, setMessage(result, "success", success))
	} else {
		failureMessage, failureErr := nestedMessage(result, "error")
		require.NoError(t, failureErr)
		require.NoError(t, setString(failureMessage, "error", failure))
		require.NoError(t, setMessage(result, "error", failureMessage))
	}
	require.NoError(t, setMessage(generateImage, "result", result))
	require.NoError(t, setMessage(toolCall, "generate_image_tool_call", generateImage))
	require.NoError(t, setMessage(completed, "tool_call", toolCall))
	require.NoError(t, setMessage(interaction, "tool_call_completed", completed))
	require.NoError(t, setMessage(server, "interaction_update", interaction))
	raw, err := proto.Marshal(server)
	require.NoError(t, err)
	return raw
}
