// active_ping_queue_frame_test.go 锁定「上游排队帧不等于账号故障」这条语义。
package main

import (
	"errors"
	"strings"
	"testing"
)

// queueFrameActivePingPayload 复刻生产原帧（与 stream_queue_frame_test.go 同源）。
const queueFrameActivePingPayload = `403: 10605: {"errorCode":0,"isQueued":true,"modelKey":"qmodel_latest","queueCount":1,"queueType":"p4","retryAfterSeconds":60,"serviceAvailable":true,"subCode":"RESOURCE_LIMIT","waitTime":60}`

func TestParseQueueFrameInfo_ProductionFrame(t *testing.T) {
	info, ok := parseQueueFrameInfo(queueFrameActivePingPayload)
	if !ok {
		t.Fatal("生产排队帧应被解析出来")
	}
	if !info.IsQueued || info.QueueCount != 1 || info.QueueType != "p4" {
		t.Fatalf("排队字段解析错误: %+v", info)
	}
	if info.RetryAfterSeconds != 60 || info.WaitTime != 60 {
		t.Fatalf("等待秒数解析错误: %+v", info)
	}
	if info.SubCode != "RESOURCE_LIMIT" || info.ModelKey != "qmodel_latest" {
		t.Fatalf("子码/模型键解析错误: %+v", info)
	}
	if !info.ServiceAvailable {
		t.Fatal("serviceAvailable 应为 true（账号本身健康）")
	}
	if got := info.waitSeconds(); got != 60 {
		t.Fatalf("waitSeconds() = %d, want 60", got)
	}
}

// TestParseQueueFrameInfo_PrefixedAndNonQueue 锁定前缀容忍与非排队文本的边界。
func TestParseQueueFrameInfo_PrefixedAndNonQueue(t *testing.T) {
	prefixed := "upstream 200: " + queueFrameActivePingPayload
	if _, ok := parseQueueFrameInfo(prefixed); !ok {
		t.Fatal("带 upstream 前缀的排队帧也应解析出来")
	}
	for _, body := range []string{
		"",
		"upstream 200: 112: {\"pricingUrl\":\"https://qoder.com.cn/pricing\"}",
		"upstream 429: too many requests",
		"upstream stream closed before first payload",
	} {
		if _, ok := parseQueueFrameInfo(body); ok {
			t.Fatalf("非排队文本不应解析为排队帧: %q", body)
		}
	}
}

// TestWaitSecondsFallsBackToWaitTime 锁定 waitTime 兜底。
func TestWaitSecondsFallsBackToWaitTime(t *testing.T) {
	if got := (queueFrameInfo{WaitTime: 478}).waitSeconds(); got != 478 {
		t.Fatalf("waitSeconds() = %d, want 478", got)
	}
	if got := (queueFrameInfo{}).waitSeconds(); got != 0 {
		t.Fatalf("无等待信息时 waitSeconds() = %d, want 0", got)
	}
}

// TestQueueFrameErrorKeepsCanonicalText 锁定错误文本与改造前逐字一致：
// failover_disable.go 的探活日志与历史日志都按该文本落盘。
func TestQueueFrameErrorKeepsCanonicalText(t *testing.T) {
	info, ok := parseQueueFrameInfo(queueFrameActivePingPayload)
	if !ok {
		t.Fatal("生产排队帧应被解析出来")
	}
	err := &queueFrameError{status: 200, info: info, raw: queueFrameActivePingPayload}
	want := "upstream 200: " + queueFrameActivePingPayload
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	var target *queueFrameError
	if !errors.As(error(err), &target) {
		t.Fatal("queueFrameError 必须能被 errors.As 取出")
	}
}

