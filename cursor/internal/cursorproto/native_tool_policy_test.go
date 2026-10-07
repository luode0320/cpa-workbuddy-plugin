package cursorproto

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func Test_ReplyNativeReadOnlyExec_omits_unexposed_tool_names(t *testing.T) {
	for _, operation := range []protoreflect.Name{"read_args", "grep_args"} {
		t.Run(string(operation), func(t *testing.T) {
			// Given
			raw := encodeNativeReadOnlyExecRequest(t, 41, "exec-41", operation, "/downstream/workspace")

			// When
			reply, handled, err := ReplyNativeReadOnlyExec(raw, nil)

			// Then
			require.NoError(t, err)
			require.True(t, handled)
			client, err := newMessage("AgentClientMessage")
			require.NoError(t, err)
			require.NoError(t, proto.Unmarshal(reply, client))
			exec := client.Get(field(client, "exec_client_message")).Message()
			result := exec.Get(exec.WhichOneof(exec.Descriptor().Oneofs().ByName("message"))).Message()
			failure := result.Get(field(result, "error")).Message()
			policy := failure.Get(field(failure, "error")).String()
			require.NotEmpty(t, policy)
			require.Empty(t, regexp.MustCompile("`[^`]+`").FindAllString(policy, -1))
		})
	}
}

func Test_ReplyNativeExec_routes_only_to_catalog_tools(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation protoreflect.Name
		tools     []string
		want      string
	}{
		{"bash", "shell_args", []string{"bash"}, "`bash`"},
		{"shell", "shell_args", []string{"shell"}, "`shell`"},
		{"exec", "shell_args", []string{"exec"}, "`exec`"},
		{"prefer bash", "shell_stream_args", []string{"exec", "shell", "bash"}, "`bash`"},
		{"prefer shell to exec", "shell_stream_args", []string{"exec", "shell"}, "`shell`"},
		{"empty shell catalog", "shell_args", nil, ""},
		{"unrelated shell catalog", "shell_stream_args", []string{"read", "grep", "shell_history", "bash_help"}, ""},
		{"exact names only", "shell_args", []string{"BASH", "namespace_bash"}, ""},
		{"read", "read_args", []string{"bash", "read"}, "`read`"},
		{"read file", "read_args", []string{"read_file"}, "`read_file`"},
		{"prefer read", "read_args", []string{"read_file", "read"}, "`read`"},
		{"unrelated read catalog", "read_args", []string{"bash", "grep", "read_file_metadata"}, ""},
		{"grep", "grep_args", []string{"bash", "grep"}, "`grep`"},
		{"grep search", "grep_args", []string{"grep_search"}, "`grep_search`"},
		{"prefer grep", "grep_args", []string{"grep_search", "grep"}, "`grep`"},
		{"unrelated grep catalog", "grep_args", []string{"bash", "read", "web_search"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given
			var tools []ToolDefinition
			for _, name := range test.tools {
				tools = append(tools, ToolDefinition{Name: name, Description: "bash shell exec read read_file grep grep_search"})
			}
			var replies [][]byte
			var handled bool
			var err error

			// When
			switch test.operation {
			case "shell_args", "shell_stream_args":
				raw := encodeNativeShellExecRequest(t, 51, "exec-51", test.operation, "pwd", "/downstream/workspace")
				replies, handled, err = ReplyNativeShellExec(raw, tools)
			case "read_args", "grep_args":
				raw := encodeNativeReadOnlyExecRequest(t, 41, "exec-41", test.operation, "/downstream/workspace")
				var reply []byte
				reply, handled, err = ReplyNativeReadOnlyExec(raw, tools)
				replies = [][]byte{reply}
			}

			// Then
			require.NoError(t, err)
			require.True(t, handled)
			wantReplies := 1
			if test.operation == "shell_stream_args" {
				wantReplies = 5
			}
			require.Len(t, replies, wantReplies)
			for _, reply := range replies {
				client, err := newMessage("AgentClientMessage")
				require.NoError(t, err)
				require.NoError(t, proto.Unmarshal(reply, client))
				if client.WhichOneof(client.Descriptor().Oneofs().ByName("message")).Name() != "exec_client_message" {
					continue
				}
				exec := client.Get(field(client, "exec_client_message")).Message()
				active := exec.WhichOneof(exec.Descriptor().Oneofs().ByName("message"))
				result := exec.Get(active).Message()
				var policy string
				switch active.Name() {
				case "shell_result":
					failure := result.Get(field(result, "failure")).Message()
					policy = failure.Get(field(failure, "stderr")).String()
				case "shell_stream":
					if result.WhichOneof(result.Descriptor().Oneofs().ByName("event")).Name() != "stderr" {
						continue
					}
					stderr := result.Get(field(result, "stderr")).Message()
					policy = stderr.Get(field(stderr, "data")).String()
				case "read_result", "grep_result":
					failure := result.Get(field(result, "error")).Message()
					policy = failure.Get(field(failure, "error")).String()
				default:
					t.Fatalf("unexpected refusal result: %s", active.Name())
				}
				var want []string
				if test.want != "" {
					want = []string{test.want}
				}
				require.NotEmpty(t, policy)
				require.Equal(t, want, regexp.MustCompile("`[^`]+`").FindAllString(policy, -1))
			}
		})
	}
}
