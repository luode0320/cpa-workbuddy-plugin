// usage.go implements the UsagePlugin capability: the host calls handleUsage
// after every request with a canonical record, and we forward to CPAMP.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func handleUsage(raw []byte) ([]byte, error) {
	var record pluginapi.UsageRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	if record.Provider != "" && record.Provider != providerName && record.Provider != "workbuddy-ai" {
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

func publishUsage(requestedModel, upstreamModel, authID string, started time.Time, detail usage.Detail, failed bool, statusCode int, errBody, reasoningEffort string, ttftNS uint64, accountLabel, sessionKey string) {
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
		recordUsageFeed(alias, model, authID, started, normalizeUsageDetail(detail), failed, statusCode, reasoningEffort, ttftNS, accountLabel, sessionKey)
		forwardUsageToCPAMP(alias, model, authID, started, normalizeUsageDetail(detail), failed, statusCode, errBody)
	}()
}

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
		if statusCode == 200 {
			failCode = 403
		} else {
			failCode = statusCode
		}
		if failCode <= 0 {
			failCode = 502
		}
		failBody = truncate(redactSecrets(errBody), 512)
	}
	payload := map[string]any{
		"timestamp":     ts.UTC().Format(time.RFC3339Nano),
		"latency_ms":    latencyMs,
		"source":        "workbuddy-ai",
		"auth_index":    strings.TrimSpace(authID),
		"provider":      providerName,
		"model":         model,
		"alias":         alias,
		"endpoint":      "POST /v1/chat/completions",
		"auth_type":     "oauth",
		"executor_type": "workbuddy-ai",
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
	resp, err := hostHTTPDo(req)
	if err != nil {
		return
	}
	_ = resp.Body
}

func normalizeUsageDetail(d usage.Detail) usage.Detail {
	if d.TotalTokens == 0 {
		if total := d.InputTokens + d.OutputTokens + d.ReasoningTokens; total > 0 {
			d.TotalTokens = total
		}
	}
	return d
}

func usageDetailFromMap(m map[string]any) usage.Detail {
	if len(m) == 0 {
		return usage.Detail{}
	}
	num := func(keys ...string) int64 {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				switch n := v.(type) {
				case float64:
					return int64(n)
				case int64:
					return n
				case json.Number:
					i, _ := n.Int64()
					return i
				}
			}
		}
		return 0
	}
	d := usage.Detail{
		InputTokens:     num("prompt_tokens", "input_tokens"),
		OutputTokens:    num("completion_tokens", "output_tokens"),
		TotalTokens:     num("total_tokens"),
		CachedTokens:    num("cached_tokens"),
		CacheReadTokens: num("cache_read_input_tokens"),
	}
	if ct, ok := m["completion_tokens_details"].(map[string]any); ok {
		if v, ok2 := ct["reasoning_tokens"].(float64); ok2 {
			d.ReasoningTokens = int64(v)
		}
	}
	return d
}

func usageDetailFromCompletion(payload []byte) usage.Detail {
	var obj map[string]any
	if json.Unmarshal(payload, &obj) != nil {
		return usage.Detail{}
	}
	m, _ := obj["usage"].(map[string]any)
	return usageDetailFromMap(m)
}

type sseUsageCollector struct {
	last        map[string]any
	firstByteAt time.Time
	// attemptStarted 是本次账号尝试的发出时刻：TTFT 必须以它为基准，见 ttftNS。
	attemptStarted time.Time
}

func (c *sseUsageCollector) feed(rawJSON string) {
	if c.firstByteAt.IsZero() {
		c.firstByteAt = time.Now()
	}
	var chunk map[string]any
	if json.Unmarshal([]byte(rawJSON), &chunk) != nil {
		return
	}
	if u, ok := chunk["usage"].(map[string]any); ok && len(u) > 0 {
		c.last = u
	}
}

func (c *sseUsageCollector) detail() usage.Detail {
	return usageDetailFromMap(c.last)
}

// markAttemptStart 记录本次账号尝试的发出时刻，作为 TTFT 的计时基准。
//
// 必须每轮重试都调用一次：请求级 started 跨 attempt 复用，用它算会把上一个
// 账号的失败耗时算进当前账号（账号 A 失败 10s → 换到 B、B 首帧 2s 被记成 12s），
// 慢速冷却判据吃这个样本会误禁健康号。
// [参数] t：本次尝试的发出时刻。
// [返回] 无。
// 最近修改时间：2026-10-26；改动原因：新增 per-attempt 计时基准，阻断慢速判定样本污染。
func (c *sseUsageCollector) markAttemptStart(t time.Time) {
	c.attemptStarted = t
}

func (c *sseUsageCollector) ttftNS(started time.Time) uint64 {
	// 优先用本次尝试的发出时刻：started 是请求级时间戳，跨重试复用会把前序
	// 账号的失败耗时算进来。未注入时回退到 started，保持历史调用点行为不变。
	base := c.attemptStarted
	if base.IsZero() {
		base = started
	}
	if c.firstByteAt.IsZero() || base.IsZero() {
		return 0
	}
	d := c.firstByteAt.Sub(base)
	if d <= 0 {
		return 0
	}
	return uint64(d)
}
