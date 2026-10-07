package openai

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ContentKind string

const (
	ContentText       ContentKind = "text"
	ContentImage      ContentKind = "image"
	ContentAttachment ContentKind = "attachment"
)

type ContentPart struct {
	Kind       ContentKind
	Text       string
	Image      Image
	Attachment Attachment
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type Message struct {
	Role       Role
	Content    []ContentPart
	ToolCalls  []ToolCall
	ToolCallID string
	Name       string
}

type Digest string

type Lineage struct {
	SystemDigest  Digest
	PrefixDigests []Digest
	tail          [sha256.Size]byte
}

type canonicalContent struct {
	Kind       ContentKind `json:"kind"`
	Text       string      `json:"text,omitempty"`
	Name       string      `json:"name,omitempty"`
	MIMEType   string      `json:"mime_type,omitempty"`
	DataDigest string      `json:"data_digest,omitempty"`
}

type canonicalTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  string `json:"parameters"`
}

type canonicalToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type canonicalMessage struct {
	Role       Role                `json:"role"`
	Content    []canonicalContent  `json:"content"`
	ToolCalls  []canonicalToolCall `json:"tool_calls,omitempty"`
	ToolCallID string              `json:"tool_call_id,omitempty"`
	Name       string              `json:"name,omitempty"`
}

func buildLineage(model string, tools []Tool, transcript []Message) Lineage {
	canonicalTools := make([]canonicalTool, len(tools))
	for index, tool := range tools {
		canonicalTools[index] = canonicalTool{
			Name: tool.Name, Description: tool.Description, Parameters: canonicalJSON(tool.Parameters),
		}
	}
	seed, _ := json.Marshal(struct {
		Model string          `json:"model"`
		Tools []canonicalTool `json:"tools"`
	}{Model: model, Tools: canonicalTools})
	prefix := sha256.Sum256(seed)
	systemHash := sha256.New()
	prefixDigests := make([]Digest, 0, len(transcript))
	for _, message := range transcript {
		canonical := canonicalizeMessage(message)
		encoded, _ := json.Marshal(canonical)
		if message.Role == RoleSystem || message.Role == RoleDeveloper {
			systemHash.Write(encoded)
		}
		step := sha256.New()
		step.Write(prefix[:])
		step.Write(encoded)
		copy(prefix[:], step.Sum(nil))
		prefixDigests = append(prefixDigests, Digest(hex.EncodeToString(prefix[:])))
	}
	return Lineage{
		SystemDigest: Digest(hex.EncodeToString(systemHash.Sum(nil))), PrefixDigests: prefixDigests, tail: prefix,
	}
}

func canonicalizeMessage(message Message) canonicalMessage {
	content := make([]canonicalContent, 0, len(message.Content))
	for index := 0; index < len(message.Content); index++ {
		part := message.Content[index]
		switch part.Kind {
		case ContentText:
			texts := []string{part.Text}
			for index+1 < len(message.Content) && message.Content[index+1].Kind == ContentText {
				index++
				texts = append(texts, message.Content[index].Text)
			}
			content = append(content, canonicalContent{Kind: part.Kind, Text: strings.Join(texts, "\n")})
		case ContentImage:
			content = append(content, canonicalContent{
				Kind: part.Kind, Name: part.Image.Name, MIMEType: part.Image.MIMEType, DataDigest: digestBytes(part.Image.Data),
			})
		case ContentAttachment:
			content = append(content, canonicalContent{
				Kind: part.Kind, Name: part.Attachment.Name, DataDigest: digestBytes([]byte(part.Attachment.Content)),
			})
		}
	}
	toolCalls := make([]canonicalToolCall, len(message.ToolCalls))
	for index, call := range message.ToolCalls {
		toolCalls[index] = canonicalToolCall{ID: call.ID, Name: call.Name, Arguments: canonicalJSON([]byte(call.Arguments))}
	}
	return canonicalMessage{
		Role: message.Role, Content: content, ToolCalls: toolCalls, ToolCallID: message.ToolCallID, Name: message.Name,
	}
}

func canonicalJSON(raw []byte) string {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if len(raw) == 0 || decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return string(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
