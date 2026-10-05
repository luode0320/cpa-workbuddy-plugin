// usage_feed.go implements the shared NDJSON usage feed for the standalone
// token-usage-tracker plugin.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const (
	defaultUsageFeedName = "token-usage-feed.ndjson"
)

var maxUsageFeedBytes int64 = 128 << 20

var (
	usageFeedMu      sync.RWMutex
	usageFeedEnabled = true
	usageFeedPath    = ""
)

func configureUsageFeed(raw []byte) {
	enabled := true
	dataPath := ""
	if len(raw) > 0 {
		var req struct {
			ConfigYAML []byte `json:"config_yaml"`
		}
		if err := json.Unmarshal(raw, &req); err == nil {
			for _, line := range strings.Split(string(req.ConfigYAML), "\n") {
				line = strings.TrimSpace(line)
				switch {
				case strings.HasPrefix(line, "usage_feed_enabled:"):
					v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "usage_feed_enabled:")), "\"'")
					enabled = v == "true" || v == "1" || v == "yes" || v == "on"
				case strings.HasPrefix(line, "usage_feed_path:"):
					v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "usage_feed_path:")), "\"'")
					if v != "" {
						dataPath = v
					}
				}
			}
		}
	}
	if dataPath == "" {
		dataPath = defaultUsageFeedPath()
	}
	usageFeedMu.Lock()
	usageFeedEnabled = enabled
	usageFeedPath = dataPath
	usageFeedMu.Unlock()
}

func defaultUsageFeedPath() string {
	if root, ok := cliProxyRootFromWorkingDir(); ok {
		return filepath.Join(root, "data", defaultUsageFeedName)
	}
	if root, ok := cliProxyRootFromExecutable(); ok {
		return filepath.Join(root, "data", defaultUsageFeedName)
	}
	return filepath.Join("data", defaultUsageFeedName)
}

func cliProxyRootFromWorkingDir() (string, bool) {
	wd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	absolute, err := filepath.Abs(wd)
	if err != nil {
		return "", false
	}
	return cliProxyRootFromDir(absolute)
}

func cliProxyRootFromExecutable() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	return cliProxyRootFromDir(filepath.Dir(exe))
}

func cliProxyRootFromDir(dir string) (string, bool) {
	dir = filepath.Clean(dir)
	for {
		info, err := os.Stat(filepath.Join(dir, "plugins"))
		if err == nil && info.IsDir() && filepath.Dir(dir) != dir {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func recordUsageFeed(alias, model, authUID string, started time.Time, detail usage.Detail, failed bool, statusCode int, reasoningEffort string, ttftNS uint64, accountLabel, sessionKey string) {
	usageFeedMu.RLock()
	enabled := usageFeedEnabled
	path := usageFeedPath
	usageFeedMu.RUnlock()
	if !enabled || path == "" {
		return
	}
	ts := started
	if ts.IsZero() {
		ts = time.Now()
	}
	latencyMs := int64(0)
	if !started.IsZero() {
		if d := time.Since(started).Milliseconds(); d > 0 {
			latencyMs = d
		}
	}
	total := detail.TotalTokens
	if total == 0 {
		total = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}
	record := map[string]any{
		"timestamp":        ts.UTC().Format(time.RFC3339Nano),
		"latency_ms":       latencyMs,
		"source":           strings.TrimSpace(accountLabel),
		"auth_index":       strings.TrimSpace(authUID),
		"provider":         providerName,
		"model":            model,
		"alias":            alias,
		"endpoint":         "POST /v1internal:generateContent",
		"auth_type":        "oauth",
		"executor_type":    providerName,
		"failed":           failed,
		"status_code":      statusCode,
		"session_key":      sessionKey,
		"reasoning_effort": strings.TrimSpace(reasoningEffort),
		"ttft_ns":          ttftNS,
		"tokens": map[string]any{
			"input_tokens":          detail.InputTokens,
			"output_tokens":         detail.OutputTokens,
			"reasoning_tokens":      detail.ReasoningTokens,
			"cached_tokens":         detail.CachedTokens,
			"cache_read_tokens":     detail.CacheReadTokens,
			"cache_creation_tokens": detail.CacheCreationTokens,
			"total_tokens":          total,
		},
	}
	body, err := json.Marshal(record)
	if err != nil {
		usageFeedWarnf("marshal: %v", err)
		return
	}
	body = append(body, '\n')
	appendUsageFeedLine(path, body)
}

func appendUsageFeedLine(path string, line []byte) {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			usageFeedWarnf("mkdir %s: %v", dir, err)
			return
		}
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxUsageFeedBytes {
		if err := os.Truncate(path, 0); err != nil {
			usageFeedWarnf("truncate %s: %v", path, err)
			return
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		usageFeedWarnf("open %s: %v", path, err)
		return
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		usageFeedWarnf("write %s: %v", path, err)
	}
}

func usageFeedWarnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[gemini-provider] usage feed: "+format+"\n", args...)
}
