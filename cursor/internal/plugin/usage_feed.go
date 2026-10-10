package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultUsageFeedName = "token-usage-feed.ndjson"
)

var (
	usageFeedMu      sync.RWMutex
	usageFeedEnabled = true
	usageFeedPath    = ""
)

// usageFeedRecord 定义写入 token-usage-feed.ndjson 的通用事件行结构。
// 格式严格与 workbuddy 及 token-usage-tracker 消费契约一致。
type usageFeedRecord struct {
	Timestamp        string  `json:"timestamp"`
	Provider         string  `json:"provider"`
	Account          string  `json:"account"`
	Model            string  `json:"model"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Cost             float64 `json:"cost,omitempty"`
	TTFTMS           int64   `json:"ttft_ms,omitempty"`
	DurationMS       int64   `json:"duration_ms,omitempty"`
	Failed           bool    `json:"failed"`
	StatusCode       int     `json:"status_code"`
	SessionKey       string  `json:"session_key,omitempty"`
}

// defaultUsageFeedPath 自动探测 CLIProxyAPI 数据目录中的 token-usage-feed.ndjson 路径。
// [返回] 探测到的 feed 物理文件路径。
// 最近修改时间 2026-10-11（为 Cursor 插件实现统一用量文件发现）
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

// recordCursorUsageFeed 向统一数据文件异步追加单条请求用量记录。
// [参数] model: 请求模型；account: 账号标识；promptTokens: 输入 Token；completionTokens: 输出 Token；
// durationMS: 总耗时；failed: 是否失败；statusCode: HTTP 状态码；sessionKey: 会话标识。
// 最近修改时间 2026-10-11（打通 Cursor 插件与 TokenTracker 统一看板用量数据通道）
func recordCursorUsageFeed(model, account string, promptTokens, completionTokens int, durationMS int64, failed bool, statusCode int, sessionKey string) {
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

	record := usageFeedRecord{
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
		Provider:         providerName,
		Account:          strings.TrimSpace(account),
		Model:            strings.TrimSpace(model),
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		DurationMS:       durationMS,
		Failed:           failed,
		StatusCode:       statusCode,
		SessionKey:       strings.TrimSpace(sessionKey),
	}

	// 异步写入避免磁盘 I/O 阻塞主推理调用
	go func(targetPath string, rec usageFeedRecord) {
		line, err := json.Marshal(rec)
		if err != nil {
			return
		}
		line = append(line, '\n')
		if dir := filepath.Dir(targetPath); dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0755)
		}
		f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.Write(line)
	}(path, record)
}
