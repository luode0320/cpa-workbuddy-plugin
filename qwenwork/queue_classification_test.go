// queue_classification_test.go 锁定「排队 / 模型池忙」的错误分类边界。
//
// 生产证据（2026-11-28）：qwenwork 24 小时内出现 27 次 minimax 429 与 38 次
// deepseek-flash 400；裸 429 此前一律计入账号阶梯冷却，健康号被逐个推进冷却，
// 流量被迫集中到更少的账号上，进一步加剧排队。
//
// 社区一致口径（dsh-qwenwork-connector src/adapter/stream.ts:100-150、
// dsh-qwenwork-connect src/core/errors.ts:183-204、modelmux upstream.ts:624-637）：
//  1. 排队判据 = code 10605 或 isQueued===true 或 serviceAvailable===false；
//  2. 模型池忙（All models failed）归 503 排队类，不冷却账号；
//  3. 换号对模型池忙无效（模型池在账号之间共享）。
package main

import (
	"net/http"
	"testing"
)

// TestIsQueueFrame_ExtendedClassification 锁定排队帧的完整判据集合。
func TestIsQueueFrame_ExtendedClassification(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"生产实测 isQueued 帧", `403: 10605: {"isQueued":true,"queueCount":14250,"serviceAvailable":true,"waitTime":478}`, true},
		{"裸 10605 业务码", "upstream 200: 403: 10605", true},
		{"仅 isQueued 标记", `{"isQueued":true}`, true},
		{"仅 queueCount 字段", `{"queueCount":3}`, true},
		{"serviceAvailable false 无 isQueued", `{"serviceAvailable":false,"retryAfterSeconds":30}`, true},
		{"serviceAvailable false 带空格无 isQueued", `{"serviceAvailable": false, "retryAfterSeconds":30}`, true},
		{"serviceAvailable true 单独出现", `{"serviceAvailable":true,"retryAfterSeconds":30}`, false},
		{"模型池忙 429", "upstream 429: All models failed: no instance available", true},
		{"模型池忙大小写变体", "upstream 429: all models failed", true},
		{"空文本", "", false},
		{"普通 403 权限帧", `{"code":"403","message":"Forbidden"}`, false},
		{"额度耗尽", `{"error":"insufficient credit"}`, false},
		{"普通业务帧", `{"choices":[{"delta":{"content":"hi"}}]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isQueueFrame(tc.body); got != tc.want {
				t.Fatalf("isQueueFrame(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestIsModelPoolBusyBody 锁定模型池忙的独立判据。
func TestIsModelPoolBusyBody(t *testing.T) {
	if !isModelPoolBusyBody("upstream 429: All models failed") {
		t.Fatal("All models failed 必须判为模型池忙")
	}
	if isModelPoolBusyBody("upstream 429: too many requests") {
		t.Fatal("普通限流不得判为模型池忙")
	}
	if isModelPoolBusyBody("") {
		t.Fatal("空文本不得判为模型池忙")
	}
}

// TestModelPoolBusy_DoesNotCoolAccount 锁定模型池忙不计入账号冷却阶梯。
//
// 这是本轮修复的核心行为：模型池忙反映模型池容量而非账号健康，
// 冷却健康号会把可用池缩小，流量集中后更堵。
func TestModelPoolBusy_DoesNotCoolAccount(t *testing.T) {
	resetFailover(t)
	for i := 0; i < 12; i++ {
		if recordAccountFailure("acc-pool-busy", http.StatusTooManyRequests, "upstream 429: All models failed") {
			t.Fatalf("第 %d 次：模型池忙不得计入账号失败", i)
		}
		if isAccountCoolingDown("acc-pool-busy") {
			t.Fatalf("第 %d 次：模型池忙不得冷却账号", i)
		}
	}
}

// TestModelPoolBusy_DoesNotRotateAccount 锁定模型池忙不在同请求内换号。
//
// 模型池在账号之间共享，换号只会撞同一堵墙并烧掉重试预算；
// 正确路径是按 503 快速失败，由宿主跨 provider 轮转。
func TestModelPoolBusy_DoesNotRotateAccount(t *testing.T) {
	body := "upstream 429: All models failed"
	if shouldRotateOnUpstreamErr(http.StatusTooManyRequests, body) {
		t.Fatal("模型池忙不得在同请求内换号")
	}
	if shouldRotateOnUpstreamErr(http.StatusOK, "upstream 200: All models failed") {
		t.Fatal("模型池忙（200 内错误帧）不得在同请求内换号")
	}
}

// TestQueueFrameEnvelopeCarries503 锁定排队类失败对外带 503。
//
// 宿主 statusCodeFromError 只认数字状态码，纯文本错误恒得 0，
// 跨 provider 轮转永不触发。
func TestQueueFrameEnvelopeCarries503(t *testing.T) {
	for _, body := range []string{
		"upstream 200: 403: 10605: {\"isQueued\":true}",
		"upstream 429: All models failed",
		"{\"serviceAvailable\":false}",
	} {
		if got := envelopeHTTPStatusForError(body); got != httpStatusServiceUnavailable {
			t.Fatalf("排队类失败必须带 503，body=%q got=%d", body, got)
		}
	}
}
