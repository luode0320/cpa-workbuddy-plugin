package cursorapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func Test_Client_Run_streams_text_until_turn_end(t *testing.T) {
	// Given
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/agent.v1.AgentService/Run":
			header := make([]byte, 5)
			_, err := io.ReadFull(request.Body, header)
			require.NoError(t, err)
			payload := make([]byte, binary.BigEndian.Uint32(header[1:]))
			_, err = io.ReadFull(request.Body, payload)
			require.NoError(t, err)
			require.NotEmpty(t, payload)
			writer.Header().Set("content-type", "application/connect+proto")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(connectFrame(setBlobServerMessage(7, []byte("blob-id"), []byte("blob-data"))))
			writer.(http.Flusher).Flush()
			kvReply := readConnectPayload(t, request.Body)
			require.Equal(t, byte(0x1a), kvReply[0])
			_, _ = writer.Write(connectFrame(textServerMessage("cursor-plugin-ok")))
			_, _ = writer.Write(connectFrame([]byte{0x0a, 0x02, 0x72, 0x00}))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, ClientVersion: "test", HTTPClient: server.Client()})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var events []cursorproto.ServerEvent

	// When
	_, err = client.Run(ctx, RunInput{AccessToken: "token", Model: "auto", Prompt: "say ok"}, func(event cursorproto.ServerEvent) error {
		events = append(events, event)
		return nil
	})

	// Then
	require.NoError(t, err)
	require.Equal(t, []cursorproto.EventKind{cursorproto.EventIgnored, cursorproto.EventText, cursorproto.EventDone}, eventKinds(events))
	require.Equal(t, "kv_server_message", events[0].Type)
	require.Equal(t, "cursor-plugin-ok", events[1].Text)
}

func Test_Client_Run_keeps_HTTP2_request_stream_open(t *testing.T) {
	// Given
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if _, ok := request.Body.(*io.PipeReader); !ok {
			return responseWithBody(http.StatusBadRequest, nil), nil
		}
		header := make([]byte, 5)
		_, err := io.ReadFull(request.Body, header)
		require.NoError(t, err)
		payload := make([]byte, binary.BigEndian.Uint32(header[1:]))
		_, err = io.ReadFull(request.Body, payload)
		require.NoError(t, err)
		body := append(connectFrame(textServerMessage("cursor-plugin-ok")), connectFrame([]byte{0x0a, 0x02, 0x72, 0x00})...)
		return responseWithBody(http.StatusOK, body), nil
	})
	client, err := NewClient(Config{
		BaseURL:    "https://api2.cursor.sh",
		HTTPClient: &http.Client{Transport: transport},
	})
	require.NoError(t, err)

	// When
	_, err = client.Run(context.Background(), RunInput{AccessToken: "token", Model: "default", Prompt: "say ok"}, func(cursorproto.ServerEvent) error {
		return nil
	})

	// Then
	require.NoError(t, err)
}

func Test_Client_Run_returns_after_exec_mcp_tool_call_without_turn_end(t *testing.T) {
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		_ = readConnectPayload(t, request.Body)
		return responseWithBody(http.StatusOK, connectFrame(mcpExecServerMessage(7, "call_1", "read_file", "path", "probe.txt"))), nil
	})
	client, err := NewClient(Config{
		BaseURL:    "https://api2.cursor.sh",
		HTTPClient: &http.Client{Transport: transport},
	})
	require.NoError(t, err)
	var events []cursorproto.ServerEvent

	result, err := client.Run(context.Background(), RunInput{AccessToken: "token", Model: "default", Prompt: "read probe"}, func(event cursorproto.ServerEvent) error {
		events = append(events, event)
		return nil
	})

	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, cursorproto.EventToolCall, events[0].Kind)
	require.Equal(t, "call_1", events[0].ID)
	require.Equal(t, "read_file", events[0].Name)
	require.JSONEq(t, `{"path":"probe.txt"}`, events[0].Arguments)
	require.False(t, result.OutputExposed)
	require.True(t, result.ToolExposed)
	// Windows 计时器精度约 15.6ms，mock 传输可能在同一 tick 内完成，故不断言非零。
	require.GreaterOrEqual(t, result.TTFT, time.Duration(0))
}

func Test_Client_Run_sends_heartbeat_before_response_headers(t *testing.T) {
	// Given
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "trailers", request.Header.Get("te"))
		first := readConnectPayload(t, request.Body)
		require.NotEmpty(t, first)
		heartbeat := readConnectPayload(t, request.Body)
		require.Equal(t, []byte{0x3a, 0x00}, heartbeat)
		writer.Header().Set("content-type", "application/connect+proto")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(connectFrame(textServerMessage("cursor-plugin-ok")))
		_, _ = writer.Write(connectFrame([]byte{0x0a, 0x02, 0x72, 0x00}))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client, err := NewClient(Config{
		BaseURL:           server.URL,
		ClientVersion:     "test",
		HTTPClient:        server.Client(),
		HeartbeatInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// When
	_, err = client.Run(ctx, RunInput{AccessToken: "token", Model: "default", Prompt: "say ok"}, func(cursorproto.ServerEvent) error {
		return nil
	})

	// Then
	require.NoError(t, err)
}

func Test_Client_Run_times_out_before_response_headers(t *testing.T) {
	// Given
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		_ = readConnectPayload(t, request.Body)
		<-request.Context().Done()
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client, err := NewClient(Config{
		BaseURL:           server.URL,
		HTTPClient:        server.Client(),
		FirstFrameTimeout: 50 * time.Millisecond,
	})
	require.NoError(t, err)

	// When
	_, err = client.Run(context.Background(), RunInput{AccessToken: "token", Model: "default", Prompt: "say ok"}, func(cursorproto.ServerEvent) error {
		return nil
	})

	// Then
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrFirstFrameTimeout))
}

