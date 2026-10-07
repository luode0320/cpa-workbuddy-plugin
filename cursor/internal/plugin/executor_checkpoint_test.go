package plugin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
)

func Test_Checkpoint_LargeHistory_replays_only_strict_suffix(t *testing.T) {
	// Given
	history := strings.Repeat("tok ", 95_000)
	client := &recordingCursorClient{steps: []cursorRunStep{
		successfulTextStep("seed-answer", "conversation-1", []byte("checkpoint-1")),
		successfulTextStep("follow-up-answer", "conversation-1", []byte("checkpoint-2")),
	}}
	handler := NewHandler(Dependencies{Cursor: client})
	first := executorFixture(t, "session-a", "account-a", "auth-a", "auto", "", []map[string]any{
		textMessage("system", "be exact"), textMessage("user", history),
	})
	second := executorFixture(t, "session-a", "account-a", "auth-a", "auto", "", []map[string]any{
		textMessage("system", "be exact"), textMessage("user", history),
		textMessage("assistant", "seed-answer"), textMessage("user", "only the suffix"),
	})
	_, err := handler.execute(context.Background(), first)
	require.NoError(t, err)

	// When
	_, err = handler.execute(context.Background(), second)

	// Then
	require.NoError(t, err)
	inputs := client.Inputs()
	require.Len(t, inputs, 2)
	require.Equal(t, cursorproto.FullReplay, inputs[0].Mode)
	require.Equal(t, cursorproto.CheckpointSuffix, inputs[1].Mode)
	require.Equal(t, "conversation-1", inputs[1].ConversationID)
	require.Equal(t, []byte("checkpoint-1"), inputs[1].Checkpoint)
	require.Equal(t, "User: only the suffix", inputs[1].Prompt)
	firstReplayBytes := replayBytes(inputs[0])
	suffixBytes := replayBytes(inputs[1])
	reduction := 1 - float64(suffixBytes)/float64(firstReplayBytes)
	t.Logf("synthetic_tokens=95000 first_replay_bytes=%d suffix_bytes=%d reduction_percent=%.4f", firstReplayBytes, suffixBytes, reduction*100)
	require.GreaterOrEqual(t, reduction, 0.90)
}

func Test_Checkpoint_StableAccountIdentity_ignores_token_rotation_and_falls_back_to_AuthID(t *testing.T) {
	for _, test := range []struct {
		name      string
		accountID string
		authID    string
	}{
		{name: "credential account", accountID: "account-a", authID: "changing-host-auth"},
		{name: "host auth id", authID: "stable-host-auth"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &recordingCursorClient{steps: []cursorRunStep{
				successfulTextStep("seed-answer", "conversation", []byte("checkpoint")),
				successfulTextStep("next-answer", "conversation", []byte("checkpoint-next")),
			}}
			handler := NewHandler(Dependencies{Cursor: client})
			seed := executorFixture(t, "session", test.accountID, test.authID, "auto", "", []map[string]any{textMessage("user", "seed")})
			_, err := handler.execute(context.Background(), seed)
			require.NoError(t, err)
			continuation := executorFixture(t, "session", test.accountID, test.authID, "auto", "", []map[string]any{
				textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "next"),
			})
			continuation = rotateFixtureAccessToken(t, continuation, "rotated-access-token")
			if test.accountID != "" {
				continuation = replaceFixtureAuthID(t, continuation, "rotated-host-auth-id")
			}

			_, err = handler.execute(context.Background(), continuation)

			require.NoError(t, err)
			inputs := client.Inputs()
			require.Equal(t, cursorproto.CheckpointSuffix, inputs[1].Mode)
			require.Equal(t, "rotated-access-token", inputs[1].AccessToken)
		})
	}
}

