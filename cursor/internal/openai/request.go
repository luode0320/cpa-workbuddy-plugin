package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidRequest = errors.New("invalid OpenAI chat request")

type InvalidRequestError struct {
	Message string
}

func (err *InvalidRequestError) Error() string { return err.Message }

func (err *InvalidRequestError) Is(target error) bool { return target == ErrInvalidRequest }

func invalidRequest(message string) error { return &InvalidRequestError{Message: message} }

type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type Image struct {
	Name     string
	MIMEType string
	Data     []byte
}

type Attachment struct {
	Name    string
	Content string
}

type ChatRequest struct {
	OutputByteLimit int64
	Model           string
	System          string
	Prompt          string
	Stream          bool
	Tools           []Tool
	Images          []Image
	Attachments     []Attachment
	Transcript      []Message
	Lineage         Lineage
}

type wireChatRequest struct {
	Model      string          `json:"model"`
	Messages   []wireMessage   `json:"messages"`
	Stream     bool            `json:"stream"`
	Tools      []wireTool      `json:"tools"`
	ToolChoice json.RawMessage `json:"tool_choice"`
}

type wireMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []wireToolCall  `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Arguments   string          `json:"arguments"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

func ParseChatRequest(raw []byte) (ChatRequest, error) {
	var wire wireChatRequest
	if err := json.Unmarshal(raw, &wire); err != nil {
		return ChatRequest{}, invalidRequest("decode OpenAI chat request: " + err.Error())
	}
	model := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(wire.Model), "cursor/"))
	if model == "" || len(wire.Messages) == 0 {
		return ChatRequest{}, invalidRequest("OpenAI chat request requires model and messages")
	}
	tools, err := decodeTools(wire.Tools, wire.ToolChoice)
	if err != nil {
		return ChatRequest{}, err
	}
	system := make([]string, 0, 2)
	history := make([]string, 0, len(wire.Messages))
	images := make([]Image, 0)
	attachments := make([]Attachment, 0)
	transcript := make([]Message, 0, len(wire.Messages))
	toolNames := make(map[string]string)
	seenUser := false
	for _, message := range wire.Messages {
		role, ok := parseRole(message.Role)
		if !ok {
			return ChatRequest{}, invalidRequest(fmt.Sprintf("unsupported OpenAI message role %q", message.Role))
		}
		content, err := decodeMessageContent(message.Content, role == RoleAssistant)
		if err != nil {
			return ChatRequest{}, err
		}
		if role == RoleAssistant && len(message.ToolCalls) == 0 && strings.TrimSpace(content.Text) == "" && len(content.Images) == 0 && len(content.Attachments) == 0 {
			continue
		}
		images = append(images, content.Images...)
		attachments = append(attachments, content.Attachments...)
		text := content.promptText()
		canonical := Message{Role: role, Content: content.Parts, ToolCallID: message.ToolCallID, Name: strings.TrimSpace(message.Name)}
		switch role {
		case RoleSystem, RoleDeveloper:
			if text != "" {
				system = append(system, text)
			}
		case RoleUser:
			seenUser = true
			history = append(history, "User: "+text)
		case RoleAssistant:
			if text != "" {
				history = append(history, "Assistant: "+text)
			}
			for _, call := range message.ToolCalls {
				if call.ID == "" || call.Function.Name == "" {
					return ChatRequest{}, invalidRequest("assistant tool_calls require id and function.name")
				}
				toolNames[call.ID] = call.Function.Name
				canonical.ToolCalls = append(canonical.ToolCalls, ToolCall{
					ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments,
				})
				history = append(history, fmt.Sprintf("Assistant tool call %s (%s): %s", call.Function.Name, call.ID, call.Function.Arguments))
			}
		case RoleTool:
			name := canonical.Name
			if name == "" {
				name = toolNames[message.ToolCallID]
			}
			canonical.Name = name
			history = append(history, fmt.Sprintf("Tool result %s (%s): %s", name, message.ToolCallID, text))
		}
		transcript = append(transcript, canonical)
	}
	if !seenUser {
		return ChatRequest{}, invalidRequest("OpenAI chat request requires a user message")
	}
	history = appendToolResultContinuation(history, transcript)
	request := ChatRequest{
		Model: model, System: strings.Join(system, "\n\n"), Prompt: strings.Join(history, "\n"), Stream: wire.Stream,
		Tools: tools, Images: images, Attachments: attachments, Transcript: transcript,
	}
	request.Lineage = buildLineage(model, tools, transcript)
	return request, nil
}

func parseRole(value string) (Role, bool) {
	switch Role(value) {
	case RoleSystem, RoleDeveloper, RoleUser, RoleAssistant, RoleTool:
		return Role(value), true
	default:
		return "", false
	}
}

func decodeTools(wireTools []wireTool, rawChoice json.RawMessage) ([]Tool, error) {
	tools := make([]Tool, 0, len(wireTools))
	for _, wire := range wireTools {
		if wire.Type != "function" || strings.TrimSpace(wire.Function.Name) == "" {
			return nil, invalidRequest("Cursor plugin supports named function tools only")
		}
		parameters := wire.Function.Parameters
		if len(parameters) == 0 || string(parameters) == "null" {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		if !json.Valid(parameters) {
			return nil, invalidRequest("tool parameters must be valid JSON")
		}
		tools = append(tools, Tool{Name: wire.Function.Name, Description: wire.Function.Description, Parameters: parameters})
	}
	choice := strings.TrimSpace(string(rawChoice))
	if strings.HasPrefix(choice, "\"") {
		var namedChoice string
		if err := json.Unmarshal(rawChoice, &namedChoice); err != nil {
			return nil, invalidRequest("unsupported tool_choice")
		}
		switch namedChoice {
		case "none":
			return nil, nil
		case "auto", "required":
			return tools, nil
		default:
			return nil, invalidRequest("unsupported tool_choice")
		}
	}
	if len(rawChoice) > 0 && choice != "null" {
		var named struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if err := json.Unmarshal(rawChoice, &named); err != nil || named.Type != "function" || named.Function.Name == "" {
			return nil, invalidRequest("unsupported tool_choice")
		}
		for _, tool := range tools {
			if tool.Name == named.Function.Name {
				return []Tool{tool}, nil
			}
		}
		return nil, invalidRequest("tool_choice names an unavailable function")
	}
	return tools, nil
}
