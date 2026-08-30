package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestConfigureUsageFeedParsesConfigYAML(t *testing.T) {
	usageFeedMu.Lock()
	usageFeedEnabled = true
	usageFeedPath = ""
	usageFeedMu.Unlock()
	defer func() {
		usageFeedMu.Lock()
		usageFeedEnabled = true
		usageFeedPath = ""
		usageFeedMu.Unlock()
	}()

	dir := t.TempDir()
	// The host serializes config_yaml as base64 (ConfigYAML []byte over the
	// RPC wire); pass a []byte value so json.Marshal mimics the real transport.
	raw, _ := json.Marshal(map[string]any{
		"config_yaml": []byte("usage_feed_enabled: true\nusage_feed_path: \"" + filepath.Join(dir, "feed.ndjson") + "\"\n"),
	})
	configureUsageFeed(raw)
	usageFeedMu.RLock()
	enabled := usageFeedEnabled
	path := usageFeedPath
	usageFeedMu.RUnlock()
	if !enabled {
		t.Fatal("feed should be enabled")
	}
	if path != filepath.Join(dir, "feed.ndjson") {
		t.Fatalf("feed path = %q", path)
	}

	// Disabled case.
	raw, _ = json.Marshal(map[string]any{
		"config_yaml": []byte("usage_feed_enabled: false\n"),
	})
	configureUsageFeed(raw)
	usageFeedMu.RLock()
	enabled = usageFeedEnabled
	usageFeedMu.RUnlock()
	if enabled {
		t.Fatal("feed should be disabled")
	}
}

func TestRecordUsageFeedAppendsNDJSON(t *testing.T) {
	dir := t.TempDir()
	feedPath := filepath.Join(dir, "feed.ndjson")
	usageFeedMu.Lock()
	usageFeedEnabled = true
	usageFeedPath = feedPath
	usageFeedMu.Unlock()
	defer func() {
		usageFeedMu.Lock()
		usageFeedEnabled = true
		usageFeedPath = ""
		usageFeedMu.Unlock()
	}()

	started := time.Now().Add(-3 * time.Second)
	detail := usage.Detail{
		InputTokens:     100,
		OutputTokens:    200,
		ReasoningTokens: 50,
		TotalTokens:     350,
	}
	recordUsageFeed("alias-m", "deepseek-v4", "u-1", started, detail, false, 200)
	recordUsageFeed("alias-m", "deepseek-v4", "u-2", started.Add(time.Second), detail, true, 502)

	raw, err := os.ReadFile(feedPath)
	if err != nil {
		t.Fatalf("read feed: %v", err)
	}
	lines := splitLines(string(raw))
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	var rec struct {
		Timestamp       string `json:"timestamp"`
		Source          string `json:"source"`
		AuthIndex       string `json:"auth_index"`
		Provider        string `json:"provider"`
		ExecutorType    string `json:"executor_type"`
		AuthType        string `json:"auth_type"`
		Model           string `json:"model"`
		Failed          bool   `json:"failed"`
		StatusCode      int    `json:"status_code"`
		SessionKey      string `json:"session_key"`
		ReasoningEffort string `json:"reasoning_effort"`
		TTFTNS          int64  `json:"ttft_ns"`
		Tokens          struct {
			TotalTokens int64 `json:"total_tokens"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("decode line 0: %v", err)
	}
	// Source mirrors the oauth account UID (traework has no nickname label);
	// provider must be the plugin id so the tracker dashboard's provider
	// dimension separates traework records from workbuddy ones.
	if rec.Source != "u-1" || rec.Provider != providerName || rec.ExecutorType != "traework" {
		t.Fatalf("record = %+v", rec)
	}
	if rec.AuthIndex != "u-1" {
		t.Fatalf("auth_index = %q", rec.AuthIndex)
	}
	if rec.AuthType != "oauth" {
		t.Fatalf("auth_type = %q", rec.AuthType)
	}
	if rec.Tokens.TotalTokens != 350 {
		t.Fatalf("total_tokens = %d", rec.Tokens.TotalTokens)
	}
	if rec.Failed {
		t.Fatal("line 0 should be success")
	}
	if rec.StatusCode != 200 {
		t.Fatalf("line 0 status_code = %d", rec.StatusCode)
	}
	// Fields traework cannot observe are still written with zero values so
	// the feed schema stays self-documenting (the tracker decodes them).
	if rec.SessionKey != "" || rec.ReasoningEffort != "" || rec.TTFTNS != 0 {
		t.Fatalf("line 0 zero-value fields = %+v", rec)
	}
	// Line 2: failed request.
	if err := json.Unmarshal([]byte(lines[1]), &rec); err != nil {
		t.Fatalf("decode line 1: %v", err)
	}
	if !rec.Failed {
		t.Fatal("line 1 should be failed")
	}
	if rec.StatusCode != 502 {
		t.Fatalf("line 1 status_code = %d", rec.StatusCode)
	}
	if rec.Source != "u-2" {
		t.Fatalf("line 1 source = %q, want u-2", rec.Source)
	}
	// Timestamp format must be parseable RFC3339Nano (the tracker imports it).
	if _, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err != nil {
		t.Fatalf("timestamp %q not RFC3339Nano: %v", rec.Timestamp, err)
	}
}

func TestAppendUsageFeedLineRotation(t *testing.T) {
	dir := t.TempDir()
	feedPath := filepath.Join(dir, "feed.ndjson")

	oldCap := maxUsageFeedBytes
	maxUsageFeedBytes = 64 // tiny cap for the test
	defer func() { maxUsageFeedBytes = oldCap }()

	// Exceed the cap: a 100-byte line triggers truncation on the next append.
	bigLine := string(make([]byte, 100)) // 100 NUL bytes; only the size matters
	if err := os.WriteFile(feedPath, []byte(bigLine), 0o644); err != nil {
		t.Fatal(err)
	}
	appendUsageFeedLine(feedPath, []byte("{\"a\":1}\n"))
	raw, err := os.ReadFile(feedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{\"a\":1}\n" {
		t.Fatalf("feed content after rotation = %q", raw)
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