func Test_Checkpoint_Isolation_and_history_invalidation_force_full_replay(t *testing.T) {
	tests := []struct {
		name          string
		seedSession   string
		seedAccount   string
		seedModel     string
		nextSession   string
		nextAccount   string
		nextModel     string
		nextMessages  []map[string]any
		withoutID     bool
		restartBefore bool
	}{
		{name: "account", seedSession: "s", seedAccount: "a", seedModel: "auto", nextSession: "s", nextAccount: "b", nextModel: "auto"},
		{name: "model", seedSession: "s", seedAccount: "a", seedModel: "auto", nextSession: "s", nextAccount: "a", nextModel: "other"},
		{name: "session", seedSession: "s1", seedAccount: "a", seedModel: "auto", nextSession: "s2", nextAccount: "a", nextModel: "auto"},
		{name: "history edit", seedSession: "s", seedAccount: "a", seedModel: "auto", nextSession: "s", nextAccount: "a", nextModel: "auto", nextMessages: []map[string]any{
			textMessage("user", "edited"), textMessage("assistant", "seed-answer"), textMessage("user", "next"),
		}},
		{name: "system edit", seedSession: "s", seedAccount: "a", seedModel: "auto", nextSession: "s", nextAccount: "a", nextModel: "auto", nextMessages: []map[string]any{
			textMessage("system", "new system"), textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "next"),
		}},
		{name: "compaction", seedSession: "s", seedAccount: "a", seedModel: "auto", nextSession: "s", nextAccount: "a", nextModel: "auto", nextMessages: []map[string]any{
			textMessage("user", "compacted next"),
		}},
		{name: "restart", seedSession: "s", seedAccount: "a", seedModel: "auto", nextSession: "s", nextAccount: "a", nextModel: "auto", restartBefore: true},
		{name: "missing identity", seedSession: "", seedAccount: "", seedModel: "auto", nextSession: "", nextAccount: "", nextModel: "auto", withoutID: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &recordingCursorClient{steps: []cursorRunStep{
				successfulTextStep("seed-answer", "conversation-seed", []byte("checkpoint-seed")),
				successfulTextStep("next-answer", "conversation-next", []byte("checkpoint-next")),
			}}
			handler := NewHandler(Dependencies{Cursor: client})
			authID := "auth-a"
			if test.withoutID {
				authID = ""
			}
			seed := executorFixture(t, test.seedSession, test.seedAccount, authID, test.seedModel, "", []map[string]any{textMessage("user", "seed")})
			_, err := handler.execute(context.Background(), seed)
			require.NoError(t, err)
			if test.restartBefore {
				handler = NewHandler(Dependencies{Cursor: client})
			}
			messages := test.nextMessages
			if messages == nil {
				messages = []map[string]any{textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", "next")}
			}
			next := executorFixture(t, test.nextSession, test.nextAccount, authID, test.nextModel, "", messages)

			_, err = handler.execute(context.Background(), next)

			require.NoError(t, err)
			inputs := client.Inputs()
			require.Len(t, inputs, 2)
			require.Equal(t, cursorproto.FullReplay, inputs[1].Mode)
			require.Empty(t, inputs[1].Checkpoint)
		})
	}
}

func Test_Checkpoint_SameKey_serializes_branches_but_independent_sessions_overlap(t *testing.T) {
	t.Run("same key", func(t *testing.T) {
		firstEntered := make(chan struct{})
		secondEntered := make(chan struct{})
		releaseFirst := make(chan struct{})
		client := &recordingCursorClient{steps: []cursorRunStep{
			successfulTextStep("seed-answer", "conversation", []byte("checkpoint-seed")),
			{entered: firstEntered, release: releaseFirst, events: successfulTextStep("branch-answer", "conversation", []byte("checkpoint-branch")).events, result: successfulTextStep("branch-answer", "conversation", []byte("checkpoint-branch")).result},
			{entered: secondEntered, events: successfulTextStep("other-answer", "other", []byte("checkpoint-other")).events, result: successfulTextStep("other-answer", "other", []byte("checkpoint-other")).result},
		}}
		handler := NewHandler(Dependencies{Cursor: client})
		_, err := handler.execute(context.Background(), executorFixture(t, "same", "account", "auth", "auto", "", []map[string]any{textMessage("user", "seed")}))
		require.NoError(t, err)
		branch := func(question string) []byte {
			return executorFixture(t, "same", "account", "auth", "auto", "", []map[string]any{
				textMessage("user", "seed"), textMessage("assistant", "seed-answer"), textMessage("user", question),
			})
		}
		errors := make(chan error, 2)
		go func() { _, runErr := handler.execute(context.Background(), branch("branch-a")); errors <- runErr }()
		<-firstEntered
		go func() { _, runErr := handler.execute(context.Background(), branch("branch-b")); errors <- runErr }()
		select {
		case <-secondEntered:
			t.Fatal("same-key branch entered Cursor before the active turn completed")
		case <-time.After(50 * time.Millisecond):
		}
		close(releaseFirst)
		<-secondEntered
		require.NoError(t, <-errors)
		require.NoError(t, <-errors)
		inputs := client.Inputs()
		require.Equal(t, cursorproto.CheckpointSuffix, inputs[1].Mode)
		require.Equal(t, cursorproto.FullReplay, inputs[2].Mode)
	})

	t.Run("independent sessions", func(t *testing.T) {
		enteredA := make(chan struct{})
		enteredB := make(chan struct{})
		release := make(chan struct{})
		client := &recordingCursorClient{steps: []cursorRunStep{
			{entered: enteredA, release: release, events: successfulTextStep("a", "a", []byte("a")).events, result: successfulTextStep("a", "a", []byte("a")).result},
			{entered: enteredB, release: release, events: successfulTextStep("b", "b", []byte("b")).events, result: successfulTextStep("b", "b", []byte("b")).result},
		}}
		handler := NewHandler(Dependencies{Cursor: client})
		errors := make(chan error, 2)
		go func() {
			_, err := handler.execute(context.Background(), executorFixture(t, "a", "account", "auth", "auto", "", []map[string]any{textMessage("user", "a")}))
			errors <- err
		}()
		go func() {
			_, err := handler.execute(context.Background(), executorFixture(t, "b", "account", "auth", "auto", "", []map[string]any{textMessage("user", "b")}))
			errors <- err
		}()
		select {
		case <-enteredA:
		case <-time.After(time.Second):
			t.Fatal("session a did not enter")
		}
		select {
		case <-enteredB:
		case <-time.After(time.Second):
			t.Fatal("independent session was serialized")
		}
		close(release)
		require.NoError(t, <-errors)
		require.NoError(t, <-errors)
	})
}
