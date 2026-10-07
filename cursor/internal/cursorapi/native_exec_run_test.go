package cursorapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func Test_Client_Run_replies_to_native_grep_read_and_shell_stream_without_stalling(t *testing.T) {
	body := &nativeReadOnlyBody{
		grepRequest:  connectFrame(nativeReadOnlyExecMessage(41, "exec-grep", 5, "/downstream/workspace")),
		readRequest:  connectFrame(nativeReadOnlyExecMessage(42, "exec-read", 7, "/downstream/workspace/config.json")),
		shellRequest: connectFrame(nativeShellExecMessage(43, "exec-shell", 14, "pwd", "/downstream/workspace")),
		completion:   append(connectFrame(textServerMessage("continued")), connectFrame(turnEndedServerMessage())...),
		grepReply:    make(chan []byte, 1),
		readReply:    make(chan []byte, 1),
		shellReplies: make(chan [][]byte, 1),
		closed:       make(chan struct{}),
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
	input := validRunInput()
	for _, name := range []string{"grep", "read", "bash"} {
		input.Tools = append(input.Tools, cursorproto.ToolDefinition{Name: name, Parameters: []byte(`{"type":"object"}`)})
	}

	result, err := client.Run(context.Background(), input, func(event cursorproto.ServerEvent) error {
		events = append(events, event)
		return nil
	})

	require.NoError(t, err)
	require.True(t, result.OutputExposed)
	require.True(t, result.InteractionResponded)
	require.Equal(t, []cursorproto.EventKind{
		cursorproto.EventIgnored, cursorproto.EventIgnored, cursorproto.EventIgnored, cursorproto.EventText, cursorproto.EventDone,
	}, eventKinds(events))
	requireNativeReadOnlyPolicyReply(t, <-body.grepReply, 41, "exec-grep", 5, "")
	requireNativeReadOnlyPolicyReply(t, <-body.readReply, 42, "exec-read", 7, "/downstream/workspace/config.json")
	requireNativeShellStreamReplies(t, <-body.shellReplies, 43, "exec-shell")
}

type nativeReadOnlyBody struct {
	request      io.Reader
	grepRequest  []byte
	readRequest  []byte
	shellRequest []byte
	completion   []byte
	grepReply    chan []byte
	readReply    chan []byte
	shellReplies chan [][]byte
	stage        int
	buffer       bytes.Reader
	closed       chan struct{}
	closeOnce    sync.Once
}

func (body *nativeReadOnlyBody) Read(target []byte) (int, error) {
	if body.buffer.Len() > 0 {
		return body.buffer.Read(target)
	}
	switch body.stage {
	case 0:
		body.stage = 1
		body.buffer.Reset(body.grepRequest)
	case 1:
		reply, err := body.awaitReply()
		if err != nil {
			return 0, err
		}
		body.grepReply <- reply
		body.stage = 2
		body.buffer.Reset(body.readRequest)
	case 2:
		reply, err := body.awaitReply()
		if err != nil {
			return 0, err
		}
		body.readReply <- reply
		body.stage = 3
		body.buffer.Reset(body.shellRequest)
	case 3:
		replies := make([][]byte, 5)
		for index := range replies {
			reply, err := body.awaitReply()
			if err != nil {
				return 0, err
			}
			replies[index] = reply
		}
		body.shellReplies <- replies
		body.stage = 4
		body.buffer.Reset(body.completion)
	default:
		return 0, io.EOF
	}
	return body.buffer.Read(target)
}

func (body *nativeReadOnlyBody) Close() error {
	body.closeOnce.Do(func() { close(body.closed) })
	return nil
}

func (body *nativeReadOnlyBody) awaitReply() ([]byte, error) {
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

func nativeReadOnlyExecMessage(id uint64, execID string, operation protowire.Number, path string) []byte {
	args := appendWireString(nil, 1, path)
	if operation == 5 {
		args = appendWireString(nil, 1, "needle")
		args = appendWireString(args, 2, path)
	}
	execMessage := protowire.AppendTag(nil, 1, protowire.VarintType)
	execMessage = protowire.AppendVarint(execMessage, id)
	execMessage = appendWireString(execMessage, 15, execID)
	execMessage = appendWireBytes(execMessage, operation, args)
	return appendWireBytes(nil, 2, execMessage)
}

func nativeShellExecMessage(id uint64, execID string, operation protowire.Number, command, workingDirectory string) []byte {
	args := appendWireString(nil, 1, command)
	args = appendWireString(args, 2, workingDirectory)
	execMessage := protowire.AppendTag(nil, 1, protowire.VarintType)
	execMessage = protowire.AppendVarint(execMessage, id)
	execMessage = appendWireString(execMessage, 15, execID)
	execMessage = appendWireBytes(execMessage, operation, args)
	return appendWireBytes(nil, 2, execMessage)
}

func requireNativeReadOnlyPolicyReply(t *testing.T, raw []byte, id uint64, execID string, resultField protowire.Number, path string) {
	t.Helper()
	execReply := requireWireBytes(t, raw, 2)
	require.Equal(t, id, requireWireVarint(t, execReply, 1))
	require.Equal(t, execID, string(requireWireBytes(t, execReply, 15)))
	result := requireWireBytes(t, execReply, resultField)
	failure := requireWireBytes(t, result, 2)
	if path == "" {
		require.Contains(t, string(requireWireBytes(t, failure, 1)), "`grep`")
		return
	}
	require.Equal(t, path, string(requireWireBytes(t, failure, 1)))
	require.Contains(t, string(requireWireBytes(t, failure, 2)), "`read`")
}

func requireNativeShellStreamReplies(t *testing.T, replies [][]byte, id uint64, execID string) {
	t.Helper()
	require.Len(t, replies, 5)
	for index, eventField := range []protowire.Number{4, 2, 3} {
		execReply := requireWireBytes(t, replies[index], 2)
		require.Equal(t, id, requireWireVarint(t, execReply, 1))
		require.Equal(t, execID, string(requireWireBytes(t, execReply, 15)))
		stream := requireWireBytes(t, execReply, 14)
		requireWireBytes(t, stream, eventField)
	}
	execReply := requireWireBytes(t, replies[3], 2)
	require.Equal(t, id, requireWireVarint(t, execReply, 1))
	require.Equal(t, execID, string(requireWireBytes(t, execReply, 15)))
	result := requireWireBytes(t, execReply, 2)
	failure := requireWireBytes(t, result, 2)
	require.Contains(t, string(requireWireBytes(t, failure, 6)), "`bash`")
	control := requireWireBytes(t, replies[4], 5)
	streamClose := requireWireBytes(t, control, 1)
	require.Equal(t, id, requireWireVarint(t, streamClose, 1))
}
