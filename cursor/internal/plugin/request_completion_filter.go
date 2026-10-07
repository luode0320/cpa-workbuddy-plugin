package plugin

import (
	"encoding/binary"
	"time"
)

const (
	requestCompletionWindow     = 15 * time.Minute
	requestCompletionFilterBits = 1 << 26
	requestCompletionFilterMask = requestCompletionFilterBits - 1
	requestCompletionWords      = requestCompletionFilterBits / 64
	requestCompletionHashes     = 8
)

// requestCompletionFilter keeps two independent 8 MiB Bloom windows. With one
// million completions per window, each filter's theoretical false-positive
// probability is below 3e-8. A completion remains detectable for at least one
// window without capacity eviction.
type requestCompletionFilter struct {
	current        []uint64
	previous       []uint64
	currentStarted time.Time
}

func newRequestCompletionFilter() *requestCompletionFilter {
	return &requestCompletionFilter{
		current:  make([]uint64, requestCompletionWords),
		previous: make([]uint64, requestCompletionWords),
	}
}

func (filter *requestCompletionFilter) contains(key requestKey, now time.Time) bool {
	filter.rotate(now)
	positions := requestCompletionPositions(key)
	return requestCompletionFilterContains(filter.current, positions) ||
		requestCompletionFilterContains(filter.previous, positions)
}

func requestCompletionFilterContains(words []uint64, positions [requestCompletionHashes]uint64) bool {
	for _, position := range positions {
		if words[position/64]&(uint64(1)<<(position%64)) == 0 {
			return false
		}
	}
	return true
}

func requestCompletionPositions(key requestKey) [requestCompletionHashes]uint64 {
	base := binary.LittleEndian.Uint64(key[:8])
	step := binary.LittleEndian.Uint64(key[8:16]) | 1
	var positions [requestCompletionHashes]uint64
	for index := 0; index < requestCompletionHashes; index++ {
		positions[index] = (base + uint64(index)*step) & requestCompletionFilterMask
	}
	return positions
}

func (filter *requestCompletionFilter) add(key requestKey, now time.Time) {
	filter.rotate(now)
	for _, position := range requestCompletionPositions(key) {
		filter.current[position/64] |= uint64(1) << (position % 64)
	}
}

func (filter *requestCompletionFilter) rotate(now time.Time) {
	if filter.currentStarted.IsZero() {
		filter.currentStarted = now
		return
	}
	if !now.After(filter.currentStarted) {
		return
	}
	elapsed := now.Sub(filter.currentStarted)
	steps := elapsed / requestCompletionWindow
	if steps == 0 {
		return
	}
	if steps == 1 {
		filter.previous, filter.current = filter.current, filter.previous
		clear(filter.current[:])
	} else {
		clear(filter.current[:])
		clear(filter.previous[:])
	}
	filter.currentStarted = filter.currentStarted.Add(steps * requestCompletionWindow)
}
