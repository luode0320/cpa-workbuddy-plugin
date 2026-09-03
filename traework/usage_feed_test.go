package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	recordUsageFeed("alias-m", "deepseek-v4", "u-1", started, detail, false, 200, "", uint64(650*time.Millisecond), "acct-nick", "sess-abc")
	recordUsageFeed("alias-m", "deepseek-v4", "u-2", started.Add(time.Second), detail, true, 502, "", 0, "u-2", "")

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
	// Source mirrors the account label (traework passes the auth UID as the
	// dashboard 来源 label); provider must be the plugin id so the tracker
	// dashboard's provider dimension separates traework records from
	// workbuddy ones.
	if rec.Source != "acct-nick" || rec.Provider != providerName || rec.ExecutorType != "traework" {
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
	// Session/TTFT dimensions must round-trip into the feed so the tracker
	// dashboard's 会话 / 首字延迟 columns light up for traework traffic
	// (parity with workbuddy records).
	if rec.SessionKey != "sess-abc" {
		t.Fatalf("line 0 session_key = %q, want sess-abc", rec.SessionKey)
	}
	if rec.TTFTNS != int64(650*time.Millisecond) {
		t.Fatalf("line 0 ttft_ns = %d, want %d", rec.TTFTNS, int64(650*time.Millisecond))
	}
	if rec.ReasoningEffort != "" {
		t.Fatalf("line 0 reasoning_effort = %q, want empty (Trae upstream has no such knob)", rec.ReasoningEffort)
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

// TestPumpTraeStreamPublishesFailureWhenStopEmitFails 验证最终 stop 无法下发时写入失败用量。
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-08-30 20:22:38；改动原因：防止客户端未收到终止分片时仍复位账号并记录成功。
func TestPumpTraeStreamPublishesFailureWhenStopEmitFails(t *testing.T) {
	// 1. 隔离共享 feed 配置，确保用例只写临时目录。
	feedPath := filepath.Join(t.TempDir(), "feed.ndjson")
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

	// 2. 使用无输出的 done 事件；测试环境没有宿主回调，因此最终 stop 必须下发失败。
	started := time.Now().Add(-time.Second)
	pumpTraeStream(strings.NewReader("event: done\ndata: {}\n\n"), traeStreamPumpContext{
		StreamID:      "stream-test",     // 使用不可用的宿主流，验证 feed 不依赖回调成功。
		Model:         "qwen-max-latest", // 客户端模型别名。
		UpstreamModel: "qwen3.8-max",     // Trae 上游实际模型。
		StatusCode:    200,               // 正常上游状态。
		AuthID:        "uid-1",           // 调度账号标识。
		AuthUID:       "uid-1",           // 统计账号维度。
		Started:       started,           // 固定为一秒前以生成正延迟。
	})

	// 3. 等待 publishUsage 的异步写入完成，再核对 feed 结果。
	deadline := time.Now().Add(time.Second)
	var raw []byte
	for time.Now().Before(deadline) {
		var err error
		raw, err = os.ReadFile(feedPath)
		if err == nil && len(raw) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	lines := splitLines(string(raw))
	if len(lines) != 1 {
		t.Fatalf("feed lines = %d, want 1; content=%q", len(lines), raw)
	}
	var rec struct {
		Alias      string `json:"alias"`
		Model      string `json:"model"`
		Provider   string `json:"provider"`
		AuthIndex  string `json:"auth_index"`
		Failed     bool   `json:"failed"`
		StatusCode int    `json:"status_code"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("decode feed: %v", err)
	}
	if rec.Alias != "qwen-max-latest" || rec.Model != "qwen3.8-max" || rec.Provider != providerName || rec.AuthIndex != "uid-1" {
		t.Fatalf("feed record = %+v", rec)
	}
	if !rec.Failed || rec.StatusCode != 200 {
		t.Fatalf("feed outcome = failed:%v status:%d", rec.Failed, rec.StatusCode)
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

// TestPumpTraeStreamOutputWithoutDoneDoesNotResetFailover 锁定上游中途断流（部分 output 无 done）
// 不清零账号故障：断流收尾走失败/收尾出口而非成功复位，避免把可兜底的中断误判为账号成功。
//
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-08-31 02:10:00；改动原因：0.1.21 对部分 output 无 done 一律报 truncated 中断，
// 修复后改为补 length 收尾；本用例验证断流不会误复位账号故障状态。
func TestPumpTraeStreamOutputWithoutDoneDoesNotResetFailover(t *testing.T) {
	// 1. 隔离共享 feed 配置，确保用例只写临时目录。
	feedPath := filepath.Join(t.TempDir(), "feed.ndjson")
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

	// 2. 给账号记一次故障，使其进入冷却；断流不应把它清零。
	resetFailover(t)
	recordAccountFailure("uid-1", 429, "rate limit")
	if !isAccountCoolingDown("uid-1") {
		t.Fatal("precondition: account should be cooling down")
	}

	// 3. 部分 output 后 EOF（无 done）：上游中途断流。测试环境宿主回调不可用，
	//    首个分片下发失败会走失败出口，但必须走失败/收尾，而非复位账号。
	started := time.Now().Add(-time.Second)
	pumpTraeStream(strings.NewReader(`event: output
data: {"response":"部分内容"}

`), traeStreamPumpContext{
		StreamID:      "stream-trunc",
		Model:         "qwen-max-latest",
		UpstreamModel: "qwen3.8-max",
		StatusCode:    200,
		AuthID:        "uid-1",
		AuthUID:       "uid-1",
		Started:       started,
	})

	// 4. 断流收尾不清零账号故障：cooldown 应保持（而非被 reset 清空）。
	deadline := time.Now().Add(time.Second)
	ok := false
	for time.Now().Before(deadline) {
		if isAccountCoolingDown("uid-1") {
			ok = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ok {
		t.Fatal("upstream truncation must not reset account failover (cooldown cleared)")
	}
}

// TestTtftNSBetween 锁定 TTFT 计算助手语义：正差返回纳秒、零值输入或负差
// （时钟抖动）一律返回 0，与 workbuddy 的 sseUsageCollector.ttftNS 一致。
//
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-03；改动原因：dashboard 首字延迟列——traework 侧 ttft_ns 采集对齐 workbuddy。
func TestTtftNSBetween(t *testing.T) {
	started := time.Now()
	if got := ttftNSBetween(time.Time{}, started); got != 0 {
		t.Fatalf("ttftNSBetween(zero started) = %d, want 0", got)
	}
	if got := ttftNSBetween(started, time.Time{}); got != 0 {
		t.Fatalf("ttftNSBetween(zero firstOutput) = %d, want 0", got)
	}
	if got := ttftNSBetween(started.Add(time.Second), started); got != 0 {
		t.Fatalf("ttftNSBetween(negative gap) = %d, want 0", got)
	}
	want := uint64(1500 * time.Millisecond)
	if got := ttftNSBetween(started, started.Add(1500*time.Millisecond)); got != want {
		t.Fatalf("ttftNSBetween(1.5s) = %d, want %d", got, want)
	}
}

// TestCollectTraeStreamReportsFirstOutputAt 锁定同步收集路径把首个有效 output
// 事件到达时间带出（TTFT 观测点）：有输出时非零且不早于调用时刻，空流错误路径返回零值。
//
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-03；改动原因：dashboard 首字延迟列——collect 路径 TTFT 观测点回归锁定。
func TestCollectTraeStreamReportsFirstOutputAt(t *testing.T) {
	before := time.Now()
	sse := "event: output\ndata: {\"response\":\"首字\"}\n\nevent: done\ndata: {}\n\n"
	_, _, firstOutputAt, err := collectTraeStream(strings.NewReader(sse), "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("collectTraeStream error = %v", err)
	}
	if firstOutputAt.IsZero() {
		t.Fatal("firstOutputAt is zero; want the first output event arrival time")
	}
	if firstOutputAt.Before(before) {
		t.Fatalf("firstOutputAt %v earlier than call time %v", firstOutputAt, before)
	}
	// 空流报错路径不得携带非零观测点（不可观测即零值，feed 写 0）。
	_, _, emptyAt, err := collectTraeStream(strings.NewReader(""), "qwen3.8-max", 200)
	if err == nil {
		t.Fatal("empty stream must be rejected")
	}
	if !emptyAt.IsZero() {
		t.Fatalf("empty stream firstOutputAt = %v, want zero", emptyAt)
	}
}