func Test_Client_DiscoverModels_decodes_model_ids(t *testing.T) {
	// Given
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/agent.v1.AgentService/GetUsableModels", request.URL.Path)
		writer.Header().Set("content-type", "application/proto")
		_, _ = writer.Write(modelsResponse("auto", "gpt-5.6"))
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, ClientVersion: "test", HTTPClient: server.Client()})
	require.NoError(t, err)

	// When
	models, err := client.DiscoverModels(context.Background(), "token")

	// Then
	require.NoError(t, err)
	require.Equal(t, []string{"auto", "gpt-5.6"}, models)
}

func Test_NewClient_default_transport_attempts_HTTP2(t *testing.T) {
	// Given
	config := Config{BaseURL: "https://api2.cursor.sh"}

	// When
	client, err := NewClient(config)

	// Then
	require.NoError(t, err)
	transport, ok := client.httpClient.Transport.(*http.Transport)
	require.True(t, ok)
	require.True(t, transport.ForceAttemptHTTP2)
	require.Nil(t, transport.TLSNextProto)
	require.Equal(t, "cli-2026.07.16-899851b", client.clientVersion)
	require.Equal(t, 90*time.Second, client.progress)
}

func Test_NewClient_captures_Cursor_workspace_context(t *testing.T) {
	workingDirectory, err := os.Getwd()
	require.NoError(t, err)
	homeDirectory, err := os.UserHomeDir()
	require.NoError(t, err)
	projectName := regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(workingDirectory, "-")
	projectName = strings.Trim(projectName, "-")

	client, err := NewClient(Config{BaseURL: "https://api2.cursor.sh"})

	require.NoError(t, err)
	require.Equal(t, workingDirectory, client.workspacePath)
	require.Equal(t, filepath.Join(homeDirectory, ".cursor", "projects", projectName), client.projectFolder)
}

func Test_randomUUID_returns_standard_version_four_identifier(t *testing.T) {
	// When
	id := randomUUID()

	// Then
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`), id)
}

func connectFrame(payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

func textServerMessage(text string) []byte {
	textDelta := protowire.AppendTag(nil, 1, protowire.BytesType)
	textDelta = protowire.AppendString(textDelta, text)
	interaction := protowire.AppendTag(nil, 1, protowire.BytesType)
	interaction = protowire.AppendBytes(interaction, textDelta)
	server := protowire.AppendTag(nil, 1, protowire.BytesType)
	return protowire.AppendBytes(server, interaction)
}

func modelsResponse(ids ...string) []byte {
	response := make([]byte, 0, len(ids)*16)
	for _, id := range ids {
		model := protowire.AppendTag(nil, 1, protowire.BytesType)
		model = protowire.AppendString(model, id)
		response = protowire.AppendTag(response, 1, protowire.BytesType)
		response = protowire.AppendBytes(response, model)
	}
	return response
}

func setBlobServerMessage(id uint64, blobID, blobData []byte) []byte {
	args := protowire.AppendTag(nil, 1, protowire.BytesType)
	args = protowire.AppendBytes(args, blobID)
	args = protowire.AppendTag(args, 2, protowire.BytesType)
	args = protowire.AppendBytes(args, blobData)
	kv := protowire.AppendTag(nil, 1, protowire.VarintType)
	kv = protowire.AppendVarint(kv, id)
	kv = protowire.AppendTag(kv, 3, protowire.BytesType)
	kv = protowire.AppendBytes(kv, args)
	server := protowire.AppendTag(nil, 4, protowire.BytesType)
	return protowire.AppendBytes(server, kv)
}

func mcpExecServerMessage(id uint64, callID, name, argumentName, argumentValue string) []byte {
	encodedValue := protowire.AppendTag(nil, 3, protowire.BytesType)
	encodedValue = protowire.AppendString(encodedValue, argumentValue)
	entry := protowire.AppendTag(nil, 1, protowire.BytesType)
	entry = protowire.AppendString(entry, argumentName)
	entry = protowire.AppendTag(entry, 2, protowire.BytesType)
	entry = protowire.AppendBytes(entry, encodedValue)
	args := protowire.AppendTag(nil, 2, protowire.BytesType)
	args = protowire.AppendBytes(args, entry)
	args = protowire.AppendTag(args, 3, protowire.BytesType)
	args = protowire.AppendString(args, callID)
	args = protowire.AppendTag(args, 4, protowire.BytesType)
	args = protowire.AppendString(args, "opencodex-responses")
	args = protowire.AppendTag(args, 5, protowire.BytesType)
	args = protowire.AppendString(args, name)
	execMessage := protowire.AppendTag(nil, 1, protowire.VarintType)
	execMessage = protowire.AppendVarint(execMessage, id)
	execMessage = protowire.AppendTag(execMessage, 11, protowire.BytesType)
	execMessage = protowire.AppendBytes(execMessage, args)
	server := protowire.AppendTag(nil, 2, protowire.BytesType)
	return protowire.AppendBytes(server, execMessage)
}

func eventKinds(events []cursorproto.ServerEvent) []cursorproto.EventKind {
	kinds := make([]cursorproto.EventKind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func readConnectPayload(t *testing.T, reader io.Reader) []byte {
	t.Helper()
	header := make([]byte, 5)
	_, err := io.ReadFull(reader, header)
	require.NoError(t, err)
	payload := make([]byte, binary.BigEndian.Uint32(header[1:]))
	_, err = io.ReadFull(reader, payload)
	require.NoError(t, err)
	return payload
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (transport roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func responseWithBody(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}
