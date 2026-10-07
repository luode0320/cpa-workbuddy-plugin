package cursorapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func Test_Client_Run_approves_and_returns_generated_image(t *testing.T) {
	body := &imageInteractionBody{
		query:      connectFrame(generateImageQueryMessage(42, "Draw a blue fox", "tool-image-1")),
		completion: append(connectFrame(generateImageCompletionMessage([]byte("\x89PNG\r\n\x1a\nimage"))), connectFrame(turnEndedServerMessage())...),
		approval:   make(chan []byte, 1),
	}
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if _, err := readConnectPayloadRaw(request.Body); err != nil {
			return nil, err
		}
		body.request = request.Body
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})
	client := newTestClient(t, transport, Config{OverallTimeout: 2 * time.Second})
	var events []cursorproto.ServerEvent

	result, err := client.Run(context.Background(), RunInput{
		AccessToken: "token", Model: "grok-4.6", Prompt: "draw a fox",
	}, func(event cursorproto.ServerEvent) error {
		events = append(events, event)
		return nil
	})

	require.NoError(t, err)
	require.True(t, result.OutputExposed)
	require.False(t, result.ToolExposed)
	require.Equal(t, []cursorproto.EventKind{cursorproto.EventIgnored, cursorproto.EventImage, cursorproto.EventDone}, eventKinds(events))
	require.Equal(t, "image/png", events[1].MIMEType)
	require.Equal(t, []byte("\x89PNG\r\n\x1a\nimage"), events[1].ImageData)
	approval := <-body.approval
	requireGenerateImageApproval(t, approval, 42, "Draw a blue fox")
}

func Test_Client_Run_replies_to_request_context_and_image_write_before_generated_image(t *testing.T) {
	var body *requestContextImageBody
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if _, err := readConnectPayloadRaw(request.Body); err != nil {
			return nil, err
		}
		body.request = request.Body
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})
	client := newTestClient(t, transport, Config{OverallTimeout: 2 * time.Second})
	client.projectFolder = filepath.Join(t.TempDir(), "project")
	image := []byte("\x89PNG\r\n\x1a\nimage")
	target := filepath.Join(client.projectFolder, "assets", "generated.png")
	body = &requestContextImageBody{
		contextRequest: connectFrame(requestContextExecMessage(7, "exec-image-context")),
		query:          connectFrame(generateImageQueryMessage(42, "Draw a blue fox", "tool-image-1")),
		writeRequest:   connectFrame(imageWriteExecMessage(8, "exec-image-write", target, image)),
		completion:     append(connectFrame(generateImageCompletionMessage(image)), connectFrame(turnEndedServerMessage())...),
		contextReply:   make(chan []byte, 1),
		approval:       make(chan []byte, 1),
		writeReply:     make(chan []byte, 1),
		closed:         make(chan struct{}),
	}
	var events []cursorproto.ServerEvent

	result, err := client.Run(context.Background(), RunInput{
		AccessToken: "token", Model: "grok-4.6", Prompt: "draw a fox",
	}, func(event cursorproto.ServerEvent) error {
		events = append(events, event)
		return nil
	})

	require.NoError(t, err)
	require.True(t, result.OutputExposed)
	require.Equal(t, []cursorproto.EventKind{
		cursorproto.EventIgnored, cursorproto.EventIgnored, cursorproto.EventIgnored, cursorproto.EventImage, cursorproto.EventDone,
	}, eventKinds(events))
	requireRequestContextReply(t, <-body.contextReply, 7, "exec-image-context")
	requireGenerateImageApproval(t, <-body.approval, 42, "Draw a blue fox")
	requireImageWriteSuccess(t, <-body.writeReply, 8, "exec-image-write", target, len(image))
	_, statErr := os.Stat(target)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func Test_Client_Run_progress_timeout_reports_safe_event_counts(t *testing.T) {
	body := newChunkBody(connectFrame(generateImageQueryMessage(42, "Draw a blue fox", "tool-image-1")))
	client := newTestClient(t, staticResponseTransport(body), Config{
		FirstDataTimeout:    100 * time.Millisecond,
		FrameSilenceTimeout: 100 * time.Millisecond,
		ProgressTimeout:     20 * time.Millisecond,
	})

	_, err := client.Run(context.Background(), RunInput{
		AccessToken: "token", Model: "grok-4.6", Prompt: "draw a fox",
	}, func(cursorproto.ServerEvent) error { return nil })

	require.ErrorIs(t, err, ErrProgressTimeout)
	require.ErrorContains(t, err, "interaction_query.generate_image_request_query=1")
}

