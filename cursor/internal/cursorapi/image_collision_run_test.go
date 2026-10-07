package cursorapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
)

func Test_Client_Run_returns_image_and_preserves_foreign_file_when_write_collides(t *testing.T) {
	// Given
	var body *requestContextImageBody
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if _, err := readConnectPayloadRaw(request.Body); err != nil {
			return nil, err
		}
		body.request = request.Body
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})
	client := newTestClient(t, transport, Config{OverallTimeout: 2 * time.Second})
	client.projectFolder = t.TempDir()
	data := []byte("\xff\xd8\xffimage")
	target := filepath.Join(client.projectFolder, "assets", "image-2.jpg")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))
	require.NoError(t, os.WriteFile(target, []byte("foreign"), 0o600))
	body = &requestContextImageBody{
		contextRequest: connectFrame(requestContextExecMessage(7, "context")),
		query:          connectFrame(generateImageQueryMessage(42, "Draw a blue fox", "tool-image-1")),
		writeRequest:   connectFrame(imageWriteExecMessage(8, "write", target, data)),
		completion:     append(connectFrame(generateImageCompletionMessage(data)), connectFrame(turnEndedServerMessage())...),
		contextReply:   make(chan []byte, 1),
		approval:       make(chan []byte, 1),
		writeReply:     make(chan []byte, 1),
		closed:         make(chan struct{}),
	}
	var images [][]byte

	// When
	result, err := client.Run(context.Background(), RunInput{
		AccessToken: "token", Model: "grok-4.6", Prompt: "draw a fox",
	}, func(event cursorproto.ServerEvent) error {
		if event.Kind == cursorproto.EventImage {
			images = append(images, event.ImageData)
		}
		return nil
	})

	// Then
	require.NoError(t, err)
	require.True(t, result.OutputExposed)
	require.Equal(t, [][]byte{data}, images)
	reply := <-body.writeReply
	exec := requireWireBytes(t, reply, 2)
	success := requireWireBytes(t, requireWireBytes(t, exec, 3), 1)
	actual := string(requireWireBytes(t, success, 1))
	require.NotEqual(t, target, actual)
	requireImageWriteSuccess(t, reply, 8, "write", actual, len(data))
	_, err = os.Stat(actual)
	require.ErrorIs(t, err, os.ErrNotExist)
	foreign, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "foreign", string(foreign))
}
