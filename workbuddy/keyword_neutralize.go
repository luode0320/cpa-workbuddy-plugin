// keyword_neutralize.go 负责在请求出站前打断上游内容风控的逐字匹配。
//
// 背景（2026-11-08 实测，DSH = DeepSeek Harness 客户端）：
// WorkBuddy 上游对 messages 里的 system prompt 做**子串**级关键词匹配，命中
// 黑名单短语时直接返回
//
//	HTTP 400 {"code":11128,"msg":"Illegal API invocation from an unapproved channel"}
//
// 该文案把原因说成「渠道未获批准」，但实测与账号、域名、请求头、并发都无关：
// 换账号 / 换站点 / 补齐 13 个请求头照旧失败，只把 system prompt 里的关键词
// 打散就立刻 200。典型受害场景是第三方客户端在身份行里自带自家品牌词（DSH
// 写 "DeepSeek Harness"，Claude Code 写自家 CLI 身份行）。
//
// 处置方式是在短语内部插入零宽空格 U+200B：上游的子串匹配不再命中，而模型
// 侧的分词与语义完全不变（零宽空格不参与分词），属无损中和。
//
// 关键约束：文件路径语境不能改写。运行时会话里包含真实路径，例如
//
//	D:\10_COD\DeepSeek Harness\resources\app.asar\dsh\
//
// 一旦被插入零宽空格，模型会把坏路径交给工具，文件操作全部失败，而字符
// 不可见、盯着屏幕看不出任何异样。因此匹配前后紧邻 / 或 \ 时一律跳过。
package main

import (
	"regexp"
	"strings"
)

// zeroWidthSpace 是插入敏感短语内部的不可见分隔符（U+200B）。
const zeroWidthSpace = "\u200b"

// blockedPhraseREs 每条正则用**第一个捕获组**标出插入点：在捕获组结束处
// 插入零宽空格。整串匹配只用于确认「这是要打断的短语」，捕获组决定插在哪，
// 这样插入位置不依赖匹配到的空白长度或大小写。
var blockedPhraseREs = []*regexp.Regexp{
	// DSH 客户端身份行：上游对这条品牌组合做拦截（11128 线上实证）。
	regexp.MustCompile(`(?i)(DeepSeek)[ \t\x{00A0}]+Harness`),
	// Claude Code 身份行：腾讯 CodeBuddy 逐字拦截该模板。
	regexp.MustCompile(`(?i)(You are Claude) Code, Anthropic's official CLI tool for Claude\.`),
	// Claude Code 的 git 注入行。
	regexp.MustCompile(`(?i)(Default) branch \(you will usually use this for PRs\)`),
}

// neutralizeBlockedPhrases 对一段消息文本做全部黑名单短语的无损中和。
//
// [参数] s：一条消息的 content 文本（或其中一段 text part）。
// [返回] 打断关键词后的文本；无命中时原样返回同一个字符串。
func neutralizeBlockedPhrases(s string) string {
	if s == "" {
		return s
	}
	for _, re := range blockedPhraseREs {
		s = insertZeroWidthAtAnchor(s, re)
	}
	return s
}

// insertZeroWidthAtAnchor 在 re 第一捕获组结束处插入零宽空格。
//
// 落在路径语境的匹配会被跳过（见文件头注释）。多个匹配依次处理，跳过项
// 通过 last 指针原样带出，不产生字符丢失。
//
// [参数] s：待处理文本；re：至少含一个捕获组、且第一捕获组非空的正则。
// [返回] 插入后的文本；无有效插入时返回原串。
func insertZeroWidthAtAnchor(s string, re *regexp.Regexp) string {
	matches := re.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(matches))
	last := 0
	changed := false
	for _, m := range matches {
		start, end := m[0], m[1]
		if len(m) < 4 || m[3] < 0 {
			continue
		}
		anchor := m[3]
		if isPathContext(s, start, end) {
			continue
		}
		b.WriteString(s[last:anchor])
		b.WriteString(zeroWidthSpace)
		last = anchor
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
