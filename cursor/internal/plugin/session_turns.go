package plugin

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	maxSessionWaiters  = 8
	maxSessionTurns    = 64
	sessionWaitTimeout = 30 * time.Second
)

var errSessionBusy = errors.New("Cursor session admission is busy; wait for the active turn before retrying")

type sessionTurnKey struct {
	account string
	model   string
	session string
}

type sessionTurnLock struct {
	token chan struct{}
	refs  int
}

type sessionTurnLocks struct {
	mu    sync.Mutex
	locks map[sessionTurnKey]*sessionTurnLock
	total int
}

func newSessionTurnLocks() *sessionTurnLocks {
	return &sessionTurnLocks{locks: make(map[sessionTurnKey]*sessionTurnLock)}
}

func (locks *sessionTurnLocks) acquire(ctx context.Context, key sessionTurnKey) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	locks.mu.Lock()
	turn := locks.locks[key]
	if locks.total >= maxSessionTurns || (turn != nil && turn.refs >= maxSessionWaiters+1) {
		locks.mu.Unlock()
		return nil, errSessionBusy
	}
	if turn == nil {
		turn = &sessionTurnLock{token: make(chan struct{}, 1)}
		locks.locks[key] = turn
	}
	turn.refs++
	locks.total++
	locks.mu.Unlock()
	releaseRef := func() {
		locks.mu.Lock()
		turn.refs--
		locks.total--
		if turn.refs == 0 {
			delete(locks.locks, key)
		}
		locks.mu.Unlock()
	}
	waitContext, cancel := context.WithTimeout(ctx, sessionWaitTimeout)
	defer cancel()
	select {
	case turn.token <- struct{}{}:
		if err := waitContext.Err(); err != nil {
			<-turn.token
			releaseRef()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errSessionBusy
		}
		return func() { <-turn.token; releaseRef() }, nil
	case <-waitContext.Done():
		releaseRef()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errSessionBusy
	}
}
