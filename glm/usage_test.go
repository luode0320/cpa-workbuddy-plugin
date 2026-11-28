package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPublishUsageFeed 验证用量记录真实写入 NDJSON 文件且字段完整。
func TestPublishUsageFeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.ndjson")
	setUsageFeedPath(path)
	t.Cleanup(func() { setUsageFeedPath("") })

	publishUsage("auth-1", "测试账号", "glm/glm-5.1", time.Now(), usageTokensDetail{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}, 123, 200, false, "")

	var line []byte
	for i := 0; i < 50; i++ {
		raw, err := os.ReadFile(path)
		if err == nil && len(raw) > 0 {
			line = raw
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(line) == 0 {
		t.Fatal("usage feed line not written")
	}
	scanner := bufio.NewScanner(bytes.NewReader(line))
	scanner.Scan()
	var got map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
		t.Fatalf("invalid NDJSON: %v", err)
	}
	if got["provider"] != providerName || got["auth_type"] != "refresh_token" {
		t.Fatalf("wrong metadata: %+v", got)
	}
	tokens, ok := got["tokens"].(map[string]any)
	if !ok || tokens["total_tokens"] != float64(30) {
		t.Fatalf("wrong tokens: %+v", got["tokens"])
	}
}
