package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func Test_RecordCursorUsageFeed_writes_ndjson_line(t *testing.T) {
	tempDir := t.TempDir()
	feedPath := filepath.Join(tempDir, "data", "token-usage-feed.ndjson")

	usageFeedMu.Lock()
	oldEnabled := usageFeedEnabled
	oldPath := usageFeedPath
	usageFeedEnabled = true
	usageFeedPath = feedPath
	usageFeedMu.Unlock()
	defer func() {
		usageFeedMu.Lock()
		usageFeedEnabled = oldEnabled
		usageFeedPath = oldPath
		usageFeedMu.Unlock()
	}()

	recordCursorUsageFeed("cursor/claude-3.5-sonnet", "user@example.test", 120, 80, 1500, false, 200, "session-123")

	// 轮询等待异步协程写入完成
	var content string
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(feedPath)
		if err != nil {
			return false
		}
		content = string(data)
		return strings.TrimSpace(content) != ""
	}, 2*time.Second, 50*time.Millisecond)

	var record usageFeedRecord
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(content)), &record))
	require.Equal(t, providerName, record.Provider)
	require.Equal(t, "user@example.test", record.Account)
	require.Equal(t, "cursor/claude-3.5-sonnet", record.Model)
	require.Equal(t, 120, record.PromptTokens)
	require.Equal(t, 80, record.CompletionTokens)
	require.Equal(t, 200, record.TotalTokens)
	require.Equal(t, int64(1500), record.DurationMS)
	require.False(t, record.Failed)
	require.Equal(t, 200, record.StatusCode)
	require.Equal(t, "session-123", record.SessionKey)
}
