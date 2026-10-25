// usage_feed.go 实现面向 token-usage-tracker 独立插件的统一 NDJSON 用量流上报。
//
// 架构契约（与 workbuddy/cursor 保持严格一致）：
//   - 插件执行器请求不触发宿主 UsagePlugin 广播，通过直接追加共享文件
//     <CLIProxyAPI root>/data/token-usage-feed.ndjson 实现跨插件统计；
//   - 每行一条完整 NDJSON，包含 tokens 嵌套结构、latency_ms、ttft_ns，以及
//     将账号展示名（如 ZCode 旅行者2435）写入 source 字段以在面板「来源」列显示。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// defaultUsageFeedName 是 CLIProxyAPI data 目录下的共享用量日志文件名。
	defaultUsageFeedName = "token-usage-feed.ndjson"
)

// maxUsageFeedBytes 限制日志文件最大大小（128MB），超限时原子截断防爆盘。
var maxUsageFeedBytes int64 = 128 << 20

var (
	usageFeedMu      sync.RWMutex
	usageFeedEnabled = true
	usageFeedPath    = ""
)

// setUsageFeedPath 覆盖用量日志路径（仅供单测与配置重载使用）。
func setUsageFeedPath(p string) {
	usageFeedMu.Lock()
	defer usageFeedMu.Unlock()
	usageFeedPath = p
}

// usageTokensDetail 包含细分的 token 统计。
type usageTokensDetail struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

// configureUsageFeed 解析 config_yaml 中的 usage_feed_* 配置字段。
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

// defaultUsageFeedPath 自动发现 CLIProxyAPI 数据目录中的 token-usage-feed.ndjson。
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

// accountLabelForAuth 提取友好的账号展示名（如 ZCode 旅行者2435），供来源列展示。
func accountLabelForAuth(authID string) string {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return "ZCode"
	}
	accounts, err := listAllAuthFiles()
	if err == nil {
		for _, acc := range accounts {
			if acc.AuthID == authID || authFileNameFor(acc.APIKey) == authID+".json" {
				if label := strings.TrimSpace(acc.Label); label != "" {
					return label
				}
				if uname := strings.TrimSpace(acc.Username); uname != "" {
					return "ZCode " + uname
				}
				if acc.UserID != "" {
					return "ZCode " + acc.UserID
				}
			}
		}
	}
	return authID
}

// publishUsage 异步记录单次请求的用量信息到共享 NDJSON 数据流。
//
// [参数] authID: 账号唯一标识; accountLabel: 账号展示名称; requestedModel: 客户端请求模型;
// upstreamModel: 上游真实模型; started: 请求发起时刻; detail: Token 统计明细;
// durationMS: 总耗时(毫秒); statusCode: HTTP 状态码; failed: 是否请求失败;
// errMsg: 错误信息; ttftNS: 首包时间(纳秒)。
func publishUsage(
	authID, accountLabel string,
	requestedModel, upstreamModel string,
	started time.Time,
	detail usageTokensDetail,
	durationMS int64,
	statusCode int,
	failed bool,
	errMsg string,
	ttftNS uint64,
) {
	usageFeedMu.RLock()
	enabled := usageFeedEnabled
	path := usageFeedPath
	usageFeedMu.RUnlock()
	if !enabled {
		return
	}
	if path == "" {
		path = defaultUsageFeedPath()
		usageFeedMu.Lock()
		usageFeedPath = path
		usageFeedMu.Unlock()
	}

	ts := started
	if ts.IsZero() {
		ts = time.Now()
	}
	latencyMs := durationMS
	if latencyMs <= 0 && !started.IsZero() {
		if d := time.Since(started).Milliseconds(); d > 0 {
			latencyMs = d
		}
	}

	alias := strings.TrimSpace(requestedModel)
	model := strings.TrimSpace(upstreamModel)
	if model == "" {
		model = alias
	}
	if alias == "" {
		alias = model
	}

	source := strings.TrimSpace(accountLabel)
	if source == "" {
		source = accountLabelForAuth(authID)
	}

	total := detail.TotalTokens
	if total == 0 {
		total = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}

	record := map[string]any{
		"timestamp":        ts.UTC().Format(time.RFC3339Nano),
		"latency_ms":       latencyMs,
		"source":           source,
		"auth_index":       strings.TrimSpace(authID),
		"provider":         providerName,
		"model":            model,
		"alias":            alias,
		"endpoint":         "POST /v1/messages",
		"auth_type":        "oauth",
		"executor_type":    providerName,
		"failed":           failed,
		"status_code":      statusCode,
		"session_key":      "",
		"reasoning_effort": "",
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
	if failed && errMsg != "" {
		// fail.body 会落进共享 NDJSON 并展示在用量看板，上游报文可能回显
		// 鉴权头，必须先脱敏再落盘。
		record["fail"] = map[string]any{
			"status_code": statusCode,
			"body":        redactSecrets(errMsg),
		}
	}

	// 异步写入避免阻塞推理主线程
	go func(targetPath string, rec map[string]any) {
		line, errMarshal := json.Marshal(rec)
		if errMarshal != nil {
			return
		}
		appendUsageFeedLine(targetPath, append(line, '\n'))
	}(path, record)
}

// appendUsageFeedLine 原子追加单行 NDJSON 到目标文件。
func appendUsageFeedLine(path string, line []byte) {
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

func usageFeedWarnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[zcode-provider] usage feed: "+format+"\n", args...)
}