type imageInteractionBody struct {
	request    io.Reader
	query      []byte
	completion []byte
	approval   chan []byte
	stage      int
	buffer     bytes.Reader
}

type requestContextImageBody struct {
	request        io.Reader
	contextRequest []byte
	query          []byte
	writeRequest   []byte
	completion     []byte
	contextReply   chan []byte
	approval       chan []byte
	writeReply     chan []byte
	stage          int
	buffer         bytes.Reader
	closed         chan struct{}
	closeOnce      sync.Once
}

func (body *requestContextImageBody) Read(target []byte) (int, error) {
	if body.buffer.Len() > 0 {
		return body.buffer.Read(target)
	}
	switch body.stage {
	case 0:
		body.stage = 1
		body.buffer.Reset(body.contextRequest)
	case 1:
		reply, err := body.readReply()
		if err != nil {
			return 0, err
		}
		body.contextReply <- reply
		body.stage = 2
		body.buffer.Reset(body.query)
	case 2:
		reply, err := body.readReply()
		if err != nil {
			return 0, err
		}
		body.approval <- reply
		body.stage = 3
		body.buffer.Reset(body.writeRequest)
	case 3:
		reply, err := body.readReply()
		if err != nil {
			return 0, err
		}
		body.writeReply <- reply
		body.stage = 4
		body.buffer.Reset(body.completion)
	default:
		return 0, io.EOF
	}
	return body.buffer.Read(target)
}

func (body *requestContextImageBody) Close() error {
	body.closeOnce.Do(func() { close(body.closed) })
	return nil
}

func (body *requestContextImageBody) readReply() ([]byte, error) {
	type result struct {
		payload []byte
		err     error
	}
	resultChannel := make(chan result, 1)
	go func() {
		payload, err := readConnectPayloadRaw(body.request)
		resultChannel <- result{payload: payload, err: err}
	}()
	select {
	case reply := <-resultChannel:
		return reply.payload, reply.err
	case <-body.closed:
		return nil, io.ErrClosedPipe
	}
}

func (body *imageInteractionBody) Read(target []byte) (int, error) {
	if body.buffer.Len() > 0 {
		return body.buffer.Read(target)
	}
	switch body.stage {
	case 0:
		body.stage = 1
		body.buffer.Reset(body.query)
		return body.buffer.Read(target)
	case 1:
		approval, err := readConnectPayloadRaw(body.request)
		if err != nil {
			return 0, err
		}
		body.approval <- approval
		body.stage = 2
		body.buffer.Reset(body.completion)
		return body.buffer.Read(target)
	default:
		return 0, io.EOF
	}
}

func (body *imageInteractionBody) Close() error {
	return nil
}

func readConnectPayloadRaw(reader io.Reader) ([]byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, fmt.Errorf("read Connect header: %w", err)
	}
	if header[0] != 0 {
		return nil, fmt.Errorf("unexpected Connect flags %d", header[0])
	}
	payload := make([]byte, binary.BigEndian.Uint32(header[1:]))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, fmt.Errorf("read Connect payload: %w", err)
	}
	return payload, nil
}

func generateImageQueryMessage(id uint64, description, toolCallID string) []byte {
	args := appendWireString(nil, 1, description)
	query := appendWireBytes(nil, 1, args)
	query = appendWireString(query, 2, toolCallID)
	interaction := protowire.AppendTag(nil, 1, protowire.VarintType)
	interaction = protowire.AppendVarint(interaction, id)
	interaction = appendWireBytes(interaction, 12, query)
	return appendWireBytes(nil, 7, interaction)
}

func requestContextExecMessage(id uint64, execID string) []byte {
	execMessage := protowire.AppendTag(nil, 1, protowire.VarintType)
	execMessage = protowire.AppendVarint(execMessage, id)
	execMessage = appendWireString(execMessage, 15, execID)
	execMessage = appendWireBytes(execMessage, 10, nil)
	return appendWireBytes(nil, 2, execMessage)
}

