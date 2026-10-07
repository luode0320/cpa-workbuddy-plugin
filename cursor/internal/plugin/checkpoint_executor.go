package plugin

import (
	"context"
	"strings"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorapi"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorsession"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/openai"
)

type runObservation struct {
	text     strings.Builder
	toolCall bool
	image    bool
}

func (handler *Handler) runCheckpointed(
	ctx context.Context,
	request executorRequest,
	chat openai.ChatRequest,
	credentials cursorauth.Credentials,
	emit func(cursorproto.ServerEvent) error,
) (cursorapi.RunResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	boundedEmit := emit
	if chat.OutputByteLimit > 0 {
		budget := &outputBudget{remaining: chat.OutputByteLimit, emit: emit, cancel: cancel}
		boundedEmit = budget.emitEvent
	}
	accountIdentity := strings.TrimSpace(credentials.AccountID)
	if accountIdentity == "" {
		accountIdentity = strings.TrimSpace(request.AuthID)
	}
	session, hasSession := request.stableSessionIdentity()
	if accountIdentity == "" || !hasSession {
		input := fullReplayInput(chat, credentials.AccessToken)
		result, _, err := handler.runAttempt(ctx, input, boundedEmit)
		handler.usage.checkpoints.recordRun(request.AuthID, input, result)
		return result, err
	}

	lockKey := sessionTurnKey{account: accountIdentity, model: chat.Model, session: session.String()}
	release, err := handler.turns.acquire(ctx, lockKey)
	if err != nil {
		return cursorapi.RunResult{}, err
	}
	defer release()
	key, err := cursorsession.NewKey(accountIdentity, chat.Model, session.String())
	if err != nil {
		return cursorapi.RunResult{}, err
	}
	token, lookup := handler.sessions.Start(key, cursorsession.Match{
		SystemDigest:  string(chat.Lineage.SystemDigest),
		PrefixDigests: lineageDigests(chat.Lineage.PrefixDigests),
	})
	handler.usage.checkpoints.recordLookup(request.AuthID, lookup.Reason == cursorsession.ReasonHit)
	input := fullReplayInput(chat, credentials.AccessToken)
	hasToolResult := false
	if lookup.Reason == cursorsession.ReasonHit {
		continuation, ok := chat.ContinuationFrom(lookup.Snapshot.CoveredMessageCount)
		if ok {
			input = cursorapi.RunInput{
				AccessToken: credentials.AccessToken, Model: chat.Model,
				ConversationID: lookup.Snapshot.ConversationID,
				Checkpoint:     lookup.Snapshot.CheckpointBytes(), Mode: cursorproto.CheckpointSuffix,
				Prompt: continuation.Prompt, Tools: cursorTools(chat.Tools),
				Images: cursorImages(continuation.Images), Attachments: cursorAttachments(continuation.Attachments),
			}
			hasToolResult = continuation.HasToolResult
		} else {
			handler.sessions.Invalidate(key, cursorsession.ReasonMessageCountMismatch)
			handler.usage.checkpoints.recordInvalidation(request.AuthID)
			token, _ = handler.sessions.Start(key, cursorsession.Match{})
			handler.usage.checkpoints.recordLookup(request.AuthID, false)
		}
	}

	result, observation, runErr := handler.runAttempt(ctx, input, boundedEmit)
	handler.usage.checkpoints.recordRun(request.AuthID, input, result)
	if shouldRetryFresh(input, result, observation, runErr, hasToolResult) {
		handler.sessions.Invalidate(key, cursorsession.ReasonInvalidated)
		handler.usage.checkpoints.recordInvalidation(request.AuthID)
		handler.usage.checkpoints.recordFallback(request.AuthID)
		token, _ = handler.sessions.Start(key, cursorsession.Match{})
		handler.usage.checkpoints.recordLookup(request.AuthID, false)
		input = fullReplayInput(chat, credentials.AccessToken)
		result, observation, runErr = handler.runAttempt(ctx, input, boundedEmit)
		handler.usage.checkpoints.recordRun(request.AuthID, input, result)
	}
	if runErr != nil {
		if input.Mode == cursorproto.CheckpointSuffix && isCheckpointDecodeFailure(runErr) {
			handler.sessions.Invalidate(key, cursorsession.ReasonDecodeFailure)
			handler.usage.checkpoints.recordInvalidation(request.AuthID)
		}
		return result, runErr
	}
	if result.ToolExposed || observation.toolCall || observation.image || result.ConversationID == "" || len(result.Checkpoint) == 0 || observation.text.Len() == 0 {
		return result, nil
	}
	confirmedLineage := chat.LineageWithAssistant(observation.text.String())
	covered := len(chat.Transcript) + 1
	if covered > len(confirmedLineage.PrefixDigests) {
		return result, nil
	}
	handler.sessions.Commit(token, cursorsession.Confirmed{
		ConversationID: result.ConversationID, Checkpoint: result.Checkpoint,
		CoveredMessageCount: covered, SystemDigest: string(confirmedLineage.SystemDigest),
		PrefixDigest: string(confirmedLineage.PrefixDigests[covered-1]),
	})
	return result, nil
}

func isCheckpointDecodeFailure(err error) bool {
	return err != nil && strings.Contains(err.Error(), "decode Cursor conversation checkpoint")
}

func (handler *Handler) runAttempt(
	ctx context.Context,
	input cursorapi.RunInput,
	emit func(cursorproto.ServerEvent) error,
) (cursorapi.RunResult, runObservation, error) {
	observation := runObservation{}
	result, err := handler.cursor.Run(ctx, input, func(event cursorproto.ServerEvent) error {
		switch event.Kind {
		case cursorproto.EventText:
			observation.text.WriteString(event.Text)
		case cursorproto.EventToolCall:
			observation.toolCall = true
		case cursorproto.EventImage:
			observation.image = true
		}
		return emit(event)
	})
	return result, observation, err
}

func fullReplayInput(chat openai.ChatRequest, accessToken string) cursorapi.RunInput {
	return cursorapi.RunInput{
		AccessToken: accessToken, Model: chat.Model, System: chat.System, Prompt: chat.Prompt,
		Mode: cursorproto.FullReplay, Tools: cursorTools(chat.Tools), Images: cursorImages(chat.Images),
		Attachments: cursorAttachments(chat.Attachments),
	}
}

func shouldRetryFresh(
	input cursorapi.RunInput,
	result cursorapi.RunResult,
	observation runObservation,
	err error,
	hasToolResult bool,
) bool {
	retryable := cursorapi.IsReplayableCheckpointError(err)
	return input.Mode == cursorproto.CheckpointSuffix && retryable &&
		!result.InteractionResponded &&
		!result.OutputExposed && !result.ToolExposed && observation.text.Len() == 0 && !observation.toolCall && !observation.image && !hasToolResult
}

func lineageDigests(digests []openai.Digest) []string {
	result := make([]string, len(digests))
	for index, digest := range digests {
		result[index] = string(digest)
	}
	return result
}
