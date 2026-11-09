// done_terminal_test.go 锁定「done 事件携带的终止原因必须上报为错误」。
//
// 生产缺陷回归（2026-11-09）：glm-5.3-flashx 请求耗时 1m26s / 1m34s、生成 2946 个
// 分片后才返回 `sensitive content error finish reason: sensitive`，而旧实现把 done
// 事件 payload 直接丢弃，按业务完整收尾 —— 客户端拿到已生成正文却没有任何错误标记。
package main

import (
	"strings"
	"testing"
)

// TestDoneTerminalError 验证 done 事件终止原因的识别边界。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-09 22:25:00；改动原因：新增，内容审核拦截需明确报错。
func TestDoneTerminalError(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{
			name: "生产实测原文",
			data: "sensitive content error finish reason: sensitive",
			want: true,
		},
		{name: "结构化载荷含 sensitive", data: `{"finish_reason":"sensitive"}`, want: true},
		{name: "大写同样识别", data: "SENSITIVE CONTENT ERROR", want: true},
		{name: "content error 短语", data: "content error finish reason: content", want: true},
		// 正常完成的各种形态都不得误判。
		{name: "空 data", data: "", want: false},
		{name: "空对象", data: "{}", want: false},
		{name: "[DONE]", data: "[DONE]", want: false},
		{name: "null", data: "null", want: false},
		{name: "正常 stop", data: `{"finish_reason":"stop"}`, want: false},
		{name: "正常 length", data: `{"finish_reason":"length"}`, want: false},
		{name: "无关字段不误判", data: `{"usage":{"total_tokens":100}}`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := doneTerminalError(tc.data)
			if tc.want && got == "" {
				t.Fatalf("doneTerminalError(%q) 应为错误，实际返回空", tc.data)
			}
			if !tc.want && got != "" {
				t.Fatalf("doneTerminalError(%q) 应为正常完成，实际返回 %q", tc.data, got)
			}
		})
	}
}

// TestDoneTerminalErrorRedacts 验证上报文本经过脱敏截断。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-09 22:25:00；改动原因：新增。
func TestDoneTerminalErrorRedacts(t *testing.T) {
	long := "sensitive content error " + strings.Repeat("x", 500)
	got := doneTerminalError(long)
	if got == "" {
		t.Fatal("应识别为错误")
	}
	if len(got) > 220 {
		t.Fatalf("上报文本未截断: %d 字符", len(got))
	}
}
