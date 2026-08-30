// usage.go implements the UsagePlugin capability: the host calls handleUsage
// after every request with a canonical record, and we forward it to the
// optional CPAMP usage-import endpoint. The legacy publishUsage path is kept
// for executor call sites and for hosts without UsagePlugin wiring.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var (
	usageReportMu  sync.RWMutex
	usageReportURL string
	usageReportKey string
)

// setUsageReport updates the optional CPAMP usage-import endpoint/key from
// config (see config.go applyConfigLines).
func setUsageReport(url, key string) {
	usageReportMu.Lock()
	usageReportURL = strings.TrimSpace(url)
	usageReportKey = strings.TrimSpace(key)
	usageReportMu.Unlock()
}

// handleUsage is the UsagePlugin entry point. The host calls this after every
// request with the canonical usage record.
func handleUsage(raw []byte) ([]byte, error) {
	var record pluginapi.UsageRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	// Only forward traework's own records.
	if record.Provider != "" && record.Provider != providerName {
		return okEnvelope(map[string]any{"forwarded": false})
	}
	detail := usage.Detail{
		InputTokens:         record.Detail.InputTokens,
		OutputTokens:        record.Detail.OutputTokens,
		ReasoningTokens:     record.Detail.ReasoningTokens,
		CachedTokens:        record.Detail.CachedTokens,
		CacheReadTokens:     record.Detail.CacheReadTokens,
		CacheCreationTokens: record.Detail.CacheCreationTokens,
		TotalTokens:         record.Detail.TotalTokens,
	}
	started := record.RequestedAt
	if started.IsZero() {
		started = time.Now().Add(-record.Latency)
	}
	forwardUsageToCPAMP(
		record.Alias,
		record.Model,
		record.AuthID,
		started,
		detail,
		record.Failed,
		record.Failure.StatusCode,
		record.Failure.Body,
	)
	return okEnvelope(map[string]any{"forwarded": true})
}

// publishUsage is the executor-side compatibility path. It is fire-and-forget
// so the executor hot path never blocks on the CPAMP round-trip or a slow
// feed filesystem.
func publishUsage(requestedModel, upstreamModel, authID string, started time.Time, detail usage.Detail, failed bool, statusCode int, errBody string) {
	// Cumulative success/failure counter (plugin-owned, persisted; counter.go).
	// authID is the account UID at every call site (authUID in executor.go), so
	// keying on it matches the scheduler / failover / counter-flush layers.
	// Increment happens synchronously (memory only) so the counter is never
	// lost to the fire-and-forget goroutine below.
	recordOutcome(authID, !failed)
	model := strings.TrimSpace(upstreamModel)
	if model == "" {
		model = strings.TrimSpace(requestedModel)
	}
	alias := strings.TrimSpace(requestedModel)
	if alias == "" {
		alias = model
	}
	go func() {
		// Shared usage feed (token-usage-tracker plugin): append the same
		// detail as one NDJSON line to <root>/data/token-usage-feed.ndjson.
		// The standalone token-usage-tracker plugin tails this file into its
		// own bbolt database and serves the dashboard — this is the only
		// cross-plugin data path for plugin executors (host UsagePlugin
		// broadcast never fires for plugin executors). Runs inside the
		// goroutine so a slow filesystem never stalls the executor.
		recordUsageFeed(alias, model, authID, started, normalizeUsageDetail(detail), failed, statusCode)
		forwardUsageToCPAMP(alias, model, authID, started, normalizeUsageDetail(detail), failed, statusCode, errBody)
	}()
}

// forwardUsageToCPAMP POSTs one NDJSON line to the CPAMP usage/import
// endpoint. Silent on misconfig / network errors — never blocks chat.
func forwardUsageToCPAMP(alias, model, authID string, started time.Time, detail usage.Detail, failed bool, statusCode int, errBody string) {
	usageReportMu.RLock()
	url := strings.TrimSpace(usageReportURL)
	key := strings.TrimSpace(usageReportKey)
	usageReportMu.RUnlock()
	if url == "" || key == "" {
		return
	}
	ts := started
	if ts.IsZero() {
		ts = time.Now()
	}
	latencyMs := int64(0)
	if !started.IsZero() {
		latencyMs = time.Since(started).Milliseconds()
		if latencyMs < 0 {
			latencyMs = 0
		}
	}
	total := detail.TotalTokens
	if total == 0 {
		total = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}
	failBody := ""
	failCode := 200
	if failed {
		failCode = statusCode
		if failCode <= 0 {
			failCode = 502
		}
		failBody = truncate(redactSecrets(errBody), 512)
	}
	payload := map[string]any{
		"timestamp":     ts.UTC().Format(time.RFC3339Nano),
		"latency_ms":    latencyMs,
		"source":        "traework",
		"auth_index":    strings.TrimSpace(authID),
		"provider":      providerName,
		"model":         model,
		"alias":         alias,
		"endpoint":      "POST /v1/chat/completions",
		"auth_type":     "oauth",
		"executor_type": "traework",
		"generate":      true,
		"failed":        failed,
		"tokens": map[string]any{
			"input_tokens":          detail.InputTokens,
			"output_tokens":         detail.OutputTokens,
			"reasoning_tokens":      detail.ReasoningTokens,
			"cached_tokens":         detail.CachedTokens,
			"cache_read_tokens":     detail.CacheReadTokens,
			"cache_creation_tokens": detail.CacheCreationTokens,
			"total_tokens":          total,
		},
		"fail": map[string]any{
			"status_code": failCode,
			"body":        failBody,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	body = append(body, '\n')
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/x-ndjson")
	_, _ = hostHTTPDo(req)
}

func normalizeUsageDetail(d usage.Detail) usage.Detail {
	if d.TotalTokens == 0 {
		if total := d.InputTokens + d.OutputTokens + d.ReasoningTokens; total > 0 {
			d.TotalTokens = total
		}
	}
	return d
}
