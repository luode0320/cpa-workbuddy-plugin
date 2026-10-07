package cursorproto

import "fmt"

func nativeExecPolicy(operation string, tools []ToolDefinition) string {
	policy := fmt.Sprintf("Cursor native %s cannot run on the gateway host. Do not retry a native operation; ", operation)
	var candidates []string
	switch operation {
	case "shell":
		candidates = []string{"bash", "shell", "exec"}
	case "read":
		candidates = []string{"read", "read_file"}
	case "grep":
		candidates = []string{"grep", "grep_search"}
	}
	for _, candidate := range candidates {
		for _, tool := range tools {
			if tool.Name == candidate {
				return policy + fmt.Sprintf("use the exposed client tool `%s` instead, following its schema, or report that the operation is unavailable and stop", tool.Name)
			}
		}
	}
	return policy + "no matching client tool is exposed. Report that the operation is unavailable and stop"
}
