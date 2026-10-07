package plugin

import (
	"sync"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorapi"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"
)

type checkpointMetricStatus struct {
	Scope                   string `json:"scope"`
	Hits                    int64  `json:"hits"`
	Misses                  int64  `json:"misses"`
	Invalidations           int64  `json:"invalidations"`
	Fallbacks               int64  `json:"fallbacks"`
	FullReplayBytes         int64  `json:"full_replay_bytes"`
	SuffixBytes             int64  `json:"suffix_bytes"`
	TTFTSamples             int64  `json:"ttft_samples"`
	TTFTTotalMilliseconds   int64  `json:"ttft_total_ms"`
	TTFTAverageMilliseconds *int64 `json:"ttft_average_ms,omitempty"`
}

type checkpointMetrics struct {
	mu     sync.RWMutex
	byAuth map[string]checkpointMetricStatus
}

func newCheckpointMetrics() *checkpointMetrics {
	return &checkpointMetrics{byAuth: make(map[string]checkpointMetricStatus)}
}

func (metrics *checkpointMetrics) recordLookup(authIndex string, hit bool) {
	metrics.update(authIndex, func(status *checkpointMetricStatus) {
		if hit {
			status.Hits++
			return
		}
		status.Misses++
	})
}

func (metrics *checkpointMetrics) recordInvalidation(authIndex string) {
	metrics.update(authIndex, func(status *checkpointMetricStatus) { status.Invalidations++ })
}

func (metrics *checkpointMetrics) recordFallback(authIndex string) {
	metrics.update(authIndex, func(status *checkpointMetricStatus) { status.Fallbacks++ })
}

func (metrics *checkpointMetrics) recordRun(authIndex string, input cursorapi.RunInput, result cursorapi.RunResult) {
	metrics.update(authIndex, func(status *checkpointMetricStatus) {
		switch input.Mode {
		case cursorproto.FullReplay:
			status.FullReplayBytes += runInputBytes(input)
		case cursorproto.CheckpointSuffix:
			status.SuffixBytes += runInputBytes(input)
		}
		if result.TTFT > 0 {
			status.TTFTSamples++
			status.TTFTTotalMilliseconds += result.TTFT.Milliseconds()
		}
	})
}

func (metrics *checkpointMetrics) snapshot(authIndex string) checkpointMetricStatus {
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	return finalizedCheckpointMetrics(metrics.byAuth[authIndex])
}

func (metrics *checkpointMetrics) total() checkpointMetricStatus {
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	var total checkpointMetricStatus
	for _, status := range metrics.byAuth {
		total.Hits += status.Hits
		total.Misses += status.Misses
		total.Invalidations += status.Invalidations
		total.Fallbacks += status.Fallbacks
		total.FullReplayBytes += status.FullReplayBytes
		total.SuffixBytes += status.SuffixBytes
		total.TTFTSamples += status.TTFTSamples
		total.TTFTTotalMilliseconds += status.TTFTTotalMilliseconds
	}
	return finalizedCheckpointMetrics(total)
}

func (metrics *checkpointMetrics) update(authIndex string, update func(*checkpointMetricStatus)) {
	if authIndex == "" {
		return
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	status := metrics.byAuth[authIndex]
	update(&status)
	metrics.byAuth[authIndex] = status
}

func finalizedCheckpointMetrics(status checkpointMetricStatus) checkpointMetricStatus {
	status.Scope = "plugin_process"
	if status.TTFTSamples > 0 {
		average := status.TTFTTotalMilliseconds / status.TTFTSamples
		status.TTFTAverageMilliseconds = &average
	}
	return status
}

func mergeCheckpointMetricStatus(left, right checkpointMetricStatus) checkpointMetricStatus {
	return finalizedCheckpointMetrics(checkpointMetricStatus{
		Hits:                  left.Hits + right.Hits,
		Misses:                left.Misses + right.Misses,
		Invalidations:         left.Invalidations + right.Invalidations,
		Fallbacks:             left.Fallbacks + right.Fallbacks,
		FullReplayBytes:       left.FullReplayBytes + right.FullReplayBytes,
		SuffixBytes:           left.SuffixBytes + right.SuffixBytes,
		TTFTSamples:           left.TTFTSamples + right.TTFTSamples,
		TTFTTotalMilliseconds: left.TTFTTotalMilliseconds + right.TTFTTotalMilliseconds,
	})
}

func runInputBytes(input cursorapi.RunInput) int64 {
	bytes := len(input.System) + len(input.Prompt)
	for _, tool := range input.Tools {
		bytes += len(tool.Name) + len(tool.Description) + len(tool.Parameters)
	}
	for _, image := range input.Images {
		bytes += len(image.Name) + len(image.MIMEType) + len(image.Data)
	}
	for _, attachment := range input.Attachments {
		bytes += len(attachment.Name) + len(attachment.Content)
	}
	return int64(bytes)
}
