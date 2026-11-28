// usage.go 实现面向 token-usage-tracker 的统一 NDJSON 用量流上报。
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultUsageFeedName = "token-usage-feed.ndjson"

var maxUsageFeedBytes int64 = 128 << 20

var (
	usageFeedMu      sync.RWMutex
	usageFeedEnabled = true
	usageFeedPath    = ""
)

// setUsageFeedPath 覆盖用量日志路径。
// [参数] p: 目标路径。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, 用量上报路径注入点。
func setUsageFeedPath(p string) {
	usageFeedMu.Lock()
	defer usageFeedMu.Unlock()
	usageFeedPath = p
}

// usageTokensDetail 是细分 token 统计。
type usageTokensDetail struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

// defaultUsageFeedPath 发现 CLIProxyAPI data 目录下的共享用量文件。
// [参数] 无。
// [返回] 文件路径。
// 最近修改时间：2026-11-28 新增, 用量上报路径发现。
func defaultUsageFeedPath() string {
	wd, err := os.Getwd()
	if err == nil {
		abs, errAbs := filepath.Abs(wd)
		if errAbs == nil {
			dir := filepath.Clean(abs)
			for {
				info, statErr := os.Stat(filepath.Join(dir, "plugins"))
				if statErr == nil && info.IsDir() && filepath.Dir(dir) != dir {
					return filepath.Join(dir, "data", defaultUsageFeedName)
				}
				parent := filepath.Dir(dir)
				if parent == dir {
					break
				}
				dir = parent
			}
		}
	}
	return filepath.Join("data", defaultUsageFeedName)
}

// publishUsage 记录单次请求的用量信息到共享 NDJSON 数据流。
// [参数] authID: 账号标识; accountLabel: 账号展示名; requestedModel: 请求模型;
// started: 起始时刻; detail: token 统计; durationMS: 耗时; statusCode: 状态码;
// failed: 是否失败; errMsg: 错误信息。
// 最近修改时间：2026-11-28 新增, 用量上报入口。
func publishUsage(authID, accountLabel, requestedModel string, started time.Time, detail usageTokensDetail, durationMS int64, statusCode int, failed bool, errMsg string) {
	usageFeedMu.RLock()
	enabled := usageFeedEnabled
	path := usageFeedPath
	usageFeedMu.RUnlock()
	if !enabled {
		return
	}
	if path == "" {
		path = defaultUsageFeedPath()
	}

	ts := started
	if ts.IsZero() {
		ts = time.Now()
	}
	total := detail.TotalTokens
	if total == 0 {
		total = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}
	source := strings.TrimSpace(accountLabel)
	if source == "" {
		source = strings.TrimSpace(authID)
	}

	record := map[string]any{
		"timestamp":     ts.UTC().Format(time.RFC3339Nano),
		"latency_ms":    durationMS,
		"source":        source,
		"auth_index":    strings.TrimSpace(authID),
		"provider":      providerName,
		"model":         strings.TrimSpace(requestedModel),
		"alias":         strings.TrimSpace(requestedModel),
		"endpoint":      "POST /v1/chat/completions",
		"auth_type":     "refresh_token",
		"executor_type": providerName,
		"failed":        failed,
		"status_code":   statusCode,
		"session_key":   "",
		"tokens": map[string]any{
			"input_tokens":     detail.InputTokens,
			"output_tokens":    detail.OutputTokens,
			"reasoning_tokens": detail.ReasoningTokens,
			"total_tokens":     total,
		},
	}
	if failed && errMsg != "" {
		record["fail"] = map[string]any{"status_code": statusCode, "body": redactSecrets(errMsg)}
	}

	go appendUsageFeedLine(path, record)
}

// appendUsageFeedLine 原子追加单行 NDJSON。
// [参数] path: 目标路径；record: 用量记录。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, NDJSON 追加写入。
func appendUsageFeedLine(path string, record map[string]any) {
	line, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		return
	}
	line = append(line, '\n')
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return
		}
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxUsageFeedBytes {
		_ = os.Truncate(path, 0)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(line)
}
