package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const toolResultContinuationPrompt = "Continue: the requested tool results above are complete. Do not repeat a completed tool call; continue with the user's request."

type Continuation struct {
	Prompt        string
	Images        []Image
	Attachments   []Attachment
	HasToolResult bool
}

func (request ChatRequest) ContinuationFrom(coveredMessageCount int) (Continuation, bool) {
	if coveredMessageCount < 0 || coveredMessageCount >= len(request.Transcript) {
		return Continuation{}, false
	}
	continuation := Continuation{}
	history := make([]string, 0, len(request.Transcript)-coveredMessageCount)
	for _, message := range request.Transcript[coveredMessageCount:] {
		text, images, attachments := messageContent(message.Content)
		continuation.Images = append(continuation.Images, images...)
		continuation.Attachments = append(continuation.Attachments, attachments...)
		switch message.Role {
		case RoleUser:
			history = append(history, "User: "+text)
		case RoleAssistant:
			if text != "" {
				history = append(history, "Assistant: "+text)
			}
			for _, call := range message.ToolCalls {
				history = append(history, fmt.Sprintf("Assistant tool call %s (%s): %s", call.Name, call.ID, call.Arguments))
			}
		case RoleTool:
			continuation.HasToolResult = true
			history = append(history, fmt.Sprintf("Tool result %s (%s): %s", message.Name, message.ToolCallID, text))
		case RoleSystem, RoleDeveloper:
			return Continuation{}, false
		}
	}
	history = appendToolResultContinuation(history, request.Transcript)
	continuation.Prompt = strings.Join(history, "\n")
	return continuation, continuation.Prompt != "" || len(continuation.Images) > 0 || len(continuation.Attachments) > 0
}

func appendToolResultContinuation(history []string, transcript []Message) []string {
	if len(transcript) == 0 || transcript[len(transcript)-1].Role != RoleTool {
		return history
	}
	return append(history, toolResultContinuationPrompt)
}

func (request ChatRequest) LineageWithAssistant(text string) Lineage {
	message := Message{
		Role:    RoleAssistant,
		Content: []ContentPart{{Kind: ContentText, Text: text}},
	}
	encoded, _ := json.Marshal(canonicalizeMessage(message))
	step := sha256.New()
	step.Write(request.Lineage.tail[:])
	step.Write(encoded)
	next := request.Lineage
	copy(next.tail[:], step.Sum(nil))
	next.PrefixDigests = make([]Digest, len(request.Lineage.PrefixDigests)+1)
	copy(next.PrefixDigests, request.Lineage.PrefixDigests)
	next.PrefixDigests[len(request.Lineage.PrefixDigests)] = Digest(hex.EncodeToString(next.tail[:]))
	return next
}

func messageContent(parts []ContentPart) (string, []Image, []Attachment) {
	texts := make([]string, 0, len(parts))
	images := make([]Image, 0)
	attachments := make([]Attachment, 0)
	for _, part := range parts {
		switch part.Kind {
		case ContentText:
			texts = append(texts, part.Text)
		case ContentImage:
			texts = append(texts, "[Attached image: "+part.Image.Name+"]")
			images = append(images, part.Image)
		case ContentAttachment:
			texts = append(texts, "[Attached file: "+part.Attachment.Name+"]")
			attachments = append(attachments, part.Attachment)
		}
	}
	return strings.Join(texts, "\n"), images, attachments
}
