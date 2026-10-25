// stream_queue_frame_test.go 锁定上游 HTTP 200 顶层 code 排队帧的识别契约。
//
// 生产证据（2026-10-12）：阿里 Qoder 对超上下文请求投入排队，在 HTTP 200 下
// 返回 `data: {"code":"403","message":"{\"code\":\"10605\",\"message\":
// \"{\\\"isQueued\\\":true,...}\"}"}`，随后保持 Keep-Alive 心跳约 60 秒才由对端
// 复位。旧解析器不识别该形态，将其当作无关行丢弃 → 插件死等 60 秒、客户端零字节。
//
// 本文件覆盖：解析器识别顶层 code 帧、错误帧提取（含内嵌 message 逐层展开）、
// 聚合路径 fail-fast 且不再死等 Keep-Alive，以及「排队帧不触发换号」的判定边界。
package main

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// queueFramePayload 是生产实测的排队帧 JSON（与 CPA 日志逐字一致）。
const queueFramePayload = `{"code":"403","message":"{\"code\":\"10605\",\"message\":\"{\\\"isQueued\\\":true,\\\"modelKey\\\":\\\"qfmodel\\\",\\\"queueCount\\\":0,\\\"queueType\\\":\\\"p3\\\",\\\"retryAfterSeconds\\\":30,\\\"serviceAvailable\\\":false,\\\"waitTime\\\":30}\"}"}`

// hangingReader 先吐出 prefix 字节，随后在 EOF 前阻塞 blockFor，模拟上游
// Keep-Alive 长连接不主动关闭。用于证明解析路径能在读到错误帧后立即退出，
// 而不是继续读到连接空闲超时。
type hangingReader struct {
	prefix   []byte
	off      int
	blockFor time.Duration
	blocked  bool
}

func (r *hangingReader) Read(p []byte) (int, error) {
	if r.off < len(r.prefix) {
		n := copy(p, r.prefix[r.off:])
		r.off += n
		return n, nil
	}
	if !r.blocked {
		r.blocked = true
		time.Sleep(r.blockFor)
	}
	return 0, io.EOF
}

// TestParseQoderSSEChunk_TopLevelCodeFrame 验证顶层 code 帧不再被丢弃，
// 且正常 chunk / 心跳 / [DONE] 的既有语义不受影响。
func TestParseQoderSSEChunk_TopLevelCodeFrame(t *testing.T) {
	// 1. 排队帧必须被识别并原样返回（交 sseErrorFrame 提取语义）
	chunk, isDone := parseQoderSSEChunk("data: " + queueFramePayload)
	if isDone {
		t.Fatal("queue frame must not be treated as stream done")
	}
	if !strings.Contains(chunk, "10605") {
		t.Fatalf("queue frame payload not returned: %q", chunk)
	}

	// 2. 无 message 的纯 code 帧也要被识别
	if got, _ := parseQoderSSEChunk(`data: {"code":"500"}`); !strings.Contains(got, `"code":"500"`) {
		t.Fatalf("code-only frame not recognized: %q", got)
	}

	// 3. 正常 chunk 仍走 choices 分支（不被误判为错误帧）
	normal := `data: {"id":"1","choices":[{"index":0,"delta":{"content":"hi"}}]}`
	if got, _ := parseQoderSSEChunk(normal); !strings.Contains(got, "hi") {
		t.Fatalf("normal chunk broken: %q", got)
	}

	// 4. 心跳与空行仍返回空
	if got, done := parseQoderSSEChunk(": keep-alive"); got != "" || done {
		t.Fatalf("heartbeat must be ignored, got %q done=%v", got, done)
	}

	// 5. 空 code 不触发识别（防误伤）
	if got, _ := parseQoderSSEChunk(`data: {"code":""}`); got != "" {
		t.Fatalf("empty code must not be treated as error frame: %q", got)
	}
}

