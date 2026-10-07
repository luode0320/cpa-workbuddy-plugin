package plugin

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"
)

var errOutputLimit = errors.New("Cursor output budget reached")

type outputBudget struct {
	remaining int64
	emit      func(cursorproto.ServerEvent) error
	cancel    context.CancelFunc
}

func (budget *outputBudget) emitEvent(event cursorproto.ServerEvent) error {
	var size int64
	switch event.Kind {
	case cursorproto.EventText, cursorproto.EventThinking:
		size = int64(len(event.Text))
	case cursorproto.EventToolCall:
		size = int64(len(event.ID)) + int64(len(event.Name)) + int64(len(event.Arguments))
	case cursorproto.EventIgnored, cursorproto.EventTokens, cursorproto.EventImage, cursorproto.EventCheckpoint, cursorproto.EventDone:
		return budget.emit(event)
	default:
		return budget.emit(event)
	}
	if size <= budget.remaining {
		budget.remaining -= size
		if err := budget.emit(event); err != nil {
			return err
		}
		if budget.remaining > 0 {
			return nil
		}
	}
	if event.Kind == cursorproto.EventText && budget.remaining > 0 {
		prefix := event.Text[:budget.remaining]
		for len(prefix) > 0 && !utf8.ValidString(prefix) {
			prefix = prefix[:len(prefix)-1]
		}
		if prefix != "" {
			event.Text = prefix
			if err := budget.emit(event); err != nil {
				return err
			}
		}
	}
	budget.cancel()
	return errOutputLimit
}
