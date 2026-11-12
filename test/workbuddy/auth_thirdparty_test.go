package main

import (
	"strings"
	"testing"
)

// TestParseStored_ThirdPartyRecord 验证 parseStored 能直接解析第三方导出的
// {"auth_raw":{...},"profile_raw":{...}} 形态；判据防令牌藏在 auth_raw 时被判成
// missing accessToken 导致导入失败，同时守住毫秒过期时间必须归一成秒；无副作用。
// 最近修改时间：2026-11-12 22:50:00 新增，覆盖第三方导出形态导入兼容
func TestParseStored_ThirdPartyRecord(t *testing.T) {
	raw := []byte(`{
		"id": "rec-1",
		"uid": "uid-top",
		"nickname": "157****3836",
		"access_token": "top-at",
		"refresh_token": "top-rt",
		"expires_at": 1796910717,
		"status": "normal",
		"auth_raw": {
			"accessToken": "raw-at",
			"refreshToken": "raw-rt",
			"expiresAt": 1796910717981,
			"domain": "www.workbuddy.cn"
		},
		"profile_raw": {"uid": "uid-profile", "nickname": "nick-profile"}
	}`)
	sa, err := parseStored(raw)
	if err != nil {
		t.Fatalf("parseStored: %v", err)
	}
	// auth_raw 优先于顶层同名语义字段。
	if sa.Auth.AccessToken != "raw-at" {
		t.Fatalf("AccessToken = %q; want raw-at", sa.Auth.AccessToken)
	}
	if sa.Auth.RefreshToken != "raw-rt" {
		t.Fatalf("RefreshToken = %q; want raw-rt", sa.Auth.RefreshToken)
	}
	// 1796910717981 毫秒必须换算成 1796910717 秒，否则过期判断整体偏移。
	if sa.Auth.ExpiresAt != 1796910717 {
		t.Fatalf("ExpiresAt = %d; want 1796910717", sa.Auth.ExpiresAt)
	}
	// domain 保留原值，CN 判定与 X-Domain 头都依赖它。
	if sa.Auth.Domain != "www.workbuddy.cn" {
		t.Fatalf("Domain = %q; want www.workbuddy.cn", sa.Auth.Domain)
	}
	if sa.Account.UID != "uid-profile" {
		t.Fatalf("UID = %q; want uid-profile", sa.Account.UID)
	}
	if sa.Account.Nickname != "nick-profile" {
		t.Fatalf("Nickname = %q; want nick-profile", sa.Account.Nickname)
	}
}

// TestParseStored_ThirdPartyFallback 验证第三方记录缺少 auth_raw 时回落到顶层
// snake_case 字段；判据防只有 access_token 的变体被直接判成无效凭证；无副作用。
// 最近修改时间：2026-11-12 22:50:00 新增，覆盖第三方导出的扁平变体
func TestParseStored_ThirdPartyFallback(t *testing.T) {
	raw := []byte(`{
		"uid": "uid-top",
		"nickname": "nick-top",
		"access_token": "top-at",
		"refresh_token": "top-rt",
		"expires_at": 1796910717
	}`)
	sa, err := parseStored(raw)
	if err != nil {
		t.Fatalf("parseStored: %v", err)
	}
	if sa.Auth.AccessToken != "top-at" || sa.Auth.RefreshToken != "top-rt" {
		t.Fatalf("tokens = %q/%q; want top-at/top-rt", sa.Auth.AccessToken, sa.Auth.RefreshToken)
	}
	// 顶层 expires_at 已是秒，不能再被换算一次。
	if sa.Auth.ExpiresAt != 1796910717 {
		t.Fatalf("ExpiresAt = %d; want 1796910717", sa.Auth.ExpiresAt)
	}
	if sa.Account.UID != "uid-top" || sa.Account.Nickname != "nick-top" {
		t.Fatalf("account = %q/%q; want uid-top/nick-top", sa.Account.UID, sa.Account.Nickname)
	}
}

// TestParseStored_NestedAndFlatUnaffected 验证新增的第三方分支没有改变原有
// 嵌套形态与扁平形态的解析结果；判据防形态分发顺序把既有导入路径带坏；无副作用。
// 最近修改时间：2026-11-12 22:50:00 新增，守住既有两种形态的回归
func TestParseStored_NestedAndFlatUnaffected(t *testing.T) {
	nested := []byte(`{"type":"workbuddy-provider","auth":{"accessToken":"n-at","refreshToken":"n-rt","expiresAt":42,"domain":"www.codebuddy.cn"},"account":{"uid":"n-uid","enterpriseId":"n-ent","nickname":"n-nick"}}`)
	sa, err := parseStored(nested)
	if err != nil {
		t.Fatalf("nested parseStored: %v", err)
	}
	if sa.Auth.AccessToken != "n-at" || sa.Auth.ExpiresAt != 42 || sa.Account.UID != "n-uid" || sa.Account.EnterpriseID != "n-ent" {
		t.Fatalf("nested = %+v; want n-at/42/n-uid/n-ent", sa)
	}

	flat := []byte(`{"accessToken":"f-at","refreshToken":"f-rt","expiresAt":43,"domain":"www.workbuddy.ai","uid":"f-uid","nickname":"f-nick"}`)
	sa, err = parseStored(flat)
	if err != nil {
		t.Fatalf("flat parseStored: %v", err)
	}
	if sa.Auth.AccessToken != "f-at" || sa.Auth.ExpiresAt != 43 || sa.Account.UID != "f-uid" || sa.Account.Nickname != "f-nick" {
		t.Fatalf("flat = %+v; want f-at/43/f-uid/f-nick", sa)
	}
}

// TestParseStored_MissingAccessToken 验证三种形态在没有可用令牌时都返回
// missing accessToken；判据防空凭证被写盘后污染账号池；无副作用。
// 最近修改时间：2026-11-12 22:50:00 新增，覆盖令牌缺失的统一报错
func TestParseStored_MissingAccessToken(t *testing.T) {
	cases := map[string]string{
		"third-party without token": `{"uid":"u","profile_raw":{"uid":"u"},"auth_raw":{"domain":"www.workbuddy.cn"}}`,
		"flat without token":        `{"uid":"u","domain":"www.codebuddy.cn"}`,
		"nested without token":      `{"auth":{"domain":"www.codebuddy.cn"},"account":{"uid":"u"}}`,
	}
	for name, body := range cases {
		_, err := parseStored([]byte(body))
		if err == nil || !strings.Contains(err.Error(), "missing accessToken") {
			t.Fatalf("%s: err = %v; want missing accessToken", name, err)
		}
	}
}

// TestEpochsToSeconds 验证时间戳归一换算；判据防毫秒被当秒用导致凭证被误判过期、
// 或秒被再除一次导致过期时间回退到 1970；无副作用。
// 最近修改时间：2026-11-12 22:50:00 新增，覆盖毫秒与秒的双向输入
func TestEpochsToSeconds(t *testing.T) {
	cases := []struct {
		in   int64
		want int64
	}{
		{1796910717981, 1796910717},
		{1796910717, 1796910717},
		{0, 0},
		{-1, 0},
		{99999999999, 99999999999},
	}
	for _, c := range cases {
		if got := epochsToSeconds(c.in); got != c.want {
			t.Fatalf("epochsToSeconds(%d) = %d; want %d", c.in, got, c.want)
		}
	}
}
