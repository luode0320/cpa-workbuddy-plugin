package main

import (
	"strings"
	"testing"
)

// TestSignAddsMsTokenAndABogus 验证签名器能在 URL 上补齐 msToken 与 a_bogus。
//
// 用真实 SDK 跑一次签名：这两个参数缺失时上游直接回 710022002，是 0.1.16
// 修复的核心，不能用桩替代。
// 最近修改时间：2026-11-15 01:20:00；改动原因：新增，签名链路回归。
func TestSignAddsMsTokenAndABogus(t *testing.T) {
	const msTokenValue = "Zm9vYmFyLW1zLXRva2VuLXBsYWNlaG9sZGVyLXZhbHVl"
	raw := doubaoSiteHost + "/chat/completion?aid=497858&device_platform=web&version_code=20800"
	const sampleBody = `{"messages":[{"content_block":[{"block_type":10000}]}]}`

	signed, errSign := getBrowserSigner().sign(raw, msTokenValue, sampleBody)
	if errSign != nil {
		t.Fatalf("签名失败：%v", errSign)
	}
	if !strings.Contains(signed, "msToken="+msTokenValue) {
		t.Fatalf("签名后缺少 msToken，实际 URL：%s", signed)
	}
	if !strings.Contains(signed, "a_bogus=") {
		t.Fatalf("签名后缺少 a_bogus，实际 URL：%s", signed)
	}
	// a_bogus 由 SDK 用自定义 base64 表编码，长度稳定在百字符量级；
	// 用下界判定而不是等值比较，避免 SDK 升级后误报。
	bogus := valueAfter(signed, "a_bogus=")
	if len(bogus) < 64 {
		t.Fatalf("a_bogus 长度异常（%d）：%s", len(bogus), bogus)
	}
}

// TestSignIsStableAcrossCalls 验证同一签名器可连续签名，且每次 a_bogus 不同。
//
// SDK 内部带时间戳与随机量，两次签名结果必须不同；相同说明签名链路被缓存
// 短路，实际请求会因时间窗口失效。
// 最近修改时间：2026-11-15 01:20:00；改动原因：新增，签名非确定性回归。
func TestSignIsStableAcrossCalls(t *testing.T) {
	raw := doubaoSiteHost + "/chat/completion?aid=497858&web_platform=browser"
	first, errFirst := getBrowserSigner().sign(raw, "token-a", "{}")
	if errFirst != nil {
		t.Fatalf("第一次签名失败：%v", errFirst)
	}
	second, errSecond := getBrowserSigner().sign(raw, "token-a", "{}")
	if errSecond != nil {
		t.Fatalf("第二次签名失败：%v", errSecond)
	}
	if !strings.Contains(first, "a_bogus=") || !strings.Contains(second, "a_bogus=") {
		t.Fatalf("连续签名缺少 a_bogus：%s / %s", first, second)
	}
	if valueAfter(first, "a_bogus=") == valueAfter(second, "a_bogus=") {
		t.Fatalf("两次签名的 a_bogus 相同，签名链路疑似被缓存：%s", first)
	}
}

// TestSignSkipsNonUpstreamURL 验证非上游地址不签名。
//
// httptest 覆盖点与本地地址签名后无法解析，必须原样返回。
// 最近修改时间：2026-11-15 01:20:00；改动原因：新增，签名边界回归。
func TestSignSkipsNonUpstreamURL(t *testing.T) {
	sa := &StoredAuth{MsToken: "token-a"}
	for _, raw := range []string{"http://127.0.0.1:8080/chat/completion", "https://example.com/x"} {
		if got := signChatCompletionURL(sa, raw, []byte("{}")); got != raw {
			t.Fatalf("非上游地址不应签名，输入 %s 得到 %s", raw, got)
		}
	}
}

// TestSignCoversRequestBody 验证 a_bogus 覆盖请求体：同一 URL 换 body 必须换签名。
//
// 这是 0.1.23 的核心回归：旧实现固定 `x.send('{}')`，与真实 3.4KB 请求体不符，
// 上游按真实 body 校验签名必然不一致，稳定回 -1 delegated input protocol violation。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-16 03:18:00；改动原因：新增，签名覆盖 body 回归。
func TestSignCoversRequestBody(t *testing.T) {
	raw := doubaoSiteHost + "/chat/completion?aid=497858&web_platform=browser"
	const msTokenValue = "token-a"

	small, errSmall := getBrowserSigner().sign(raw, msTokenValue, "{}")
	if errSmall != nil {
		t.Fatalf("小 body 签名失败：%v", errSmall)
	}
	large, errLarge := getBrowserSigner().sign(raw, msTokenValue, `{"messages":[{"text":"这是一个明显更长的请求体，用来验证签名覆盖 body"}]}`)
	if errLarge != nil {
		t.Fatalf("大 body 签名失败：%v", errLarge)
	}

	if valueAfter(small, "a_bogus=") == valueAfter(large, "a_bogus=") {
		t.Fatal("换 body 后 a_bogus 必须变化：a_bogus 覆盖请求体，否则上游按真实 body 校验会判协议异常")
	}
}

// valueAfter 取 query 里某个键的值（到下一个 & 为止）。
// [参数] raw: 完整 URL；key: 形如 "a_bogus=" 的键前缀。
// [返回] 值文本；未命中返回空串。
// 最近修改时间：2026-11-15 01:20:00；改动原因：新增，断言辅助。
func valueAfter(raw, key string) string {
	idx := strings.Index(raw, key)
	if idx < 0 {
		return ""
	}
	rest := raw[idx+len(key):]
	if end := strings.IndexAny(rest, "&#"); end >= 0 {
		rest = rest[:end]
	}
	return rest
}
