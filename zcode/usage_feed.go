package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const defaultUsageFeedName = "token-usage-feed.ndjson"

var (
	usageFeedMu      sync.Mutex
	usageFeedPath    string
	usageFeedEnabled = true
)

type usageRecord struct {
	Timestamp     string `json:"timestamp"`
	Provider      string `json:"provider"`
	AuthID        string `json:"auth_id"`
	Model         string `json:"model"`
	UpstreamModel string `json:"upstream_model"`
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
	TotalTokens   int64  `json:"total_tokens"`
	DurationMS    int64  `json:"duration_ms"`
	StatusCode    int    `json:"status_code"`
	Failed        bool   `json:"failed"`
	Error         string `json:"error,omitempty"`
}

func defaultUsageFeedPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".antigravity_cockpit", "data", defaultUsageFeedName)
}

func getUsageFeedPath() string {
	if usageFeedPath != "" {
		return usageFeedPath
	}
	return defaultUsageFeedPath()
}

func setUsageFeedPath(p string) {
	usageFeedMu.Lock()
	defer usageFeedMu.Unlock()
	usageFeedPath = p
}

// publishUsage 记录用量并追加到 NDJSON。
func publishUsage(authID, model, upstreamModel string, inTokens, outTokens int64, durationMS int64, statusCode int, failed bool, errMsg string) {
	if !usageFeedEnabled {
		return
	}
	usageFeedMu.Lock()
	defer usageFeedMu.Unlock()

	rec := usageRecord{
		Timestamp:     time.Now().Format(time.RFC3339),
		Provider:      zcodeProviderID,
		AuthID:        authID,
		Model:         model,
		UpstreamModel: upstreamModel,
		InputTokens:   inTokens,
		OutputTokens:  outTokens,
		TotalTokens:   inTokens + outTokens,
		DurationMS:    durationMS,
		StatusCode:    statusCode,
		Failed:        failed,
		Error:         errMsg,
	}

	data, err := json.Marshal(rec)
	if err != nil {
		return
	}

	feedPath := getUsageFeedPath()
	_ = os.MkdirAll(filepath.Dir(feedPath), 0755)

	f, err := os.OpenFile(feedPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	_, _ = f.Write(append(data, '\n'))
}
