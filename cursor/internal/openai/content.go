package openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxAttachmentBytes = 16 << 20

type decodedContent struct {
	Text        string
	Images      []Image
	Attachments []Attachment
	Parts       []ContentPart
}

type wireContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	ImageURL json.RawMessage `json:"image_url"`
	File     *wireFile       `json:"file"`
	Filename string          `json:"filename"`
	FileData string          `json:"file_data"`
	FileID   string          `json:"file_id"`
}

type wireFile struct {
	Filename string `json:"filename"`
	FileData string `json:"file_data"`
	FileID   string `json:"file_id"`
}

func decodeMessageContent(raw json.RawMessage, allowEmpty bool) (decodedContent, error) {
	if len(raw) == 0 || string(raw) == "null" {
		if allowEmpty {
			return decodedContent{}, nil
		}
		return decodedContent{}, invalidRequest("OpenAI message content is empty")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" && !allowEmpty {
			return decodedContent{}, invalidRequest("OpenAI message content is empty")
		}
		return decodedContent{Text: text, Parts: []ContentPart{{Kind: ContentText, Text: text}}}, nil
	}
	var parts []wireContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return decodedContent{}, invalidRequest("OpenAI message content must be text or content parts")
	}
	result := decodedContent{}
	texts := make([]string, 0, len(parts))
	for index, part := range parts {
		switch part.Type {
		case "text", "input_text":
			if part.Text != "" {
				texts = append(texts, part.Text)
				result.Parts = append(result.Parts, ContentPart{Kind: ContentText, Text: part.Text})
			}
		case "image_url", "input_image":
			image, err := decodeImagePart(part, index)
			if err != nil {
				return decodedContent{}, err
			}
			result.Images = append(result.Images, image)
			result.Parts = append(result.Parts, ContentPart{Kind: ContentImage, Image: image})
		case "file", "input_file":
			image, attachment, err := decodeFilePart(part)
			if err != nil {
				return decodedContent{}, err
			}
			if image != nil {
				result.Images = append(result.Images, *image)
				result.Parts = append(result.Parts, ContentPart{Kind: ContentImage, Image: *image})
			}
			if attachment != nil {
				result.Attachments = append(result.Attachments, *attachment)
				result.Parts = append(result.Parts, ContentPart{Kind: ContentAttachment, Attachment: *attachment})
			}
		default:
			return decodedContent{}, invalidRequest(fmt.Sprintf("unsupported OpenAI content part %q", part.Type))
		}
	}
	result.Text = strings.Join(texts, "\n")
	if result.Text == "" && len(result.Images) == 0 && len(result.Attachments) == 0 && !allowEmpty {
		return decodedContent{}, invalidRequest("OpenAI message content is empty")
	}
	return result, nil
}

func (content decodedContent) promptText() string {
	parts := make([]string, 0, len(content.Parts))
	for _, part := range content.Parts {
		switch part.Kind {
		case ContentText:
			parts = append(parts, part.Text)
		case ContentImage:
			parts = append(parts, "[Attached image: "+part.Image.Name+"]")
		case ContentAttachment:
			parts = append(parts, "[Attached file: "+part.Attachment.Name+"]")
		}
	}
	return strings.Join(parts, "\n")
}

func decodeImagePart(part wireContentPart, index int) (Image, error) {
	var source string
	if err := json.Unmarshal(part.ImageURL, &source); err != nil {
		var object struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(part.ImageURL, &object); err != nil {
			return Image{}, invalidRequest("image_url must contain a URL")
		}
		source = object.URL
	}
	mimeType, data, err := decodeDataURL(source)
	if err != nil {
		return Image{}, err
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return Image{}, invalidRequest("image_url data must use an image MIME type")
	}
	return Image{Name: fmt.Sprintf("image-%d%s", index+1, extensionForMIME(mimeType)), MIMEType: mimeType, Data: data}, nil
}

func decodeFilePart(part wireContentPart) (*Image, *Attachment, error) {
	file := wireFile{Filename: part.Filename, FileData: part.FileData, FileID: part.FileID}
	if part.File != nil {
		file = *part.File
	}
	if file.FileData == "" {
		if file.FileID != "" {
			return nil, nil, invalidRequest("file_id attachments cannot be resolved by the Cursor plugin; provide file_data")
		}
		return nil, nil, invalidRequest("file attachment requires file_data")
	}
	mimeType, data, err := decodeFileData(file.FileData)
	if err != nil {
		return nil, nil, err
	}
	name := filepath.Base(strings.TrimSpace(file.Filename))
	if name == "." || name == "" {
		name = "attachment" + extensionForMIME(mimeType)
	}
	if strings.HasPrefix(mimeType, "image/") {
		return &Image{Name: name, MIMEType: mimeType, Data: data}, nil, nil
	}
	if !utf8.Valid(data) {
		return nil, nil, invalidRequest(fmt.Sprintf("binary file attachment %q is unsupported", name))
	}
	return nil, &Attachment{Name: name, Content: string(data)}, nil
}

func decodeFileData(source string) (string, []byte, error) {
	if strings.HasPrefix(source, "data:") {
		return decodeDataURL(source)
	}
	data, err := base64.StdEncoding.DecodeString(source)
	if err != nil {
		return "", nil, invalidRequest("file_data must be base64 or a data URL")
	}
	if len(data) > maxAttachmentBytes {
		return "", nil, invalidRequest("attachment exceeds 16 MiB")
	}
	return "application/octet-stream", data, nil
}

func decodeDataURL(source string) (string, []byte, error) {
	if !strings.HasPrefix(source, "data:") {
		return "", nil, invalidRequest("Cursor image attachments require an inline data URL")
	}
	parts := strings.SplitN(strings.TrimPrefix(source, "data:"), ",", 2)
	if len(parts) != 2 {
		return "", nil, invalidRequest("invalid attachment data URL")
	}
	metadata := strings.Split(parts[0], ";")
	mimeType := metadata[0]
	if mimeType == "" {
		mimeType = "text/plain"
	}
	var data []byte
	var err error
	if metadata[len(metadata)-1] == "base64" {
		data, err = base64.StdEncoding.DecodeString(parts[1])
	} else {
		var decoded string
		decoded, err = url.PathUnescape(parts[1])
		data = []byte(decoded)
	}
	if err != nil {
		return "", nil, invalidRequest("invalid attachment data encoding")
	}
	if len(data) > maxAttachmentBytes {
		return "", nil, invalidRequest("attachment exceeds 16 MiB")
	}
	return mimeType, data, nil
}

func extensionForMIME(mimeType string) string {
	extensions, err := mime.ExtensionsByType(mimeType)
	if err == nil && len(extensions) > 0 {
		return extensions[0]
	}
	return ""
}
