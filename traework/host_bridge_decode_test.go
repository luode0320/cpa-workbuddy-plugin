package main

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestDecodeBridgeHTTPResponse_TolerantStatus locks in the 0.1.10 fix: host
// versions differ in the JSON key used for the upstream status inside the
// host.http.do result. Decoding only "status_code" silently produced
// StatusCode 0, which callers treated as a hard HTTP failure even when the
// body was intact — check-in/points broke on Linux (bridge path) while the
// Windows direct path kept working, hiding the bug in dev.
func TestDecodeBridgeHTTPResponse_TolerantStatus(t *testing.T) {
	cases := []struct {
		name string
		wire string
		want int
	}{
		{"snake", `{"status_code":200,"body":"e30="}`, 200},
		{"camel", `{"statusCode":201,"body":"e30="}`, 201},
		{"status-number", `{"status":200,"body":"e30="}`, 200},
		{"status-string", `{"status":"200","body":"e30="}`, 200},
		{"code-number", `{"code":200,"body":"e30="}`, 200},
		{"code-string", `{"code":"403","body":"e30="}`, 403},
		{"absent", `{"body":"e30="}`, 0},
	}
	for _, tc := range cases {
		resp, err := decodeBridgeHTTPResponse(json.RawMessage(tc.wire))
		if err != nil {
			t.Fatalf("%s: decode error: %v", tc.name, err)
		}
		if resp.StatusCode != tc.want {
			t.Fatalf("%s: StatusCode=%d; want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
}

// TestDecodeBridgeHTTPResponse_BodyIntact ensures the body survives the
// tolerant decode (the check-in business code rides in the body even when
// the status key is unrecognized).
func TestDecodeBridgeHTTPResponse_BodyIntact(t *testing.T) {
	wire := `{"status":200,"headers":{"Content-Type":["application/json"]},"body":"eyJjb2RlIjo5MDc0fQ=="}`
	resp, err := decodeBridgeHTTPResponse(json.RawMessage(wire))
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if string(resp.Body) != `{"code":9074}` {
		t.Fatalf("body=%q; want {\"code\":9074}", string(resp.Body))
	}
	if resp.Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("headers lost in decode")
	}
}

func TestIsDefiniteHTTPFailure(t *testing.T) {
	if !isDefiniteHTTPFailure(500) || !isDefiniteHTTPFailure(403) {
		t.Fatalf("definite non-200 statuses must fail")
	}
	if isDefiniteHTTPFailure(200) {
		t.Fatalf("200 must not fail")
	}
	if isDefiniteHTTPFailure(0) {
		t.Fatalf("0 = undecodable bridge status; must NOT be a definite failure (body decides)")
	}
}

func TestIsBusyThrottleMsg(t *testing.T) {
	if !isBusyThrottleMsg("当前参与用户太多，请稍后再试") {
		t.Fatalf("Trae 9074 throttle message must match")
	}
	if isBusyThrottleMsg("签到成功") {
		t.Fatalf("success message must not match throttle")
	}
}

// TestRPCHostHTTPRequestWireCarriesCallbackID 锁定异步流请求必须把 CPA callback 标识放在外层 wire。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-08-30 20:22:38；改动原因：防止宿主流桥退回短生命周期 fallback context。
func TestRPCHostHTTPRequestWireCarriesCallbackID(t *testing.T) {
	wire := rpcHostHTTPRequestWire{
		HostCallbackID: "callback-1",
		Request: &rpcHostHTTPInner{
			Method: "POST",
			URL:    "https://example.invalid/chat",
		},
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode wire: %v", err)
	}
	if decoded["host_callback_id"] != "callback-1" {
		t.Fatalf("host_callback_id = %v", decoded["host_callback_id"])
	}
}

// TestScanSSEReassemblesHostChunks 验证宿主任意分片不会破坏 SSE 行重组。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-08-30 20:22:38；改动原因：覆盖实时流桥按块读取时的跨 chunk SSE 边界。
func TestScanSSEReassemblesHostChunks(t *testing.T) {
	reader := &chunkReader{chunks: [][]byte{
		[]byte(`event: output
data: {"response":"第一段"}

 eve`),
		[]byte(`nt: done
data: {}

`),
	}}
	var events []sseEvent
	if err := scanSSE(reader, func(event sseEvent) error {
		events = append(events, event)
		return nil
	}, nil); err != nil {
		t.Fatalf("scanSSE: %v", err)
	}
	if len(events) != 2 || events[0].Event != "output" || events[1].Event != "done" {
		t.Fatalf("events = %+v", events)
	}
}

// TestScanSSEFlushesEOFWithoutNewline 验证 EOF 前没有换行的最后一帧仍会被解析。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-08-31 00:24:32；改动原因：覆盖无换行 done、output 和残缺尾帧边界。
func TestScanSSEFlushesEOFWithoutNewline(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantEvent string
		wantData  string
	}{
		{
			name:      "done",
			input:     "event: done\ndata: {}",
			wantEvent: "done",
			wantData:  "{}",
		},
		{
			name: "output",
			input: `event: output
data: {"response":"尾帧"}`,
			wantEvent: "output",
			wantData:  `{"response":"尾帧"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events []sseEvent
			if err := scanSSE(strings.NewReader(tc.input), func(event sseEvent) error {
				events = append(events, event)
				return nil
			}, nil); err != nil {
				t.Fatalf("scanSSE: %v", err)
			}
			if len(events) != 1 || events[0].Event != tc.wantEvent || events[0].Data != tc.wantData {
				t.Fatalf("events = %+v", events)
			}
		})
	}

	// 只有 event 行而没有 data 行时不能伪造业务事件。
	var events []sseEvent
	if err := scanSSE(strings.NewReader("event: done"), func(event sseEvent) error {
		events = append(events, event)
		return nil
	}, nil); err != nil {
		t.Fatalf("scanSSE incomplete tail: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("incomplete tail events = %+v", events)
	}
}

// chunkReader 按预设边界逐块返回字节，用于模拟宿主流桥的任意分片。
type chunkReader struct {
	chunks [][]byte // 依次返回的宿主分片。
	index  int      // 下一分片下标。
}

// Read 返回一个预设宿主分片，所有分片读完后返回 EOF。
// [参数] p: 调用方提供的目标缓冲区。
// [返回] n: 本次复制的字节数；error: 分片耗尽时为 io.EOF。
// 最近修改时间：2026-08-30 23:40:18；改动原因：为跨宿主 chunk 的 SSE 重组测试提供可控读取边界。
func (r *chunkReader) Read(p []byte) (int, error) {
	// 1. 分片耗尽后显式返回 EOF，模拟宿主流读取完成。
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	// 2. 每次只返回一个预设分片，确保扫描器必须跨 Read 调用重组 SSE 行。
	n := copy(p, r.chunks[r.index])
	r.index++
	return n, nil
}

// TestCollectTraeStreamFoldsOutputWithoutDoneToLength 锁定部分 output 后 EOF（上游断流）
// 应兜底补 length 收尾，保留已生成内容，而不是报错中断或伪装成完整 stop。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-08-31 02:10:00；改动原因：0.1.21 把部分输出无 done 一律报 truncated 错误导致 IDE 中断，
// 实际是上游中途断流，应补 length 正常收尾；仅空响应（无 output 无 done）才真正报错。
func TestCollectTraeStreamFoldsOutputWithoutDoneToLength(t *testing.T) {
	chunks, _, _, _, err := collectTraeStream(strings.NewReader(`event: output
data: {"response":"未完成"}

`), "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("collectTraeStream error = %v; want nil (upstream truncation is recoverable)", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d; want 2 (output + length finish)", len(chunks))
	}
	var tail struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(chunks[len(chunks)-1].Payload, &tail); err != nil {
		t.Fatalf("decode tail chunk: %v", err)
	}
	if len(tail.Choices) != 1 || tail.Choices[0].FinishReason != "length" {
		t.Fatalf("finish_reason = %+v; want length", tail.Choices)
	}
}

// TestCollectTraeStreamRejectsEmptyWithoutDone 锁定既无 output 也无 done 的空响应仍报错（防 0.1.20 空成功回归）。
//
// [参数] t: 当前测试。
// [返回] 无。
func TestCollectTraeStreamRejectsEmptyWithoutDone(t *testing.T) {
	_, _, _, _, err := collectTraeStream(strings.NewReader(""), "qwen3.8-max", 200)
	if err == nil {
		t.Fatalf("empty response without done must be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "missing output and done event") {
		t.Fatalf("error = %v, want missing output and done event", err)
	}
}

// streamErrReader 在返回预设 SSE 字节后以读错误断开，模拟上游真实断流
// （对端 RST / unexpected EOF / 宿主流桥桥接错误），而非干净 EOF。
type streamErrReader struct {
	data []byte
	pos  int
	err  error
}

// Read 先交付预设字节，耗尽后返回读错误而非 io.EOF。
// [参数] p: 调用方提供的目标缓冲区。
// [返回] n: 本次复制的字节数；error: 预设字节耗尽后返回读错误。
// 最近修改时间：2026-08-31 15:20:00；改动原因：为读错误型断流兜底测试提供可控读取边界。
func (r *streamErrReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// TestCollectTraeStreamFoldsReadErrorAfterOutputToLength 锁定部分 output 后遭遇读错误
// （真实断流形态）应与干净 EOF 一样兜底补 length 收尾，保留已生成内容。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-08-31 15:20:00；改动原因：0.1.22 只兜住干净 EOF 型断流，读错误型断流仍中断 IDE，需回归锁定。
func TestCollectTraeStreamFoldsReadErrorAfterOutputToLength(t *testing.T) {
	body := "event: output\ndata: {\"response\":\"未完成\"}\n\nevent: output\ndata: {\"response\":\"继续\"}\n\n"
	r := &streamErrReader{data: []byte(body), err: errors.New("connection reset by peer")}
	chunks, _, _, _, err := collectTraeStream(r, "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("collectTraeStream error = %v; want nil (read error after output is recoverable)", err)
	}
	// 两个 output 分片 + 一个 length 终止分片。
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d; want 3 (2 output + length finish)", len(chunks))
	}
	var tail struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(chunks[len(chunks)-1].Payload, &tail); err != nil {
		t.Fatalf("decode tail chunk: %v", err)
	}
	if len(tail.Choices) != 1 || tail.Choices[0].FinishReason != "length" {
		t.Fatalf("finish_reason = %+v; want length", tail.Choices)
	}
}

// TestCollectTraeStreamPropagatesReadErrorWithoutOutput 锁定读错误前未收到任何业务内容时
// 仍按致命错误上报，避免把空响应继续伪装成成功（防 0.1.20 空成功回归）。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-08-31 15:20:00；改动原因：读错误兜底仅限已有可交付内容的场景，零内容不得豁免。
func TestCollectTraeStreamPropagatesReadErrorWithoutOutput(t *testing.T) {
	r := &streamErrReader{data: []byte(""), err: errors.New("connection reset by peer")}
	_, _, _, _, err := collectTraeStream(r, "qwen3.8-max", 200)
	if err == nil {
		t.Fatalf("read error without any output must be fatal, got nil")
	}
	if !strings.Contains(err.Error(), "connection reset by peer") {
		t.Fatalf("error = %v; want the underlying read error surfaced", err)
	}
}

// TestAggregateTraeCompletionFoldsReadErrorToLength 锁定非流式聚合路径在读错误型断流后
// 同样补 length 收尾并保留已累积正文。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-08-31 15:20:00；改动原因：三条响应路径需统一读错误断流收尾语义。
func TestAggregateTraeCompletionFoldsReadErrorToLength(t *testing.T) {
	body := "event: output\ndata: {\"response\":\"部分内容\"}\n\n"
	r := &streamErrReader{data: []byte(body), err: errors.New("unexpected EOF")}
	out, _, err := aggregateTraeCompletion(r, "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("aggregateTraeCompletion error = %v; want nil (read error after output is recoverable)", err)
	}
	var completion struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &completion); err != nil {
		t.Fatalf("decode completion: %v", err)
	}
	if len(completion.Choices) != 1 {
		t.Fatalf("choices = %d; want 1", len(completion.Choices))
	}
	if completion.Choices[0].FinishReason != "length" {
		t.Fatalf("finish_reason = %q; want length", completion.Choices[0].FinishReason)
	}
	if completion.Choices[0].Message.Content != "部分内容" {
		t.Fatalf("content = %q; want 部分内容（已生成内容必须保留）", completion.Choices[0].Message.Content)
	}
}
