package cursorproto

import (
	"bytes"
	"encoding/hex"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func Test_Checkpoint_DecodeServerEvent_captures_fixture_as_opaque_state(t *testing.T) {
	fixture, err := os.ReadFile("testdata/conversation_checkpoint_update.hex")
	require.NoError(t, err)
	wire, err := hex.DecodeString(string(bytes.TrimSpace(fixture)))
	require.NoError(t, err)

	event, err := DecodeServerEvent(wire)

	require.NoError(t, err)
	require.Equal(t, EventCheckpoint, event.Kind)
	require.Equal(t, "conversation_checkpoint_update", event.Type)
	require.Equal(t, "220770656e64696e67", hex.EncodeToString(event.Checkpoint))
}

func Test_Checkpoint_DecodeServerEvent_rejects_malformed_fixture(t *testing.T) {
	_, err := DecodeServerEvent([]byte{0x1a, 0x02, 0xff, 0xff})

	require.Error(t, err)
}

func Test_CheckpointDescriptor_uses_proven_wire_fields(t *testing.T) {
	server, err := newMessage("AgentServerMessage")
	require.NoError(t, err)
	run, err := newMessage("AgentRunRequest")
	require.NoError(t, err)
	action, err := nestedMessage(run, "action")
	require.NoError(t, err)
	resume, err := nestedMessage(action, "resume_action")
	require.NoError(t, err)
	requestContext, err := nestedMessage(resume, "request_context")
	require.NoError(t, err)
	environment, err := nestedMessage(requestContext, "env")
	require.NoError(t, err)

	require.Equal(t, 3, int(field(server, "conversation_checkpoint_update").Number()))
	require.Equal(t, 1, int(field(run, "conversation_state").Number()))
	require.Equal(t, 2, int(field(run, "action").Number()))
	require.Equal(t, 1, int(field(action, "user_message_action").Number()))
	require.Equal(t, 2, int(field(action, "resume_action").Number()))
	require.Equal(t, 2, int(field(resume, "request_context").Number()))
	require.Equal(t, 2, int(field(environment, "workspace_paths").Number()))
	require.Equal(t, 11, int(field(environment, "project_folder").Number()))
}

func Test_Checkpoint_EncodeRunRequest_distinguishes_full_replay_suffix_and_resume(t *testing.T) {
	checkpoint, err := hex.DecodeString("220770656e64696e67")
	require.NoError(t, err)
	tests := []struct {
		name             string
		mode             ContinuationMode
		checkpoint       []byte
		prompt           string
		wantAction       string
		wantPendingCalls int
		wantText         string
	}{
		{name: "full replay", mode: FullReplay, prompt: "full prompt", wantAction: "user_message_action", wantText: "system\n\nfull prompt"},
		{name: "checkpoint suffix", mode: CheckpointSuffix, checkpoint: checkpoint, prompt: "suffix only", wantAction: "user_message_action", wantPendingCalls: 1, wantText: "suffix only"},
		{name: "checkpoint resume", mode: CheckpointResume, checkpoint: checkpoint, wantAction: "resume_action", wantPendingCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := EncodeRunRequest(RunRequest{
				ConversationID: "conversation-1",
				MessageID:      "message-1",
				Model:          "cursor-model",
				System:         "system",
				Prompt:         tt.prompt,
				WorkspacePaths: []string{"/workspace"},
				ProjectFolder:  "/root/.cursor/projects/workspace",
				Mode:           tt.mode,
				Checkpoint:     tt.checkpoint,
			})
			require.NoError(t, err)

			client, err := newMessage("AgentClientMessage")
			require.NoError(t, err)
			require.NoError(t, proto.Unmarshal(raw, client))
			run := client.Get(field(client, "run_request")).Message()
			state := run.Get(field(run, "conversation_state")).Message()
			require.Equal(t, tt.wantPendingCalls, state.Get(field(state, "pending_tool_calls")).List().Len())
			action := run.Get(field(run, "action")).Message()
			selectedAction := action.WhichOneof(action.Descriptor().Oneofs().ByName("action"))
			require.Equal(t, tt.wantAction, string(selectedAction.Name()))
			contextParent := action.Get(selectedAction).Message()
			requestContext := contextParent.Get(field(contextParent, "request_context")).Message()
			environment := requestContext.Get(field(requestContext, "env")).Message()
			workspacePaths := environment.Get(field(environment, "workspace_paths")).List()
			require.Equal(t, 1, workspacePaths.Len())
			require.Equal(t, "/workspace", workspacePaths.Get(0).String())
			require.Equal(t, "/root/.cursor/projects/workspace", environment.Get(field(environment, "project_folder")).String())
			if tt.wantAction == "user_message_action" {
				userMessage := contextParent.Get(field(contextParent, "user_message")).Message()
				require.Equal(t, tt.wantText, userMessage.Get(field(userMessage, "text")).String())
			}
		})
	}
}

func Test_Checkpoint_EncodeRunRequest_rejects_invalid_state(t *testing.T) {
	_, err := EncodeRunRequest(RunRequest{
		ConversationID: "conversation-1",
		MessageID:      "message-1",
		Model:          "cursor-model",
		Prompt:         "suffix",
		Mode:           CheckpointSuffix,
		Checkpoint:     []byte{0xff},
	})

	require.Error(t, err)
}
