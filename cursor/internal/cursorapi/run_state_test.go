package cursorapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func Test_Client_Run_uses_stable_conversation_and_checkpoint_suffix(t *testing.T) {
	checkpoint := []byte{0x22, 0x07, 0x70, 0x65, 0x6e, 0x64, 0x69, 0x6e, 0x67}
	var requestPayload []byte
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requestPayload = readConnectPayload(t, request.Body)
		body := append(connectFrame(textServerMessage("ok")), connectFrame(turnEndedServerMessage())...)
		return responseWithBody(http.StatusOK, body), nil
	})
	client := newTestClient(t, transport, Config{})

	result, err := client.Run(context.Background(), RunInput{
		AccessToken:    "token",
		Model:          "default",
		Prompt:         "suffix",
		ConversationID: "stable-conversation",
		Checkpoint:     checkpoint,
		Mode:           cursorproto.CheckpointSuffix,
	}, func(cursorproto.ServerEvent) error { return nil })

	require.NoError(t, err)
	require.Equal(t, "stable-conversation", result.ConversationID)
	require.True(t, bytes.Contains(requestPayload, []byte("stable-conversation")))
	require.True(t, result.OutputExposed)
	require.False(t, result.ToolExposed)
	// Windows 计时器精度约 15.6ms，mock 传输可能在同一 tick 内完成，故不断言非零。
	require.GreaterOrEqual(t, result.TTFT, time.Duration(0))
}

func Test_Client_Run_uses_stable_conversation_for_full_replay(t *testing.T) {
	var requestPayload []byte
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requestPayload = readConnectPayload(t, request.Body)
		body := append(connectFrame(turnEndedServerMessage()), connectFrame(checkpointServerMessage([]byte{0x22, 0x01, 0x78}))...)
		return responseWithBody(http.StatusOK, body), nil
	})
	client := newTestClient(t, transport, Config{})

	result, err := client.Run(context.Background(), RunInput{
		AccessToken: "token", Model: "default", Prompt: "full transcript", ConversationID: "stable-full-replay",
	}, func(cursorproto.ServerEvent) error { return nil })

	require.NoError(t, err)
	require.Equal(t, "stable-full-replay", result.ConversationID)
	require.True(t, bytes.Contains(requestPayload, []byte("stable-full-replay")))
	require.True(t, result.OutputExposed)
}

func Test_Client_Run_decodes_fragmented_first_frame(t *testing.T) {
	frame := connectFrame(textServerMessage("fragmented"))
	body := newChunkBody(
		frame[:1], frame[1:4], frame[4:7], frame[7:],
		connectFrame(turnEndedServerMessage()),
		connectFrame(checkpointServerMessage([]byte{0x22, 0x01, 0x78})),
	)
	client := newTestClient(t, staticResponseTransport(body), Config{})
	var text string

	_, err := client.Run(context.Background(), validRunInput(), func(event cursorproto.ServerEvent) error {
		if event.Kind == cursorproto.EventText {
			text += event.Text
		}
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, "fragmented", text)
}

func Test_Client_Run_times_out_when_headers_arrive_without_data(t *testing.T) {
	body := newBlockingBody()
	client := newTestClient(t, staticResponseTransport(body), Config{FirstDataTimeout: 20 * time.Millisecond})

	_, err := client.Run(context.Background(), validRunInput(), func(cursorproto.ServerEvent) error { return nil })

	require.ErrorIs(t, err, ErrFirstDataTimeout)
	requireClosed(t, body.closed)
}

func Test_Client_Run_times_out_when_first_frame_stalls_fragmented(t *testing.T) {
	body := newChunkBody([]byte{0})
	client := newTestClient(t, staticResponseTransport(body), Config{
		FirstDataTimeout:    100 * time.Millisecond,
		FrameSilenceTimeout: 20 * time.Millisecond,
	})

	_, err := client.Run(context.Background(), validRunInput(), func(cursorproto.ServerEvent) error { return nil })

	require.ErrorIs(t, err, ErrFrameSilenceTimeout)
	requireClosed(t, body.closed)
}

func Test_Client_Run_times_out_after_decoded_frame_silence(t *testing.T) {
	body := newChunkBody(connectFrame(checkpointServerMessage([]byte{0x22, 0x01, 0x78})))
	client := newTestClient(t, staticResponseTransport(body), Config{
		FirstDataTimeout:    100 * time.Millisecond,
		FrameSilenceTimeout: 20 * time.Millisecond,
		ProgressTimeout:     200 * time.Millisecond,
	})

	_, err := client.Run(context.Background(), validRunInput(), func(cursorproto.ServerEvent) error { return nil })

	require.ErrorIs(t, err, ErrFrameSilenceTimeout)
}

func Test_Client_Run_times_out_when_only_liveness_frames_arrive(t *testing.T) {
	tests := []struct {
		name  string
		frame []byte
	}{
		{name: "checkpoint", frame: connectFrame(checkpointServerMessage([]byte{0x22, 0x01, 0x78}))},
		{name: "heartbeat", frame: connectFrame(setBlobServerMessage(7, []byte("id"), []byte("data")))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := newRepeatingBody(tt.frame, 5*time.Millisecond)
			client := newTestClient(t, staticResponseTransport(body), Config{
				FirstDataTimeout:    100 * time.Millisecond,
				FrameSilenceTimeout: 50 * time.Millisecond,
				ProgressTimeout:     25 * time.Millisecond,
			})

			_, err := client.Run(context.Background(), validRunInput(), func(cursorproto.ServerEvent) error { return nil })

			require.ErrorIs(t, err, ErrProgressTimeout)
			requireClosed(t, body.closed)
		})
	}
}

