package openai

import (
	"encoding/json"
	"fmt"
	"slices"
)

const noProgressLimit = 3

type ToolLoopError struct {
	Tool string
}

func (err *ToolLoopError) Error() string {
	return fmt.Sprintf("Cursor's configured tool-loop guard stopped %s after %d consecutive completions with identical arguments and results. No new upstream request was sent. Remove this tool from tool_loop_guard_tools or send a new user message to continue.", err.Tool, noProgressLimit)
}

func (err *ToolLoopError) Is(target error) bool { return target == ErrInvalidRequest }

// ValidateToolProgress applies only to explicitly selected tool names and adjacent,
// unambiguously linked completed exchanges. An empty policy never hard-stops tools;
// identical results alone cannot prove that arbitrary tools made no progress.
func (request ChatRequest) ValidateToolProgress(guardedTools []string) error {
	if len(guardedTools) == 0 || len(request.Transcript) < noProgressLimit*2 {
		return nil
	}
	var previous string
	var tool string
	seenIDs := make(map[string]struct{}, noProgressLimit)
	for offset := range noProgressLimit {
		index := len(request.Transcript) - 1 - offset*2
		result, assistant := request.Transcript[index], request.Transcript[index-1]
		if result.Role != RoleTool || assistant.Role != RoleAssistant || len(assistant.ToolCalls) != 1 || len(result.Content) == 0 {
			return nil
		}
		call := assistant.ToolCalls[0]
		if !slices.Contains(guardedTools, call.Name) {
			return nil
		}
		if call.ID == "" || call.ID != result.ToolCallID || (result.Name != "" && result.Name != call.Name) {
			return nil
		}
		if _, duplicate := seenIDs[call.ID]; duplicate {
			return nil
		}
		seenIDs[call.ID] = struct{}{}
		canonical := canonicalizeMessage(result)
		canonical.ToolCallID, canonical.Name = "", ""
		for index := range canonical.Content {
			if canonical.Content[index].Kind == ContentText {
				canonical.Content[index].Text = canonicalJSON([]byte(canonical.Content[index].Text))
			}
		}
		encoded, err := json.Marshal(struct {
			Name      string           `json:"name"`
			Arguments string           `json:"arguments"`
			Result    canonicalMessage `json:"result"`
		}{Name: call.Name, Arguments: canonicalJSON([]byte(call.Arguments)), Result: canonical})
		if err != nil {
			return fmt.Errorf("encode tool progress: %w", err)
		}
		fingerprint := digestBytes(encoded)
		if offset > 0 && fingerprint != previous {
			return nil
		}
		previous, tool = fingerprint, call.Name
	}
	return &ToolLoopError{Tool: tool}
}