// TestBuildActivePingFailureMarksQueued 锁定排队帧的结果 map：带 upstream_queued，
// error 是可读中文提示而不是截断 JSON。
func TestBuildActivePingFailureMarksQueued(t *testing.T) {
	info, ok := parseQueueFrameInfo(queueFrameActivePingPayload)
	if !ok {
		t.Fatal("生产排队帧应被解析出来")
	}
	res := buildActivePingFailure("Qwen4-Max-Preview", &queueFrameError{status: 200, info: info, raw: queueFrameActivePingPayload})

	if res["upstream_queued"] != true {
		t.Fatalf("排队帧必须带 upstream_queued=true, got %v", res["upstream_queued"])
	}
	if res["model"] != "Qwen4-Max-Preview" {
		t.Fatalf("model = %v, want Qwen4-Max-Preview", res["model"])
	}
	if got := res["retry_after_seconds"]; got != 60 {
		t.Fatalf("retry_after_seconds = %v, want 60", got)
	}
	if got := res["queue_type"]; got != "p4" {
		t.Fatalf("queue_type = %v, want p4", got)
	}
	if got := res["queue_count"]; got != 1 {
		t.Fatalf("queue_count = %v, want 1", got)
	}
	if got := res["sub_code"]; got != "RESOURCE_LIMIT" {
		t.Fatalf("sub_code = %v, want RESOURCE_LIMIT", got)
	}
	msg, _ := res["error"].(string)
	for _, want := range []string{"上游排队中", "Qwen4-Max-Preview", "60 秒后可重试", "账号与凭据正常"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("提示 %q 缺少片段 %q", msg, want)
		}
	}
	if strings.Contains(msg, "isQueued") || strings.Contains(msg, "&#34;") {
		t.Fatalf("提示不应回显原始 JSON: %q", msg)
	}
	// 原始帧仍保留在 detail 里，排障时能拿到上游原文。
	if detail, _ := res["detail"].(string); !strings.Contains(detail, "10605") {
		t.Fatalf("detail 应保留原始错误文本, got %v", res["detail"])
	}
}

// TestBuildActivePingFailureNonQueueUnchanged 锁定非排队失败仍是原「测试失败」语义。
func TestBuildActivePingFailureNonQueueUnchanged(t *testing.T) {
	res := buildActivePingFailure("qmodel_latest", errors.New("upstream 401: invalid credential"))
	if _, ok := res["upstream_queued"]; ok {
		t.Fatal("非排队失败不应带 upstream_queued")
	}
	msg, _ := res["error"].(string)
	if !strings.Contains(msg, "活跃测试失败 (模型: qmodel_latest)") {
		t.Fatalf("非排队失败文案被改动: %q", msg)
	}
	if !strings.Contains(msg, "upstream 401: invalid credential") {
		t.Fatalf("非排队失败应保留原始错误: %q", msg)
	}
}

// TestHandleTestActiveWithAuthQueued 端到端锁定面板拿到的结果结构。
func TestHandleTestActiveWithAuthQueued(t *testing.T) {
	origPingFn := sendActivePingQoderFn
	defer func() { sendActivePingQoderFn = origPingFn }()
	sendActivePingQoderFn = func(sa *storedAuth, chosenModel string) error {
		info, ok := parseQueueFrameInfo(queueFrameActivePingPayload)
		if !ok {
			t.Fatal("生产排队帧应被解析出来")
		}
		return &queueFrameError{status: 200, info: info, raw: queueFrameActivePingPayload}
	}

	sa := &storedAuth{Account: storedAccount{UID: "qw-user-queued"}}
	res := handleTestActiveWithAuth(sa, "idx-queued", "Qwen4-Max-Preview")
	if res["upstream_queued"] != true {
		t.Fatalf("handleTestActiveWithAuth 应把排队帧标记为 upstream_queued, got %v", res)
	}
	if _, ok := res["ok"]; ok {
		t.Fatal("排队帧不是成功，不应带 ok")
	}
	if res["retry_after_seconds"] != 60 {
		t.Fatalf("retry_after_seconds = %v, want 60", res["retry_after_seconds"])
	}
}