func Test_Client_Run_reports_text_tool_and_trailing_checkpoint_without_duplicate_output(t *testing.T) {
	checkpoint := []byte{0x22, 0x07, 0x70, 0x65, 0x6e, 0x64, 0x69, 0x6e, 0x67}
	body := newChunkBody(
		connectFrame(textServerMessage("ok")),
		connectFrame(mcpExecServerMessage(7, "call_1", "read_file", "path", "probe.txt")),
		connectFrame(turnEndedServerMessage()),
		connectFrame(turnEndedServerMessage()),
		connectFrame(textServerMessage("duplicate")),
		connectFrame(checkpointServerMessage(checkpoint)),
	)
	client := newTestClient(t, staticResponseTransport(body), Config{TrailingDrainTimeout: 100 * time.Millisecond})
	var events []cursorproto.ServerEvent

	result, err := client.Run(context.Background(), validRunInput(), func(event cursorproto.ServerEvent) error {
		events = append(events, event)
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, checkpoint, result.Checkpoint)
	require.True(t, result.OutputExposed)
	require.True(t, result.ToolExposed)
	require.Equal(t, []cursorproto.EventKind{cursorproto.EventText, cursorproto.EventToolCall, cursorproto.EventDone}, eventKinds(events))
}

func Test_Client_Run_turn_end_uses_trailing_drain_instead_of_watchdog(t *testing.T) {
	body := newChunkBody(connectFrame(turnEndedServerMessage()))
	client := newTestClient(t, staticResponseTransport(body), Config{
		FrameSilenceTimeout:  10 * time.Millisecond,
		ProgressTimeout:      10 * time.Millisecond,
		TrailingDrainTimeout: 30 * time.Millisecond,
	})

	_, err := client.Run(context.Background(), validRunInput(), func(cursorproto.ServerEvent) error { return nil })

	require.NoError(t, err)
}

func Test_Client_Run_terminal_completion_does_not_report_expected_request_pipe_close(t *testing.T) {
	// Given
	body := newChunkBody(connectFrame(turnEndedServerMessage()))
	client := newTestClient(t, staticResponseTransport(body), Config{TrailingDrainTimeout: time.Millisecond})

	// When
	_, err := client.Run(context.Background(), validRunInput(), func(cursorproto.ServerEvent) error { return nil })

	// Then
	require.NoError(t, err)
}

func Test_writeRunFrames_reports_initial_writer_failure_before_stop(t *testing.T) {
	// Given
	want := errors.New("request writer failed")
	stop := make(chan struct{})

	// When
	err := writeRunFrames(context.Background(), failingWriter{err: want}, []byte("run"), []byte("heartbeat"), time.Hour, nil, stop)

	// Then
	require.ErrorIs(t, err, want)
	require.ErrorContains(t, err, "write Cursor Run request")
}

func Test_Client_Run_cancellation_closes_reader_and_writer_goroutines(t *testing.T) {
	body := newBlockingBody()
	client := newTestClient(t, staticResponseTransport(body), Config{})
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() {
		_, err := client.Run(ctx, validRunInput(), func(cursorproto.ServerEvent) error { return nil })
		returned <- err
	}()
	cancel()

	select {
	case err := <-returned:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Run leaked after cancellation")
	}
	requireClosed(t, body.closed)
}

func validRunInput() RunInput {
	return RunInput{AccessToken: "token", Model: "default", Prompt: "say ok"}
}

type failingWriter struct {
	err error
}

func (writer failingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

func newTestClient(t *testing.T, transport http.RoundTripper, overrides Config) *Client {
	t.Helper()
	overrides.BaseURL = "https://api2.cursor.sh"
	overrides.HTTPClient = &http.Client{Transport: transport}
	client, err := NewClient(overrides)
	require.NoError(t, err)
	return client
}

func staticResponseTransport(body io.ReadCloser) roundTripperFunc {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	}
}

type blockingBody struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingBody() *blockingBody {
	return &blockingBody{closed: make(chan struct{})}
}

func (body *blockingBody) Read([]byte) (int, error) {
	<-body.closed
	return 0, io.ErrClosedPipe
}

func (body *blockingBody) Close() error {
	body.once.Do(func() { close(body.closed) })
	return nil
}

type chunkBody struct {
	chunks [][]byte
	closed chan struct{}
	once   sync.Once
}

func newChunkBody(chunks ...[]byte) *chunkBody {
	return &chunkBody{chunks: chunks, closed: make(chan struct{})}
}

func (body *chunkBody) Read(buffer []byte) (int, error) {
	if len(body.chunks) > 0 {
		chunk := body.chunks[0]
		body.chunks = body.chunks[1:]
		return copy(buffer, chunk), nil
	}
	<-body.closed
	return 0, io.ErrClosedPipe
}

func (body *chunkBody) Close() error {
	body.once.Do(func() { close(body.closed) })
	return nil
}

type repeatingBody struct {
	frame  []byte
	delay  time.Duration
	closed chan struct{}
	once   sync.Once
}

func newRepeatingBody(frame []byte, delay time.Duration) *repeatingBody {
	return &repeatingBody{frame: frame, delay: delay, closed: make(chan struct{})}
}

func (body *repeatingBody) Read(buffer []byte) (int, error) {
	timer := time.NewTimer(body.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return copy(buffer, body.frame), nil
	case <-body.closed:
		return 0, io.ErrClosedPipe
	}
}

func (body *repeatingBody) Close() error {
	body.once.Do(func() { close(body.closed) })
	return nil
}

func checkpointServerMessage(checkpoint []byte) []byte {
	server := protowire.AppendTag(nil, 3, protowire.BytesType)
	return protowire.AppendBytes(server, checkpoint)
}

func turnEndedServerMessage() []byte {
	return []byte{0x0a, 0x02, 0x72, 0x00}
}

func requireClosed(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("body was not closed")
	}
}
