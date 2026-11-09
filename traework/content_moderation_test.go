// content_moderation_test.go 锁定「内容审核拦截按请求级终局处理」。
//
// 生产背景（2026-11-09）：glm-5.3-flashx 返回 `sensitive content error finish reason:
// sensitive` 时，客户端仍收到 HTTP 200（看似成功但内容被截断），无法区分
// 「正常完成」与「被审核截断」。这类失败与账号健康度无关，换号必然重演。
package main

import (
	"testing"
)

// TestIsContentModerationFailure 验证内容审核拦截的识别边界。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-09 23:35:00；改动原因：新增，审核失败需明确报错。
func TestIsContentModerationFailure(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "生产实测原文",
			body: "upstream 200: sensitive content error finish reason: sensitive",
			want: true,
		},
		{name: "大写同样识别", body: "SENSITIVE CONTENT ERROR", want: true},
		{name: "content error 短语", body: "content error finish reason: content", want: true},
		// 无关失败不得误判。
		{name: "配额耗尽不归此类", body: "upstream 200: Your requests have exceeded the quota.", want: false},
		{name: "渠道拦截不归此类", body: `{"code":11128,"msg":"unapproved channel"}`, want: false},
		{name: "普通 500 不归此类", body: "upstream 500: internal error", want: false},
		{name: "空正文不归此类", body: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContentModerationFailure(tc.body); got != tc.want {
				t.Fatalf("isContentModerationFailure(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestEnvelopeHTTPStatusForContentModeration 验证审核失败携带 400。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-09 23:35:00；改动原因：新增。
func TestEnvelopeHTTPStatusForContentModeration(t *testing.T) {
	msg := "upstream 200: sensitive content error finish reason: sensitive"
	if got := envelopeHTTPStatusForError(msg); got != httpStatusBadRequest {
		t.Fatalf("审核失败 http_status = %d, want %d", got, httpStatusBadRequest)
	}
	// 普通错误保持不带状态码，避免改变既有分类。
	if got := envelopeHTTPStatusForError("upstream 500: internal error"); got != 0 {
		t.Fatalf("普通错误 http_status = %d, want 0", got)
	}
}
