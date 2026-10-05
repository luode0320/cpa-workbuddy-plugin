package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func handleUsage(raw []byte) ([]byte, error) {
	var record pluginapi.UsageRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	if record.Provider != "" && record.Provider != providerName && record.Provider != "gemini-cli" {
		return okEnvelope(map[string]any{"forwarded": false})
	}
	return okEnvelope(map[string]any{"forwarded": true})
}

func publishUsage(requestedModel, upstreamModel, authID string, started time.Time, detail usage.Detail, failed bool, statusCode int, errBody, reasoningEffort string, ttftNS uint64, accountLabel, sessionKey string) {
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
	}()
}

func normalizeUsageDetail(d usage.Detail) usage.Detail {
	if d.TotalTokens == 0 {
		if total := d.InputTokens + d.OutputTokens + d.ReasoningTokens; total > 0 {
			d.TotalTokens = total
		}
	}
	return d
}

func extractGeminiUsage(body []byte) usage.Detail {
	usageObj := gjson.GetBytes(body, "usageMetadata")
	if !usageObj.Exists() {
		usageObj = gjson.GetBytes(body, "response.usageMetadata")
	}
	if !usageObj.Exists() {
		return usage.Detail{}
	}
	promptTokens := usageObj.Get("promptTokenCount").Int()
	candidatesTokens := usageObj.Get("candidatesTokenCount").Int()
	totalTokens := usageObj.Get("totalTokenCount").Int()
	cachedTokens := usageObj.Get("cachedContentTokenCount").Int()

	if totalTokens == 0 {
		totalTokens = promptTokens + candidatesTokens
	}
	return usage.Detail{
		InputTokens:  promptTokens,
		OutputTokens: candidatesTokens,
		TotalTokens:  totalTokens,
		CachedTokens: cachedTokens,
	}
}

type geminiStreamUsageCollector struct {
	firstByteAt time.Time
	detail      usage.Detail
}

func (c *geminiStreamUsageCollector) feed(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	if c.firstByteAt.IsZero() {
		c.firstByteAt = time.Now()
	}
	d := extractGeminiUsage(chunk)
	if d.TotalTokens > 0 || d.InputTokens > 0 || d.OutputTokens > 0 {
		c.detail = d
	}
}

func (c *geminiStreamUsageCollector) ttftNS(started time.Time) uint64 {
	if c.firstByteAt.IsZero() || started.IsZero() {
		return 0
	}
	d := c.firstByteAt.Sub(started)
	if d <= 0 {
		return 0
	}
	return uint64(d)
}