func imageWriteExecMessage(id uint64, execID, path string, image []byte) []byte {
	args := appendWireString(nil, 1, path)
	args = appendWireString(args, 3, "tool-image-1")
	args = appendWireBytes(args, 5, image)
	execMessage := protowire.AppendTag(nil, 1, protowire.VarintType)
	execMessage = protowire.AppendVarint(execMessage, id)
	execMessage = appendWireString(execMessage, 15, execID)
	execMessage = appendWireBytes(execMessage, 3, args)
	return appendWireBytes(nil, 2, execMessage)
}

func generateImageCompletionMessage(image []byte) []byte {
	success := appendWireString(nil, 1, "generated.png")
	success = appendWireString(success, 2, base64.StdEncoding.EncodeToString(image))
	result := appendWireBytes(nil, 1, success)
	imageCall := appendWireBytes(nil, 2, result)
	toolCall := appendWireBytes(nil, 28, imageCall)
	completed := appendWireString(nil, 1, "tool-image-1")
	completed = appendWireBytes(completed, 2, toolCall)
	interaction := appendWireBytes(nil, 3, completed)
	return appendWireBytes(nil, 1, interaction)
}

func requireGenerateImageApproval(t *testing.T, raw []byte, id uint64, description string) {
	t.Helper()
	interaction := requireWireBytes(t, raw, 6)
	require.Equal(t, id, requireWireVarint(t, interaction, 1))
	response := requireWireBytes(t, interaction, 12)
	approved := requireWireBytes(t, response, 1)
	require.Equal(t, description, string(requireWireBytes(t, approved, 1)))
}

func requireRequestContextReply(t *testing.T, raw []byte, id uint64, execID string) {
	t.Helper()
	execReply := requireWireBytes(t, raw, 2)
	require.Equal(t, id, requireWireVarint(t, execReply, 1))
	require.Equal(t, execID, string(requireWireBytes(t, execReply, 15)))
	result := requireWireBytes(t, execReply, 10)
	success := requireWireBytes(t, result, 1)
	require.NotEmpty(t, requireWireBytes(t, success, 1))
}

func requireImageWriteSuccess(t *testing.T, raw []byte, id uint64, execID, path string, size int) {
	t.Helper()
	execReply := requireWireBytes(t, raw, 2)
	require.Equal(t, id, requireWireVarint(t, execReply, 1))
	require.Equal(t, execID, string(requireWireBytes(t, execReply, 15)))
	result := requireWireBytes(t, execReply, 3)
	success := requireWireBytes(t, result, 1)
	require.Equal(t, path, string(requireWireBytes(t, success, 1)))
	require.EqualValues(t, size, requireWireVarint(t, success, 3))
}

func requireWireBytes(t *testing.T, raw []byte, fieldNumber protowire.Number) []byte {
	t.Helper()
	for len(raw) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(raw)
		require.Positive(t, tagLength)
		raw = raw[tagLength:]
		if number == fieldNumber {
			require.Equal(t, protowire.BytesType, wireType)
			value, valueLength := protowire.ConsumeBytes(raw)
			require.Positive(t, valueLength)
			return value
		}
		valueLength := protowire.ConsumeFieldValue(number, wireType, raw)
		require.Positive(t, valueLength)
		raw = raw[valueLength:]
	}
	t.Fatalf("wire field %d is missing", fieldNumber)
	return nil
}

func requireWireVarint(t *testing.T, raw []byte, fieldNumber protowire.Number) uint64 {
	t.Helper()
	for len(raw) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(raw)
		require.Positive(t, tagLength)
		raw = raw[tagLength:]
		if number == fieldNumber {
			require.Equal(t, protowire.VarintType, wireType)
			value, valueLength := protowire.ConsumeVarint(raw)
			require.Positive(t, valueLength)
			return value
		}
		valueLength := protowire.ConsumeFieldValue(number, wireType, raw)
		require.Positive(t, valueLength)
		raw = raw[valueLength:]
	}
	t.Fatalf("wire field %d is missing", fieldNumber)
	return 0
}

func appendWireBytes(raw []byte, fieldNumber protowire.Number, value []byte) []byte {
	raw = protowire.AppendTag(raw, fieldNumber, protowire.BytesType)
	return protowire.AppendBytes(raw, value)
}

func appendWireString(raw []byte, fieldNumber protowire.Number, value string) []byte {
	raw = protowire.AppendTag(raw, fieldNumber, protowire.BytesType)
	return protowire.AppendString(raw, value)
}
