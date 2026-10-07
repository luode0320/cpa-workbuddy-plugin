package cursorapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"
)

type RunInput struct {
	AccessToken    string
	Model          string
	System         string
	Prompt         string
	ConversationID string
	Checkpoint     []byte
	Mode           cursorproto.ContinuationMode
	Tools          []cursorproto.ToolDefinition
	Images         []cursorproto.ImageAttachment
	Attachments    []cursorproto.FileAttachment
}

type RunResult struct {
	ConversationID       string
	Checkpoint           []byte
	OutputExposed        bool
	ToolExposed          bool
	InteractionResponded bool
	TTFT                 time.Duration
}

func (client *Client) Run(
	ctx context.Context,
	input RunInput,
	emit func(cursorproto.ServerEvent) error,
) (RunResult, error) {
	if input.AccessToken == "" || input.Model == "" || (input.Prompt == "" && input.Mode != cursorproto.CheckpointResume) {
		return RunResult{}, errors.New("Cursor run requires token, model, and prompt")
	}
	conversationID := input.ConversationID
	if conversationID == "" {
		conversationID = "cursor_" + strings.ReplaceAll(randomUUID(), "-", "")
	}
	result := RunResult{ConversationID: conversationID}
	environment := cursorproto.RequestEnvironment{
		TimeZone: "UTC", WorkspacePaths: []string{client.workspacePath}, ProjectFolder: client.projectFolder,
		Tools: input.Tools,
	}
	runPayload, err := cursorproto.EncodeRunRequest(cursorproto.RunRequest{
		ConversationID: conversationID,
		MessageID:      randomUUID(),
		Model:          input.Model,
		System:         input.System,
		Prompt:         input.Prompt,
		TimeZone:       environment.TimeZone,
		WorkspacePaths: environment.WorkspacePaths,
		ProjectFolder:  environment.ProjectFolder,
		Tools:          input.Tools,
		Images:         input.Images,
		Attachments:    input.Attachments,
		Mode:           input.Mode,
		Checkpoint:     input.Checkpoint,
	})
	if err != nil {
		return result, err
	}
	heartbeatPayload, err := cursorproto.EncodeClientHeartbeat()
	if err != nil {
		return result, err
	}
	overallContext, cancelOverall := context.WithTimeout(ctx, client.overall)
	defer cancelOverall()
	runContext, cancelRun := context.WithCancelCause(overallContext)
	defer cancelRun(nil)
	watchdogs := newRunWatchdogs(cancelRun, client)
	defer watchdogs.stop()

	runBodyReader, runBodyWriter := io.Pipe()
	request, err := http.NewRequestWithContext(runContext, http.MethodPost, client.endpoint("/agent.v1.AgentService/Run"), runBodyReader)
	if err != nil {
		return result, errors.Join(fmt.Errorf("create Cursor Run request: %w", err), runBodyReader.Close(), runBodyWriter.Close())
	}
	client.applyHeaders(request, requestHeaders{
		accessToken: input.AccessToken,
		requestID:   randomUUID(),
		sessionID:   randomUUID(),
	}, "application/connect+proto")
	stopWriter := make(chan struct{})
	writeResult := make(chan error, 1)
	outbound := make(chan []byte, 16)
	go func() {
		writeResult <- writeRunFrames(runContext, runBodyWriter, runPayload, heartbeatPayload, client.heartbeat, outbound, stopWriter)
	}()

	runResponse, err := client.httpClient.Do(request)
	if err != nil {
		readerErr := runBodyReader.CloseWithError(err)
		close(stopWriter)
		return result, errors.Join(runCause(runContext, fmt.Errorf("start Cursor Run: %w", err)), <-writeResult, readerErr, runBodyWriter.CloseWithError(err))
	}
	if runResponse.StatusCode != http.StatusOK {
		status := runResponse.StatusCode
		body, bodyErr := io.ReadAll(io.LimitReader(runResponse.Body, 1<<20))
		readerErr := runBodyReader.Close()
		close(stopWriter)
		return result, errors.Join(runStatusError(status, body), bodyErr, readerErr, <-writeResult, runResponse.Body.Close(), runBodyWriter.Close())
	}
	imageWrites := cursorproto.NewImageWriteExecutor(environment.ProjectFolder)
	result, streamErr := readRunStream(
		runContext, runResponse.Body, result, emit, cursorproto.NewBlobStore(), imageWrites, environment, outbound, watchdogs, client.trailingDrain,
	)
	close(stopWriter)
	readerErr := runBodyReader.Close()
	return result, errors.Join(streamErr, readerErr, <-writeResult, runResponse.Body.Close(), runBodyWriter.Close(), imageWrites.Cleanup())
}

func writeRunFrames(
	ctx context.Context,
	writer io.Writer,
	runPayload []byte,
	heartbeatPayload []byte,
	heartbeatInterval time.Duration,
	outbound <-chan []byte,
	stop <-chan struct{},
) error {
	if _, err := writer.Write(cursorproto.EncodeConnectFrame(runPayload)); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-stop:
			return nil
		default:
		}
		return fmt.Errorf("write Cursor Run request: %w", err)
	}
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-stop:
			return nil
		case payload := <-outbound:
			if _, err := writer.Write(cursorproto.EncodeConnectFrame(payload)); err != nil {
				return fmt.Errorf("write Cursor client reply: %w", err)
			}
		case <-ticker.C:
			if _, err := writer.Write(cursorproto.EncodeConnectFrame(heartbeatPayload)); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				select {
				case <-stop:
					return nil
				default:
				}
				return fmt.Errorf("write Cursor heartbeat: %w", err)
			}
		}
	}
}

func runCause(ctx context.Context, fallback error) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return fallback
}
