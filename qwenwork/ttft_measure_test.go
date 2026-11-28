// ttft_measure_test.go 锁定首字延迟（TTFT）的测量口径与慢速冷却的准入闸门。
//
// 生产证据（2026-11-28）：qwenwork 的用量 feed 里 TTFT p50 = 0ms、p90 = 1ms，
// 而同期 traework / workbuddy 分别是 5063ms / 11796ms。0~1ms 在物理上不可能
// （至少一次网络 RTT），说明计时基准取错了位置——基准晚于发请求，测的其实是
// 「响应头到首帧」。后果是慢速冷却的样本全部低于绝对下限 3000ms，
// 「慢号让位」机制对 qwenwork 等于不存在（生产日志零条「slow cooldown
// entered」，同期 traework 多条）。
package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestPumpUpstreamStream_MarksTTFTBeforeRequest 锁定 TTFT 基准早于发请求。
//
// 这条是装配层断言：函数本身测对了，调用点仍可能把基准放在发请求之后
// （历史上 qwenwork 正是如此），只有顺序断言能覆盖。
func TestPumpUpstreamStream_MarksTTFTBeforeRequest(t *testing.T) {
	raw, err := os.ReadFile("stream.go")
	if err != nil {
		t.Fatalf("read stream.go: %v", err)
	}
	text := string(raw)
	baseIdx := strings.Index(text, "attemptStarted := time.Now()")
	reqIdx := strings.Index(text, "hostHTTPDoStream(curReq")
	markIdx := strings.Index(text, "collector.markAttemptStart(attemptStarted)")
	if baseIdx < 0 || reqIdx < 0 || markIdx < 0 {
		t.Fatalf("pumpUpstreamStream 必须用发请求前的时刻做 TTFT 基准（base=%d req=%d mark=%d）", baseIdx, reqIdx, markIdx)
	}
	if baseIdx > reqIdx {
		t.Fatal("TTFT 基准必须早于 hostHTTPDoStream，否则测的是「响应头到首帧」")
	}
	if strings.Contains(text, "collector.markAttemptStart(time.Now())") {
		t.Fatal("collector 的计时基准不得用发请求之后的时刻")
	}
}

// TestSseUsageCollector_TTFTCoversWaitingTime 锁定 collector 的 TTFT 口径。
func TestSseUsageCollector_TTFTCoversWaitingTime(t *testing.T) {
	c := &sseUsageCollector{}
	base := time.Now()
	c.markAttemptStart(base)
	time.Sleep(40 * time.Millisecond)
	c.feed(`{"choices":[{"delta":{"content":"hi"}}]}`)
	got := c.ttftNS(base)
	if got == 0 {
		t.Fatal("首帧到达后 TTFT 必须大于 0")
	}
	if got < 30*uint64(time.Millisecond) {
		t.Fatalf("TTFT 必须覆盖等待首帧的时间，got %d", got)
	}
}

// TestSlowPoolAdmitted_OpensForProductionPoolSize 锁定慢速冷却的准入闸门。
//
// 生产 qwenwork 账号池实测 8 个（2026-11-28「slow pool size updated: now=8」），
// 全仓统一的 20 会让整套慢速判定永久休眠；6 的下限既让生产池放行，
// 又保证 P25 的下标 ≥1（基准由「第二慢」而非「最慢」定义，N=4 时会退化）。
func TestSlowPoolAdmitted_OpensForProductionPoolSize(t *testing.T) {
	cfg := loadedSlowConfig()
	if cfg.minPoolSize > 8 {
		t.Fatalf("准入下限必须让 8 个账号的生产池放行，当前 %d", cfg.minPoolSize)
	}

	slowPoolSizeMu.Lock()
	slowPoolSize = 8
	slowPoolSizeMu.Unlock()
	if !slowPoolAdmitted(cfg.minPoolSize) {
		t.Fatalf("8 个账号的生产池必须放行慢速判定（下限 %d）", cfg.minPoolSize)
	}

	slowPoolSizeMu.Lock()
	slowPoolSize = 3
	slowPoolSizeMu.Unlock()
	if slowPoolAdmitted(cfg.minPoolSize) {
		t.Fatalf("池子过小时必须拒绝慢速判定（下限 %d）", cfg.minPoolSize)
	}

	slowPoolSizeMu.Lock()
	slowPoolSize = 0
	slowPoolSizeMu.Unlock()
	if slowPoolAdmitted(cfg.minPoolSize) {
		t.Fatal("尚未探测到池规模时必须保守拒绝慢速判定")
	}
}
