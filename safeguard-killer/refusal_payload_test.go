package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestInspectPayload_ResponsesStringDelta 验证 responses 的裸字符串 delta 能被识别成正文。
// 判据防的是 delta 声明成对象后整帧反序列化失败、正文全丢，进而被下游判成空响应。
// 无副作用，纯函数判定。
func TestInspectPayload_ResponsesStringDelta(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantText   string
		wantTool   bool
		wantEnvel  bool
		wantRefuse string
	}{
		{
			name:      "裸字符串 delta 认出正文",
			body:      `{"type":"response.output_text.delta","sequence_number":7,"item_id":"msg_0","output_index":0,"content_index":0,"delta":"收到"}`,
			wantText:  "收到",
			wantEnvel: true,
		},
		{
			name:      "对象 delta 仍走 text_delta 分支",
			body:      `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}`,
			wantText:  "你好",
			wantEnvel: true,
		},
		{
			name:      "对象 delta 的 input_json_delta 记为工具调用",
			body:      `{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"a\":1}"}}`,
			wantTool:  true,
			wantEnvel: true,
		},
		{
			name:       "对象 delta 的 refusal 判拒绝",
			body:       `{"type":"content_block_delta","delta":{"type":"text_delta","refusal":"I cannot help"}}`,
			wantRefuse: "refusal",
			wantEnvel:  true,
		},
		{
			name:      "output_item.done 的 item.content 带正文",
			body:      `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_0","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"收到","annotations":[]}]}}`,
			wantText:  "收到",
			wantEnvel: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refusal, visible, sawTool, sawEnvelope := inspectPayload([]byte(tc.body))
			if refusal != tc.wantRefuse {
				t.Fatalf("refusal = %q, want %q", refusal, tc.wantRefuse)
			}
			if !strings.Contains(visible, tc.wantText) {
				t.Fatalf("visible = %q, want 包含 %q", visible, tc.wantText)
			}
			if sawTool != tc.wantTool {
				t.Fatalf("sawTool = %v, want %v", sawTool, tc.wantTool)
			}
			if sawEnvelope != tc.wantEnvel {
				t.Fatalf("sawEnvelope = %v, want %v", sawEnvelope, tc.wantEnvel)
			}
		})
	}
}

// TestCheckNonStreamResponse_ResponsesStringDeltaPasses 验证字符串 delta 的 responses 正文不再被判空响应。
// 判据防的正是「上游正常返回、SK 却报 empty response 并滚到下一个模型」这条回归路径。
func TestCheckNonStreamResponse_ResponsesStringDeltaPasses(t *testing.T) {
	body := `{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"deepseek-v4-pro","output":[{"id":"msg_0","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"收到","annotations":[]}]}]}}`
	if errCheck := checkNonStreamResponse([]byte(body)); errCheck != nil {
		t.Fatalf("checkNonStreamResponse() = %v, want nil", errCheck)
	}
}

// TestPrefetchStream_ResponsesStringDeltaPasses 验证流式裸字符串 delta 能在预取阶段累积出可见正文。
// 桩只模拟宿主流式读取，不触碰网络与磁盘。
func TestPrefetchStream_ResponsesStringDeltaPasses(t *testing.T) {
	chunks := []string{
		`data: {"type":"response.created","sequence_number":1,"response":{"id":"resp_1","object":"response","status":"in_progress","output":[],"model":"deepseek-v4-pro"}}`,
		`data: {"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"msg_0","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`data: {"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_0","output_index":0,"delta":"收到"}`,
		`data: {"type":"response.completed","sequence_number":4,"response":{"id":"resp_1","status":"completed","output":[],"model":"deepseek-v4-pro"}}`,
	}
	index := 0
	stubHost(t, func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case methodHostModelStreamRead:
			if index >= len(chunks) {
				return json.Marshal(hostStreamReadResponse{Done: true})
			}
			payloadText := chunks[index] + "\n\n"
			index++
			return json.Marshal(hostStreamReadResponse{Payload: []byte(payloadText), Done: index >= len(chunks)})
		case methodHostModelStreamClose:
			return json.RawMessage(`{}`), nil
		}
		t.Fatalf("unexpected host call %s", method)
		return nil, nil
	})

	prefetched, _, errPrefetch := prefetchStream("src", "cb", true, 128)
	if errPrefetch != nil {
		t.Fatalf("prefetchStream() = %v, want nil", errPrefetch)
	}
	if len(prefetched) == 0 {
		t.Fatalf("prefetchStream() 未返回任何分片")
	}
}
