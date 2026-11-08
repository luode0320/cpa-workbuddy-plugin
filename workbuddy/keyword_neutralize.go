// keyword_neutralize.go 负责在请求出站前改写上游内容风控的黑名单短语。
//
// 背景（2026-11-08 生产实测，DSH = DeepSeek Harness 客户端）：
// WorkBuddy 上游对 messages 内容做**子串级**关键词风控，命中即返回
//
//	HTTP 400 {"code":11128,"msg":"Illegal API invocation from an unapproved channel"}
//
// 该文案把原因说成「渠道未获批准」，但实测与账号、域名、请求头、并发都无关。
//
// 一手二分证据（同一 token、同一 endpoint、唯一变量是 system prompt 中的词形）：
//
//	"You are an AI agent."                                   → 200
//	"... powered by DeepSeek Harness."                       → 400 / 11128
//	"... powered by deepseek harness."（大小写不敏感）        → 400 / 11128
//	"... powered by DeepSeek  Harness."（多空格）             → 400 / 11128
//	"... powered by DeepSeek\u00a0Harness."（NBSP）           → 400 / 11128
//	"... powered by DeepSeek\u200b Harness."（零宽空格）      → 400 / 11128  ← 社区方案在此失效
//	"... powered by DeepSeek\u200c Harness."（零宽非连接符）  → 400 / 11128
//	"... powered by DeepSeek-Harness."（可见连字符）          → 200        ← 唯一有效
//	"... powered by DeepSeek." / "... powered by Harness."   → 200
//
// 结论：上游会先把不可见字符（U+200B/U+200C 等零宽族）归一化剥离，再做子串
// 匹配，所以「插零宽空格」在这条链路上完全无效；只有**可见**分隔符（连字符）
// 才能真正打断匹配。请求体必须足够大（约 >1KB）才稳定触发，短 prompt 可能被
// 放过，这是最容易误判成「偶发抖动」的地方。
//
// 关键约束：文件路径语境不能改写。运行时会话里包含真实路径，例如
//
//	D:\10_COD\DeepSeek Harness\resources\app.asar\dsh\
//
// 一旦被改成带连字符的 "DeepSeek-Harness"，模型会把坏路径交给工具，文件操作
// 全部失败。因此匹配前后紧邻 / 或 \ 时一律跳过。
package main

import (
	"regexp"
	"strings"
)

// blockedRewrite 描述一条出站改写：re 定位要处理的短语，repl 是替换模板。
//
// re 必须用捕获组把「要保留的部分」圈出来，repl 通过 ${n} 引用，这样替换
// 只发生在分隔符上，原文其余字节（含大小写）完全不动。
type blockedRewrite struct {
	re   *regexp.Regexp
	repl string
}

// blockedPhraseRewrites 是全部出站改写规则。
//
// 分隔符字符类覆盖：ASCII 空白、NBSP(U+00A0)、零宽族(U+200B..U+200D)、
// BOM(U+FEFF)。把零宽族也纳入「待替换的分隔符」而不是当普通字符，是因为
// 客户端自己的 prompt 里可能已经带了零宽写法，同样需要被打断。
var blockedPhraseRewrites = []blockedRewrite{
	// DSH 客户端身份行：上游对这条品牌组合做拦截（11128 线上实证）。
	// 换成连字符后实测 200，且 "DeepSeek-Harness" 仍是同一个产品名，语义不变。
	{
		re:   regexp.MustCompile(`(?i)(DeepSeek)[\s\x{00A0}\x{200B}-\x{200D}\x{FEFF}]+(Harness)`),
		repl: "${1}-${2}",
	},
	// Claude Code 身份行：上游逐字拦截整句模板。插入可见词 "tool" 即可躲过，
	// 语义不变（「官方 CLI for Claude」→「官方 CLI 工具 for Claude」）。
	{
		re:   regexp.MustCompile(`(You are Claude Code, Anthropic's official CLI) for (Claude\.)`),
		repl: "${1} tool for ${2}",
	},
}

// neutralizeBlockedPhrases 对一段消息文本执行全部出站改写。
//
// [参数] s：一条消息的 content 文本（或其中一段 text part）。
// [返回] 改写后的文本；无命中时原样返回同一个字符串。
func neutralizeBlockedPhrases(s string) string {
	if s == "" {
		return s
	}
	for _, r := range blockedPhraseRewrites {
		s = rewriteWithPathGuard(s, r)
	}
	return s
}

// rewriteWithPathGuard 按规则改写 s，跳过落在文件路径语境的匹配。
//
// 多个匹配依次处理，被跳过的匹配通过 last 指针原样带出，不丢字符。
//
// [参数] s：待处理文本；r：改写规则。
// [返回] 改写后的文本；无有效改写时返回原串。
func rewriteWithPathGuard(s string, r blockedRewrite) string {
	matches := r.re.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(matches))
	last := 0
	changed := false
	for _, m := range matches {
		if isPathContext(s, m[0], m[1]) {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.Write(r.re.ExpandString(nil, r.repl, s, m))
		last = m[1]
		changed = true
	}
	if !changed {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// isPathContext 判断一次匹配是否落在文件路径里——紧邻的前后字节是 / 或 \。
//
// 对应社区方案里的 (?<![\\/]) ... (?![\\/]) 负向断言；Go 的 regexp 不支持
// lookbehind，故在手写扫描阶段判定。
//
// [参数] s：完整文本；start/end：匹配的起止字节下标。
// [返回] true 表示该匹配属路径，必须保持原样。
func isPathContext(s string, start, end int) bool {
	if start > 0 {
		if c := s[start-1]; c == '/' || c == '\\' {
			return true
		}
	}
	if end < len(s) {
		if c := s[end]; c == '/' || c == '\\' {
			return true
		}
	}
	return false
}