// TestSSEErrorFrame_TopLevelCodeFrame 验证顶层 code 帧的语义提取，
// 含内嵌 JSON message 的逐层展开，且不影响既有 error 帧契约。
func TestSSEErrorFrame_TopLevelCodeFrame(t *testing.T) {
	got := sseErrorFrame(queueFramePayload)
	if !strings.HasPrefix(got, "403: ") {
		t.Fatalf("expected 403 prefix, got %q", got)
	}
	if !strings.Contains(got, "10605") || !strings.Contains(got, "isQueued") {
		t.Fatalf("nested message not unwrapped: %q", got)
	}

	// 纯 code 无 message：只回 code
	if got := sseErrorFrame(`{"code":"500"}`); got != "500" {
		t.Fatalf("code-only frame = %q, want 500", got)
	}

	// 既有 error 帧契约不变
	if got := sseErrorFrame(`{"error":{"code":"14018","message":"quota"}}`); got != "14018: quota" {
		t.Fatalf("legacy error frame broken: %q", got)
	}
	// 正常 chunk 不误报
	if got := sseErrorFrame(`{"id":"1","choices":[{"index":0,"delta":{"content":"hi"}}]}`); got != "" {
		t.Fatalf("normal chunk false positive: %q", got)
	}
	// 顶层 code 但 code 为空串：不识别
	if got := sseErrorFrame(`{"code":"","message":"x"}`); got != "" {
		t.Fatalf("empty code false positive: %q", got)
	}
}

// TestUnwrapNestedMessage 验证内嵌 JSON message 的逐层展开与边界（保留首层业务码）。
func TestUnwrapNestedMessage(t *testing.T) {
	// 双层嵌套：保留内层业务码 10605，最终拼成 "code: 描述"
	two := `{"code":"10605","message":"{\"isQueued\":true}"}`
	if got := unwrapNestedMessage(two); got != `10605: {"isQueued":true}` {
		t.Fatalf("two-level unwrap = %q", got)
	}
	// 非 JSON 原样返回
	if got := unwrapNestedMessage("plain text"); got != "plain text" {
		t.Fatalf("plain text mangled: %q", got)
	}
	// 只有 code 没有 message 时停在当前层（不 panic、不死循环）
	if got := unwrapNestedMessage(`{"code":"10605"}`); got != `10605: {"code":"10605"}` {
		t.Fatalf("code-only unwrap = %q", got)
	}
}

// TestAggregateQoderSSE_QueueFrameFailsFast 是核心回归：HTTP 200 下先到排队帧、
// 随后连接 Keep-Alive 挂起 10 秒时，聚合必须在读到错误帧后立即返回错误
// （远快于挂起时长），而不是死等到连接空闲超时。
func TestAggregateQoderSSE_QueueFrameFailsFast(t *testing.T) {
	body := "data: " + queueFramePayload + "\n\n"
	r := &hangingReader{prefix: []byte(body), blockFor: 10 * time.Second}

	start := time.Now()
	_, err := aggregateQoderSSE(r, "qwen3.8-flash", 200, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("queue frame must fail the completion, got nil")
	}
	if !strings.Contains(err.Error(), "upstream 200:") || !strings.Contains(err.Error(), "10605") {
		t.Fatalf("error = %v, want upstream 200 prefixed queue message", err)
	}
	// 修复前会读到 EOF（10s 挂起）；修复后应在毫秒级返回。
	if elapsed > 2*time.Second {
		t.Fatalf("fail-fast violated: took %v, expected < 2s (hangingReader blocks 10s)", elapsed)
	}
}

