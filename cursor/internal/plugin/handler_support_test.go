package plugin

import (
	"context"
	"sync"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorapi"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"
)

type fakeCursorClient struct{}

func (fakeCursorClient) Run(_ context.Context, _ cursorapi.RunInput, emit func(cursorproto.ServerEvent) error) (cursorapi.RunResult, error) {
	if err := emit(cursorproto.ServerEvent{Kind: cursorproto.EventText, Text: "cursor-plugin-ok"}); err != nil {
		return cursorapi.RunResult{OutputExposed: true}, err
	}
	return cursorapi.RunResult{OutputExposed: true}, emit(cursorproto.ServerEvent{Kind: cursorproto.EventDone})
}

type toolCursorClient struct {
	input cursorapi.RunInput
	id    string
}

func (client *toolCursorClient) Run(_ context.Context, input cursorapi.RunInput, emit func(cursorproto.ServerEvent) error) (cursorapi.RunResult, error) {
	client.input = input
	id := client.id
	if id == "" {
		id = "call_1"
	}
	err := emit(cursorproto.ServerEvent{
		Kind: cursorproto.EventToolCall, ID: id, Name: "read_file", Arguments: `{"path":"a.txt"}`,
	})
	return cursorapi.RunResult{ToolExposed: true}, err
}

type emptyCursorClient struct{}

func (emptyCursorClient) Run(_ context.Context, _ cursorapi.RunInput, emit func(cursorproto.ServerEvent) error) (cursorapi.RunResult, error) {
	return cursorapi.RunResult{}, emit(cursorproto.ServerEvent{Kind: cursorproto.EventDone})
}

func (emptyCursorClient) DiscoverModels(context.Context, string) ([]string, error) {
	return []string{"auto"}, nil
}

type imageCursorClient struct{}

func (imageCursorClient) Run(_ context.Context, _ cursorapi.RunInput, emit func(cursorproto.ServerEvent) error) (cursorapi.RunResult, error) {
	if err := emit(cursorproto.ServerEvent{Kind: cursorproto.EventImage, MIMEType: "image/png", ImageData: []byte("image")}); err != nil {
		return cursorapi.RunResult{OutputExposed: true}, err
	}
	return cursorapi.RunResult{OutputExposed: true}, emit(cursorproto.ServerEvent{Kind: cursorproto.EventDone})
}

func (imageCursorClient) DiscoverModels(context.Context, string) ([]string, error) {
	return []string{"grok-4.6"}, nil
}

func (*toolCursorClient) DiscoverModels(context.Context, string) ([]string, error) {
	return []string{"auto"}, nil
}

func (fakeCursorClient) DiscoverModels(context.Context, string) ([]string, error) {
	return []string{"auto"}, nil
}

const (
	upstreamToolCallID         = "call-550e8400-e29b-41d4-a716-446655440000-0"
	joinedToolCallID           = upstreamToolCallID + "\nfc_550e8400-e29b-41d4-a716-446655440000_0"
	normalizedJoinedToolCallID = "call_cursor_e34799599f4bb2bb19a2d487e5bd7a34889d0840aedc90589e89"
)

type captureEmitter struct {
	mu         sync.Mutex
	payloads   [][]byte
	closeError error
	done       chan struct{}
}

func (emitter *captureEmitter) Emit(_ context.Context, _ string, payload []byte) error {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	emitter.payloads = append(emitter.payloads, append([]byte(nil), payload...))
	return nil
}

func (emitter *captureEmitter) Close(_ string, err error) error {
	emitter.mu.Lock()
	emitter.closeError = err
	emitter.mu.Unlock()
	close(emitter.done)
	return nil
}
