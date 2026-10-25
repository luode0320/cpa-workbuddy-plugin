package main

import (
	"strings"
	"testing"
)

// TestRedactSecretsTable 覆盖四类凭据形态与两类不应误伤的普通文本。
// 上游错误报文会原样进入宿主错误帧与用量数据流，任一类漏掉都等于凭据外泄。
func TestRedactSecretsTable(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	cases := []struct {
		name     string
		in       string
		wantGone []string
		wantKept []string
	}{
		{
			name:     "Bearer 头回显",
			in:       "upstream 401: Authorization: Bearer sk-abcdef1234567890abcdef",
			wantGone: []string{"sk-abcdef1234567890abcdef"},
			wantKept: []string{"Bearer ***", "upstream 401"},
		},
		{
			name:     "三段式 JWT",
			in:       `upstream error {"token":"` + jwt + `"}`,
			wantGone: []string{jwt},
			wantKept: []string{"***jwt***", "upstream error"},
		},
		{
			name:     "access_token 键值",
			in:       "refresh failed access_token=abcdef1234567890abcdef&scope=all",
			wantGone: []string{"abcdef1234567890abcdef"},
			wantKept: []string{"access_token=***", "scope=all"},
		},
		{
			name:     "裸 JWT 无前缀",
			in:       "invalid credential " + jwt,
			wantGone: []string{jwt},
			wantKept: []string{"***jwt***", "invalid credential"},
		},
		{
			name:     "中文额度错误不误伤",
			in:       "额度已用尽，请稍后再试",
			wantKept: []string{"额度已用尽", "请稍后再试"},
		},
		{
			name:     "空文本原样返回",
			in:       "",
			wantKept: []string{""},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.in)
			for _, secret := range tc.wantGone {
				if secret != "" && strings.Contains(got, secret) {
					t.Fatalf("凭据未被脱敏：%q 仍出现在 %q 中", secret, got)
				}
			}
			for _, keep := range tc.wantKept {
				if !strings.Contains(got, keep) {
					t.Fatalf("应保留的文本被破坏：期望包含 %q，实际为 %q", keep, got)
				}
			}
		})
	}
}

// TestTruncateRedactedCapsLength 断言限长出口既脱敏又不超过上限。
func TestTruncateRedactedCapsLength(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	long := "upstream 500: " + strings.Repeat("x", 2000) + " token=" + jwt

	got := truncateRedacted(long, 128)
	if len(got) > 128 {
		t.Fatalf("截断后长度 = %d, want <= 128", len(got))
	}
	if strings.Contains(got, jwt) {
		t.Fatalf("截断路径未脱敏，JWT 仍存在：%q", got)
	}
	if !strings.HasPrefix(got, "upstream 500: ") {
		t.Fatalf("截断应保留前缀，实际为 %q", got)
	}
}

// TestTruncateRedactedKeepsShortText 断言短文本不被截断改写。
func TestTruncateRedactedKeepsShortText(t *testing.T) {
	in := "upstream 429: rate limited"
	if got := truncateRedacted(in, 512); got != in {
		t.Fatalf("短文本应原样返回，got %q", got)
	}
}
