package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// resetStreamSeams 把三个流出口替换为内存桩并在测试结束后还原。
// 必须替换：真实出口在 shim 下会因宿主 API 缺失而报错，使 pump 提前 return，
// 那样测出来的「已停止读取」是出口失败的假阳性，而不是终止标记生效。
// [参数] t：当前测试。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，隔离宿主出口依赖。
func resetStreamSeams(t *testing.T) {
	t.Helper()
	prevEmit, prevClose, prevErr := streamEmitFn, streamCloseFn, streamEmitErrorFn
	streamEmitFn = func(string, []byte) error { return nil }
	streamCloseFn = func(string) {}
	streamEmitErrorFn = func(string, string) {}
	t.Cleanup(func() {
		streamEmitFn, streamCloseFn, streamEmitErrorFn = prevEmit, prevClose, prevErr
	})
}

// TestIsStreamDoneLineTable 覆盖两种协议的四类终止写法与不应命中的普通行。
// 少认一种就等于把该上游的流交给 Keep-Alive 空闲连接去等，用户侧表现为答完不收尾。
func TestIsStreamDoneLineTable(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"data: [DONE]", true},
		{"data:[DONE]", true},
		{"event: done", true},
		{"event:done", true},
		{"data: {\"type\":\"content_block_delta\"}", false},
		{"data: [done]", false},
		{"", false},
		{"event: ping", false},
	}

	for _, tc := range cases {
		if got := isStreamDoneLine(tc.line); got != tc.want {
			t.Fatalf("isStreamDoneLine(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

// TestPumpUpstreamStreamStopsAtDone 断言 pump 读到终止标记后立即退出。
//
// 用 io.Pipe 模拟 Keep-Alive 空闲连接：终止标记之后不再写入任何数据，
// 也不关闭写端。若实现没有在终止标记处 break，pump 会阻塞在 ReadBytes 上
// 直到测试超时——这正是生产里「答完不收尾」的形态。
func TestPumpUpstreamStreamStopsAtDone(t *testing.T) {
	resetStreamSeams(t)

	pr, pw := io.Pipe()
	defer pw.Close()

	resp := &http.Response{StatusCode: 200, Body: pr}

	done := make(chan struct{})
	go func() {
		defer close(done)
		pumpUpstreamStream(context.Background(), func() {}, "", resp, "auth-1", "label", "GLM-5.3", "glm-5.3", time.Now())
	}()

	if _, err := pw.Write([]byte("data: {\"type\":\"content_block_delta\"}\n")); err != nil {
		t.Fatalf("写入首帧失败: %v", err)
	}
	// 发出终止标记后保持连接打开（不 Close 写端），复现空闲 Keep-Alive。
	if _, err := pw.Write([]byte("data: [DONE]\n")); err != nil {
		t.Fatalf("写入终止标记失败: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pump 未在终止标记后退出，仍在等待上游空闲连接")
	}
}

// TestPumpUpstreamStreamEventDoneStops 覆盖 `event: done` 形态的终止。
func TestPumpUpstreamStreamEventDoneStops(t *testing.T) {
	resetStreamSeams(t)

	pr, pw := io.Pipe()
	defer pw.Close()

	resp := &http.Response{StatusCode: 200, Body: pr}
	done := make(chan struct{})
	go func() {
		defer close(done)
		pumpUpstreamStream(context.Background(), func() {}, "", resp, "auth-1", "label", "GLM-5.3", "glm-5.3", time.Now())
	}()

	if _, err := pw.Write([]byte("event: done\n")); err != nil {
		t.Fatalf("写入终止标记失败: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("event: done 后 pump 未退出")
	}
}

// TestPumpUpstreamStreamWithoutDoneReadsToEOF 断言无终止标记的流仍按 EOF 正常收尾，
// 防止「只认终止标记、正常结束反而被截断」的回归。
func TestPumpUpstreamStreamWithoutDoneReadsToEOF(t *testing.T) {
	resetStreamSeams(t)

	upstream := "data: {\"type\":\"message_stop\"}\n"
	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(upstream))}

	done := make(chan struct{})
	go func() {
		defer close(done)
		pumpUpstreamStream(context.Background(), func() {}, "", resp, "auth-1", "label", "GLM-5.3", "glm-5.3", time.Now())
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("无终止标记的流应在 EOF 后退出")
	}
}

// TestPumpUpstreamStreamDoneFrameNotForwarded 断言终止帧本身不会被转发给宿主。
// 宿主会自行追加流终止符，插件再转发一帧会让下游出现两个结束标记。
func TestPumpUpstreamStreamDoneFrameNotForwarded(t *testing.T) {
	prevEmit, prevClose := streamEmitFn, streamCloseFn
	t.Cleanup(func() { streamEmitFn, streamCloseFn = prevEmit, prevClose })

	var emitted []string
	streamEmitFn = func(_ string, payload []byte) error {
		emitted = append(emitted, string(payload))
		return nil
	}
	streamCloseFn = func(string) {}

	body := "data: {\"type\":\"content_block_delta\"}\ndata: [DONE]\n"
	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}

	pumpUpstreamStream(context.Background(), func() {}, "", resp, "auth-1", "label", "GLM-5.3", "glm-5.3", time.Now())

	for _, frame := range emitted {
		if strings.Contains(frame, "[DONE]") {
			t.Fatalf("终止帧不应被转发，实际转发了 %q", frame)
		}
	}
}