// TestAggregateQoderSSE_HealthyStreamUnaffected 验证正常流不受排队帧识别影响。
func TestAggregateQoderSSE_HealthyStreamUnaffected(t *testing.T) {
	sse := `data: {"id":"1","choices":[{"index":0,"delta":{"content":"hello"}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	out, err := aggregateQoderSSE(strings.NewReader(sse), "m", 200, nil)
	if err != nil {
		t.Fatalf("healthy stream must not error: %v", err)
	}
	if !strings.Contains(string(out), "hello") {
		t.Fatalf("content mangled: %s", out)
	}
}

// TestQueueFrameDoesNotRotate 锁定「排队帧按立即报错处理、不触发换号」的判定：
// 排队帧文本不含 credit / rate-limit marker，shouldRotateOnUpstreamErr 必须为
// false，使请求立即以错误收尾而非在号池内继续消耗。
func TestQueueFrameDoesNotRotate(t *testing.T) {
	msg := sseErrorFrame(queueFramePayload)
	if msg == "" {
		t.Fatal("queue frame must produce an error message")
	}
	// 同步路径构造的 canonical 错误文本
	canonical := "upstream 200: " + msg
	if shouldRotateOnUpstreamErr(200, canonical) {
		t.Fatalf("queue frame must NOT rotate accounts, body=%q", canonical)
	}
}

// queueFrameLivePayload 是 2026-10-15 生产实测的排队帧展开文本
// （serviceAvailable:true、queueCount 上万、waitTime 数百秒）。
const queueFrameLivePayload = `upstream 200: 403: 10605: {"isQueued":true,"modelKey":"qfmodel","queueCount":14250,"queueType":"p3","retryAfterSeconds":30,"serviceAvailable":true,"waitTime":478}`

// TestIsQueueFrame_Classification 表驱动锁定排队帧识别边界。
func TestIsQueueFrame_Classification(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"生产实测展开文本", queueFrameLivePayload, true},
		{"裸 10605 业务码", "upstream 200: 403: 10605", true},
		{"原始排队帧 JSON", queueFramePayload, true},
		{"仅 isQueued 标记", `{"isQueued":true}`, true},
		{"仅 queueCount 字段", `{"queueCount":3}`, true},
		{"空文本", "", false},
		{"普通 403 权限帧", `{"code":"403","message":"Forbidden"}`, false},
		{"110 日次数上限", "upstream 200: 110: Billing daily count exceeded", false},
		{"credit 耗尽", `{"error":"insufficient credit"}`, false},
		{"正常业务帧", `{"choices":[{"delta":{"content":"hi"}}]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isQueueFrame(tc.body); got != tc.want {
				t.Fatalf("isQueueFrame(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestQueueFrameDoesNotCoolOrCount 是本轮核心回归：排队帧是上游容量状态，
// 既不推进连续失败计数、也不产生账号冷却。修复前它被 remap 成 403 走硬失败
// 通道，导致上游排队持续期间健康账号被逐个冷却、整池 auth_unavailable。
func TestQueueFrameDoesNotCoolOrCount(t *testing.T) {
	resetFailover(t)
	// 连续 12 次排队（远超 6 账号池规模）都必须不记账、不冷却。
	for i := 0; i < 12; i++ {
		if recordAccountFailure("acc-queue", 403, queueFrameLivePayload) {
			t.Fatalf("iteration %d: queue frame must not be counted as account failure", i)
		}
		if count, _, ok := failoverStateSnapshot("acc-queue"); ok && count != 0 {
			t.Fatalf("iteration %d: queue frame must not advance count, got %d", i, count)
		}
		if isAccountCoolingDown("acc-queue") {
			t.Fatalf("iteration %d: queue frame must not put a healthy account into cooldown", i)
		}
	}
	// 对照：同文本若走 noteAccountFailure 亦不得生效（唯一记账入口已拦截）。
	if noteAccountFailure("acc-queue-2", 403, queueFrameLivePayload) {
		t.Fatal("noteAccountFailure must also skip queue frames")
	}
	if isAccountCoolingDown("acc-queue-2") {
		t.Fatal("queue frame via noteAccountFailure must not cool the account")
	}
}

// TestErrorEnvelopeCarriesQueueStatus 锁定第 2 步：排队帧失败信封必须携带
// 数字状态码 503，使宿主 statusCodeFromError 命中跨 provider 轮转白名单；
// 非排队错误保持省略（宿主按既有语义处理）。
func TestErrorEnvelopeCarriesQueueStatus(t *testing.T) {
	// 1. 排队帧 → http_status=503
	var probe struct {
		OK    bool `json:"ok"`
		Error struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			HTTPStatus int    `json:"http_status"`
		} `json:"error"`
	}
	raw := errorEnvelope("plugin_error", queueFrameLivePayload)
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if probe.OK {
		t.Fatal("failure envelope must have ok=false")
	}
	if probe.Error.HTTPStatus != 503 {
		t.Fatalf("queue frame envelope http_status = %d, want 503", probe.Error.HTTPStatus)
	}
	if !strings.Contains(probe.Error.Message, "10605") {
		t.Fatalf("message must carry the queue frame: %q", probe.Error.Message)
	}

	// 2. 非排队错误 → 不携带 http_status（omitempty）
	rawPlain := errorEnvelope("plugin_error", "some other failure")
	if strings.Contains(string(rawPlain), "http_status") {
		t.Fatalf("non-queue failure must omit http_status: %s", rawPlain)
	}
}
