package cursorapi

import (
	"context"
	"sync"
	"time"
)

type runWatchdogs struct {
	mu            sync.Mutex
	cancel        context.CancelCauseFunc
	firstData     *time.Timer
	frameSilence  *time.Timer
	progress      *time.Timer
	frameDelay    time.Duration
	progressDelay time.Duration
	firstSeen     bool
	stopped       bool
}

func newRunWatchdogs(cancel context.CancelCauseFunc, client *Client) *runWatchdogs {
	watchdogs := &runWatchdogs{
		cancel: cancel, frameDelay: client.frameSilence, progressDelay: client.progress,
	}
	watchdogs.firstData = time.AfterFunc(client.firstData, func() { cancel(ErrFirstDataTimeout) })
	watchdogs.progress = time.AfterFunc(client.progress, func() { cancel(ErrProgressTimeout) })
	return watchdogs
}

func (watchdogs *runWatchdogs) sawData() {
	watchdogs.mu.Lock()
	defer watchdogs.mu.Unlock()
	if watchdogs.stopped || watchdogs.firstSeen {
		return
	}
	watchdogs.firstSeen = true
	watchdogs.firstData.Stop()
	watchdogs.frameSilence = time.AfterFunc(watchdogs.frameDelay, func() { watchdogs.cancel(ErrFrameSilenceTimeout) })
}

func (watchdogs *runWatchdogs) sawFrame() {
	watchdogs.mu.Lock()
	defer watchdogs.mu.Unlock()
	if watchdogs.stopped || watchdogs.frameSilence == nil {
		return
	}
	watchdogs.frameSilence.Reset(watchdogs.frameDelay)
}

func (watchdogs *runWatchdogs) sawProgress() {
	watchdogs.mu.Lock()
	defer watchdogs.mu.Unlock()
	if !watchdogs.stopped {
		watchdogs.progress.Reset(watchdogs.progressDelay)
	}
}

func (watchdogs *runWatchdogs) stop() {
	watchdogs.mu.Lock()
	defer watchdogs.mu.Unlock()
	watchdogs.stopped = true
	watchdogs.firstData.Stop()
	watchdogs.progress.Stop()
	if watchdogs.frameSilence != nil {
		watchdogs.frameSilence.Stop()
	}
}
