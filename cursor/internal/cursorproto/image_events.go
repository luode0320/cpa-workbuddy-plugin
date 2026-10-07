package cursorproto

import (
	"encoding/base64"
	"fmt"
	"google.golang.org/protobuf/reflect/protoreflect"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

func completedImageToolCallEvent(completed, imageCall protoreflect.Message) (ServerEvent, error) {
	callIDField, err := requireField(completed, "call_id")
	if err != nil {
		return ServerEvent{}, err
	}
	callID := completed.Get(callIDField).String()
	if callID == "" {
		return ServerEvent{}, fmt.Errorf("Cursor completed image generation requires a call id")
	}
	resultField, err := requireField(imageCall, "result")
	if err != nil {
		return ServerEvent{}, err
	}
	if !imageCall.Has(resultField) {
		return ServerEvent{}, fmt.Errorf("Cursor image generation %q completed without a result", callID)
	}
	result := imageCall.Get(resultField).Message()
	active := result.WhichOneof(result.Descriptor().Oneofs().ByName("result"))
	if active == nil {
		return ServerEvent{}, fmt.Errorf("Cursor image generation %q returned an empty result", callID)
	}
	if active.Name() == "error" {
		failure := result.Get(active).Message()
		errorField, fieldErr := requireField(failure, "error")
		if fieldErr != nil {
			return ServerEvent{}, fieldErr
		}
		message := strings.TrimSpace(failure.Get(errorField).String())
		if message == "" {
			message = "unknown upstream error"
		}
		return ServerEvent{}, fmt.Errorf("Cursor image generation failed: %s", message)
	}
	if active.Name() != "success" {
		return ServerEvent{}, fmt.Errorf("Cursor image generation %q returned unsupported result %q", callID, active.Name())
	}
	success := result.Get(active).Message()
	dataField, err := requireField(success, "image_data")
	if err != nil {
		return ServerEvent{}, err
	}
	encoded := strings.TrimSpace(success.Get(dataField).String())
	if encoded == "" {
		return ServerEvent{}, fmt.Errorf("Cursor image generation %q returned no image data", callID)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ServerEvent{}, fmt.Errorf("decode Cursor generated image %q: %w", callID, err)
	}
	pathField, err := requireField(success, "file_path")
	if err != nil {
		return ServerEvent{}, err
	}
	path := success.Get(pathField).String()
	mimeType := generatedImageMIME(data, path)
	if !strings.HasPrefix(mimeType, "image/") {
		return ServerEvent{}, fmt.Errorf("Cursor image generation %q returned non-image data (%s)", callID, mimeType)
	}
	return ServerEvent{
		Kind: EventImage, Type: "tool_call_completed.generate_image_tool_call", ID: callID,
		MIMEType: mimeType, ImageData: data, Path: path,
	}, nil
}

func generatedImageMIME(data []byte, path string) string {
	detected := http.DetectContentType(data)
	if detected != "application/octet-stream" {
		if mediaType, _, err := mime.ParseMediaType(detected); err == nil {
			return mediaType
		}
		return detected
	}
	if extensionType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); extensionType != "" {
		if mediaType, _, err := mime.ParseMediaType(extensionType); err == nil {
			return mediaType
		}
		return extensionType
	}
	return detected
}
