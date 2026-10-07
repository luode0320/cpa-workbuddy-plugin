package cursorapi

import (
	"context"
	"io"
	"time"
)

type bodyRead struct {
	data []byte
	err  error
}

func startBodyReader(body io.Reader) <-chan bodyRead {
	result := make(chan bodyRead, 1)
	go func() {
		defer close(result)
		for {
			buffer := make([]byte, 32<<10)
			count, err := body.Read(buffer)
			result <- bodyRead{data: append([]byte(nil), buffer[:count]...), err: err}
			if err != nil {
				return
			}
		}
	}()
	return result
}

func stopBodyReader(body io.Closer, reads <-chan bodyRead) {
	_ = body.Close()
	for range reads {
	}
}

func nextBodyRead(ctx context.Context, reads <-chan bodyRead, drain *time.Timer) (bodyRead, bool, error) {
	var drainChannel <-chan time.Time
	if drain != nil {
		drainChannel = drain.C
	}
	select {
	case read := <-reads:
		return read, false, nil
	case <-drainChannel:
		return bodyRead{}, true, nil
	case <-ctx.Done():
		return bodyRead{}, false, context.Cause(ctx)
	}
}
